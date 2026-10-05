//go:build linux

package main

import (
	"bytes"
	"net"
	"testing"
	"time"
)

func testUDPRouter(batchSize int, offload bool) *Router {
	return NewRouter(Config{BufferSize: 2048, UDPBatchSize: batchSize, UDPOffload: &offload})
}

func testPayloads(sizes []int) [][]byte {
	out := make([][]byte, len(sizes))
	for i, size := range sizes {
		out[i] = make([]byte, size)
		for j := range out[i] {
			out[i][j] = byte(i*7 + j)
		}
	}
	return out
}

// mixed sizes: runs of equal size, short tails and an oversized datagram
func testSizes(n int) []int {
	sizes := make([]int, n)
	for i := range sizes {
		switch {
		case i%50 == 49:
			sizes[i] = 300
		case i%70 == 69:
			sizes[i] = 1500
		case i%90 == 45 || i%90 == 46:
			sizes[i] = 0
		default:
			sizes[i] = 1400
		}
	}
	return sizes
}

type testDatagram struct {
	data []byte
	addr net.Addr
}

func readDatagrams(t *testing.T, r *udpBatchReader, conn *net.UDPConn, want int) []testDatagram {
	t.Helper()
	var got []testDatagram
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	for len(got) < want {
		n, err := r.Read()
		if err != nil {
			t.Fatalf("read after %d datagrams: %v", len(got), err)
		}
		for i := 0; i < n; i++ {
			p, addr := r.Take(i, "t")
			if p.BatchID() == 0 {
				t.Fatal("packet without batch id")
			}
			got = append(got, testDatagram{data: append([]byte(nil), p.GetData()...), addr: addr})
			p.Release(1)
		}
	}
	return got
}

func TestUDPBatchRoundTrip(t *testing.T) {
	for _, tc := range []struct {
		name      string
		batchSize int
		offload   bool
	}{
		{"single", 1, false},
		{"mmsg", 64, false},
		{"gso_gro", 64, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := testUDPRouter(tc.batchSize, tc.offload)
			rx, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
			if err != nil {
				t.Fatal(err)
			}
			defer rx.Close()
			_ = rx.SetReadBuffer(8 << 20)
			tx, err := net.DialUDP("udp4", nil, rx.LocalAddr().(*net.UDPAddr))
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Close()

			reader := newUDPBatchReader(rx, r)
			defer reader.Close()
			if tc.offload && !reader.gro {
				t.Skip("UDP_GRO unavailable")
			}
			w := newUDPBatchWriter(r, "t")

			want := testPayloads(testSizes(300))
			for start := 0; start < len(want); start += w.MaxBatch() {
				end := min(start+w.MaxBatch(), len(want))
				n, err := w.Write(tx, want[start:end], nil)
				if err != nil || n != end-start {
					t.Fatalf("write: n=%d err=%v", n, err)
				}
			}
			if tc.offload && !w.gso {
				t.Fatal("GSO got disabled")
			}

			got := readDatagrams(t, reader, rx, len(want))
			for i := range want {
				if !bytes.Equal(got[i].data, want[i]) {
					t.Fatalf("datagram %d mismatch: len got %d want %d", i, len(got[i].data), len(want[i]))
				}
			}
		})
	}
}

// A socket bound to the unspecified address is dual-stack; batched and GSO
// writes must reach both IPv4 and IPv6 peers from it.
func TestUDPBatchDualStack(t *testing.T) {
	for _, offload := range []bool{false, true} {
		r := testUDPRouter(64, offload)
		srv, err := net.ListenUDP("udp", &net.UDPAddr{})
		if err != nil {
			t.Fatal(err)
		}
		port := srv.LocalAddr().(*net.UDPAddr).Port

		rx4, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
		if err != nil {
			t.Fatal(err)
		}
		rx6, err := net.ListenUDP("udp6", &net.UDPAddr{IP: net.IPv6loopback})
		if err != nil {
			rx4.Close()
			srv.Close()
			t.Skip("IPv6 loopback unavailable")
		}

		// Interleave destinations so GSO runs must split on address changes.
		var data [][]byte
		var addrs []net.Addr
		payloads := testPayloads(testSizes(40))
		for i, d := range payloads {
			data = append(data, d)
			if (i/5)%2 == 0 {
				addrs = append(addrs, rx4.LocalAddr())
			} else {
				addrs = append(addrs, rx6.LocalAddr())
			}
		}
		w := newUDPBatchWriter(r, "t")
		if n, err := w.Write(srv, data, addrs); err != nil || n != len(data) {
			t.Fatalf("offload=%v: write n=%d err=%v", offload, n, err)
		}
		if offload && !w.gso {
			t.Fatal("GSO got disabled on a dual-stack socket")
		}

		for _, rx := range []*net.UDPConn{rx4, rx6} {
			var want [][]byte
			for i := range data {
				if addrs[i] == rx.LocalAddr() {
					want = append(want, data[i])
				}
			}
			reader := newUDPBatchReader(rx, testUDPRouter(64, false))
			got := readDatagrams(t, reader, rx, len(want))
			for i := range want {
				if !bytes.Equal(got[i].data, want[i]) {
					t.Fatalf("offload=%v %s: datagram %d mismatch", offload, rx.LocalAddr(), i)
				}
				if src := got[i].addr.(*net.UDPAddr); src.Port != port {
					t.Fatalf("offload=%v: unexpected source %s", offload, src)
				}
			}
			reader.Close()
		}

		// And reads on the dual-stack socket see both families.
		reader := newUDPBatchReader(srv, r)
		for _, c := range []*net.UDPConn{rx4, rx6} {
			if _, err := c.WriteTo([]byte("ping"), &net.UDPAddr{IP: map[bool]net.IP{true: net.IPv4(127, 0, 0, 1), false: net.IPv6loopback}[c == rx4], Port: port}); err != nil {
				t.Fatal(err)
			}
		}
		got := readDatagrams(t, reader, srv, 2)
		for _, g := range got {
			if string(g.data) != "ping" {
				t.Fatalf("unexpected payload %q", g.data)
			}
		}
		reader.Close()
		rx4.Close()
		rx6.Close()
		srv.Close()
	}
}
