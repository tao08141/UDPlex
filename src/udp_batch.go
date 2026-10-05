package main

import (
	"errors"
	"net"

	"golang.org/x/net/ipv4"
)

const (
	defaultUDPBatchSize = 64

	// GRO super-datagrams can be up to 64KB; read them into dedicated buffers.
	udpGROBufSize    = 65535
	udpGROMaxBatch   = 16
	udpGSOMaxSegs    = 64
	udpGSOMaxPayload = 65000
	// Never coalesce segments larger than what fits an IPv6 1500-byte MTU, so
	// oversized datagrams keep relying on IP fragmentation instead of failing GSO.
	udpGSOMaxSegSize = 1452
)

// udpIOOptions controls batched UDP I/O for listen/forward sockets.
type udpIOOptions struct {
	batchSize int  // datagrams per recvmmsg/sendmmsg, 1 disables batching
	offload   bool // UDP GRO on reads and GSO on writes (Linux only)
}

func newUDPIOOptions(cfg Config) udpIOOptions {
	opts := udpIOOptions{batchSize: cfg.UDPBatchSize, offload: true}
	if opts.batchSize <= 0 {
		opts.batchSize = defaultUDPBatchSize
	}
	if cfg.UDPOffload != nil {
		opts.offload = *cfg.UDPOffload
	}
	if !udpBatchSupported {
		opts.batchSize = 1
		opts.offload = false
	}
	return opts
}

type udpReadItem struct {
	buf  []byte
	n    int
	addr net.Addr
}

// udpBatchReader reads datagrams with recvmmsg and, when UDP GRO is enabled,
// splits coalesced super-datagrams back into the original datagrams.
type udpBatchReader struct {
	conn    *net.UDPConn
	pc      *ipv4.PacketConn
	router  *Router
	msgs    []ipv4.Message
	bufs    [][]byte
	gro     bool
	items   []udpReadItem
	batchID uint64
}

func newUDPBatchReader(conn *net.UDPConn, router *Router) *udpBatchReader {
	opts := router.udpIO
	r := &udpBatchReader{conn: conn, router: router}
	n := opts.batchSize
	if n > 1 {
		r.pc = ipv4.NewPacketConn(conn)
	}
	if opts.offload && enableUDPGRO(conn) {
		r.gro = true
		r.pc = ipv4.NewPacketConn(conn)
		n = min(max(n, 1), udpGROMaxBatch)
	}
	r.msgs = make([]ipv4.Message, n)
	r.bufs = make([][]byte, n)
	for i := range r.msgs {
		if r.gro {
			r.bufs[i] = make([]byte, udpGROBufSize)
			r.msgs[i].Buffers = [][]byte{r.bufs[i]}
			r.msgs[i].OOB = make([]byte, udpGROControlSize)
		} else {
			r.bufs[i] = router.GetBuffer()
			r.msgs[i].Buffers = [][]byte{r.bufs[i][router.config.BufferOffset:]}
		}
	}
	r.items = make([]udpReadItem, 0, n)
	return r
}

// Read blocks for at least one datagram and returns how many were received.
// Use Take to claim each of them.
func (r *udpBatchReader) Read() (int, error) {
	r.items = r.items[:0]
	r.batchID = r.router.NextBatchID()
	if r.pc == nil {
		n, addr, err := r.conn.ReadFromUDP(r.msgs[0].Buffers[0])
		if err != nil {
			return 0, err
		}
		r.handoff(0, n, addr)
		return 1, nil
	}

	n, err := r.pc.ReadBatch(r.msgs, 0)
	if err != nil {
		return 0, err
	}
	off := r.router.config.BufferOffset
	maxLen := r.router.config.BufferSize
	for i := 0; i < n; i++ {
		m := &r.msgs[i]
		if !r.gro {
			r.handoff(i, m.N, m.Addr)
			continue
		}
		seg := max(m.N, 1) // an empty datagram still yields one packet
		if s := udpGROSegmentSize(m.OOB[:m.NN]); s > 0 && s < m.N {
			seg = s
		}
		for start := 0; start < max(m.N, 1); start += seg {
			end := min(start+seg, m.N)
			size := min(end-start, maxLen) // truncate like a plain read into a BufferSize buffer
			buf := r.router.GetBuffer()
			copy(buf[off:off+size], r.bufs[i][start:start+size])
			r.items = append(r.items, udpReadItem{buf: buf, n: size, addr: m.Addr})
		}
		m.OOB = m.OOB[:cap(m.OOB)]
	}
	return len(r.items), nil
}

// handoff gives the message buffer to the caller and refills the slot.
func (r *udpBatchReader) handoff(i, n int, addr net.Addr) {
	r.items = append(r.items, udpReadItem{buf: r.bufs[i], n: n, addr: addr})
	buf := r.router.GetBuffer()
	r.bufs[i] = buf
	r.msgs[i].Buffers[0] = buf[r.router.config.BufferOffset:]
}

// Take wraps datagram i of the last Read into a packet owning its buffer.
func (r *udpBatchReader) Take(i int, srcTag string) (Packet, net.Addr) {
	item := r.items[i]
	packet := r.router.GetPacketWithBuffer(srcTag, item.buf, r.router.config.BufferOffset)
	packet.SetLength(item.n)
	packet.batchID = r.batchID
	return packet, item.addr
}

// Close returns the buffers still owned by the reader to the pool.
func (r *udpBatchReader) Close() {
	if r.gro {
		return
	}
	for _, buf := range r.bufs {
		r.router.PutBuffer(buf)
	}
	r.bufs = nil
}

// udpBatchWriter writes datagrams with sendmmsg and, when enabled, coalesces
// runs of equal-sized datagrams to the same destination with UDP GSO.
type udpBatchWriter struct {
	router *Router
	conn   *net.UDPConn
	pc     *ipv4.PacketConn
	msgs   []ipv4.Message
	oob    [][]byte
	gso    bool
	maxSeg int // largest segment size GSO is used for; lowered when the path MTU rejects it
	shrink int // number of times maxSeg was lowered
	tag    string
}

func newUDPBatchWriter(router *Router, tag string) *udpBatchWriter {
	opts := router.udpIO
	w := &udpBatchWriter{router: router, gso: opts.offload, maxSeg: udpGSOMaxSegSize, tag: tag}
	if opts.batchSize > 1 || opts.offload {
		w.msgs = make([]ipv4.Message, max(opts.batchSize, 1))
		w.oob = make([][]byte, len(w.msgs))
		for i := range w.msgs {
			w.msgs[i].Buffers = make([][]byte, 0, udpGSOMaxSegs)
		}
	}
	return w
}

// MaxBatch is the number of datagrams the caller should collect per Write.
func (w *udpBatchWriter) MaxBatch() int {
	return max(len(w.msgs), 1)
}

// Write sends data[i] to addrs[i], or to the connected peer when addrs is nil.
// It returns the number of datagrams handed to the kernel; on error the
// datagram at that index failed and the rest were not attempted.
func (w *udpBatchWriter) Write(conn *net.UDPConn, data [][]byte, addrs []net.Addr) (int, error) {
	if w.msgs == nil {
		for i, d := range data {
			var err error
			if addrs == nil {
				_, err = conn.Write(d)
			} else {
				_, err = conn.WriteTo(d, addrs[i])
			}
			if err != nil {
				return i, err
			}
		}
		return len(data), nil
	}
	if w.conn != conn {
		w.conn = conn
		w.pc = ipv4.NewPacketConn(conn)
	}

	// Build one message per datagram, or per GSO run of datagrams.
	var startsBuf [udpGSOMaxSegs * 4]int
	starts := startsBuf[:0] // index in data of each message's first datagram
	nm := 0
	for i := 0; i < len(data) && nm < len(w.msgs); {
		m := &w.msgs[nm]
		m.Buffers = append(m.Buffers[:0], data[i])
		m.OOB = nil
		m.Addr = nil
		if addrs != nil {
			m.Addr = addrs[i]
		}
		starts = append(starts, i)
		segSize, total := len(data[i]), len(data[i])
		i++
		// Empty datagrams cannot be GSO segments, so they are always sent on their own.
		for w.gso && segSize > 0 && segSize <= w.maxSeg && i < len(data) && len(m.Buffers) < udpGSOMaxSegs {
			d := data[i]
			if len(d) == 0 || len(d) > segSize || total+len(d) > udpGSOMaxPayload || (addrs != nil && !sameUDPAddr(addrs[i], m.Addr)) {
				break
			}
			m.Buffers = append(m.Buffers, d)
			total += len(d)
			i++
			if len(d) < segSize {
				break // only the last segment of a GSO run may be shorter
			}
		}
		if len(m.Buffers) > 1 {
			w.oob[nm] = udpGSOControl(w.oob[nm], segSize)
			m.OOB = w.oob[nm]
		}
		nm++
	}

	sentMsgs := 0
	for sentMsgs < nm {
		n, err := w.pc.WriteBatch(w.msgs[sentMsgs:nm], 0)
		if err != nil {
			if len(w.msgs[sentMsgs].Buffers) > 1 && isUDPGSOError(err) {
				w.onGSOError(len(w.msgs[sentMsgs].Buffers[0]), err)
				done := starts[sentMsgs]
				n, err := w.Write(conn, data[done:], sliceAddrs(addrs, done))
				return done + n, err
			}
			return starts[sentMsgs], err
		}
		if n <= 0 {
			return starts[sentMsgs], errors.New("sendmmsg made no progress")
		}
		sentMsgs += n
	}
	if nm > 0 {
		if done := starts[nm-1] + len(w.msgs[nm-1].Buffers); done < len(data) {
			// More datagrams than message slots: send the remainder.
			n, err := w.Write(conn, data[done:], sliceAddrs(addrs, done))
			return done + n, err
		}
	}
	return len(data), nil
}

// onGSOError adapts after the kernel rejected a GSO send of segSize segments.
// EINVAL usually means the segment does not fit the path MTU, so only larger
// segments stop using GSO; other errors (e.g. EIO without checksum offload)
// disable GSO for this writer.
func (w *udpBatchWriter) onGSOError(segSize int, err error) {
	if isUDPGSOSizeError(err) && w.shrink < 8 && segSize > 576 {
		w.maxSeg = segSize - 1
		w.shrink++
		logger.Infof("%s: UDP GSO rejected %d-byte segments, limiting GSO to %d bytes: %v", w.tag, segSize, w.maxSeg, err)
		return
	}
	w.gso = false
	logger.Warnf("%s: UDP GSO unavailable, falling back to sendmmsg: %v", w.tag, err)
}

func sliceAddrs(addrs []net.Addr, from int) []net.Addr {
	if addrs == nil {
		return nil
	}
	return addrs[from:]
}

func sameUDPAddr(a, b net.Addr) bool {
	if a == b {
		return true
	}
	ua, ok1 := a.(*net.UDPAddr)
	ub, ok2 := b.(*net.UDPAddr)
	return ok1 && ok2 && ua.Port == ub.Port && ua.IP.Equal(ub.IP) && ua.Zone == ub.Zone
}
