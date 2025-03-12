package main

import (
    "fmt"
    "net"
    "sync"
    "time"
)

const (
    localAddr   = ":51888" // 服务端监听的地址
    forwardAddr = "127.0.0.1:51866" // 转发的本地地址
    bufferSize  = 4096    // 缓冲区大小
    timeout     = 120 * time.Second // 超时时间，每个连接超过这个时间没有活动将被清理
)

// AddrMapping 保存每个映射地址及最后活跃时间
type AddrMapping struct {
    addr      net.Addr
    lastActive time.Time
}



func main() {
    listenConn, err := net.ListenPacket("udp", localAddr)
    if err != nil {
        fmt.Println("Failed to set up packet listener for server:", err)
        return
    }
    defer listenConn.Close()

    fmt.Printf("Server is listening on %s\n", localAddr)


    addr, err := net.ResolveUDPAddr("udp", forwardAddr)
    if err != nil {
        fmt.Println("解析地址失败:", err)
        return
    }

    forwardConn, err := net.DialUDP("udp", nil, addr)
    if err != nil {
        fmt.Println("Failed to set up packet dialer for forwarding:", err)
        return
    }
    defer forwardConn.Close()

    handleServerPackets(listenConn, forwardConn)
}

func handleServerPackets(listenConn net.PacketConn, forwardConn *net.UDPConn) {
    mappings := make(map[string]AddrMapping)
    var mutex sync.Mutex

    go func() {

        for {
            buffer := make([]byte, bufferSize)
            // 读取转发端的响应
            respLen, _, err := forwardConn.ReadFrom(buffer)
            if err != nil {
                fmt.Println("Error reading from forwarding conn:", err)
                continue
            }

            mutex.Lock()
            for _, origAddr := range mappings {
                // 将来自外部端口的响应发送回所有已知的原始地址
                _, err = listenConn.WriteTo(buffer[:respLen], origAddr.addr)
                if err != nil {
                    fmt.Println("Error sending response to original addr:", err)
                    continue
                }
            }
            mutex.Unlock()
        }
    }()


    for {
        // 设置读取超时为无限期等待，实现超时逻辑在独立goroutine中
        //err := listenConn.SetReadDeadline(time.Time{})
        //if err != nil {
        //  fmt.Println("Failed to unset read deadline:", err)
        //}
        buffer := make([]byte, bufferSize)

        length, addr, err := listenConn.ReadFrom(buffer)
        if err != nil {
            fmt.Println("Server read error:", err)
            continue
        }

        mutex.Lock()
        // 记录源地址和外部地址之间的映射，并更新最后活跃时间
        mappings[addr.String()] = AddrMapping{addr: addr, lastActive: time.Now()}
        mutex.Unlock()



            // 转发到forwardAddr
        _, err = forwardConn.Write(buffer[:length])
        if err != nil {
            fmt.Println("Error forwarding packet:", err)
            continue
        }


        // 启动独立goroutine去清理超时连接
        go clearStaleMappings(&mappings, &mutex)
    }
}

func clearStaleMappings(mappings *map[string]AddrMapping, mutex *sync.Mutex) {
    mutex.Lock()
    defer mutex.Unlock()

    for addrString, mapping := range *mappings {
        if time.Since(mapping.lastActive) > timeout {
            delete(*mappings, addrString)
        }
    }
}

