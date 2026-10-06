package main

import (
	"context"
	"net"
	"net/netip"
	"strconv"
	"sync"
	"testing"
	"time"

	ovpn "github.com/sagernet/sing-openvpn"
)

// startDuplicatingUDPProxy relays datagrams between one client and target and
// sends every datagram twice in both directions, like redundant lines do.
func startDuplicatingUDPProxy(t *testing.T, target string) string {
	t.Helper()
	conn, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	upstream, err := net.Dial("udp", target)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close(); upstream.Close() })
	var mu sync.Mutex
	var client net.Addr
	go func() {
		buf := make([]byte, 65535)
		for {
			n, addr, err := conn.ReadFrom(buf)
			if err != nil {
				return
			}
			mu.Lock()
			client = addr
			mu.Unlock()
			upstream.Write(buf[:n])
			upstream.Write(buf[:n])
		}
	}()
	go func() {
		buf := make([]byte, 65535)
		for {
			n, err := upstream.Read(buf)
			if err != nil {
				return
			}
			mu.Lock()
			addr := client
			mu.Unlock()
			if addr != nil {
				conn.WriteTo(buf[:n], addr)
				conn.WriteTo(buf[:n], addr)
			}
		}
	}()
	return conn.LocalAddr().String()
}

// TestOpenVPNDuplicatedDatagrams checks that the handshake completes and that
// no data packet is delivered twice when every datagram arrives twice, as with
// a relay that sends each packet over two lines.
func TestOpenVPNDuplicatedDatagrams(t *testing.T) {
	pki := newTestOpenVPNPKI(t)
	router := NewRouter(Config{BufferSize: 2048, QueueSize: 1024})

	listenAddr := freeUDPAddr(t)
	listen := NewListenComponent(ComponentConfig{
		Tag:           "access",
		ListenAddr:    listenAddr,
		Timeout:       60,
		Detour:        []string{"ovpn"},
		BroadcastMode: boolPtr(false),
	}, router)
	server, err := NewOpenVPNComponent(OpenVPNComponentConfig{
		Tag:       "ovpn",
		BindMode:  "udplex",
		Addresses: []string{"10.9.0.1/24"},
		CA:        pki.ca,
		Cert:      pki.serverCert,
		Key:       pki.serverKey,
	}, router)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []Component{listen, server} {
		if err := router.Register(c); err != nil {
			t.Fatal(err)
		}
	}
	if err := listen.Start(); err != nil {
		t.Fatal(err)
	}
	defer listen.Stop()
	if _, err := server.startServer(); err != nil {
		server.closeRuntime()
		t.Fatal(err)
	}
	defer server.closeRuntime()

	proxy := startDuplicatingUDPProxy(t, listenAddr)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	configurations := make(chan ovpn.TunnelConfiguration, 4)
	host, port, _ := net.SplitHostPort(proxy)
	portNumber, _ := strconv.Atoi(port)
	client, err := ovpn.NewClient(ovpn.ClientOptions{
		Context: ctx,
		Mode:    ovpn.ModeTLS,
		Transport: ovpn.ClientTransportOptions{
			Remotes:  []ovpn.Remote{{Host: host, Port: uint16(portNumber), Protocol: "udp"}},
			Protocol: "udp",
			DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
				var dialer net.Dialer
				return dialer.DialContext(ctx, network, address)
			},
		},
		TLS: ovpn.ClientTLSOptions{
			CertificateAuthority: openVPNMaterial(pki.ca),
			Certificate:          openVPNMaterial(pki.clientCert),
			Key:                  openVPNMaterial(pki.clientKey),
			RemoteCertificateTLS: "server",
		},
		Pull:         ovpn.ClientPullOptions{Enabled: true},
		KeyDirection: -1,
		Logger:       openVPNLogger{tag: "test-client"},
		OnTunnelConfiguration: func(event ovpn.TunnelConfigurationEvent) error {
			configurations <- event.Configuration
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Start(); err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if err := client.WaitReady(ctx); err != nil {
		t.Fatalf("handshake with duplicated datagrams: %v", err)
	}
	var tunnel ovpn.TunnelConfiguration
	select {
	case tunnel = <-configurations:
	case <-ctx.Done():
		t.Fatal("no tunnel configuration")
	}
	clientIP := tunnel.LocalIPv4[0].Addr()
	serverIP := netip.MustParseAddr("10.9.0.1")

	const count = 50
	for i := 0; i < count; i++ {
		if err := client.WriteDataPacket(testIPv4Packet(clientIP, serverIP, strconv.Itoa(i))); err != nil {
			t.Fatal(err)
		}
	}
	upstream := map[string]int{}
	readCtx, readCancel := context.WithTimeout(ctx, 2*time.Second)
	for {
		packets, err := server.server.ReadDataPackets(readCtx)
		if err != nil {
			break
		}
		for _, p := range packets {
			upstream[string(p.Buffer.Bytes()[28:])]++
		}
		releaseServerDataBuffers(packets)
	}
	readCancel()

	for i := 0; i < count; i++ {
		if _, err := server.server.WriteDataPacketsByDestination([][]byte{testIPv4Packet(serverIP, clientIP, strconv.Itoa(i))}); err != nil {
			t.Fatal(err)
		}
	}
	downstream := map[string]int{}
	readCtx, readCancel = context.WithTimeout(ctx, 2*time.Second)
	for {
		packet, err := client.ReadDataPacket(readCtx)
		if err != nil {
			break
		}
		downstream[string(packet[28:])]++
	}
	readCancel()

	for name, got := range map[string]map[string]int{"client->server": upstream, "server->client": downstream} {
		dups := 0
		for _, n := range got {
			dups += n - 1
		}
		if len(got) != count || dups != 0 {
			t.Errorf("%s: %d distinct packets, %d duplicates; want %d and 0", name, len(got), dups, count)
		}
	}
}
