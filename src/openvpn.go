package main

import (
	"context"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	ovpn "github.com/sagernet/sing-openvpn"
	"github.com/sagernet/sing/common/buf"
	F "github.com/sagernet/sing/common/format"
	wgtun "golang.zx2c4.com/wireguard/tun"
)

const (
	openVPNBindModeNative = "native"
	openVPNBindModeUDPlex = "udplex"

	// openVPNTunOffset is the room kept in front of packets for the TUN device
	// (virtio header on Linux, address family on BSD).
	openVPNTunOffset = 16

	// openVPNOutboundQueueSize is the number of batches queued per client
	// before packets to it are dropped.
	openVPNOutboundQueueSize = 256
)

// OpenVPNComponent embeds an OpenVPN server (sing-openvpn) with its own TUN
// interface. Clients connect directly (native bind mode) or through other
// components (udplex bind mode); their traffic leaves through the kernel, where
// policy routes can send it into another tunnel.
type OpenVPNComponent struct {
	BaseComponent

	bindMode            string
	proto               string
	listenAddr          string
	detour              []string
	reuseIncomingDetour bool
	interfaceName       string
	actualInterfaceName string
	mtu                 int
	addresses           []string
	routes              []string
	setupInterface      bool
	netConfig           TunNetConfig
	serverOptions       ovpn.ServerOptions

	tunDevice wgtun.Device
	server    *ovpn.Server
	pipe      atomic.Pointer[openVPNPipeConn] // udplex bind mode
	transport interface{ Close() error }
	netSetup  *tunNetSetup
	cancel    context.CancelFunc
	loops     sync.WaitGroup

	activeClients  atomic.Int64
	rxPackets      atomic.Uint64 // client -> interface
	txPackets      atomic.Uint64 // interface -> client
	droppedPackets atomic.Uint64
	routeMisses    atomic.Uint64
}

func NewOpenVPNComponent(cfg OpenVPNComponentConfig, router *Router) (*OpenVPNComponent, error) {
	bindMode := strings.ToLower(strings.TrimSpace(cfg.BindMode))
	if bindMode == "" {
		bindMode = openVPNBindModeNative
	}
	proto := strings.ToLower(strings.TrimSpace(cfg.Proto))
	if proto == "" {
		proto = "udp"
	}

	switch bindMode {
	case openVPNBindModeNative:
		if strings.TrimSpace(cfg.ListenAddr) == "" {
			return nil, fmt.Errorf("%s: listen_addr is required in native bind mode", cfg.Tag)
		}
		if proto != "udp" && proto != "tcp" {
			return nil, fmt.Errorf("%s: unknown proto %q, expected udp or tcp", cfg.Tag, proto)
		}
	case openVPNBindModeUDPlex:
		if proto != "udp" {
			return nil, fmt.Errorf("%s: udplex bind mode only carries proto udp", cfg.Tag)
		}
	default:
		return nil, fmt.Errorf("%s: unknown bind_mode %q, expected %q or %q", cfg.Tag, bindMode, openVPNBindModeNative, openVPNBindModeUDPlex)
	}

	mtu := cfg.MTU
	if mtu <= 0 {
		mtu = 1500
	}

	serverOptions, err := buildOpenVPNServerOptions(cfg, proto, mtu)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", cfg.Tag, err)
	}

	setupInterface := true
	if cfg.SetupInterface != nil {
		setupInterface = *cfg.SetupInterface
	}
	reuseIncomingDetour := true
	if cfg.ReuseIncomingDetour != nil {
		reuseIncomingDetour = *cfg.ReuseIncomingDetour
	}
	interfaceName := strings.TrimSpace(cfg.InterfaceName)
	if interfaceName == "" {
		interfaceName = cfg.Tag
	}

	return &OpenVPNComponent{
		BaseComponent:       NewBaseComponent(cfg.Tag, router, timeDurationOrDefault(cfg.SendTimeout, 500)),
		bindMode:            bindMode,
		proto:               proto,
		listenAddr:          strings.TrimSpace(cfg.ListenAddr),
		detour:              append([]string(nil), cfg.Detour...),
		reuseIncomingDetour: reuseIncomingDetour,
		interfaceName:       interfaceName,
		mtu:                 mtu,
		addresses:           append([]string(nil), cfg.Addresses...),
		routes:              append([]string(nil), cfg.Routes...),
		setupInterface:      setupInterface,
		netConfig:           cfg.TunNetConfig,
		serverOptions:       serverOptions,
	}, nil
}

func buildOpenVPNServerOptions(cfg OpenVPNComponentConfig, proto string, mtu int) (ovpn.ServerOptions, error) {
	if len(cfg.Addresses) == 0 {
		return ovpn.ServerOptions{}, errors.New("addresses is required, e.g. 10.9.0.1/24")
	}
	var pools, localAddresses []netip.Prefix
	var hasIPv4, hasIPv6 bool
	for _, raw := range cfg.Addresses {
		prefix, err := netip.ParsePrefix(strings.TrimSpace(raw))
		if err != nil {
			return ovpn.ServerOptions{}, fmt.Errorf("invalid address %q: %w", raw, err)
		}
		if prefix.Addr().Is4() {
			if hasIPv4 {
				return ovpn.ServerOptions{}, errors.New("only one IPv4 address pool is supported")
			}
			hasIPv4 = true
		} else {
			if hasIPv6 {
				return ovpn.ServerOptions{}, errors.New("only one IPv6 address pool is supported")
			}
			hasIPv6 = true
		}
		pools = append(pools, prefix)
		localAddresses = append(localAddresses, netip.PrefixFrom(prefix.Addr(), prefix.Addr().BitLen()))
	}

	certificate := openVPNMaterial(cfg.Cert)
	key := openVPNMaterial(cfg.Key)
	if !certificate.IsSet() || !key.IsSet() {
		return ovpn.ServerOptions{}, errors.New("cert and key are required")
	}

	verifyClientCertificate := strings.TrimSpace(cfg.VerifyClientCertificate)
	switch verifyClientCertificate {
	case "", "require", "optional", "none":
	default:
		return ovpn.ServerOptions{}, fmt.Errorf("invalid verify_client_certificate %q, expected require, optional or none", verifyClientCertificate)
	}
	if strings.TrimSpace(cfg.CA) == "" && verifyClientCertificate != "none" {
		if len(cfg.Users) == 0 {
			return ovpn.ServerOptions{}, errors.New("ca is required to verify client certificates, or set users with verify_client_certificate none")
		}
		verifyClientCertificate = "none"
	}

	tlsOptions := ovpn.ServerTLSOptions{
		CertificateAuthority:    openVPNMaterial(cfg.CA),
		Certificate:             certificate,
		Key:                     key,
		VerifyClientCertificate: verifyClientCertificate,
		CRLVerify:               strings.TrimSpace(cfg.CRLVerify),
	}
	if verifyClientCertificate != "none" {
		tlsOptions.RemoteCertificateTLS = "client"
	}

	keyDirection := -1
	wraps := 0
	if cfg.TLSAuth != "" {
		wraps++
		tlsOptions.Auth = openVPNMaterial(cfg.TLSAuth)
		if cfg.KeyDirection != nil {
			keyDirection = *cfg.KeyDirection
			if keyDirection != 0 && keyDirection != 1 {
				return ovpn.ServerOptions{}, errors.New("key_direction must be 0 or 1")
			}
		}
	}
	if cfg.TLSCrypt != "" {
		wraps++
		tlsOptions.Crypt = openVPNMaterial(cfg.TLSCrypt)
	}
	if cfg.TLSCryptV2 != "" {
		wraps++
		tlsOptions.CryptV2 = openVPNMaterial(cfg.TLSCryptV2)
	}
	if wraps > 1 {
		return ovpn.ServerOptions{}, errors.New("tls_auth, tls_crypt and tls_crypt_v2 are mutually exclusive")
	}

	topology := strings.TrimSpace(cfg.Topology)
	if topology == "" {
		topology = "subnet"
	}

	keepaliveInterval := cfg.KeepaliveInterval
	if keepaliveInterval <= 0 {
		keepaliveInterval = 10
	}
	keepaliveTimeout := cfg.KeepaliveTimeout
	if keepaliveTimeout <= 0 {
		keepaliveTimeout = 60
	}

	options := ovpn.ServerOptions{
		Mode:         ovpn.ModeTLS,
		KeyDirection: keyDirection,
		Transport:    ovpn.ServerTransportOptions{Protocol: proto},
		Resources:    ovpn.ServerResourceOptions{MaxClients: cfg.MaxClients},
		DataChannel: ovpn.ServerDataChannelOptions{
			MTU:            uint32(mtu),
			Ciphers:        append([]string(nil), cfg.DataCiphers...),
			FallbackCipher: cfg.DataCiphersFallback,
			Auth:           cfg.Auth,
		},
		TLS: tlsOptions,
		Authentication: ovpn.ServerAuthenticationOptions{
			Authenticator: openVPNAuthenticator(cfg.Users),
			DuplicateCN:   cfg.DuplicateCN,
		},
		// Like `keepalive interval timeout`: the server waits twice as long
		// as the timeout it pushes to clients.
		Timing: ovpn.ServerTimingOptions{
			PingInterval: time.Duration(keepaliveInterval) * time.Second,
			PingRestart:  time.Duration(keepaliveTimeout*2) * time.Second,
		},
		Tunnel: ovpn.ServerTunnelOptions{
			AddressPools: pools,
			Topology:     topology,
			LocalAddress: localAddresses,
		},
		Push: ovpn.ServerPushOptions{
			PingInterval: time.Duration(keepaliveInterval) * time.Second,
			PingRestart:  time.Duration(keepaliveTimeout) * time.Second,
		},
	}

	for _, raw := range cfg.PushRoutes {
		prefix, err := netip.ParsePrefix(strings.TrimSpace(raw))
		if err != nil {
			return ovpn.ServerOptions{}, fmt.Errorf("invalid push route %q: %w", raw, err)
		}
		options.Push.Routes = append(options.Push.Routes, prefix)
	}
	for _, raw := range cfg.PushDNS {
		addr, err := netip.ParseAddr(strings.TrimSpace(raw))
		if err != nil {
			return ovpn.ServerOptions{}, fmt.Errorf("invalid push dns %q: %w", raw, err)
		}
		options.Push.DNS = append(options.Push.DNS, addr)
	}
	if cfg.RedirectGateway {
		options.Push.RedirectGateway = true
		options.Push.RedirectGatewayFlags = []string{"def1"}
		if hasIPv6 {
			options.Push.RedirectGatewayFlags = append(options.Push.RedirectGatewayFlags, "ipv6")
		}
	}

	return options, nil
}

// openVPNMaterial takes inline PEM content or a file path.
func openVPNMaterial(value string) ovpn.Material {
	value = strings.TrimSpace(value)
	if value == "" {
		return ovpn.Material{}
	}
	if strings.Contains(value, "-----BEGIN") {
		return ovpn.Material{Content: []byte(value + "\n")}
	}
	return ovpn.Material{Path: value}
}

func openVPNAuthenticator(users []OpenVPNUserConfig) ovpn.UserPassAuthenticator {
	if len(users) == 0 {
		return nil
	}
	accounts := make(map[string][]byte, len(users))
	for _, user := range users {
		accounts[user.Username] = []byte(user.Password)
	}
	return func(ctx context.Context, username string, password string) error {
		expected, ok := accounts[username]
		if !ok || subtle.ConstantTimeCompare(expected, []byte(password)) != 1 {
			return errors.New("invalid username or password")
		}
		return nil
	}
}

func (o *OpenVPNComponent) Start() error {
	tunDevice, err := wgtun.CreateTUN(o.interfaceName, o.mtu)
	if err != nil {
		return fmt.Errorf("%s: failed to create openvpn interface %s: %w", o.tag, o.interfaceName, err)
	}
	actualName, err := tunDevice.Name()
	if err != nil {
		_ = tunDevice.Close()
		return fmt.Errorf("%s: failed to query openvpn interface name: %w", o.tag, err)
	}
	o.tunDevice = tunDevice
	o.actualInterfaceName = actualName

	if o.setupInterface {
		if err := configureTunInterface(o.tag, actualName, o.mtu, o.addresses, o.routes); err != nil {
			o.closeRuntime()
			return err
		}
	}

	ctx, err := o.startServer()
	if err != nil {
		o.closeRuntime()
		return err
	}

	o.loops.Add(2)
	go o.runServerToTun(ctx)
	go o.runTunToServer()

	where := o.listenAddr + "/" + o.proto
	if o.bindMode == openVPNBindModeUDPlex {
		where = "udplex pipeline"
	}
	logger.Infof("%s: OpenVPN server on %s started with interface %s", o.tag, where, actualName)
	return nil
}

// startServer opens the transport and starts the OpenVPN server. The caller
// cleans up with closeRuntime on error.
func (o *OpenVPNComponent) startServer() (context.Context, error) {
	options := o.serverOptions
	switch {
	case o.bindMode == openVPNBindModeUDPlex:
		pipe := newOpenVPNPipeConn(o)
		o.pipe.Store(pipe)
		o.transport = pipe
		options.Transport.PacketConn = pipe
	case o.proto == "tcp":
		listener, err := net.Listen("tcp", o.listenAddr)
		if err != nil {
			return nil, fmt.Errorf("%s: failed to listen on %s: %w", o.tag, o.listenAddr, err)
		}
		o.transport = listener
		options.Transport.Listener = listener
	default:
		packetConn, err := net.ListenPacket("udp", o.listenAddr)
		if err != nil {
			return nil, fmt.Errorf("%s: failed to listen on %s: %w", o.tag, o.listenAddr, err)
		}
		if udpConn, ok := packetConn.(*net.UDPConn); ok {
			_ = udpConn.SetReadBuffer(7 << 20)
			_ = udpConn.SetWriteBuffer(7 << 20)
		}
		o.transport = packetConn
		options.Transport.PacketConn = packetConn
	}

	ctx, cancel := context.WithCancel(context.Background())
	o.cancel = cancel
	options.Context = ctx
	options.Logger = openVPNLogger{tag: o.tag}
	options.IncomingPacketHeadroom = func() int { return openVPNTunOffset }
	options.NewOutboundQueue = o.newOutboundQueue

	server, err := ovpn.NewServer(options)
	if err != nil {
		return nil, fmt.Errorf("%s: failed to create openvpn server: %w", o.tag, err)
	}
	o.server = server
	if err := server.Start(); err != nil {
		return nil, fmt.Errorf("%s: failed to start openvpn server: %w", o.tag, err)
	}

	return ctx, nil
}

func (o *OpenVPNComponent) PostStart() error {
	// Interfaces of all components exist now, so policy routes may point at them.
	o.netSetup = newTunNetSetup(o.tag, o.actualInterfaceName)
	return o.netSetup.apply(o.netConfig, o.addresses)
}

func (o *OpenVPNComponent) Stop() error {
	close(o.GetStopChannel())
	if o.netSetup != nil {
		o.netSetup.teardown()
		o.netSetup = nil
	}
	o.closeRuntime()
	return nil
}

func (o *OpenVPNComponent) closeRuntime() {
	if o.cancel != nil {
		o.cancel()
	}
	if o.server != nil {
		_ = o.server.Close()
	}
	if o.transport != nil {
		_ = o.transport.Close()
	}
	if o.tunDevice != nil {
		// Closing the device unblocks runTunToServer.
		_ = o.tunDevice.Close()
	}
	o.loops.Wait()
	o.server = nil
	o.transport = nil
	o.pipe.Store(nil)
	o.tunDevice = nil
	o.cancel = nil
}

// runServerToTun writes decrypted client packets to the interface.
func (o *OpenVPNComponent) runServerToTun(ctx context.Context) {
	defer o.loops.Done()
	var bufs [][]byte
	for {
		packetBuffers, err := o.server.ReadDataPackets(ctx)
		if err != nil {
			if ctx.Err() == nil && !errors.Is(err, ovpn.ErrServerClosed) {
				logger.Errorf("%s: failed to read openvpn data packets: %v", o.tag, err)
			}
			return
		}

		bufs = bufs[:0]
		for i := range packetBuffers {
			packet := packetBuffers[i].Buffer
			if packet.Start() < openVPNTunOffset {
				// Not built with our headroom, copy it behind some.
				moved := buf.NewSize(openVPNTunOffset + packet.Len())
				moved.Advance(openVPNTunOffset)
				_, _ = moved.Write(packet.Bytes())
				packet.Release()
				packet = moved
				packetBuffers[i].Buffer = moved
			}
			packet.ExtendHeader(openVPNTunOffset)
			bufs = append(bufs, packet.Bytes())
		}

		if _, err := o.tunDevice.Write(bufs, openVPNTunOffset); err != nil {
			if errors.Is(err, os.ErrClosed) {
				releaseServerDataBuffers(packetBuffers)
				return
			}
			logger.Debugf("%s: failed to write to %s: %v", o.tag, o.actualInterfaceName, err)
		} else {
			o.rxPackets.Add(uint64(len(bufs)))
		}
		releaseServerDataBuffers(packetBuffers)
	}
}

func releaseServerDataBuffers(packetBuffers []ovpn.ServerDataBuffer) {
	for _, packetBuffer := range packetBuffers {
		packetBuffer.Buffer.Release()
	}
}

// runTunToServer sends packets read from the interface to the client owning
// their destination address.
func (o *OpenVPNComponent) runTunToServer() {
	defer o.loops.Done()
	batchSize := o.tunDevice.BatchSize()
	bufs := make([][]byte, batchSize)
	for i := range bufs {
		bufs[i] = make([]byte, openVPNTunOffset+o.mtu+256)
	}
	sizes := make([]int, batchSize)
	packets := make([][]byte, 0, batchSize)

	for {
		count, err := o.tunDevice.Read(bufs, sizes, openVPNTunOffset)
		if err != nil && count == 0 {
			if errors.Is(err, os.ErrClosed) || errors.Is(err, net.ErrClosed) {
				return
			}
			select {
			case <-o.GetStopChannel():
				return
			default:
			}
			if !errors.Is(err, wgtun.ErrTooManySegments) {
				logger.Debugf("%s: failed to read from %s: %v", o.tag, o.actualInterfaceName, err)
			}
			continue
		}

		packets = packets[:0]
		for i := 0; i < count; i++ {
			packets = append(packets, bufs[i][openVPNTunOffset:openVPNTunOffset+sizes[i]])
		}
		routeMisses, err := o.server.WriteDataPacketsByDestination(packets)
		if len(routeMisses) > 0 {
			o.routeMisses.Add(uint64(len(routeMisses)))
		}
		if err != nil {
			if errors.Is(err, ovpn.ErrServerClosed) {
				return
			}
			logger.Debugf("%s: failed to send packets to openvpn clients: %v", o.tag, err)
		}
	}
}

// HandlePacket takes OpenVPN datagrams from other components in udplex bind mode.
func (o *OpenVPNComponent) HandlePacket(packet *Packet) error {
	defer packet.Release(1)
	pipe := o.pipe.Load()
	if pipe == nil {
		if o.bindMode == openVPNBindModeNative {
			return fmt.Errorf("%s: native bind mode does not take packets from other components", o.tag)
		}
		return fmt.Errorf("%s: openvpn server is not running", o.tag)
	}
	return pipe.enqueue(packet)
}

// openVPNOutboundQueue hands packets of one client to a goroutine that
// encrypts and sends them, so a slow client (TCP) does not stall the others.
type openVPNOutboundQueue struct {
	component *OpenVPNComponent
	write     func(buffers []*buf.Buffer)
	batches   chan []*buf.Buffer
	done      chan struct{}
	closeOnce sync.Once
}

func (o *OpenVPNComponent) newOutboundQueue(write func(buffers []*buf.Buffer)) ovpn.OutboundQueue {
	queue := &openVPNOutboundQueue{
		component: o,
		write:     write,
		batches:   make(chan []*buf.Buffer, openVPNOutboundQueueSize),
		done:      make(chan struct{}),
	}
	o.activeClients.Add(1)
	go queue.run()
	return queue
}

func (q *openVPNOutboundQueue) WriteBuffers(buffers []*buf.Buffer) {
	// The caller reuses the slice.
	batch := append([]*buf.Buffer(nil), buffers...)
	select {
	case <-q.done:
		buf.ReleaseMulti(batch)
		return
	default:
	}
	select {
	case q.batches <- batch:
	default:
		q.component.droppedPackets.Add(uint64(len(batch)))
		buf.ReleaseMulti(batch)
	}
}

func (q *openVPNOutboundQueue) run() {
	for {
		select {
		case <-q.done:
			for {
				select {
				case batch := <-q.batches:
					buf.ReleaseMulti(batch)
				default:
					return
				}
			}
		case batch := <-q.batches:
			q.component.txPackets.Add(uint64(len(batch)))
			q.write(batch)
		}
	}
}

func (q *openVPNOutboundQueue) Close() error {
	q.closeOnce.Do(func() {
		close(q.done)
		q.component.activeClients.Add(-1)
	})
	return nil
}

// openVPNPeerAddr names a client reached through other components. The
// connection id is stable across lines, so it is the whole identity.
type openVPNPeerAddr struct {
	connID ConnID
}

func (a *openVPNPeerAddr) Network() string { return "udplex" }
func (a *openVPNPeerAddr) String() string  { return "udplex-" + hex.EncodeToString(a.connID[:]) }

type openVPNLocalAddr string

func (a openVPNLocalAddr) Network() string { return "udplex" }
func (a openVPNLocalAddr) String() string  { return "udplex:" + string(a) }

// openVPNPipeConn is the net.PacketConn of the OpenVPN server in udplex bind
// mode: it reads datagrams handed over by HandlePacket and routes written ones
// back to the component the client came from.
type openVPNPipeConn struct {
	component    *OpenVPNComponent
	rx           chan *Packet
	closed       chan struct{}
	closeOnce    sync.Once
	readDeadline atomic.Pointer[time.Time]

	returnAccess sync.Mutex
	returnTags   map[ConnID]openVPNReturnPath
}

type openVPNReturnPath struct {
	tag      string
	lastSeen time.Time
}

func newOpenVPNPipeConn(component *OpenVPNComponent) *openVPNPipeConn {
	queueSize := component.router.config.QueueSize
	if queueSize <= 0 {
		queueSize = 1024
	}
	return &openVPNPipeConn{
		component:  component,
		rx:         make(chan *Packet, queueSize),
		closed:     make(chan struct{}),
		returnTags: make(map[ConnID]openVPNReturnPath),
	}
}

func (c *openVPNPipeConn) enqueue(packet *Packet) error {
	if packet.ConnID() == (ConnID{}) {
		return fmt.Errorf("%s: packet has no connection id, enable auth or use a listen component", c.component.tag)
	}
	if c.component.reuseIncomingDetour && packet.SrcTag() != "" {
		c.rememberReturnPath(packet.ConnID(), packet.SrcTag())
	}

	select {
	case <-c.closed:
		return net.ErrClosed
	default:
	}
	packet.AddRef(1)
	select {
	case c.rx <- packet:
		return nil
	default:
		packet.Release(1)
		return fmt.Errorf("%s: openvpn receive queue is full", c.component.tag)
	}
}

func (c *openVPNPipeConn) rememberReturnPath(connID ConnID, tag string) {
	now := time.Now()
	c.returnAccess.Lock()
	defer c.returnAccess.Unlock()
	if path, ok := c.returnTags[connID]; ok && path.tag == tag && now.Sub(path.lastSeen) < time.Second {
		return
	}
	if len(c.returnTags) >= 4096 {
		for id, path := range c.returnTags {
			if now.Sub(path.lastSeen) > 10*time.Minute {
				delete(c.returnTags, id)
			}
		}
	}
	c.returnTags[connID] = openVPNReturnPath{tag: tag, lastSeen: now}
}

func (c *openVPNPipeConn) returnPath(connID ConnID) []string {
	if c.component.reuseIncomingDetour {
		c.returnAccess.Lock()
		path, ok := c.returnTags[connID]
		c.returnAccess.Unlock()
		if ok {
			return []string{path.tag}
		}
	}
	return c.component.detour
}

func (c *openVPNPipeConn) ReadFrom(b []byte) (int, net.Addr, error) {
	var timeout <-chan time.Time
	if deadline := c.readDeadline.Load(); deadline != nil && !deadline.IsZero() {
		wait := time.Until(*deadline)
		if wait <= 0 {
			return 0, nil, os.ErrDeadlineExceeded
		}
		timer := time.NewTimer(wait)
		defer timer.Stop()
		timeout = timer.C
	}

	select {
	case <-c.closed:
		return 0, nil, net.ErrClosed
	case <-timeout:
		return 0, nil, os.ErrDeadlineExceeded
	case packet := <-c.rx:
		n := copy(b, packet.GetData())
		addr := &openVPNPeerAddr{connID: packet.ConnID()}
		packet.Release(1)
		return n, addr, nil
	}
}

func (c *openVPNPipeConn) WriteTo(b []byte, addr net.Addr) (int, error) {
	peer, ok := addr.(*openVPNPeerAddr)
	if !ok {
		return 0, fmt.Errorf("%s: unexpected openvpn peer address %v", c.component.tag, addr)
	}
	select {
	case <-c.closed:
		return 0, net.ErrClosed
	default:
	}

	tags := c.returnPath(peer.connID)
	if len(tags) == 0 {
		return 0, fmt.Errorf("%s: no detour available for openvpn packet", c.component.tag)
	}

	router := c.component.router
	packet := router.GetPacket(c.component.tag)
	if len(b) > len(packet.BufAtOffset()) {
		packet.Release(1)
		return 0, fmt.Errorf("%s: openvpn packet of %d bytes exceeds buffer_size", c.component.tag, len(b))
	}
	copy(packet.BufAtOffset(), b)
	packet.SetLength(len(b))
	packet.SetConnID(peer.connID)
	err := router.Route(&packet, tags)
	packet.Release(1)
	if err != nil {
		return 0, err
	}
	return len(b), nil
}

func (c *openVPNPipeConn) Close() error {
	c.closeOnce.Do(func() {
		close(c.closed)
		for {
			select {
			case packet := <-c.rx:
				packet.Release(1)
			default:
				return
			}
		}
	})
	return nil
}

func (c *openVPNPipeConn) LocalAddr() net.Addr {
	return openVPNLocalAddr(c.component.tag)
}

func (c *openVPNPipeConn) SetDeadline(t time.Time) error {
	return c.SetReadDeadline(t)
}

func (c *openVPNPipeConn) SetReadDeadline(t time.Time) error {
	c.readDeadline.Store(&t)
	return nil
}

func (c *openVPNPipeConn) SetWriteDeadline(time.Time) error {
	return nil
}

// openVPNLogger forwards sing-openvpn logs to the UDPlex logger.
type openVPNLogger struct {
	tag string
}

func (l openVPNLogger) message(args []any) string {
	return l.tag + ": " + F.ToString(args...)
}

func (l openVPNLogger) Trace(args ...any) { logger.Debug(l.message(args)) }
func (l openVPNLogger) Debug(args ...any) { logger.Debug(l.message(args)) }
func (l openVPNLogger) Info(args ...any)  { logger.Info(l.message(args)) }
func (l openVPNLogger) Warn(args ...any)  { logger.Warn(l.message(args)) }
func (l openVPNLogger) Error(args ...any) { logger.Error(l.message(args)) }
func (l openVPNLogger) Fatal(args ...any) { logger.Error(l.message(args)) }
func (l openVPNLogger) Panic(args ...any) { logger.Error(l.message(args)) }

func (l openVPNLogger) TraceContext(_ context.Context, args ...any) { l.Trace(args...) }
func (l openVPNLogger) DebugContext(_ context.Context, args ...any) { l.Debug(args...) }
func (l openVPNLogger) InfoContext(_ context.Context, args ...any)  { l.Info(args...) }
func (l openVPNLogger) WarnContext(_ context.Context, args ...any)  { l.Warn(args...) }
func (l openVPNLogger) ErrorContext(_ context.Context, args ...any) { l.Error(args...) }
func (l openVPNLogger) FatalContext(_ context.Context, args ...any) { l.Fatal(args...) }
func (l openVPNLogger) PanicContext(_ context.Context, args ...any) { l.Panic(args...) }
