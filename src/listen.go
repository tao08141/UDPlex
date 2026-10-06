package main

import (
	"fmt"
	"maps"
	"net"
	"net/netip"
	"sync"
	"sync/atomic"
	"time"
)

type listenSendJob struct {
	packet *Packet
	addr   net.Addr
}

// ListenConn represents a logical UDP "connection" from a remote address
// It encapsulates per-peer state like authentication and activity timestamps.
type ListenConn struct {
	addr       net.Addr
	lastActive time.Time
	authState  *AuthState // Authentication state for this connection
	connID     ConnID     // Unique connection identifier
	heartbeatTracker
}

// Address returns the remote address associated with this logical connection.
func (c *ListenConn) Address() net.Addr { return c.addr }

// NewListenComponent creates a new listen component
func NewListenComponent(cfg ComponentConfig, router *Router) *ListenComponent {
	timeout := time.Duration(cfg.Timeout) * time.Second
	if timeout == 0 {
		timeout = 120 * time.Second // Default timeout
	}

	// Initialize auth manager
	authManager, err := NewAuthManager(cfg.Auth, router)
	if err != nil {
		logger.Errorf("Failed to create auth manager: %v", err)
		return nil
	}

	broadcastMode := true
	if cfg.BroadcastMode != nil && !*cfg.BroadcastMode {
		broadcastMode = false
	}
	preserveConnID := cfg.PreserveConnID
	if preserveConnID && (authManager == nil || !broadcastMode) {
		// Replies are looked up by the line's connection ID in non-broadcast
		// mode, and only auth carries the peer's ID.
		logger.Warnf("%s: preserve_conn_id needs auth and broadcast_mode, ignoring it", cfg.Tag)
		preserveConnID = false
	}

	sendTimeout := time.Duration(cfg.SendTimeout) * time.Millisecond
	if sendTimeout == 0 {
		sendTimeout = 500 * time.Millisecond
	}

	queueSize := router.config.QueueSize
	if queueSize <= 0 {
		queueSize = 10240
	}

	component := &ListenComponent{
		BaseComponent:     NewBaseComponent(cfg.Tag, router, sendTimeout),
		listenAddr:        cfg.ListenAddr,
		timeout:           timeout,
		replaceOldMapping: cfg.ReplaceOldMapping,
		detour:            cfg.Detour,
		mappings:          make(map[string]*ListenConn),
		authManager:       authManager,
		broadcastMode:     broadcastMode,
		preserveConnID:    preserveConnID,
		sendTimeout:       sendTimeout,
		recvBufferSize:    cfg.RecvBufferSize,
		sendBufferSize:    cfg.SendBufferSize,
		sendQueue:         make(chan listenSendJob, queueSize),
		sendQueuePrio:     make(chan listenSendJob, max(4, queueSize/16)),
	}

	// Initialize an atomic value with an empty map
	initialMap := make(map[string]*ListenConn)
	component.mappingsAtomic.Store(initialMap)
	if !broadcastMode {
		component.connIDIndex.Store(make(map[ConnID]*ListenConn))
	}

	if shaper, err := newShaperSettings(cfg.Shaper); err != nil {
		logger.Errorf("%s: Shaper disabled: %v", cfg.Tag, err)
	} else {
		if shaper != nil && shaper.autorate != nil && authManager == nil {
			logger.Warnf("%s: Shaper autorate needs auth, using the fixed rate", cfg.Tag)
			shaper.autorate = nil
		}
		component.shaper = shaper
	}
	return component
}

// ListenComponent implements a UDP listener with authentication
type ListenComponent struct {
	BaseComponent

	listenAddr        string
	timeout           time.Duration
	replaceOldMapping bool
	detour            []string
	broadcastMode     bool
	preserveConnID    bool // keep the connection ID carried by auth data messages
	conn              net.PacketConn
	mappings          map[string]*ListenConn
	mappingsAtomic    atomic.Value
	connIDIndex       atomic.Value // map[ConnID]*ListenConn for O(1) lookup in non-broadcast mode
	authManager       *AuthManager
	sendTimeout       time.Duration
	recvBufferSize    int
	sendBufferSize    int
	sendQueue         chan listenSendJob
	sendQueuePrio     chan listenSendJob
	shaper            *shaperSettings
	shaperStats       shaperStats
	autorates         sync.Map     // netip.AddrPort -> *autorate, published by the shaped send loop
	queueDelay        atomic.Int64 // ns, largest shaper queue delay over all clients
}

// listenPath is the shaper state of one client address.
type listenPath struct {
	*pathShaper
	addr      net.Addr
	ctl       *autorate
	nextProbe time.Time
}

// addrPortKey returns the unmapped address of a UDP address.
func addrPortKey(addr net.Addr) (netip.AddrPort, bool) {
	ua, ok := addr.(*net.UDPAddr)
	if !ok || ua == nil {
		return netip.AddrPort{}, false
	}
	key := ua.AddrPort()
	return netip.AddrPortFrom(key.Addr().Unmap(), key.Port()), true
}

func (l *ListenComponent) runSendLoop() {
	if l.shaper != nil {
		if udpConn, ok := l.conn.(*net.UDPConn); ok {
			l.runShapedSendLoop(udpConn)
			return
		}
	}
	var lastDeadlineUpdate time.Time
	refreshInterval := l.sendTimeout / 4
	if refreshInterval <= 0 {
		refreshInterval = l.sendTimeout
	}

	udpConn, _ := l.conn.(*net.UDPConn)
	writer := newUDPBatchWriter(l.router, l.tag)
	maxBatch := writer.MaxBatch()
	if udpConn == nil {
		maxBatch = 1
	}
	jobs := make([]listenSendJob, 0, maxBatch)
	data := make([][]byte, 0, maxBatch)
	addrs := make([]net.Addr, 0, maxBatch)

	addJob := func(job listenSendJob) {
		if job.packet == nil {
			return
		}
		if job.addr == nil {
			job.packet.Release(1)
			return
		}
		jobs = append(jobs, job)
	}

	flush := func() {
		if len(jobs) == 0 {
			return
		}
		if l.sendTimeout > 0 {
			now := time.Now()
			if lastDeadlineUpdate.IsZero() || now.Sub(lastDeadlineUpdate) >= refreshInterval {
				if err := l.conn.SetWriteDeadline(now.Add(l.sendTimeout)); err != nil {
					logger.Infof("%s: Failed to set write deadline: %v", l.tag, err)
				}
				lastDeadlineUpdate = now
			}
		}
		if udpConn == nil {
			for _, job := range jobs {
				if _, err := l.conn.WriteTo(job.packet.GetData(), job.addr); err != nil {
					logger.Infof("%s: Failed to send packet: %v", l.tag, err)
				}
			}
		} else {
			data, addrs = data[:0], addrs[:0]
			for _, job := range jobs {
				data = append(data, job.packet.GetData())
				addrs = append(addrs, job.addr)
			}
			for sent := 0; sent < len(data); {
				n, err := writer.Write(udpConn, data[sent:], addrs[sent:])
				sent += n
				if err != nil {
					// Skip the datagram that failed, like a single WriteTo would.
					logger.Infof("%s: Failed to send packet: %v", l.tag, err)
					sent++
				}
			}
		}
		for i := range jobs {
			jobs[i].packet.Release(1)
			jobs[i] = listenSendJob{}
		}
		jobs = jobs[:0]
	}

	// collect drains already queued jobs without blocking, priority first.
	collect := func() bool {
		for len(jobs) < maxBatch {
			select {
			case job, ok := <-l.sendQueuePrio:
				if !ok {
					return false
				}
				addJob(job)
				continue
			default:
			}
			select {
			case job, ok := <-l.sendQueuePrio:
				if !ok {
					return false
				}
				addJob(job)
			case job, ok := <-l.sendQueue:
				if !ok {
					return false
				}
				addJob(job)
			default:
				return true
			}
		}
		return true
	}

	for {
		// Drain priority queue first.
		select {
		case <-l.GetStopChannel():
			l.drainSendQueue()
			return
		case job, ok := <-l.sendQueuePrio:
			if !ok {
				l.drainSendQueue()
				return
			}
			addJob(job)
		default:
			// No priority packets pending, wait on both.
			select {
			case <-l.GetStopChannel():
				l.drainSendQueue()
				return
			case job, ok := <-l.sendQueuePrio:
				if !ok {
					l.drainSendQueue()
					return
				}
				addJob(job)
			case job, ok := <-l.sendQueue:
				if !ok {
					l.drainSendQueue()
					return
				}
				addJob(job)
			}
		}

		open := collect()
		flush()
		if !open {
			l.drainSendQueue()
			return
		}
	}
}

// runShapedSendLoop is runSendLoop with a rate shaper per client address, so
// each client's downlink is paced separately, small packets first.
func (l *ListenComponent) runShapedSendLoop(udpConn *net.UDPConn) {
	const shaperIdleTimeout = 30 * time.Second
	var lastDeadlineUpdate time.Time
	refreshInterval := l.sendTimeout / 4
	if refreshInterval <= 0 {
		refreshInterval = l.sendTimeout
	}

	shapers := make(map[netip.AddrPort]*listenPath)
	defer func() {
		for key, s := range shapers {
			s.reset()
			l.autorates.Delete(key)
		}
		l.queueDelay.Store(0)
	}()
	writer := newUDPBatchWriter(l.router, l.tag)
	out := make([]shaperItem, 0, writer.MaxBatch())
	data := make([][]byte, 0, writer.MaxBatch())
	addrs := make([]net.Addr, 0, writer.MaxBatch())
	timer := time.NewTimer(time.Hour)
	timer.Stop()
	defer timer.Stop()
	lastSweep := time.Now()

	enqueue := func(job listenSendJob, control bool, now time.Time) {
		if job.packet == nil {
			return
		}
		key, ok := addrPortKey(job.addr)
		if !ok {
			job.packet.Release(1)
			return
		}
		s := shapers[key]
		if s == nil {
			s = &listenPath{pathShaper: newPathShaper(l.shaper, &l.shaperStats, now), addr: job.addr}
			if l.shaper.autorate != nil {
				s.ctl = newAutorate(l.shaper.autorate, now)
				l.autorates.Store(key, s.ctl)
			}
			shapers[key] = s
		}
		s.enqueue(job.packet, job.addr, control, now)
	}

	// drain moves already queued jobs into the shapers; false if a queue was closed.
	drain := func(now time.Time) bool {
		for range 4096 {
			select {
			case job, ok := <-l.sendQueuePrio:
				if !ok {
					return false
				}
				enqueue(job, true, now)
			case job, ok := <-l.sendQueue:
				if !ok {
					return false
				}
				enqueue(job, false, now)
			default:
				return true
			}
		}
		return true
	}

	send := func(now time.Time) {
		out = out[:0]
		for _, s := range shapers {
			if len(out) >= cap(out) {
				break
			}
			out = s.dequeue(now, out, cap(out))
		}
		if len(out) == 0 {
			return
		}
		if l.sendTimeout > 0 && (lastDeadlineUpdate.IsZero() || now.Sub(lastDeadlineUpdate) >= refreshInterval) {
			if err := l.conn.SetWriteDeadline(now.Add(l.sendTimeout)); err != nil {
				logger.Infof("%s: Failed to set write deadline: %v", l.tag, err)
			}
			lastDeadlineUpdate = now
		}
		data, addrs = data[:0], addrs[:0]
		for _, it := range out {
			data = append(data, it.pkt.GetData())
			addrs = append(addrs, it.addr)
		}
		for sent := 0; sent < len(data); {
			n, err := writer.Write(udpConn, data[sent:], addrs[sent:])
			sent += n
			if err != nil {
				// Skip the datagram that failed, like a single WriteTo would.
				logger.Infof("%s: Failed to send packet: %v", l.tag, err)
				sent++
			}
		}
		for i := range out {
			out[i].pkt.Release(1)
			out[i] = shaperItem{}
		}
	}

	// probe updates the rate of a path from its latest delay samples and sends the next probe when due.
	probe := func(s *listenPath, now time.Time) {
		if s.ctl == nil || now.Before(s.nextProbe) {
			return
		}
		s.setRate(now, s.ctl.tick(now, s.sent))
		s.nextProbe = now.Add(s.ctl.probeInterval(now, s.lastActive))
		pkt := l.router.GetPacket(l.tag)
		pkt.SetLength(createProbe(pkt.BufAtOffset(), now))
		s.enqueue(&pkt, s.addr, true, now)
	}

	// wait returns the earliest time any shaper can send or probe, or -1 if
	// there is nothing to do. It also publishes the largest queue delay.
	wait := func(now time.Time) time.Duration {
		next := time.Duration(-1)
		var qdelay time.Duration
		for _, s := range shapers {
			w := s.wait(now)
			if s.ctl != nil {
				if until := s.nextProbe.Sub(now); w < 0 || until < w {
					w = until
				}
				if w < 0 {
					w = 0
				}
			}
			if w >= 0 && (next < 0 || w < next) {
				next = w
			}
			if d := s.queueDelay(); d > qdelay {
				qdelay = d
			}
		}
		l.queueDelay.Store(int64(qdelay))
		return next
	}

	for {
		now := time.Now()
		if !drain(now) {
			l.drainSendQueue()
			return
		}
		if l.shaper.autorate != nil {
			for _, s := range shapers {
				probe(s, now)
			}
		}
		send(now)
		if now.Sub(lastSweep) >= shaperIdleTimeout {
			for key, s := range shapers {
				if s.idle(now, shaperIdleTimeout) {
					s.reset()
					delete(shapers, key)
					l.autorates.Delete(key)
				}
			}
			lastSweep = now
		}
		w := wait(time.Now())
		if w == 0 {
			continue
		}
		var tick <-chan time.Time
		if w > 0 {
			timer.Reset(w)
			tick = timer.C
		}
		select {
		case <-l.GetStopChannel():
			l.drainSendQueue()
			return
		case job, ok := <-l.sendQueuePrio:
			if !ok {
				l.drainSendQueue()
				return
			}
			enqueue(job, true, time.Now())
		case job, ok := <-l.sendQueue:
			if !ok {
				l.drainSendQueue()
				return
			}
			enqueue(job, false, time.Now())
		case <-tick:
		}
		timer.Stop()
	}
}

func (l *ListenComponent) drainSendQueue() {
	for {
		select {
		case job := <-l.sendQueuePrio:
			if job.packet != nil {
				job.packet.Release(1)
			}
		case job := <-l.sendQueue:
			if job.packet != nil {
				job.packet.Release(1)
			}
		default:
			return
		}
	}
}

func (l *ListenComponent) queueSend(addr net.Addr, packet *Packet) {
	l.queueSendWithPriority(addr, packet, false)
}

func (l *ListenComponent) queueSendHigh(addr net.Addr, packet *Packet) {
	l.queueSendWithPriority(addr, packet, true)
}

func (l *ListenComponent) queueSendWithPriority(addr net.Addr, packet *Packet, high bool) {
	if packet == nil {
		return
	}

	packet.AddRef(1)

	if addr == nil {
		packet.Release(1)
		return
	}
	select {
	case <-l.GetStopChannel():
		packet.Release(1)
		return
	default:
	}
	job := listenSendJob{packet: packet, addr: addr}
	if high {
		select {
		case l.sendQueuePrio <- job:
			return
		default:
			// Fallback to normal queue.
			select {
			case l.sendQueue <- job:
				return
			default:
				logger.Infof("%s: Send priority queue full, dropping packet for %s", l.tag, addr.String())
				packet.Release(1)
				return
			}
		}
	}

	select {
	case l.sendQueue <- job:
		return
	default:
		logger.Infof("%s: Send queue full, dropping packet for %s", l.tag, addr.String())
		packet.Release(1)
		return
	}
}

// Start initializes and starts the listener
func (l *ListenComponent) Start() error {
	// Create UDP address to listen on
	udpAddr, err := net.ResolveUDPAddr("udp", l.listenAddr)
	if err != nil {
		return fmt.Errorf("failed to resolve UDP address: %w", err)
	}

	// Create UDP connection with specific options
	conn, err := net.ListenUDP("udp", udpAddr)
	if err != nil {
		return fmt.Errorf("failed to set up UDP listener: %w", err)
	}

	// Apply socket optimizations if configured
	if l.recvBufferSize > 0 {
		if err := conn.SetReadBuffer(l.recvBufferSize); err != nil {
			logger.Warnf("%s: Failed to set read buffer size to %d: %v", l.tag, l.recvBufferSize, err)
		} else {
			logger.Infof("%s: Set UDP read buffer size to %d bytes", l.tag, l.recvBufferSize)
		}
	}

	if l.sendBufferSize > 0 {
		if err := conn.SetWriteBuffer(l.sendBufferSize); err != nil {
			logger.Warnf("%s: Failed to set write buffer size to %d: %v", l.tag, l.sendBufferSize, err)
		} else {
			logger.Infof("%s: Set UDP write buffer size to %d bytes", l.tag, l.sendBufferSize)
		}
	}

	l.conn = conn
	logger.Infof("%s is listening on %s", l.tag, conn.LocalAddr())

	// Start packet handling routine
	go l.handlePackets()
	go l.runSendLoop()

	return nil
}

// Stop closes the listener
func (l *ListenComponent) Stop() error {
	close(l.GetStopChannel())
	return l.conn.Close()
}

// IsAvailable checks if the component has any established connections
// QueueDelay returns the largest shaper queue delay over the clients.
func (l *ListenComponent) QueueDelay() time.Duration {
	return time.Duration(l.queueDelay.Load())
}

func (l *ListenComponent) IsAvailable() bool {
	// First check if the listener is active
	if l.conn == nil {
		return false
	}

	// Check if there are any established connections
	mappings := l.mappingsAtomic.Load().(map[string]*ListenConn)
	return len(mappings) > 0
}

// performCleanup handles the cleaning of inactive mappings
func (l *ListenComponent) performCleanup() {
	now := time.Now()
	isSync := false

	// Remove inactive mappings
	for addrString, mapping := range l.mappings {
		if now.Sub(mapping.lastActive) > l.timeout {
			if l.removeMapping(addrString) {
				isSync = true
				logger.Warnf("%s: Removed inactive mapping: %s", l.tag, addrString)
			}
		}
	}

	if isSync {
		l.syncMapping()
	}
}

func (l *ListenComponent) syncMapping() {
	mappingsTemp := make(map[string]*ListenConn, len(l.mappings))
	maps.Copy(mappingsTemp, l.mappings)
	l.mappingsAtomic.Store(mappingsTemp)

	// Rebuild ConnID index for O(1) lookup in non-broadcast mode.
	if !l.broadcastMode {
		idx := make(map[ConnID]*ListenConn, len(l.mappings))
		for _, m := range l.mappings {
			idx[m.connID] = m
		}
		l.connIDIndex.Store(idx)
	}
}

func (l *ListenComponent) removeMapping(addrKey string) bool {
	mapping, exists := l.mappings[addrKey]
	if !exists {
		return false
	}
	mapping.MarkPendingHeartbeatLost()
	delete(l.mappings, addrKey)
	l.RemoveConnData(mapping.connID)
	return true
}

// handleAuthMessage processes authentication messages
func (l *ListenComponent) handleAuthMessage(header *ProtocolHeader, buffer []byte, addr net.Addr) {

	addrKey := addr.String()

	switch header.MsgType {
	case MsgTypeAuthChallenge:
		// Get or create an auth state
		mapping, exists := l.mappings[addrKey]
		if !exists {
			mapping = &ListenConn{
				addr:       addr,
				lastActive: time.Now(),
				authState:  &AuthState{},
				connID:     l.generateConnID(),
			}
			mapping.SetHeartbeatStatsTracker(l.GetHeartbeatStatsTracker())
			l.mappings[addrKey] = mapping
			l.syncMapping()
		}

		// Process challenge and send response
		data := buffer[HeaderSize : HeaderSize+header.Length]
		forwardID, poolID, err := l.authManager.ProcessAuthChallenge(data)
		if err != nil {
			logger.Infof("%s: %s Authentication challenge failed: %v", l.tag, addr.String(), err)
			return
		}

		if l.replaceOldMapping {
			addrIP := addr.(*net.UDPAddr).IP.String()
			isSync := false

			for key, mapping := range l.mappings {
				if mapping.addr.(*net.UDPAddr).IP.String() == addrIP && key != addrKey {
					logger.Warnf("%s: Replacing old mapping: %s", l.tag, mapping.addr.String())
					if l.removeMapping(key) {
						isSync = true
					}
				}
			}

			if isSync {
				l.syncMapping()
			}
		}

		// Create response
		responseBuffer := l.router.GetBuffer()
		defer l.router.PutBuffer(responseBuffer)
		responseLen, err := l.authManager.CreateAuthChallenge(responseBuffer, MsgTypeAuthResponse, forwardID, poolID)
		if err != nil {
			logger.Warnf("%s: Failed to create auth challenge response: %v", l.tag, err)
		}

		if l.sendTimeout > 0 {
			if err := l.conn.SetWriteDeadline(time.Now().Add(l.sendTimeout)); err != nil {
				logger.Infof("%s: Failed to set write deadline: %v", l.tag, err)
			}
		}

		// Send response
		_, err = l.conn.WriteTo(responseBuffer[:responseLen], addr)
		if err != nil {
			logger.Warnf("%s: Failed to send auth response: %v", l.tag, err)
		}

		mapping.authState.SetAuthenticated(1)

		mapping.lastActive = time.Now()
		logger.Infof("%s: Authentication successful for %s", l.tag, addr.String())

	case MsgTypeProbe:
		if mapping, exists := l.mappings[addrKey]; exists && mapping.authState != nil && mapping.authState.IsAuthenticated() {
			pkt := l.router.GetPacket(l.tag)
			if n := createProbeAck(pkt.BufAtOffset(), buffer, time.Now()); n > 0 {
				pkt.SetLength(n)
				l.queueSendHigh(addr, &pkt)
			}
			pkt.Release(1)
		}

	case MsgTypeProbeAck:
		if key, ok := addrPortKey(addr); ok {
			if v, ok := l.autorates.Load(key); ok {
				now := time.Now()
				if owd, rtt, ok := parseProbeAck(buffer, now); ok {
					v.(*autorate).addSample(owd, rtt, now)
				}
			}
		}

	case MsgTypeHeartbeat:

		// Update mapping if exists
		if mapping, exists := l.mappings[addrKey]; exists {
			mapping.lastActive = time.Now()
			if mapping.authState != nil {
				mapping.MarkPendingHeartbeatLost()
				// Echo's heartbeat back
				pkt := l.router.GetPacket(l.tag)
				responseLen := CreateHeartbeat(pkt.BufAtOffset())
				pkt.SetLength(responseLen)
				if l.sendTimeout > 0 {
					if err := l.conn.SetWriteDeadline(time.Now().Add(l.sendTimeout)); err != nil {
						logger.Infof("%s: Failed to set write deadline: %v", l.tag, err)
					}
				}
				l.queueSendHigh(addr, &pkt)
				pkt.Release(1)
				mapping.NoteHeartbeatSent()
			}
		}

	case MsgTypeHeartbeatAck:
		// Update mapping if exists
		if mapping, exists := l.mappings[addrKey]; exists {
			mapping.lastActive = time.Now()
			if mapping.authState != nil {
				mapping.MarkHeartbeatResponse()
				// If this is a response to our heartbeat, measure delay
				if lastHeartbeatSent := mapping.LastHeartbeatSent(); !lastHeartbeatSent.IsZero() {
					delay := time.Since(lastHeartbeatSent)
					if l.authManager != nil {
						l.authManager.RecordDelayMeasurement(delay)
					}
				}
				mapping.ClearLastHeartbeatSent()
			}
		}

	case MsgTypeDisconnect:
		if l.removeMapping(addrKey) {
			l.syncMapping()
			logger.Infof("%s: Client %s disconnected", l.tag, addr.String())
		}
	}

}

// handlePackets processes incoming UDP packets
func (l *ListenComponent) handlePackets() {
	cleanupInterval := l.timeout / 2
	lastCleanupTime := time.Now()
	shortDeadline := min(time.Second*1, cleanupInterval)
	deadlineRefresh := shortDeadline / 4
	if deadlineRefresh <= 0 {
		deadlineRefresh = shortDeadline
	}
	var lastDeadlineUpdate time.Time

	reader := newUDPBatchReader(l.conn.(*net.UDPConn), l.router)
	defer reader.Close()

	for {
		select {
		case <-l.GetStopChannel():
			return
		default:
			func() {
				now := time.Now()
				if now.Sub(lastCleanupTime) >= cleanupInterval {
					l.performCleanup()
					lastCleanupTime = now
				}

				// Batch read deadline refresh
				if lastDeadlineUpdate.IsZero() || now.Sub(lastDeadlineUpdate) >= deadlineRefresh {
					if err := l.conn.SetReadDeadline(now.Add(shortDeadline)); err != nil {
						logger.Warnf("%s: Error setting read deadline: %v", l.tag, err)
					}
					lastDeadlineUpdate = now
				}

				n, err := reader.Read()
				if isTimeoutError(err) {
					lastDeadlineUpdate = time.Time{} // Force refresh on next iteration
					return
				} else if err != nil {
					logger.Warnf("%s: Read error: %v", l.tag, err)
					return
				}

				for i := 0; i < n; i++ {
					packet, addr := reader.Take(i, l.tag)
					l.handleIncoming(&packet, addr)
					packet.Release(1)
				}
			}()

		}
	}
}

// handleIncoming authenticates and maps a datagram received from addr, then routes it.
func (l *ListenComponent) handleIncoming(packet *Packet, addr net.Addr) {
	// Handle authentication if enabled
	if l.authManager != nil {
		if packet.Length() < HeaderSize {
			logger.Infof("%s: %s Packet too short for header: %d bytes", l.tag, addr.String(), packet.Length())
			return
		}

		header, err := l.authManager.UnwrapData(packet)
		if err != nil {
			if err.Error() != "duplicate packet detected" {
				logger.Infof("%s: %s Failed to unwrap data: %v", l.tag, addr.String(), err)
			}
			return
		}

		// Handle auth messages
		if header.MsgType != MsgTypeData {
			l.handleAuthMessage(header, packet.GetData(), addr)
			return
		}

		// For data messages, check authentication
		addrKey := addr.String()
		mapping, exists := l.mappings[addrKey]
		if !exists || mapping.authState == nil || !mapping.authState.IsAuthenticated() {
			// Not authenticated - silently drop
			return
		}

		mapping.lastActive = time.Now()
		// With preserve_conn_id a relay's per-client IDs survive the line, so
		// the receiver can tell its clients apart and see one client as the
		// same connection on every line.
		if !l.preserveConnID || packet.ConnID() == (ConnID{}) {
			packet.SetConnID(mapping.connID)
		}
	}

	// Handle address mapping for non-auth mode
	if l.authManager == nil {
		addrKey := addr.String()
		// Check if this is a new mapping
		mapping, exists := l.mappings[addrKey]
		if !exists {
			if l.replaceOldMapping {
				addrIP := addr.(*net.UDPAddr).IP.String()
				removed := false
				for key, existing := range l.mappings {
					if existing.addr.(*net.UDPAddr).IP.String() == addrIP {
						logger.Warnf("%s: Replacing old mapping: %s", l.tag, existing.addr.String())
						if l.removeMapping(key) {
							removed = true
						}
					}
				}
				if removed {
					l.syncMapping()
				}
			}

			logger.Warnf("%s: New mapping: %s", l.tag, addr.String())
			connID := l.generateConnID()
			mapping = &ListenConn{addr: addr, lastActive: time.Now(), connID: connID}
			mapping.SetHeartbeatStatsTracker(l.GetHeartbeatStatsTracker())
			l.mappings[addrKey] = mapping
			l.syncMapping()
			packet.SetConnID(connID)
		} else {
			mapping.lastActive = time.Now()
			packet.SetConnID(mapping.connID)
		}
	}

	packet.SetSrcAddr(addr)

	// Forward the packet to detour components
	if err := l.router.Route(packet, l.detour); err != nil {
		logger.Infof("%s: Error routing: %v", l.tag, err)
	}
}

// HandlePacket processes packets from other components
func (l *ListenComponent) HandlePacket(packet *Packet) error {
	defer packet.Release(1)

	if l.authManager != nil {
		err := l.authManager.WrapData(packet)
		if err != nil {
			return err
		}
	}

	if l.broadcastMode {
		mappingsSnapshot := l.mappingsAtomic.Load().(map[string]*ListenConn)
		for _, mapping := range mappingsSnapshot {
			if l.authManager != nil && (mapping.authState == nil || !mapping.authState.IsAuthenticated()) {
				continue
			}

			l.queueSend(mapping.addr, packet)
		}
	} else {
		if packet.ConnID() == (ConnID{}) {
			logger.Infof("%s: Packet has no connection ID, dropping", l.tag)
			return nil
		}

		// O(1) lookup via ConnID index.
		idx, _ := l.connIDIndex.Load().(map[ConnID]*ListenConn)
		if idx != nil {
			if mapping, ok := idx[packet.ConnID()]; ok {
				if l.authManager != nil && (mapping.authState == nil || !mapping.authState.IsAuthenticated()) {
					logger.Debugf("%s: Connection not authenticated, dropping packet", l.tag)
					return nil
				}
				l.queueSend(mapping.addr, packet)
				return nil
			}
		}

		logger.Debugf("%s: No mapping found for connection ID: %s", l.tag, packet.ConnID())
	}

	return nil
}
