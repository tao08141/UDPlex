package main

import (
	"fmt"
	"net"
	"sync"
	"time"
)

type TcpTunnelListenComponent struct {
	BaseComponent

	listenAddr        string
	timeout           time.Duration
	replaceOldMapping bool
	detour            []string
	broadcastMode     bool

	stopCh chan struct{}

	connections      map[ConnID]*TcpConnection
	connectionsMutex sync.RWMutex

	authManager *AuthManager
}

type TcpConnection struct {
	conn       net.Conn
	authState  *AuthState
	connID     ConnID
	lastActive time.Time
}

func NewTcpTunnelListenComponent(cfg ComponentConfig, router *Router) *TcpTunnelListenComponent {
	timeout := time.Duration(cfg.Timeout) * time.Second
	if timeout == 0 {
		timeout = 120 * time.Second // Default timeout
	}

	authManager, err := NewAuthManager(cfg.Auth, router)
	if err != nil {
		logger.Errorf("Failed to create auth manager: %v", err)
		return nil
	}

	broadcastMode := true
	if cfg.BroadcastMode != nil && !*cfg.BroadcastMode {
		broadcastMode = false
	}

	return &TcpTunnelListenComponent{
		BaseComponent: NewBaseComponent(cfg.Tag, router),

		listenAddr:        cfg.ListenAddr,
		timeout:           timeout,
		replaceOldMapping: cfg.ReplaceOldMapping,
		detour:            cfg.Detour,
		stopCh:            make(chan struct{}),
		authManager:       authManager,
		broadcastMode:     broadcastMode,
		connections:       make(map[ConnID]*TcpConnection),
	}
}

func (l *TcpTunnelListenComponent) Start() error {
	ln, err := net.Listen("tcp", l.listenAddr)
	if err != nil {
		return err
	}
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				select {
				case <-l.stopCh:
					return
				default:
				}
				continue
			}
			go l.handleConn(conn)
		}
	}()
	return nil
}

func (l *TcpTunnelListenComponent) handleConn(conn net.Conn) {
	defer conn.Close()

	authState := &AuthState{}
	authDeadline := time.Now().Add(l.authManager.authTimeout)

	authState.lastHeartbeat = time.Now()
	connID := l.generateConnID()
	tcpConn := &TcpConnection{
		conn:       conn,
		authState:  authState,
		connID:     connID,
		lastActive: time.Now(),
	}

	l.connectionsMutex.Lock()
	l.connections[connID] = tcpConn
	l.connectionsMutex.Unlock()

	// Make sure to clean up when we're done
	defer func() {
		l.connectionsMutex.Lock()
		delete(l.connections, connID)
		l.connectionsMutex.Unlock()
	}()

	// Buffer to accumulate incoming data
	buffer := l.router.GetBuffer()
	defer l.router.PutBuffer(buffer)
	bufferUsed := 0

	for {

		select {
		case <-l.stopCh:
			logger.Infof("%s: Stopping connection handling for %s", l.tag, conn.RemoteAddr())
			return
		default:

			if !authState.IsAuthenticated() {
				_ = conn.SetReadDeadline(authDeadline)
			} else {
				_ = conn.SetReadDeadline(time.Now().Add(l.timeout))
			}

			// Read into the unused portion of the buffer
			n, err := conn.Read(buffer[bufferUsed:])
			if err != nil {
				logger.Infof("%s: %s Read error: %v", l.tag, conn.RemoteAddr(), err)
				return
			}

			bufferUsed += n

			// Process all complete messages in the buffer
			processedBytes := 0
			for processedBytes < bufferUsed {
				// Need at least a header to continue
				if bufferUsed-processedBytes < HeaderSize {
					break
				}

				// Parse header
				header, err := ParseHeader(buffer[processedBytes:])
				if err != nil {
					logger.Infof("%s: %s Invalid header: %v", l.tag, conn.RemoteAddr(), err)
					return
				}

				totalMessageSize := HeaderSize + int(header.Length)

				// If we don't have the full message yet, wait for more data
				if bufferUsed-processedBytes < totalMessageSize {
					break
				}

				// We have a complete message, process it
				messageBuffer := buffer[processedBytes : processedBytes+totalMessageSize]

				if authState.IsAuthenticated() {
					// Process authenticated messages
					if header.MsgType == MsgTypeData {
						packet := l.router.GetPacket(l.tag)
						packet.length = totalMessageSize
						copy(packet.buffer, messageBuffer)

						// Unwrap and route the packet
						_, err := l.authManager.UnwrapData(&packet)
						if err == nil {
							l.router.Route(&packet, l.detour)
						} else {
							logger.Infof("%s: %s Failed to unwrap data: %v", l.tag, conn.RemoteAddr(), err)
							packet.Release(1)
						}
					} else if header.MsgType == MsgTypeHeartbeat {
						// Update heartbeat time
						authState.UpdateHeartbeat()

						// Send heartbeat response
						responseBuffer := make([]byte, HeaderSize)
						CreateHeartbeat(responseBuffer)
						conn.Write(responseBuffer)
					} else if header.MsgType == MsgTypeDisconnect {
						logger.Infof("%s: %s Client requested disconnect", l.tag, conn.RemoteAddr())
						return
					}
				} else {
					// Handle authentication
					if header.MsgType == MsgTypeAuthChallenge {
						data := messageBuffer[HeaderSize:totalMessageSize]
						err := l.authManager.ProcessAuthChallenge(data, authState)
						if err != nil {
							logger.Infof("%s: %s Failed to process auth challenge: %v", l.tag, conn.RemoteAddr(), err)
							return
						}

						// Send auth response
						responseBuffer := l.router.GetBuffer()
						responseLen, err := l.authManager.CreateAuthChallenge(responseBuffer, MsgTypeAuthResponse, ForwardID{}, PoolID{})
						if err != nil {
							logger.Warnf("%s: Failed to create auth challenge response: %v", l.tag, err)
							l.router.PutBuffer(responseBuffer)
							return
						}

						if _, err := conn.Write(responseBuffer[:responseLen]); err != nil {
							logger.Infof("%s: %s Failed to send auth response: %v", l.tag, conn.RemoteAddr(), err)
							l.router.PutBuffer(responseBuffer)
							return
						}

						l.router.PutBuffer(responseBuffer)
						// Mark as authenticated
						authState.authenticated = 1
					} else {
						logger.Infof("%s: %s Received unexpected message type %d before authentication", l.tag, conn.RemoteAddr(), header.MsgType)
						return
					}
				}

				// Move past this message in the buffer
				processedBytes += totalMessageSize
			}

			// Shift any remaining partial message to the beginning of the buffer
			if processedBytes > 0 {
				copy(buffer, buffer[processedBytes:bufferUsed])
				bufferUsed -= processedBytes
			}

			// Check if buffer is full but we couldn't process anything - likely a malformed packet
			if bufferUsed == len(buffer) && processedBytes == 0 {
				logger.Infof("%s: %s Buffer full but no complete message, likely malformed data", l.tag, conn.RemoteAddr())
				return
			}
		}
	}
}

func (l *TcpTunnelListenComponent) HandlePacket(packet *Packet) error {
	defer packet.Release(1)

	// Wrap data with authentication if needed
	l.authManager.WrapData(packet)

	// If broadcasting, send to all authenticated connections
	if l.broadcastMode {
		l.connectionsMutex.RLock()
		for _, conn := range l.connections {
			if conn.authState == nil || !conn.authState.IsAuthenticated() {
				continue
			}

			// Write packet data to connection
			if err := l.router.SendPacket(l, packet, conn.conn); err != nil {
				logger.Infof("%s: Failed to queue packet for sending: %v", l.tag, err)
			}
		}
		l.connectionsMutex.RUnlock()
	} else {
		// Direct mode: only send to the specific connection ID
		if packet.connID == (ConnID{}) {
			logger.Infof("%s: Packet has no connection ID, dropping", l.tag)
			return nil
		}

		l.connectionsMutex.RLock()
		conn, exists := l.connections[packet.connID]
		l.connectionsMutex.RUnlock()

		if !exists {
			logger.Debugf("%s: No connection found for ID: %s", l.tag, packet.connID)
			return nil
		}

		// Check authentication if required
		if conn.authState == nil || !conn.authState.IsAuthenticated() {
			logger.Debugf("%s: Connection not authenticated, dropping packet", l.tag)
			return nil
		}

		// Write packet data to connection
		if err := l.router.SendPacket(l, packet, conn.conn); err != nil {
			logger.Infof("%s: Failed to queue packet for sending: %v", l.tag, err)
		}

	}

	return nil
}

func (l *TcpTunnelListenComponent) SendPacket(packet *Packet, metadata any) error {
	conn, ok := metadata.(net.Conn)
	if !ok {
		return fmt.Errorf("%s: Invalid connection type", l.tag)
	}

	if conn == nil {
		return fmt.Errorf("%s: Connection is nil", l.tag)
	}

	_, err := conn.Write(packet.GetData())
	if err != nil {
		logger.Infof("%s: Failed to send packet: %v", l.tag, err)
		return err
	}

	return nil
}

func (l *TcpTunnelListenComponent) Stop() error {
	close(l.stopCh)

	logger.Infof("%s: Stopped listening on %s", l.tag, l.listenAddr)
	return nil
}
