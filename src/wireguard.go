package main

import (
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	wgconn "golang.zx2c4.com/wireguard/conn"
	wgdevice "golang.zx2c4.com/wireguard/device"
	wgtun "golang.zx2c4.com/wireguard/tun"
)

type WireGuardComponent struct {
	BaseComponent

	interfaceName       string
	actualInterfaceName string
	detour              []string
	privateKey          string
	listenPort          uint16
	addresses           []string
	routes              []string
	mtu                 int
	peers               []WireGuardPeerConfig
	routeAllowedIPs     bool
	setupInterface      bool
	reuseIncomingDetour bool

	tunDevice wgtun.Device
	wgDevice  *wgdevice.Device
	bind      *WireGuardBind
}

type WireGuardBind struct {
	component *WireGuardComponent
	port      uint16
	rxQueue   chan wireGuardInboundPacket
	state     atomic.Pointer[wireGuardBindState]
}

type wireGuardInboundPacket struct {
	packet   *Packet
	endpoint *WireGuardEndpoint
}

type wireGuardBindState struct {
	closeCh chan struct{}
}

type WireGuardEndpoint struct {
	raw       string
	connID    ConnID
	routeTags []string
	dstIP     netip.Addr
	srcIP     netip.Addr
	dstBytes  []byte
}

func NewWireGuardComponent(cfg WireGuardComponentConfig, router *Router) *WireGuardComponent {
	sendTimeout := timeDurationOrDefault(cfg.SendTimeout, 500)

	setupInterface := true
	if cfg.SetupInterface != nil {
		setupInterface = *cfg.SetupInterface
	}

	reuseIncomingDetour := true
	if cfg.ReuseIncomingDetour != nil {
		reuseIncomingDetour = *cfg.ReuseIncomingDetour
	}

	routeAllowedIPs := false
	if cfg.RouteAllowedIPs != nil {
		routeAllowedIPs = *cfg.RouteAllowedIPs
	}

	interfaceName := strings.TrimSpace(cfg.InterfaceName)
	if interfaceName == "" {
		interfaceName = cfg.Tag
	}

	mtu := cfg.MTU
	if mtu <= 0 {
		mtu = 1420
	}

	return &WireGuardComponent{
		BaseComponent:       NewBaseComponent(cfg.Tag, router, sendTimeout),
		interfaceName:       interfaceName,
		detour:              append([]string(nil), cfg.Detour...),
		privateKey:          strings.TrimSpace(cfg.PrivateKey),
		listenPort:          uint16(cfg.ListenPort),
		addresses:           append([]string(nil), cfg.Addresses...),
		routes:              append([]string(nil), cfg.Routes...),
		mtu:                 mtu,
		peers:               append([]WireGuardPeerConfig(nil), cfg.Peers...),
		routeAllowedIPs:     routeAllowedIPs,
		setupInterface:      setupInterface,
		reuseIncomingDetour: reuseIncomingDetour,
	}
}

func NewWireGuardBind(component *WireGuardComponent) *WireGuardBind {
	queueSize := component.router.config.QueueSize
	if queueSize <= 0 {
		queueSize = 1024
	}

	bind := &WireGuardBind{
		component: component,
		port:      component.listenPort,
		rxQueue:   make(chan wireGuardInboundPacket, queueSize),
	}
	return bind
}

func (w *WireGuardComponent) Start() error {
	if w.privateKey == "" {
		return fmt.Errorf("%s: wireguard private_key is required", w.tag)
	}

	w.bind = NewWireGuardBind(w)

	tunDevice, err := wgtun.CreateTUN(w.interfaceName, w.mtu)
	if err != nil {
		_ = w.bind.Close()
		return fmt.Errorf("%s: failed to create wireguard interface %s: %w", w.tag, w.interfaceName, err)
	}

	actualName, err := tunDevice.Name()
	if err != nil {
		_ = tunDevice.Close()
		_ = w.bind.Close()
		return fmt.Errorf("%s: failed to query wireguard interface name: %w", w.tag, err)
	}

	w.tunDevice = tunDevice
	w.actualInterfaceName = actualName

	wgLogger := wgdevice.NewLogger(wgdevice.LogLevelError, fmt.Sprintf("%s: ", w.tag))
	w.wgDevice = wgdevice.NewDevice(w.tunDevice, w.bind, wgLogger)

	if err := w.wgDevice.IpcSet(w.buildIPCConfig()); err != nil {
		w.closeRuntime()
		return fmt.Errorf("%s: failed to configure wireguard device: %w", w.tag, err)
	}

	if err := w.wgDevice.Up(); err != nil {
		w.closeRuntime()
		return fmt.Errorf("%s: failed to bring wireguard device up: %w", w.tag, err)
	}

	if w.setupInterface {
		if err := w.configureInterface(); err != nil {
			w.closeRuntime()
			return err
		}
	}

	logger.Infof("%s: WireGuard interface %s started with %d peers", w.tag, w.actualInterfaceName, len(w.peers))
	return nil
}

func (w *WireGuardComponent) PostStart() error {
	w.warmPeerHandshakes(5 * time.Second)
	return nil
}

func (w *WireGuardComponent) Stop() error {
	close(w.GetStopChannel())
	w.closeRuntime()
	return nil
}

func (w *WireGuardComponent) closeRuntime() {
	if w.wgDevice != nil {
		w.wgDevice.Close()
		w.wgDevice = nil
	}
	if w.bind != nil {
		_ = w.bind.Close()
		w.bind = nil
	}
	if w.tunDevice != nil {
		_ = w.tunDevice.Close()
		w.tunDevice = nil
	}
}

func (w *WireGuardComponent) HandlePacket(packet *Packet) error {
	defer packet.Release(1)

	if w.bind == nil {
		return fmt.Errorf("%s: wireguard bind is not initialized", w.tag)
	}

	endpoint := w.endpointFromPacket(packet)
	if endpoint == nil {
		return fmt.Errorf("%s: failed to build endpoint for inbound packet", w.tag)
	}

	return w.bind.Enqueue(packet, endpoint)
}

func (w *WireGuardComponent) endpointFromPacket(packet *Packet) *WireGuardEndpoint {
	srcAddr := packet.SrcAddr()
	raw := ""
	if srcAddr != nil {
		raw = srcAddr.String()
	}
	if raw == "" {
		raw = fmt.Sprintf("conn:%x", packet.ConnID())
	}

	srcTag := ""
	if w.reuseIncomingDetour {
		srcTag = packet.SrcTag()
	}

	return newWireGuardEndpoint(raw, packet.ConnID(), srcTag, srcAddr)
}

func (w *WireGuardComponent) buildIPCConfig() string {
	var builder strings.Builder

	builder.WriteString("private_key=")
	builder.WriteString(normalizeWGKey(w.privateKey))
	builder.WriteByte('\n')

	if w.listenPort != 0 {
		builder.WriteString("listen_port=")
		builder.WriteString(strconv.Itoa(int(w.listenPort)))
		builder.WriteByte('\n')
	}

	builder.WriteString("replace_peers=true\n")

	for _, peer := range w.peers {
		builder.WriteString("public_key=")
		builder.WriteString(normalizeWGKey(peer.PublicKey))
		builder.WriteByte('\n')
		builder.WriteString("replace_allowed_ips=true\n")

		if psk := strings.TrimSpace(peer.PresharedKey); psk != "" {
			builder.WriteString("preshared_key=")
			builder.WriteString(normalizeWGKey(psk))
			builder.WriteByte('\n')
		}

		if endpoint := strings.TrimSpace(peer.Endpoint); endpoint != "" {
			builder.WriteString("endpoint=")
			builder.WriteString(endpoint)
			builder.WriteByte('\n')
		}

		if peer.PersistentKeepalive > 0 {
			builder.WriteString("persistent_keepalive_interval=")
			builder.WriteString(strconv.Itoa(peer.PersistentKeepalive))
			builder.WriteByte('\n')
		}

		for _, allowedIP := range peer.AllowedIPs {
			builder.WriteString("allowed_ip=")
			builder.WriteString(strings.TrimSpace(allowedIP))
			builder.WriteByte('\n')
		}
	}

	builder.WriteByte('\n')
	return builder.String()
}

func (w *WireGuardComponent) configureInterface() error {
	if runtime.GOOS != "linux" {
		return fmt.Errorf("%s: wg interface setup is only implemented on linux, set setup_interface=false to manage %s manually", w.tag, w.interfaceName)
	}

	if w.actualInterfaceName == "" {
		return fmt.Errorf("%s: wireguard interface name is empty", w.tag)
	}

	if w.mtu > 0 {
		if err := runIP("link", "set", "dev", w.actualInterfaceName, "mtu", strconv.Itoa(w.mtu)); err != nil {
			return fmt.Errorf("%s: failed to set MTU on %s: %w", w.tag, w.actualInterfaceName, err)
		}
	}

	for _, address := range w.addresses {
		address = strings.TrimSpace(address)
		if address == "" {
			continue
		}
		if err := runIP("address", "add", address, "dev", w.actualInterfaceName); err != nil && !isIPAlreadyExists(err) {
			return fmt.Errorf("%s: failed to add address %s to %s: %w", w.tag, address, w.actualInterfaceName, err)
		}
	}

	if err := runIP("link", "set", "dev", w.actualInterfaceName, "up"); err != nil {
		return fmt.Errorf("%s: failed to bring interface %s up: %w", w.tag, w.actualInterfaceName, err)
	}

	for _, route := range w.collectRoutes() {
		args := []string{"route", "replace", route, "dev", w.actualInterfaceName}
		if strings.Contains(route, ":") {
			args = []string{"-6", "route", "replace", route, "dev", w.actualInterfaceName}
		}
		if err := runIP(args...); err != nil {
			return fmt.Errorf("%s: failed to add route %s via %s: %w", w.tag, route, w.actualInterfaceName, err)
		}
	}

	return nil
}

func (w *WireGuardComponent) collectRoutes() []string {
	unique := make(map[string]struct{})
	routes := make([]string, 0, len(w.routes))

	appendRoute := func(route string) {
		route = strings.TrimSpace(route)
		if route == "" {
			return
		}
		if _, exists := unique[route]; exists {
			return
		}
		unique[route] = struct{}{}
		routes = append(routes, route)
	}

	for _, route := range w.routes {
		appendRoute(route)
	}

	if w.routeAllowedIPs {
		for _, peer := range w.peers {
			for _, allowedIP := range peer.AllowedIPs {
				appendRoute(allowedIP)
			}
		}
	}

	return routes
}

func (w *WireGuardComponent) warmPeerHandshakes(timeout time.Duration) {
	targets := w.buildWarmupTargets()
	if len(targets) == 0 {
		return
	}

	deadline := time.Now().Add(timeout)
	for {
		for _, target := range targets {
			localAddr := &net.UDPAddr{IP: net.IP(target.local.AsSlice())}
			remoteAddr := &net.UDPAddr{IP: net.IP(target.remote.AsSlice()), Port: 9}

			conn, err := net.DialUDP("udp", localAddr, remoteAddr)
			if err != nil {
				logger.Debugf("%s: failed to dial WireGuard warmup target %s: %v", w.tag, remoteAddr.String(), err)
				continue
			}
			_, _ = conn.Write([]byte{0})
			_ = conn.Close()
		}

		if w.hasEstablishedHandshakes(targets) {
			return
		}
		if time.Now().After(deadline) {
			logger.Debugf("%s: timed out waiting for WireGuard handshake warmup", w.tag)
			return
		}

		select {
		case <-w.GetStopChannel():
			return
		case <-time.After(100 * time.Millisecond):
		}
	}
}

type wireGuardWarmupTarget struct {
	local   netip.Addr
	remote  netip.Addr
	peerKey string
}

func (w *WireGuardComponent) buildWarmupTargets() []wireGuardWarmupTarget {
	localByFamily := map[int]netip.Addr{}
	for _, raw := range w.addresses {
		prefix, err := netip.ParsePrefix(strings.TrimSpace(raw))
		if err != nil {
			continue
		}
		addr := prefix.Addr()
		if !addr.IsValid() || !addr.Is4() {
			continue
		}
		localByFamily[4] = addr
	}

	targets := make([]wireGuardWarmupTarget, 0, len(w.peers))
	for _, peer := range w.peers {
		if strings.TrimSpace(peer.Endpoint) == "" {
			continue
		}

		for _, rawAllowedIP := range peer.AllowedIPs {
			prefix, err := netip.ParsePrefix(strings.TrimSpace(rawAllowedIP))
			if err != nil {
				continue
			}
			dst := prefix.Addr()
			src, ok := localByFamily[4]
			if !ok || !dst.IsValid() || !dst.Is4() {
				continue
			}

			targets = append(targets, wireGuardWarmupTarget{
				local:   src,
				remote:  dst,
				peerKey: normalizeWGKey(peer.PublicKey),
			})
			break
		}
	}

	return targets
}

func (w *WireGuardComponent) hasEstablishedHandshakes(targets []wireGuardWarmupTarget) bool {
	if w.wgDevice == nil {
		return false
	}

	state, err := w.wgDevice.IpcGet()
	if err != nil {
		logger.Debugf("%s: failed to inspect WireGuard state: %v", w.tag, err)
		return false
	}

	handshakeByKey := map[string]int64{}
	currentKey := ""
	for _, line := range strings.Split(state, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		switch {
		case strings.HasPrefix(line, "public_key="):
			currentKey = strings.TrimSpace(strings.TrimPrefix(line, "public_key="))
		case strings.HasPrefix(line, "last_handshake_time_sec=") && currentKey != "":
			secStr := strings.TrimSpace(strings.TrimPrefix(line, "last_handshake_time_sec="))
			secs, err := strconv.ParseInt(secStr, 10, 64)
			if err == nil {
				handshakeByKey[currentKey] = secs
			}
		}
	}

	for _, target := range targets {
		if handshakeByKey[target.peerKey] <= 0 {
			return false
		}
	}

	return true
}

func (b *WireGuardBind) Open(port uint16) ([]wgconn.ReceiveFunc, uint16, error) {
	if b.state.Load() != nil {
		return nil, 0, wgconn.ErrBindAlreadyOpen
	}
	if port != 0 {
		b.port = port
	}
	state := &wireGuardBindState{closeCh: make(chan struct{})}
	if !b.state.CompareAndSwap(nil, state) {
		return nil, 0, wgconn.ErrBindAlreadyOpen
	}
	return []wgconn.ReceiveFunc{b.makeReceiveFunc(state.closeCh)}, b.port, nil
}

func (b *WireGuardBind) Close() error {
	state := b.state.Swap(nil)
	if state == nil {
		return nil
	}
	close(state.closeCh)
	for {
		select {
		case msg := <-b.rxQueue:
			if msg.packet != nil {
				msg.packet.Release(1)
			}
		default:
			return nil
		}
	}
}

func (b *WireGuardBind) SetMark(mark uint32) error {
	return nil
}

func (b *WireGuardBind) Send(bufs [][]byte, ep wgconn.Endpoint) error {
	endpoint, ok := ep.(*WireGuardEndpoint)
	if !ok {
		return wgconn.ErrWrongEndpointType
	}

	destTags := endpoint.routeTags
	if len(destTags) == 0 {
		destTags = b.component.detour
	}
	if len(destTags) == 0 {
		return fmt.Errorf("%s: no detour available for wireguard packet", b.component.tag)
	}

	for _, buf := range bufs {
		packet := b.component.router.GetPacket(b.component.tag)
		copy(packet.BufAtOffset(), buf)
		packet.SetLength(len(buf))
		packet.SetConnID(endpoint.connID)

		if err := b.component.router.Route(&packet, destTags); err != nil {
			packet.Release(1)
			return err
		}
		packet.Release(1)
	}

	return nil
}

func (b *WireGuardBind) ParseEndpoint(s string) (wgconn.Endpoint, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, fmt.Errorf("wireguard endpoint is empty")
	}
	return newWireGuardEndpoint(s, ConnID{}, "", nil), nil
}

func (b *WireGuardBind) BatchSize() int {
	return wgconn.IdealBatchSize
}

func (b *WireGuardBind) Enqueue(packet *Packet, endpoint *WireGuardEndpoint) error {
	closeCh, ok := b.currentCloseCh()
	if !ok {
		return net.ErrClosed
	}

	select {
	case <-closeCh:
		return net.ErrClosed
	default:
	}

	packet.AddRef(1)

	msg := wireGuardInboundPacket{
		packet:   packet,
		endpoint: endpoint,
	}

	select {
	case <-closeCh:
		packet.Release(1)
		return net.ErrClosed
	case b.rxQueue <- msg:
		return nil
	default:
		packet.Release(1)
		return fmt.Errorf("%s: wireguard receive queue is full", b.component.tag)
	}
}

func (b *WireGuardBind) makeReceiveFunc(closeCh chan struct{}) wgconn.ReceiveFunc {
	return func(packets [][]byte, sizes []int, eps []wgconn.Endpoint) (int, error) {
		readOne := func(index int, msg wireGuardInboundPacket) {
			if msg.packet == nil {
				sizes[index] = 0
				eps[index] = msg.endpoint
				return
			}
			sizes[index] = copy(packets[index], msg.packet.GetData())
			eps[index] = msg.endpoint
			msg.packet.Release(1)
		}

		select {
		case <-closeCh:
			return 0, net.ErrClosed
		case msg := <-b.rxQueue:
			readOne(0, msg)
		}

		count := 1
		for count < len(packets) {
			select {
			case msg := <-b.rxQueue:
				readOne(count, msg)
				count++
			default:
				return count, nil
			}
		}

		return count, nil
	}
}

func (b *WireGuardBind) currentCloseCh() (chan struct{}, bool) {
	state := b.state.Load()
	if state == nil || state.closeCh == nil {
		return nil, false
	}
	return state.closeCh, true
}

func newWireGuardEndpoint(raw string, connID ConnID, srcTag string, srcAddr net.Addr) *WireGuardEndpoint {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		raw = "wireguard-peer"
	}

	var routeTags []string
	if srcTag != "" {
		routeTags = []string{srcTag}
	}

	addr := addrToIP(srcAddr)
	if !addr.IsValid() {
		addr = parsedEndpointAddr(raw)
	}
	dstIP := addr
	if !dstIP.IsValid() {
		dstIP = endpointIP(raw, connID)
	}

	dstBytes := endpointBytesFromAddr(srcAddr)
	if len(dstBytes) == 0 {
		dstBytes = endpointBytes(raw, connID)
	}

	return &WireGuardEndpoint{
		raw:       raw,
		connID:    connID,
		routeTags: routeTags,
		dstIP:     dstIP,
		srcIP:     addr,
		dstBytes:  dstBytes,
	}
}

func (e *WireGuardEndpoint) ClearSrc() {}

func (e *WireGuardEndpoint) SrcToString() string {
	if !e.srcIP.IsValid() {
		return ""
	}
	return e.srcIP.String()
}

func (e *WireGuardEndpoint) DstToString() string {
	return e.raw
}

func (e *WireGuardEndpoint) DstToBytes() []byte {
	return e.dstBytes
}

func (e *WireGuardEndpoint) DstIP() netip.Addr {
	return e.dstIP
}

func (e *WireGuardEndpoint) SrcIP() netip.Addr {
	return e.srcIP
}

func endpointBytes(raw string, connID ConnID) []byte {
	if connID == (ConnID{}) {
		hash := sha256.Sum256([]byte(raw))
		return hash[:]
	}

	buf := make([]byte, len(raw)+1+len(connID))
	copy(buf, raw)
	buf[len(raw)] = 0
	copy(buf[len(raw)+1:], connID[:])
	hash := sha256.Sum256(buf)
	return hash[:]
}

func endpointBytesFromAddr(addr net.Addr) []byte {
	switch value := addr.(type) {
	case *net.UDPAddr:
		if value == nil {
			return nil
		}
		if ip, ok := netip.AddrFromSlice(value.IP); ok {
			bytes, err := netip.AddrPortFrom(ip.Unmap(), uint16(value.Port)).MarshalBinary()
			if err == nil {
				return bytes
			}
		}
	case *net.TCPAddr:
		if value == nil {
			return nil
		}
		if ip, ok := netip.AddrFromSlice(value.IP); ok {
			bytes, err := netip.AddrPortFrom(ip.Unmap(), uint16(value.Port)).MarshalBinary()
			if err == nil {
				return bytes
			}
		}
	}
	return nil
}

func parsedEndpointAddr(raw string) netip.Addr {
	if addrPort, err := netip.ParseAddrPort(raw); err == nil {
		return addrPort.Addr()
	}
	if addr, err := netip.ParseAddr(raw); err == nil {
		return addr
	}
	return netip.Addr{}
}

func normalizeWGKey(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return value
	}

	if len(value) == 64 && isHexString(value) {
		return strings.ToLower(value)
	}

	decoded, err := base64.StdEncoding.DecodeString(value)
	if err != nil {
		return value
	}

	if len(decoded) != 32 {
		return value
	}

	return fmt.Sprintf("%x", decoded)
}

func isHexString(value string) bool {
	for _, r := range value {
		switch {
		case r >= '0' && r <= '9':
		case r >= 'a' && r <= 'f':
		case r >= 'A' && r <= 'F':
		default:
			return false
		}
	}
	return true
}

func endpointIP(raw string, connID ConnID) netip.Addr {
	if addr := parsedEndpointAddr(raw); addr.IsValid() {
		return addr
	}

	hash := sha256.Sum256(endpointBytes(raw, connID))
	return netip.AddrFrom4([4]byte{127, hash[0], hash[1], hash[2]})
}

func addrToIP(addr net.Addr) netip.Addr {
	if addr == nil {
		return netip.Addr{}
	}

	switch v := addr.(type) {
	case *net.UDPAddr:
		if ip, ok := netip.AddrFromSlice(v.IP); ok {
			return ip
		}
	case *net.TCPAddr:
		if ip, ok := netip.AddrFromSlice(v.IP); ok {
			return ip
		}
	case interface{ String() string }:
		if addrPort, err := netip.ParseAddrPort(v.String()); err == nil {
			return addrPort.Addr()
		}
	}

	return netip.Addr{}
}

func runIP(args ...string) error {
	cmd := exec.Command("ip", args...)
	output, err := cmd.CombinedOutput()
	if err == nil {
		return nil
	}

	msg := strings.TrimSpace(string(output))
	if msg == "" {
		msg = err.Error()
	}
	return errors.New(msg)
}

func isIPAlreadyExists(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "File exists")
}

func timeDurationOrDefault(ms int, defaultMS int) time.Duration {
	value := ms
	if value <= 0 {
		value = defaultMS
	}
	return time.Duration(value) * time.Millisecond
}
