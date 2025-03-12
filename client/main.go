package main

import (
    "encoding/json"
    "fmt"
    "net"
    "os"
    "sync"
    "time"
)

type Config struct {
    ListenAddr string   `json:"listen_addr"`
    Forwarders []string `json:"forwarders"`
    bufferSize int      `json:"buffer_size"`
    queueSize  int      `json:"queue_size"`
}

var config Config


func loadConfig(filename string) (*Config, error) {
	configFile, err := os.ReadFile(filename)
	if err != nil {
		return nil, fmt.Errorf("error reading config file: %w", err)
	}

	var config Config

    config.bufferSize = 1500
    config.queueSize = 1024

	if err := json.Unmarshal(configFile, &config); err != nil {
		return nil, fmt.Errorf("error parsing config: %w", err)
	}

	if config.ListenAddr == "" || len(config.Forwarders) == 0 {
		return nil, fmt.Errorf("invalid config")
	}

	return &config, nil
}

func main() {
	// 读取配置文件
    config, err := loadConfig("config.json")
	if err != nil {
		panic(err)
	}


    // 监听特定UDP端口
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

    fmt.Printf("Client listening on %s and will forward packets to remote IPs.\n", config.ListenAddr)

    forwardConnMap := sync.Map{}

    fmt.Printf("Client listening on %s and will forward packets to the following IPs: %v\n", config.ListenAddr, config.Forwarders)

    // 打印bufferSize和queueSize
    fmt.Printf("bufferSize: %d\n", config.bufferSize)
    fmt.Printf("queueSize: %d\n", config.queueSize)
    

    // 处理接收到的数据包并转发
    handlePackets(listenConn, &forwardConnMap, config.Forwarders)
}


// 用于并发安全地存储和获取发送用的UDP连接
func getForwardConn(forwardConnMap *sync.Map, remoteAddr string, listenPipe chan []byte) (chan []byte, error) {
    if pipe, ok := forwardConnMap.Load(remoteAddr); ok {
        return pipe.(chan []byte), nil
    }

    udpAddr, err := net.ResolveUDPAddr("udp", remoteAddr)
    if err != nil {
        return nil, fmt.Errorf("failed to resolve address %v: %v", remoteAddr, err)
    }

    // 为每个远程地址创建一个新的UDP连接
    conn, err := net.DialUDP("udp", nil, udpAddr)
    if err != nil {
        return nil, err
    }


    pipe := make(chan []byte, config.queueSize)

    // write
    go func() {
        for {
            data := <-pipe
            _, err := conn.Write(data)
            if err != nil {
                fmt.Printf("Error write from buffer: %v\n", err)
                conn.Close();
                forwardConnMap.Delete(remoteAddr)
                break
            }
        }
    }()

    // read
    go func() {
        for {
            returnBuffer := make([]byte, config.bufferSize)
            conn.SetDeadline(time.Now().Add(120 * time.Second))
            length, _, err := conn.ReadFrom(returnBuffer)
            if err != nil {
                fmt.Printf("Error reading from buffer: %v\n", err)
                conn.Close();
                pipe <- returnBuffer
                forwardConnMap.Delete(remoteAddr)
                break
            }
            listenPipe <- returnBuffer[:length]

        }
    }()


    forwardConnMap.Store(remoteAddr, pipe)
    return pipe, nil
}


// 处理接收到的UDP包
func handlePackets(listenConn *net.UDPConn, forwardConnMap *sync.Map, remoteAddrs []string) {

    listenPipe := make(chan []byte, config.queueSize)
    var returnAddr *net.UDPAddr

    go func() {
        for {
            data := <-listenPipe
            listenConn.WriteTo(data, returnAddr)
        }
    }()


    for {
        buffer := make([]byte, config.bufferSize)
        length, addr, err := listenConn.ReadFromUDP(buffer)
        if err != nil {
            fmt.Printf("Error reading from buffer: %v\n", err)
            continue
        }
        if addr.String() != returnAddr.String() {
            returnAddr = addr
        }
        
        if length == 0 {
            continue
        }

        // 为每个远程地址创建连接，并发送数据
        for _, remoteAddr := range remoteAddrs {
            forwardPipe, err := getForwardConn(forwardConnMap, remoteAddr, listenPipe)
            if err != nil {
                fmt.Printf("Error getting sender connection for %s: %v\n", remoteAddr, err)
                continue
            }

            forwardPipe <-buffer[:length]
        }
    }
}


