package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/binary"
	"encoding/pem"
	"math/big"
	"net"
	"net/netip"
	"strings"
	"testing"
	"time"

	ovpn "github.com/sagernet/sing-openvpn"
)

type testOpenVPNPKI struct {
	ca, serverCert, serverKey, clientCert, clientKey string
}

func newTestOpenVPNPKI(t *testing.T) testOpenVPNPKI {
	t.Helper()
	now := time.Now()
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	caTemplate := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "udplex-test-ca"},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	caCert, _ := x509.ParseCertificate(caDER)

	issue := func(serial int64, name string, usage x509.ExtKeyUsage) (string, string) {
		key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		template := &x509.Certificate{
			SerialNumber: big.NewInt(serial),
			Subject:      pkix.Name{CommonName: name},
			NotBefore:    now.Add(-time.Hour),
			NotAfter:     now.Add(time.Hour),
			KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyAgreement,
			ExtKeyUsage:  []x509.ExtKeyUsage{usage},
		}
		der, err := x509.CreateCertificate(rand.Reader, template, caCert, &key.PublicKey, caKey)
		if err != nil {
			t.Fatal(err)
		}
		keyDER, err := x509.MarshalPKCS8PrivateKey(key)
		if err != nil {
			t.Fatal(err)
		}
		return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})),
			string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}))
	}

	pki := testOpenVPNPKI{ca: string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER}))}
	pki.serverCert, pki.serverKey = issue(2, "server", x509.ExtKeyUsageServerAuth)
	pki.clientCert, pki.clientKey = issue(3, "client", x509.ExtKeyUsageClientAuth)
	return pki
}

func freeUDPAddr(t *testing.T) string {
	t.Helper()
	conn, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := conn.LocalAddr().String()
	_ = conn.Close()
	return addr
}

// testIPv4Packet builds a minimal IPv4/UDP packet.
func testIPv4Packet(src, dst netip.Addr, payload string) []byte {
	packet := make([]byte, 28+len(payload))
	packet[0] = 0x45
	binary.BigEndian.PutUint16(packet[2:], uint16(len(packet)))
	packet[8] = 64
	packet[9] = 17
	copy(packet[12:16], src.AsSlice())
	copy(packet[16:20], dst.AsSlice())
	var sum uint32
	for i := 0; i < 20; i += 2 {
		sum += uint32(binary.BigEndian.Uint16(packet[i:]))
	}
	for sum > 0xffff {
		sum = sum>>16 + sum&0xffff
	}
	binary.BigEndian.PutUint16(packet[10:], ^uint16(sum))
	binary.BigEndian.PutUint16(packet[20:], 40000)
	binary.BigEndian.PutUint16(packet[22:], 53)
	binary.BigEndian.PutUint16(packet[24:], uint16(8+len(payload)))
	copy(packet[28:], payload)
	return packet
}

// TestOpenVPNServerHandshakeAndData runs real OpenVPN handshakes and data
// exchanges against each transport. In udplex bind mode the client reaches the
// server through a listen component: client -> UDP -> listen -> openvpn.
func TestOpenVPNServerHandshakeAndData(t *testing.T) {
	t.Run("udplex", func(t *testing.T) { testOpenVPNServer(t, "udplex", "udp") })
	t.Run("native-udp", func(t *testing.T) { testOpenVPNServer(t, "native", "udp") })
	t.Run("native-tcp", func(t *testing.T) { testOpenVPNServer(t, "native", "tcp") })
}

func testOpenVPNServer(t *testing.T, bindMode, proto string) {
	pki := newTestOpenVPNPKI(t)
	router := NewRouter(Config{BufferSize: 2048, QueueSize: 1024})

	cfg := OpenVPNComponentConfig{
		Tag:       "ovpn",
		BindMode:  bindMode,
		Proto:     proto,
		Addresses: []string{"10.9.0.1/24"},
		CA:        pki.ca,
		Cert:      pki.serverCert,
		Key:       pki.serverKey,
		PushDNS:   []string{"1.1.1.1"},
	}
	var remoteAddr string
	var listen *ListenComponent
	switch {
	case bindMode == "udplex":
		remoteAddr = freeUDPAddr(t)
		listen = NewListenComponent(ComponentConfig{
			Tag:           "access",
			ListenAddr:    remoteAddr,
			Timeout:       60,
			Detour:        []string{"ovpn"},
			BroadcastMode: boolPtr(false),
		}, router)
		if err := router.Register(listen); err != nil {
			t.Fatal(err)
		}
	case proto == "tcp":
		remoteAddr = freeTCPAddr(t)
		cfg.ListenAddr = remoteAddr
	default:
		remoteAddr = freeUDPAddr(t)
		cfg.ListenAddr = remoteAddr
	}

	server, err := NewOpenVPNComponent(cfg, router)
	if err != nil {
		t.Fatalf("new openvpn component: %v", err)
	}
	if err := router.Register(server); err != nil {
		t.Fatal(err)
	}
	if listen != nil {
		if err := listen.Start(); err != nil {
			t.Fatalf("start listen: %v", err)
		}
		defer listen.Stop()
	}
	_, err = server.startServer()
	if err != nil {
		server.closeRuntime()
		t.Fatalf("start openvpn server: %v", err)
	}
	defer server.closeRuntime()

	clientCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	configurations := make(chan ovpn.TunnelConfiguration, 4)
	host, port, _ := net.SplitHostPort(remoteAddr)
	portNumber, _ := net.LookupPort(proto, port)
	client, err := ovpn.NewClient(ovpn.ClientOptions{
		Context: clientCtx,
		Mode:    ovpn.ModeTLS,
		Transport: ovpn.ClientTransportOptions{
			Remotes:  []ovpn.Remote{{Host: host, Port: uint16(portNumber), Protocol: proto}},
			Protocol: proto,
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
		t.Fatalf("new client: %v", err)
	}
	if err := client.Start(); err != nil {
		t.Fatalf("start client: %v", err)
	}
	defer client.Close()
	if err := client.WaitReady(clientCtx); err != nil {
		t.Fatalf("client not ready: %v", err)
	}

	var tunnel ovpn.TunnelConfiguration
	select {
	case tunnel = <-configurations:
	case <-clientCtx.Done():
		t.Fatal("no tunnel configuration")
	}
	if len(tunnel.LocalIPv4) == 0 {
		t.Fatalf("no address pushed: %+v", tunnel)
	}
	clientIP := tunnel.LocalIPv4[0].Addr()
	if !netip.MustParsePrefix("10.9.0.0/24").Contains(clientIP) || clientIP == netip.MustParseAddr("10.9.0.1") {
		t.Fatalf("unexpected client address %s", clientIP)
	}
	if len(tunnel.DNS) != 1 || tunnel.DNS[0].String() != "1.1.1.1" {
		t.Fatalf("dns not pushed: %v", tunnel.DNS)
	}
	if server.activeClients.Load() != 1 {
		t.Fatalf("active clients = %d, want 1", server.activeClients.Load())
	}

	// Client -> server.
	serverIP := netip.MustParseAddr("10.9.0.1")
	if err := client.WriteDataPacket(testIPv4Packet(clientIP, serverIP, "ping")); err != nil {
		t.Fatalf("client write: %v", err)
	}
	packets, err := server.server.ReadDataPackets(clientCtx)
	if err != nil {
		t.Fatalf("server read: %v", err)
	}
	got := string(packets[0].Buffer.Bytes()[28:])
	if packets[0].Buffer.Start() < openVPNTunOffset {
		t.Fatalf("incoming packet headroom %d < %d", packets[0].Buffer.Start(), openVPNTunOffset)
	}
	releaseServerDataBuffers(packets)
	if got != "ping" {
		t.Fatalf("server got %q", got)
	}

	// Server -> client, routed by destination address.
	misses, err := server.server.WriteDataPacketsByDestination([][]byte{testIPv4Packet(serverIP, clientIP, "pong")})
	if err != nil || len(misses) != 0 {
		t.Fatalf("server write: misses=%v err=%v", misses, err)
	}
	reply, err := client.ReadDataPacket(clientCtx)
	if err != nil {
		t.Fatalf("client read: %v", err)
	}
	if !strings.HasSuffix(string(reply), "pong") {
		t.Fatalf("client got %q", reply)
	}

	// Unknown destination is a route miss, not an error.
	misses, err = server.server.WriteDataPacketsByDestination([][]byte{testIPv4Packet(serverIP, netip.MustParseAddr("10.9.0.200"), "x")})
	if err != nil || len(misses) != 1 {
		t.Fatalf("expected one route miss, misses=%v err=%v", misses, err)
	}
}

func TestOpenVPNConfigValidation(t *testing.T) {
	router := NewRouter(Config{})
	pki := newTestOpenVPNPKI(t)
	base := func() OpenVPNComponentConfig {
		return OpenVPNComponentConfig{Tag: "ovpn", ListenAddr: "0.0.0.0:1194", Addresses: []string{"10.9.0.1/24"}, CA: pki.ca, Cert: pki.serverCert, Key: pki.serverKey}
	}

	cases := map[string]func(*OpenVPNComponentConfig){
		"no listen_addr":     func(c *OpenVPNComponentConfig) { c.ListenAddr = "" },
		"bad bind mode":      func(c *OpenVPNComponentConfig) { c.BindMode = "bogus" },
		"tcp over udplex":    func(c *OpenVPNComponentConfig) { c.BindMode = "udplex"; c.Proto = "tcp" },
		"bad proto":          func(c *OpenVPNComponentConfig) { c.Proto = "sctp" },
		"no addresses":       func(c *OpenVPNComponentConfig) { c.Addresses = nil },
		"two ipv4 pools":     func(c *OpenVPNComponentConfig) { c.Addresses = []string{"10.9.0.1/24", "10.10.0.1/24"} },
		"no cert":            func(c *OpenVPNComponentConfig) { c.Cert = "" },
		"no ca and no users": func(c *OpenVPNComponentConfig) { c.CA = "" },
		"two control wraps":  func(c *OpenVPNComponentConfig) { c.TLSCrypt = "a"; c.TLSAuth = "b" },
		"bad key direction":  func(c *OpenVPNComponentConfig) { c.TLSAuth = "a"; c.KeyDirection = new(int); *c.KeyDirection = 2 },
		"bad push route":     func(c *OpenVPNComponentConfig) { c.PushRoutes = []string{"nope"} },
		"bad verify policy":  func(c *OpenVPNComponentConfig) { c.VerifyClientCertificate = "maybe" },
	}
	for name, mutate := range cases {
		cfg := base()
		mutate(&cfg)
		if _, err := NewOpenVPNComponent(cfg, router); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}

	cfg := base()
	cfg.CA = ""
	cfg.Users = []OpenVPNUserConfig{{Username: "alice", Password: "secret"}}
	cfg.RedirectGateway = true
	component, err := NewOpenVPNComponent(cfg, router)
	if err != nil {
		t.Fatalf("users without ca: %v", err)
	}
	options := component.serverOptions
	if options.TLS.VerifyClientCertificate != "none" || options.Authentication.Authenticator == nil {
		t.Fatalf("users without ca must skip client certificates: %+v", options.TLS)
	}
	if err := options.Authentication.Authenticator(context.Background(), "alice", "secret"); err != nil {
		t.Fatalf("valid user rejected: %v", err)
	}
	if err := options.Authentication.Authenticator(context.Background(), "alice", "wrong"); err == nil {
		t.Fatal("wrong password accepted")
	}
	if !options.Push.RedirectGateway || options.Push.PingInterval != 10*time.Second || options.Timing.PingRestart != 120*time.Second {
		t.Fatalf("unexpected push/timing: %+v %+v", options.Push, options.Timing)
	}
	if component.bindMode != openVPNBindModeNative || component.proto != "udp" || component.mtu != 1500 {
		t.Fatalf("unexpected defaults: %s %s %d", component.bindMode, component.proto, component.mtu)
	}
}

func TestOpenVPNPipeConnReadDeadline(t *testing.T) {
	router := NewRouter(Config{})
	component := &OpenVPNComponent{BaseComponent: NewBaseComponent("ovpn", router, time.Second), bindMode: openVPNBindModeUDPlex}
	pipe := newOpenVPNPipeConn(component)
	defer pipe.Close()

	_ = pipe.SetReadDeadline(time.Now().Add(20 * time.Millisecond))
	start := time.Now()
	_, _, err := pipe.ReadFrom(make([]byte, 64))
	if netErr, ok := err.(net.Error); !ok || !netErr.Timeout() {
		t.Fatalf("expected timeout error, got %v", err)
	}
	if time.Since(start) > time.Second {
		t.Fatal("deadline not honored")
	}

	packet := router.GetPacket("access")
	packet.SetLength(copy(packet.BufAtOffset(), "hello"))
	if err := pipe.enqueue(&packet); err == nil {
		t.Fatal("packet without connection id must be rejected")
	}
	packet.SetConnID(ConnIDFromUint64(7))
	if err := pipe.enqueue(&packet); err != nil {
		t.Fatal(err)
	}
	packet.Release(1)

	_ = pipe.SetReadDeadline(time.Time{})
	buffer := make([]byte, 64)
	n, addr, err := pipe.ReadFrom(buffer)
	if err != nil || string(buffer[:n]) != "hello" {
		t.Fatalf("read %q %v", buffer[:n], err)
	}
	if addr.String() != "udplex-0000000000000007" {
		t.Fatalf("unexpected peer address %s", addr)
	}

	_ = pipe.Close()
	if _, _, err := pipe.ReadFrom(buffer); err != net.ErrClosed {
		t.Fatalf("expected net.ErrClosed after close, got %v", err)
	}
}
