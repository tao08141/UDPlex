package main

import (
    "encoding/json"
    "fmt"
    "log"
    "net"
    "os"
    "sync"
    "sync/atomic"
    "time"
)

type Config struct {
    ListenAddr          string   `json:"listen_addr"`
    Forwarders          []string `json:"forwarders"`
    BufferSize          int      `json:"buffer_size"`
    QueueSize           int      `json:"queue_size"`
    ReconnectInterval   int      `json:"reconnect_interval"`    // Seconds between reconnection attempts
    ConnectionCheckTime int      `json:"connection_check_time"` // Seconds between connection checks
}

var (
    config *Config
    // Buffer pool to reduce memory allocations
    bufferPool = sync.Pool{
        New: func() interface{} {
            return make([]byte, 1500) // Default size, will be adjusted after config is loaded
        },
    }
    returnAddr atomic.Value // Stores *net.UDPAddr
)

func loadConfig(filename string) (*Config, error) {
    configFile, err := os.ReadFile(filename)
    if err != nil {
        return nil, fmt.Errorf("error reading config file: %w", err)
    }

    config := &Config{
        BufferSize:          1500, // Default values
        QueueSize:           1024,
        ReconnectInterval:   5,    // Default 5 seconds between reconnect attempts
        ConnectionCheckTime: 30,   // Default 30 seconds between connection checks
    }

    if err := json.Unmarshal(configFile, config); err != nil {
        return nil, fmt.Errorf("error parsing config: %w", err)
    }

    if config.ListenAddr == "" || len(config.Forwarders) == 0 {
        return nil, fmt.Errorf("invalid config")
    }

    return config, nil
}

type ForwardConn struct {
    conn                *net.UDPConn
    sendQueue           chan []byte
    isConnected         int32     // Atomic flag for connection status: 0=disconnected, 1=connected
    remoteAddr          string    // Store the remote address for reconnection
    udpAddr             *net.UDPAddr
    lastReconnectAttempt time.Time
    reconnectMutex      sync.Mutex
}

func main() {
    log.SetFlags(log.Ldate | log.Ltime | log.Lmicroseconds | log.Lshortfile)
    
    configPath := flag.String("c", "config.json", "path to configuration file")
    flag.Parse()
    
    var err error
    config, err = loadConfig(*configPath)
    if err != nil {
        log.Fatalf("Failed to load configuration: %v", err)
    }

    bufferPool = sync.Pool{
        New: func() interface{} {
            return make([]byte, config.BufferSize)
        },
    }

    listenAddr, err := net.ResolveUDPAddr("udp", config.ListenAddr)
    if err != nil {
        log.Printf("Failed to resolve local address: %v", err)
        return
    }
    
    listenConn, err := net.ListenUDP("udp", listenAddr)
    if err != nil {
        log.Printf("Failed to listen on %s: %v", config.ListenAddr, err)
        return
    }
    defer listenConn.Close()

    log.Printf("Client listening on %s and will forward packets to: %v", config.ListenAddr, config.Forwarders)
    log.Printf("BufferSize: %d, QueueSize: %d", config.BufferSize, config.QueueSize)

    // Pre-initialize connections
    forwardConns := make(map[string]*ForwardConn)
    var forwardConnList []*ForwardConn

    returnAddr.Store((*net.UDPAddr)(nil))
    responseChan := make(chan packetData, config.QueueSize)

    go func() {
        for packet := range responseChan {
            currentReturnAddr := returnAddr.Load().(*net.UDPAddr)
            if currentReturnAddr != nil {
                listenConn.WriteTo(packet.data, currentReturnAddr)
            }
            bufferPool.Put(packet.buffer)
        }
    }()

    for _, addr := range config.Forwarders {
        conn, err := setupForwarder(addr, responseChan)
        if err != nil {
            log.Printf("Failed to initialize forwarder %s: %v", addr, err)
            continue
        }
        forwardConns[addr] = conn
        forwardConnList = append(forwardConnList, conn)
    }

    go func() {
        for {
            time.Sleep(time.Duration(config.ConnectionCheckTime) * time.Second)
            for _, conn := range forwardConnList {
                if atomic.LoadInt32(&conn.isConnected) == 0 {
                    go tryReconnect(conn, responseChan)
                } else {
                    select {
                    case conn.sendQueue <- []byte{0}:
                    default:
                    }
                }
            }
        }
    }()

    for {
        buffer := make([]byte, config.BufferSize)
        length, addr, err := listenConn.ReadFromUDP(buffer)
        if err != nil {
            log.Printf("Error reading from UDP: %v", err)
            continue
        }

        if length == 0 {
            continue
        }

        returnAddr.Store(addr)

        data := buffer[:length]
        
        for _, conn := range forwardConnList {
            if atomic.LoadInt32(&conn.isConnected) == 1 {
                select {
                case conn.sendQueue <- data:
                default:
                    log.Printf("Queue full for %s, dropping packet", conn.remoteAddr)
                }
            }
        }
    }
}

type packetData struct {
    data   []byte
    buffer []byte
}

func tryReconnect(conn *ForwardConn, responseChan chan<- packetData) {
    conn.reconnectMutex.Lock()
    defer conn.reconnectMutex.Unlock()

    if time.Since(conn.lastReconnectAttempt) < time.Duration(config.ReconnectInterval)*time.Second {
        return
    }
    
    conn.lastReconnectAttempt = time.Now()
    
    if conn.conn != nil {
        conn.conn.Close()
        conn.conn = nil
    }
    
    log.Printf("Attempting to reconnect to %s", conn.remoteAddr)
    
    newConn, err := net.DialUDP("udp", nil, conn.udpAddr)
    if err != nil {
        log.Printf("Reconnection to %s failed: %v", conn.remoteAddr, err)
        return
    }
    
    conn.conn = newConn
    atomic.StoreInt32(&conn.isConnected, 1)
    log.Printf("Successfully reconnected to %s", conn.remoteAddr)
    
    go readFromForwarder(conn, responseChan)
}

func readFromForwarder(conn *ForwardConn, responseChan chan<- packetData) {
    for atomic.LoadInt32(&conn.isConnected) == 1 {
        buffer := bufferPool.Get().([]byte)
        conn.conn.SetReadDeadline(time.Now().Add(time.Duration(config.ConnectionCheckTime) * time.Second))
        length, err := conn.conn.Read(buffer)
        
        if err != nil {
            bufferPool.Put(buffer)
            if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
                continue
            }
            
            log.Printf("Error reading from %s: %v", conn.remoteAddr, err)
            atomic.StoreInt32(&conn.isConnected, 0)
            return
        }
        
        if length > 0 {
            data := buffer[:length]
            responseChan <- packetData{data: data, buffer: buffer}

        } else {
            bufferPool.Put(buffer)
        }
    }
}

func setupForwarder(remoteAddr string, responseChan chan<- packetData) (*ForwardConn, error) {
    udpAddr, err := net.ResolveUDPAddr("udp", remoteAddr)
    if err != nil {
        return nil, fmt.Errorf("failed to resolve address %v: %v", remoteAddr, err)
    }

    conn, err := net.DialUDP("udp", nil, udpAddr)
    if err != nil {
        return nil, err
    }

    forwardConn := &ForwardConn{
        conn:                conn,
        sendQueue:           make(chan []byte, config.QueueSize),
        isConnected:         1,
        remoteAddr:          remoteAddr,
        udpAddr:             udpAddr,
        lastReconnectAttempt: time.Now(),
    }

    go func() {
        for data := range forwardConn.sendQueue {
            if atomic.LoadInt32(&forwardConn.isConnected) == 0 {
                continue
            }
            
            currentConn := forwardConn.conn
            if currentConn == nil {
                continue
            }
            
            _, err := currentConn.Write(data)
            if err != nil {
                log.Printf("Error writing to %s: %v", remoteAddr, err)
                atomic.StoreInt32(&forwardConn.isConnected, 0)
            }
        }
    }()

    go readFromForwarder(forwardConn, responseChan)

    return forwardConn, nil
}