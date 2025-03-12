package main

import (
    "encoding/json"
    "fmt"
    "net"
    "os"
    "sync"
    "sync/atomic"
    "time"
)

type Config struct {
    ListenAddr string   `json:"listen_addr"`
    Forwarders []string `json:"forwarders"`
    BufferSize int      `json:"buffer_size"` // Exported fields for proper JSON unmarshaling
    QueueSize  int      `json:"queue_size"`
}

var (
    config *Config
    // Buffer pool to reduce memory allocations
    bufferPool = sync.Pool{
        New: func() interface{} {
            return make([]byte, 1500) // Default size, will be adjusted after config is loaded
        },
    }
)

func loadConfig(filename string) (*Config, error) {
    configFile, err := os.ReadFile(filename)
    if err != nil {
        return nil, fmt.Errorf("error reading config file: %w", err)
    }

    config := &Config{
        BufferSize: 1500, // Default values
        QueueSize:  1024,
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
    conn        *net.UDPConn
    sendQueue   chan []byte
    isConnected int32 // Atomic flag for connection status
}

func main() {
    var err error
    config, err = loadConfig("config.json")
    if err != nil {
        panic(err)
    }

    // Update buffer pool size based on config
    bufferPool = sync.Pool{
        New: func() interface{} {
            return make([]byte, config.BufferSize)
        },
    }

    listenAddr, err := net.ResolveUDPAddr("udp", config.ListenAddr)
    if err != nil {
        fmt.Printf("Failed to resolve local address: %v\n", err)
        return
    }
    
    listenConn, err := net.ListenUDP("udp", listenAddr)
    if err != nil {
        fmt.Printf("Failed to listen on %s: %v\n", config.ListenAddr, err)
        return
    }
    defer listenConn.Close()

    fmt.Printf("Client listening on %s and will forward packets to: %v\n", config.ListenAddr, config.Forwarders)
    fmt.Printf("BufferSize: %d, QueueSize: %d\n", config.BufferSize, config.QueueSize)

    // Pre-initialize connections
    forwardConns := make(map[string]*ForwardConn)
    var forwardMapMutex sync.RWMutex

    // Setup response handler
    returnAddrMutex := &sync.RWMutex{}
    var returnAddr *net.UDPAddr
    responseChan := make(chan packetData, config.QueueSize)

    // Response writer goroutine
    go func() {
        for packet := range responseChan {
            returnAddrMutex.RLock()
            currentReturnAddr := returnAddr
            returnAddrMutex.RUnlock()
            
            if currentReturnAddr != nil {
                listenConn.WriteTo(packet.data, currentReturnAddr)
            }
            
            // Return buffer to pool
            bufferPool.Put(packet.buffer)
        }
    }()

    // Initialize forwarders
    for _, addr := range config.Forwarders {
        conn, err := setupForwarder(addr, responseChan)
        if err != nil {
            fmt.Printf("Failed to initialize forwarder %s: %v\n", addr, err)
            continue
        }
        forwardConns[addr] = conn
    }

    // Main packet processing loop
    for {
        buffer := bufferPool.Get().([]byte)
        length, addr, err := listenConn.ReadFromUDP(buffer)
        if err != nil {
            fmt.Printf("Error reading from UDP: %v\n", err)
            bufferPool.Put(buffer)
            continue
        }

        if length == 0 {
            bufferPool.Put(buffer)
            continue
        }

        returnAddrMutex.Lock()
        returnAddr = addr
        returnAddrMutex.Unlock()

        // Create a copy of the actual data
        data := make([]byte, length)
        copy(data, buffer[:length])
        
        // Forward packet to all destinations
        forwardMapMutex.RLock()
        for addr, conn := range forwardConns {
            if atomic.LoadInt32(&conn.isConnected) == 1 {
                select {
                case conn.sendQueue <- data:
                    // Successfully queued
                default:
                    fmt.Printf("Queue full for %s, dropping packet\n", addr)
                }
            }
        }
        forwardMapMutex.RUnlock()
        
        // Return the read buffer to the pool
        bufferPool.Put(buffer)
    }
}

type packetData struct {
    data   []byte
    buffer []byte // Original buffer for returning to pool
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
        conn:        conn,
        sendQueue:   make(chan []byte, config.QueueSize),
        isConnected: 1,
    }

    // Writer goroutine
    go func() {
        for data := range forwardConn.sendQueue {
            _, err := conn.Write(data)
            if err != nil {
                fmt.Printf("Error writing to %s: %v\n", remoteAddr, err)
                atomic.StoreInt32(&forwardConn.isConnected, 0)
                conn.Close()
                return
            }
        }
    }()

    // Reader goroutine
    go func() {
        for atomic.LoadInt32(&forwardConn.isConnected) == 1 {
            buffer := bufferPool.Get().([]byte)
            conn.SetReadDeadline(time.Now().Add(30 * time.Second))
            length, err := conn.Read(buffer)
            
            if err != nil {
                bufferPool.Put(buffer)
                if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
                    // Just a timeout, continue
                    continue
                }
                
                fmt.Printf("Error reading from %s: %v\n", remoteAddr, err)
                atomic.StoreInt32(&forwardConn.isConnected, 0)
                conn.Close()
                return
            }
            
            if length > 0 {
                // Copy data to avoid race conditions
                data := make([]byte, length)
                copy(data, buffer[:length])
                
                responseChan <- packetData{
                    data:   data,
                    buffer: buffer,
                }
            } else {
                bufferPool.Put(buffer)
            }
        }
    }()

    return forwardConn, nil
}