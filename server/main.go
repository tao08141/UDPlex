package main

import (
    "encoding/json"
    "flag"
    "fmt"
    "log"
    "net"
    "os"
    "sync"
    "time"
)

// AddrMapping stores each mapped address and its last active timestamp
type AddrMapping struct {
    addr        net.Addr
    lastActive  time.Time
}

type Config struct {
    ListenAddr  string `json:"listen_addr"`
    ForwardAddr string `json:"forward_addr"`
    BufferSize  int    `json:"buffer_size"`
    TimeoutSec  int    `json:"timeout"`
    Timeout     time.Duration `json:"-"`
}

var (
    bufferPool = sync.Pool{
        New: func() interface{} {
            return make([]byte, 1500)
        },
    }
)

func loadConfig(filename string) (*Config, error) {
    configFile, err := os.ReadFile(filename)
    if err != nil {
        return nil, fmt.Errorf("error reading config file: %w", err)
    }

    config := &Config{
        BufferSize: 1500,
        TimeoutSec: 60,
    }

    if err := json.Unmarshal(configFile, config); err != nil {
        return nil, fmt.Errorf("error parsing config: %w", err)
    }

    config.Timeout = time.Duration(config.TimeoutSec) * time.Second

    if config.ListenAddr == "" || config.ForwardAddr == "" {
        return nil, fmt.Errorf("invalid config")
    }

    return config, nil
}

func main() {
    log.SetFlags(log.Ldate | log.Ltime | log.Lmicroseconds | log.Lshortfile)
    configPath := flag.String("config", "config.json", "Path to configuration file")
    flag.Parse()

    // Load configuration
    config, err := loadConfig(*configPath)
    if err != nil {
        log.Fatalf("Failed to load config: %v", err)
    }

    // Update buffer pool size if configured differently
    if config.BufferSize > 0 && config.BufferSize != 1500 {
        bufferPool = sync.Pool{
            New: func() interface{} {
                return make([]byte, config.BufferSize)
            },
        }
    }

    listenConn, err := net.ListenPacket("udp", config.ListenAddr)
    if err != nil {
        log.Fatalf("Failed to set up packet listener for server: %v", err)
        return
    }
    defer listenConn.Close()

    log.Printf("Server is listening on %s", config.ListenAddr)

    addr, err := net.ResolveUDPAddr("udp", config.ForwardAddr)
    if err != nil {
        log.Fatalf("Failed to resolve address: %v", err)
        return
    }

    forwardConn, err := net.DialUDP("udp", nil, addr)
    if err != nil {
        log.Fatalf("Failed to set up packet dialer for forwarding: %v", err)
        return
    }
    defer forwardConn.Close()

    log.Printf("Listening on %s, forwarding to %s", config.ListenAddr, config.ForwardAddr)
    handleServerPackets(listenConn, forwardConn, config)
}

func handleServerPackets(listenConn net.PacketConn, forwardConn *net.UDPConn, config *Config) {
    mappings := make(map[string]AddrMapping)
    var mutex sync.RWMutex

    go func() {
        ticker := time.NewTicker(config.Timeout / 2)
        defer ticker.Stop()

        for range ticker.C {
            now := time.Now()
            
            mutex.Lock()
            for addrString, mapping := range mappings {
                if now.Sub(mapping.lastActive) > config.Timeout {
                    delete(mappings, addrString)
                    log.Printf("Removed inactive mapping: %s", mapping.addr.String())
                }
            }
            mutex.Unlock()
        }
    }()

    // Handle responses from forwarded connection
    go func() {

        clientAddrs := make([]net.Addr, 0, 100)
        for {
            buffer := bufferPool.Get().([]byte)
            respLen, _, err := forwardConn.ReadFrom(buffer)
            if err != nil {
                log.Printf("Error reading from forwarding conn: %v", err)
                bufferPool.Put(buffer)
                continue
            }

            clientAddrs = clientAddrs[:0]       
            mutex.RLock()
            for _, mapping := range mappings {
                clientAddrs = append(clientAddrs, mapping.addr)
            }
            mutex.RUnlock()

            responseData := buffer[:respLen]
            for _, clientAddr := range clientAddrs {
                if _, err = listenConn.WriteTo(responseData, clientAddr); err != nil {
                    log.Printf("Response error: %v", err)
                    mutex.Lock()
                    delete(mappings, clientAddr.String())
                    mutex.Unlock()
                }
            }

            bufferPool.Put(buffer)
        }
    }()

    // Handle incoming client packets
    for {
        buffer := bufferPool.Get().([]byte)
        length, addr, err := listenConn.ReadFrom(buffer)
        if err != nil {
            log.Printf("Client read error: %v", err)
            bufferPool.Put(buffer)
            continue
        }

        addrKey := addr.String()
        mutex.Lock()
        if entry, ok := mappings[addrKey]; ok {
            entry.lastActive = time.Now()
            mappings[addrKey] = entry
        } else {
            log.Printf("New mapping: %s", addr.String())
            mappings[addrKey] = AddrMapping{addr: addr, lastActive: time.Now()}
        }
        mutex.Unlock()

        if _, err = forwardConn.Write(buffer[:length]); err != nil {
            log.Printf("Forward write error: %v", err)
        }
        bufferPool.Put(buffer)
    }
}