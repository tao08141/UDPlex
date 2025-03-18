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
    ListenAddr      string        `json:"listen_addr"`
    ForwardAddr     string        `json:"forward_addr"`
    BufferSize      int           `json:"buffer_size"`
    TimeoutSec      int           `json:"timeout"`
    Timeout         time.Duration `json:"-"`
    ReplaceOldConns bool          `json:"replace_old_conns"`
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

    configPath := flag.String("c", "config.json", "Path to configuration file")
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
    activeClients := make(map[string]struct{})
    var mutex sync.RWMutex

    go func() {
        ticker := time.NewTicker(config.Timeout / 2)
        defer ticker.Stop()

        for range ticker.C {
            now := time.Now()
            
            mutex.Lock()
            // Process activity markers and update timestamps in batch
            for addrString := range activeClients {
                if mapping, exists := mappings[addrString]; exists {
                    mapping.lastActive = now
                    mappings[addrString] = mapping
                }
            }
            // Clear activity tracking for next cycle
            activeClients = make(map[string]struct{})
            
            // Remove inactive mappings
            for addrString, mapping := range mappings {
                if now.Sub(mapping.lastActive) > config.Timeout {
                    delete(mappings, addrString)
                    log.Printf("Removed inactive mapping: %s", addrString)
                }
            }

            mutex.Unlock()
        }
    }()

    // Handle responses from forwarded connection
    go func() {

        for {
            buffer := bufferPool.Get().([]byte)
            respLen, _, err := forwardConn.ReadFrom(buffer)
            if err != nil {
                log.Printf("Error reading from forwarding conn: %v", err)
                bufferPool.Put(buffer)
                continue
            }

            var toDelete []string
            mutex.RLock()
            responseData := buffer[:respLen]
            for _, mapping := range mappings {
                if _, err = listenConn.WriteTo(responseData, mapping.addr); err != nil {
                    log.Printf("Response error: %v", err)
                    toDelete = append(toDelete, mapping.addr.String())
                }
            }
            mutex.RUnlock()

            bufferPool.Put(buffer)

            if len(toDelete) > 0 {
                mutex.Lock()
                for _, addrStr := range toDelete {
                    delete(mappings, addrStr)
                }
                mutex.Unlock()
            }
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
        
        mutex.RLock()
        _, exists := mappings[addrKey]
        mutex.RUnlock()
        
        if !exists {
            mutex.Lock()

            if config.ReplaceOldConns {
                addrIP := addr.(*net.UDPAddr).IP.String()
                
                for key, mapping := range mappings {
                    if mapping.addr.(*net.UDPAddr).IP.String() == addrIP {
                        log.Printf("Replacing old mapping: %s", mapping.addr.String())
                        delete(mappings, key)
                    }
                }
            }

            if _, ok := mappings[addrKey]; !ok {
                log.Printf("New mapping: %s", addr.String())
                mappings[addrKey] = AddrMapping{addr: addr, lastActive: time.Now()}
            }
            mutex.Unlock()
        }

        activeClients[addrKey] = struct{}{}
        

        if _, err = forwardConn.Write(buffer[:length]); err != nil {
            log.Printf("Forward write error: %v", err)
        }
        bufferPool.Put(buffer)
    }
}