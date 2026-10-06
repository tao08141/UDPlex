package main

import (
	"encoding/binary"
	"errors"
	"io"
	"math/rand/v2"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

// TCP streams over the packet pipeline.
//
// tcp_listen accepts a TCP connection, gives it a stream id and sends its
// bytes as frames through its detour (tcp_tunnel, load balancer, several
// lines...). tcp_forward on the other side dials the target and sends the
// reply bytes back the same way. Frames may be duplicated, reordered or lost
// between lines, so every frame is sequenced: the receiver drops duplicates,
// reorders and acknowledges, and the sender retransmits what is not
// acknowledged. A per-stream window bounds the bytes in flight and buffered.
//
// Frame: magic(4) type(1) flags(1) reserved(2) stream(8) seq(8) ack(8) wnd(4) payload
const (
	tcpStreamMagic       = 0x55585453 // "UXTS"
	tcpStreamHeaderSize  = 36
	tcpStreamWrapReserve = 64 // room left in a buffer for tunnel headers, nonce and AEAD tag

	tcpFrameOpen = 1 // payload is the target address; occupies one sequence number
	tcpFrameData = 2
	tcpFrameFin  = 3 // end of stream; occupies one sequence number
	tcpFrameAck  = 4
	tcpFrameRst  = 5

	tcpFlagFromClient = 1 << 0 // sent by tcp_listen
	tcpFlagProbe      = 1 << 1 // asks for an immediate ACK
	tcpFlagOutOfOrder = 1 << 2 // ACK sent because a frame arrived ahead of a gap

	defaultTcpStreamWindow  = 1 << 20
	defaultTcpStreamTimeout = 60 * time.Second
	tcpStreamInitWindow     = 64 << 10 // assumed peer window until its first ACK
	tcpStreamInitRTO        = time.Second
	tcpStreamMinRTO         = 200 * time.Millisecond
	tcpStreamMaxRTO         = 10 * time.Second
	tcpStreamDelayedAck     = 5 * time.Millisecond
	tcpStreamOpenTimeout    = 10 * time.Second // dialing, and waiting for a lost OPEN
	tcpStreamLinger         = 30 * time.Second // a closed stream still answers late frames
)

type tcpFrame struct {
	typ     byte
	flags   byte
	id      ConnID
	seq     uint64
	ack     uint64
	wnd     uint32
	payload []byte
}

func parseTcpFrame(b []byte) (tcpFrame, bool) {
	if len(b) < tcpStreamHeaderSize || binary.BigEndian.Uint32(b) != tcpStreamMagic {
		return tcpFrame{}, false
	}
	f := tcpFrame{
		typ:     b[4],
		flags:   b[5],
		seq:     binary.BigEndian.Uint64(b[16:]),
		ack:     binary.BigEndian.Uint64(b[24:]),
		wnd:     binary.BigEndian.Uint32(b[32:]),
		payload: b[tcpStreamHeaderSize:],
	}
	copy(f.id[:], b[8:16])
	if f.typ < tcpFrameOpen || f.typ > tcpFrameRst {
		return tcpFrame{}, false
	}
	return f, true
}

func putTcpFrame(b []byte, f *tcpFrame) int {
	binary.BigEndian.PutUint32(b, tcpStreamMagic)
	b[4] = f.typ
	b[5] = f.flags
	b[6], b[7] = 0, 0
	copy(b[8:16], f.id[:])
	binary.BigEndian.PutUint64(b[16:], f.seq)
	binary.BigEndian.PutUint64(b[24:], f.ack)
	binary.BigEndian.PutUint32(b[32:], f.wnd)
	return tcpStreamHeaderSize + copy(b[tcpStreamHeaderSize:], f.payload)
}

// tcpStreamKey separates the two ends of a stream when both run in one process.
type tcpStreamKey struct {
	id     ConnID
	client bool
}

// tcpStreams holds the streams of every endpoint, so a frame reaching any
// tcp_listen or tcp_forward finds its stream.
var tcpStreams sync.Map // tcpStreamKey -> *tcpStream

// tcpStreamEndpoint is the stream side of a tcp_listen (client) or tcp_forward.
type tcpStreamEndpoint struct {
	tag           string
	router        *Router
	detour        []string
	client        bool
	mss           int
	window        int
	timeout       time.Duration
	noDelay       bool
	target        string // tcp_forward: overrides the target of OPEN
	interfaceName string // tcp_forward: outbound interface

	streams sync.Map // ConnID -> *tcpStream
	active  atomic.Int64
	stopped atomic.Bool
}

func newTcpStreamEndpoint(cfg ComponentConfig, router *Router, client bool) *tcpStreamEndpoint {
	window := cfg.WindowSize
	if window <= 0 {
		window = defaultTcpStreamWindow
	}
	timeout := defaultTcpStreamTimeout
	if cfg.Timeout > 0 {
		timeout = time.Duration(cfg.Timeout) * time.Second
	}
	noDelay := true
	if cfg.NoDelay != nil {
		noDelay = *cfg.NoDelay
	}
	mss := router.config.BufferSize - tcpStreamWrapReserve - tcpStreamHeaderSize
	if mss < 256 {
		mss = 256
	}
	return &tcpStreamEndpoint{
		tag:           cfg.Tag,
		router:        router,
		detour:        cfg.Detour,
		client:        client,
		mss:           mss,
		window:        max(window, 2*mss),
		timeout:       timeout,
		noDelay:       noDelay,
		target:        cfg.Target,
		interfaceName: cfg.InterfaceName,
	}
}

func (e *tcpStreamEndpoint) send(f *tcpFrame) {
	if e.client {
		f.flags |= tcpFlagFromClient
	}
	packet := e.router.GetPacket(e.tag)
	packet.SetLength(putTcpFrame(packet.BufAtOffset(), f))
	packet.SetConnID(f.id)
	packet.SetNoDrop(true)
	if err := e.router.Route(&packet, e.detour); err != nil {
		logger.Debugf("%s: Failed to route stream frame: %v", e.tag, err)
	}
	packet.Release(1)
}

// handlePacket dispatches a frame from the pipeline to its stream.
func (e *tcpStreamEndpoint) handlePacket(packet *Packet) error {
	defer packet.Release(1)
	f, ok := parseTcpFrame(packet.GetData())
	if !ok {
		return nil
	}
	if (f.flags&tcpFlagFromClient != 0) == e.client {
		return nil // our own frame came back
	}
	key := tcpStreamKey{id: f.id, client: e.client}
	if v, ok := tcpStreams.Load(key); ok {
		v.(*tcpStream).handleFrame(&f)
		return nil
	}
	if f.typ == tcpFrameRst {
		return nil
	}
	if e.client || f.typ == tcpFrameAck || e.stopped.Load() {
		e.send(&tcpFrame{typ: tcpFrameRst, id: f.id})
		return nil
	}
	// A new stream. Its OPEN may still be on a slower line.
	s := newTcpStream(e, f.id, nil)
	if v, loaded := tcpStreams.LoadOrStore(key, s); loaded {
		s = v.(*tcpStream)
	} else {
		e.track(s)
		time.AfterFunc(tcpStreamOpenTimeout, s.checkOpened)
	}
	s.handleFrame(&f)
	return nil
}

// open starts a client stream for an accepted connection.
func (e *tcpStreamEndpoint) open(conn net.Conn, target string) {
	var id ConnID
	for id == (ConnID{}) {
		id = ConnIDFromUint64(rand.Uint64())
	}
	s := newTcpStream(e, id, conn)
	tcpStreams.Store(s.key, s)
	e.track(s)
	s.mu.Lock()
	s.queueSegmentLocked(tcpFrameOpen, []byte(target))
	s.startLocked()
	s.unlockAndFlush()
}

func (e *tcpStreamEndpoint) track(s *tcpStream) {
	e.streams.Store(s.id, s)
	e.active.Add(1)
}

func (e *tcpStreamEndpoint) stop() {
	e.stopped.Store(true)
	e.streams.Range(func(_, v any) bool {
		s := v.(*tcpStream)
		s.mu.Lock()
		s.abortLocked(true)
		s.unlockAndFlush()
		return true
	})
}

type tcpSegment struct {
	seq     uint64
	typ     byte
	data    []byte
	sentAt  time.Time
	retrans bool
}

func (g *tcpSegment) len() uint64 {
	if g.typ == tcpFrameData {
		return uint64(len(g.data))
	}
	return 1
}

type tcpStream struct {
	e    *tcpStreamEndpoint
	id   ConnID
	key  tcpStreamKey
	done chan struct{} // closed when the stream ends

	mu       sync.Mutex
	conn     net.Conn
	out      []tcpFrame // frames to send once mu is released
	opened   bool       // tcp_forward: OPEN received
	dead     bool
	finished bool // closed gracefully; late frames are acknowledged

	// Send side.
	sndUna, sndNxt, sndRight uint64
	unacked                  []*tcpSegment
	finQueued                bool
	srtt, rttvar, rto        time.Duration
	backoff                  uint
	dupAcks                  int
	recover                  uint64 // sndNxt at the last fast retransmit
	lastProgress, lastHeard  time.Time
	timer                    *time.Timer
	timerArmed               bool
	sendCh                   chan struct{}

	// Receive side.
	rcvNxt      uint64
	ooo         map[uint64]*tcpSegment
	rcvQueue    [][]byte
	rcvQueued   int // in-order bytes not yet written to conn
	rcvFin      bool
	finWritten  bool
	advRight    uint64
	ackPending  int
	ackTimer    *time.Timer
	ackTimerSet bool
	writeCh     chan struct{}
}

func newTcpStream(e *tcpStreamEndpoint, id ConnID, conn net.Conn) *tcpStream {
	now := time.Now()
	s := &tcpStream{
		e:            e,
		id:           id,
		key:          tcpStreamKey{id: id, client: e.client},
		done:         make(chan struct{}),
		conn:         conn,
		sndRight:     tcpStreamInitWindow,
		rto:          tcpStreamInitRTO,
		lastProgress: now,
		lastHeard:    now,
		sendCh:       make(chan struct{}, 1),
		ooo:          make(map[uint64]*tcpSegment),
		writeCh:      make(chan struct{}, 1),
	}
	s.timer = time.AfterFunc(time.Hour, s.onTimer)
	s.timer.Stop()
	s.ackTimer = time.AfterFunc(time.Hour, s.onAckTimer)
	s.ackTimer.Stop()
	return s
}

func signal(ch chan struct{}) {
	select {
	case ch <- struct{}{}:
	default:
	}
}

func (s *tcpStream) unlockAndFlush() {
	out := s.out
	s.out = nil
	s.mu.Unlock()
	for i := range out {
		s.e.send(&out[i])
	}
}

// fillAckLocked sets the acknowledgement and window of an outgoing frame.
func (s *tcpStream) fillAckLocked(f *tcpFrame) {
	f.id = s.id
	f.ack = s.rcvNxt
	free := max(s.e.window-s.rcvQueued, 0)
	f.wnd = uint32(free)
	if right := s.rcvNxt + uint64(free); right > s.advRight {
		s.advRight = right
	}
	s.ackPending = 0
	if s.ackTimerSet {
		s.ackTimer.Stop()
		s.ackTimerSet = false
	}
}

func (s *tcpStream) queueAckLocked(flags byte) {
	f := tcpFrame{typ: tcpFrameAck, flags: flags}
	s.fillAckLocked(&f)
	s.out = append(s.out, f)
}

func (s *tcpStream) queueSegmentFrameLocked(g *tcpSegment) {
	f := tcpFrame{typ: g.typ, seq: g.seq, payload: g.data}
	s.fillAckLocked(&f)
	s.out = append(s.out, f)
}

func (s *tcpStream) queueSegmentLocked(typ byte, data []byte) {
	g := &tcpSegment{seq: s.sndNxt, typ: typ, data: data, sentAt: time.Now()}
	s.unacked = append(s.unacked, g)
	s.sndNxt += g.len()
	if typ == tcpFrameFin {
		s.finQueued = true
	}
	s.queueSegmentFrameLocked(g)
	if !s.timerArmed {
		s.armTimerLocked()
	}
}

// startLocked starts copying between conn and the stream.
func (s *tcpStream) startLocked() {
	if tcpConn, ok := s.conn.(*net.TCPConn); ok {
		_ = tcpConn.SetNoDelay(s.e.noDelay)
	}
	go s.readLoop()
	go s.writeLoop()
}

func (s *tcpStream) handleFrame(f *tcpFrame) {
	s.mu.Lock()
	if s.dead {
		if f.typ != tcpFrameRst {
			if s.finished {
				// Our ACK of the peer's FIN may have been lost. Pure ACKs are
				// not answered, or two closed ends would echo forever.
				if f.typ != tcpFrameAck || f.flags&tcpFlagProbe != 0 {
					s.queueAckLocked(0)
				}
			} else {
				s.out = append(s.out, tcpFrame{typ: tcpFrameRst, id: s.id})
			}
		}
		s.unlockAndFlush()
		return
	}
	s.lastHeard = time.Now()
	if f.typ == tcpFrameRst {
		s.abortLocked(false)
		s.unlockAndFlush()
		return
	}
	s.processAckLocked(f)
	switch f.typ {
	case tcpFrameOpen, tcpFrameData, tcpFrameFin:
		s.processSegmentLocked(f)
	}
	if f.flags&tcpFlagProbe != 0 && len(s.out) == 0 {
		s.queueAckLocked(0)
	}
	s.maybeFinishLocked()
	s.unlockAndFlush()
}

func (s *tcpStream) processAckLocked(f *tcpFrame) {
	if f.ack > s.sndNxt {
		return
	}
	now := time.Now()
	if f.ack > s.sndUna {
		i := 0
		for i < len(s.unacked) && s.unacked[i].seq+s.unacked[i].len() <= f.ack {
			// Karn: only segments sent once give an RTT sample.
			if i == 0 && !s.unacked[0].retrans {
				s.updateRTTLocked(now.Sub(s.unacked[0].sentAt))
			}
			i++
		}
		clear(s.unacked[:i])
		s.unacked = s.unacked[i:]
		s.sndUna = f.ack
		s.backoff = 0
		s.dupAcks = 0
		s.lastProgress = now
		if f.ack < s.recover {
			// Partial ACK after a fast retransmit: the next hole is lost too.
			s.fastRetransmitLocked(now)
		}
		s.armTimerLocked()
	} else if f.ack == s.sndUna && f.flags&tcpFlagOutOfOrder != 0 && len(s.unacked) > 0 {
		s.dupAcks++
		if s.dupAcks == 3 {
			s.recover = s.sndNxt
			s.fastRetransmitLocked(now)
		}
	}
	if right := f.ack + uint64(f.wnd); right > s.sndRight {
		s.sndRight = right
		if !s.timerArmed {
			s.armTimerLocked()
		}
	}
	if s.sndRight > s.sndNxt {
		signal(s.sendCh)
	}
}

// fastRetransmitLocked resends the first unacknowledged segment unless it
// was sent too recently to be lost (frames on another line may just be slower).
func (s *tcpStream) fastRetransmitLocked(now time.Time) {
	if len(s.unacked) == 0 {
		return
	}
	g := s.unacked[0]
	if now.Sub(g.sentAt) < s.srtt+s.srtt/4 {
		return
	}
	g.retrans = true
	g.sentAt = now
	s.queueSegmentFrameLocked(g)
}

func (s *tcpStream) updateRTTLocked(rtt time.Duration) {
	if s.srtt == 0 {
		s.srtt = rtt
		s.rttvar = rtt / 2
	} else {
		diff := s.srtt - rtt
		if diff < 0 {
			diff = -diff
		}
		s.rttvar = (3*s.rttvar + diff) / 4
		s.srtt = (7*s.srtt + rtt) / 8
	}
	s.rto = s.srtt + 4*s.rttvar
	if s.rto < tcpStreamMinRTO {
		s.rto = tcpStreamMinRTO
	} else if s.rto > tcpStreamMaxRTO {
		s.rto = tcpStreamMaxRTO
	}
}

// armTimerLocked (re)starts the retransmission timer while data is
// unacknowledged, or the probe timer while the peer's window is closed.
func (s *tcpStream) armTimerLocked() {
	if s.dead || (len(s.unacked) == 0 && (s.sndRight > s.sndNxt || s.finQueued)) {
		if s.timerArmed {
			s.timer.Stop()
			s.timerArmed = false
		}
		return
	}
	s.timer.Reset(min(s.rto<<s.backoff, tcpStreamMaxRTO))
	s.timerArmed = true
}

func (s *tcpStream) onTimer() {
	s.mu.Lock()
	s.timerArmed = false
	if s.dead {
		s.mu.Unlock()
		return
	}
	now := time.Now()
	if len(s.unacked) > 0 {
		if now.Sub(s.lastProgress) > s.e.timeout {
			logger.Infof("%s: Stream %x timed out waiting for ACK", s.e.tag, s.id)
			s.abortLocked(true)
			s.unlockAndFlush()
			return
		}
		// Every unacknowledged segment may have been on a line that failed.
		for _, g := range s.unacked {
			g.retrans = true
			g.sentAt = now
			s.queueSegmentFrameLocked(g)
		}
	} else {
		if now.Sub(s.lastHeard) > s.e.timeout {
			logger.Infof("%s: Stream %x timed out probing a closed window", s.e.tag, s.id)
			s.abortLocked(true)
			s.unlockAndFlush()
			return
		}
		s.queueAckLocked(tcpFlagProbe)
	}
	if s.backoff < 6 {
		s.backoff++
	}
	s.armTimerLocked()
	s.unlockAndFlush()
}

func (s *tcpStream) processSegmentLocked(f *tcpFrame) {
	g := &tcpSegment{seq: f.seq, typ: f.typ}
	if f.typ != tcpFrameFin {
		g.data = f.payload
	}
	end := g.seq + g.len()
	if end <= s.rcvNxt {
		s.queueAckLocked(0) // duplicate; our ACK may have been lost
		return
	}
	if g.seq != s.rcvNxt {
		if g.seq > s.rcvNxt && end <= s.rcvNxt+uint64(s.e.window) {
			if _, dup := s.ooo[g.seq]; !dup {
				g.data = append([]byte(nil), g.data...)
				s.ooo[g.seq] = g
			}
		}
		s.queueAckLocked(tcpFlagOutOfOrder)
		return
	}
	if g.typ == tcpFrameData && s.rcvQueued+len(g.data) > s.e.window+s.e.mss {
		return // beyond the advertised window
	}
	g.data = append([]byte(nil), g.data...)
	s.deliverLocked(g)
	filled := false
	for {
		next, ok := s.ooo[s.rcvNxt]
		if !ok {
			break
		}
		delete(s.ooo, s.rcvNxt)
		s.deliverLocked(next)
		filled = true
	}
	s.ackPending++
	if filled || g.typ != tcpFrameData || s.ackPending >= 2 {
		s.queueAckLocked(0)
	} else if !s.ackTimerSet {
		s.ackTimer.Reset(tcpStreamDelayedAck)
		s.ackTimerSet = true
	}
}

func (s *tcpStream) deliverLocked(g *tcpSegment) {
	s.rcvNxt += g.len()
	switch g.typ {
	case tcpFrameOpen:
		if !s.e.client && !s.opened {
			s.opened = true
			target := s.e.target
			if target == "" {
				target = string(g.data)
			}
			go s.dial(target)
		}
	case tcpFrameData:
		if len(g.data) > 0 {
			s.rcvQueue = append(s.rcvQueue, g.data)
			s.rcvQueued += len(g.data)
			signal(s.writeCh)
		}
	case tcpFrameFin:
		s.rcvFin = true
		signal(s.writeCh)
	}
}

func (s *tcpStream) onAckTimer() {
	s.mu.Lock()
	s.ackTimerSet = false
	if !s.dead && s.ackPending > 0 {
		s.queueAckLocked(0)
	}
	s.unlockAndFlush()
}

// checkOpened resets a tcp_forward stream whose OPEN never arrived.
func (s *tcpStream) checkOpened() {
	s.mu.Lock()
	if !s.opened && !s.dead {
		logger.Infof("%s: Stream %x got data but no OPEN, resetting", s.e.tag, s.id)
		s.abortLocked(true)
	}
	s.unlockAndFlush()
}

func (s *tcpStream) dial(target string) {
	conn, err := dialTCPWithInterfaceTimeout(target, s.e.interfaceName, tcpStreamOpenTimeout)
	s.mu.Lock()
	if err != nil {
		logger.Infof("%s: Stream %x failed to connect to %s: %v", s.e.tag, s.id, target, err)
		s.abortLocked(true)
		s.unlockAndFlush()
		return
	}
	if s.dead {
		s.mu.Unlock()
		_ = conn.Close()
		return
	}
	logger.Debugf("%s: Stream %x connected to %s", s.e.tag, s.id, target)
	s.conn = conn
	s.startLocked()
	s.unlockAndFlush()
}

// readLoop sends what conn reads while the peer's window has room.
func (s *tcpStream) readLoop() {
	buf := make([]byte, s.e.mss)
	for {
		s.mu.Lock()
		for {
			if s.dead {
				s.mu.Unlock()
				return
			}
			space := int64(s.sndRight) - int64(s.sndNxt)
			// Avoid tiny segments unless nothing is in flight.
			if space >= int64(s.e.mss) || (space > 0 && len(s.unacked) == 0) {
				break
			}
			if !s.timerArmed {
				s.armTimerLocked() // probe the closed window
			}
			s.mu.Unlock()
			select {
			case <-s.sendCh:
			case <-s.done:
			}
			s.mu.Lock()
		}
		n := min(int(s.sndRight-s.sndNxt), s.e.mss)
		conn := s.conn
		s.mu.Unlock()

		n, err := conn.Read(buf[:n])
		s.mu.Lock()
		if s.dead {
			s.mu.Unlock()
			return
		}
		if n > 0 {
			s.queueSegmentLocked(tcpFrameData, append([]byte(nil), buf[:n]...))
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				s.queueSegmentLocked(tcpFrameFin, nil)
				s.maybeFinishLocked()
			} else {
				s.abortLocked(true)
			}
			s.unlockAndFlush()
			return
		}
		s.unlockAndFlush()
	}
}

// writeLoop writes the in-order bytes to conn.
func (s *tcpStream) writeLoop() {
	for {
		s.mu.Lock()
		for !s.dead && len(s.rcvQueue) == 0 && !s.rcvFin {
			s.mu.Unlock()
			select {
			case <-s.writeCh:
			case <-s.done:
			}
			s.mu.Lock()
		}
		if s.dead {
			s.mu.Unlock()
			return
		}
		conn := s.conn
		if len(s.rcvQueue) == 0 {
			s.mu.Unlock()
			closeWrite(conn)
			s.mu.Lock()
			s.finWritten = true
			s.maybeFinishLocked()
			s.unlockAndFlush()
			return
		}
		bufs := net.Buffers(s.rcvQueue)
		s.rcvQueue = nil
		s.mu.Unlock()

		n, err := bufs.WriteTo(conn)
		s.mu.Lock()
		if err != nil {
			if !s.dead {
				logger.Debugf("%s: Stream %x write error: %v", s.e.tag, s.id, err)
			}
			s.abortLocked(true)
			s.unlockAndFlush()
			return
		}
		s.rcvQueued -= int(n)
		// Tell the sender once the window has opened by a quarter.
		if s.rcvNxt+uint64(max(s.e.window-s.rcvQueued, 0)) >= s.advRight+uint64(s.e.window/4) {
			s.queueAckLocked(0)
		}
		s.unlockAndFlush()
	}
}

func closeWrite(conn net.Conn) {
	if cw, ok := conn.(interface{ CloseWrite() error }); ok {
		_ = cw.CloseWrite()
		return
	}
	_ = conn.Close()
}

// maybeFinishLocked closes the stream once both directions are done.
func (s *tcpStream) maybeFinishLocked() {
	if s.dead || !s.finQueued || s.sndUna != s.sndNxt || !s.finWritten {
		return
	}
	s.finished = true
	s.endLocked()
	if s.conn != nil {
		_ = s.conn.Close()
	}
}

// abortLocked resets the stream, telling the peer unless it reset first.
func (s *tcpStream) abortLocked(notifyPeer bool) {
	if s.dead {
		return
	}
	if notifyPeer {
		s.out = append(s.out, tcpFrame{typ: tcpFrameRst, id: s.id})
	}
	s.endLocked()
	if s.conn != nil {
		if tcpConn, ok := s.conn.(*net.TCPConn); ok {
			_ = tcpConn.SetLinger(0) // pass the reset on
		}
		_ = s.conn.Close()
	}
}

func (s *tcpStream) endLocked() {
	s.dead = true
	close(s.done)
	s.timer.Stop()
	s.timerArmed = false
	s.ackTimer.Stop()
	s.ackTimerSet = false
	s.unacked = nil
	s.ooo = nil
	s.rcvQueue = nil
	s.e.active.Add(-1)
	time.AfterFunc(tcpStreamLinger, func() {
		tcpStreams.CompareAndDelete(s.key, s)
		s.e.streams.CompareAndDelete(s.id, s)
	})
}
