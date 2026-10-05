//go:build linux

package main

import (
	"encoding/binary"
	"errors"
	"net"
	"unsafe"

	"golang.org/x/sys/unix"
)

const udpBatchSupported = true

var udpGROControlSize = unix.CmsgSpace(4)

// enableUDPGRO asks the kernel to coalesce received datagrams (Linux 5.0+).
func enableUDPGRO(conn *net.UDPConn) bool {
	rc, err := conn.SyscallConn()
	if err != nil {
		return false
	}
	var serr error
	if err := rc.Control(func(fd uintptr) {
		serr = unix.SetsockoptInt(int(fd), unix.SOL_UDP, unix.UDP_GRO, 1)
	}); err != nil || serr != nil {
		return false
	}
	return true
}

// udpGROSegmentSize returns the segment size from a UDP_GRO control message, or 0.
func udpGROSegmentSize(oob []byte) int {
	for len(oob) >= unix.SizeofCmsghdr {
		h := (*unix.Cmsghdr)(unsafe.Pointer(&oob[0]))
		if h.Len < unix.SizeofCmsghdr || int(h.Len) > len(oob) {
			return 0
		}
		if h.Level == unix.SOL_UDP && h.Type == unix.UDP_GRO {
			data := oob[unix.CmsgLen(0):h.Len]
			if len(data) >= 4 {
				return int(binary.NativeEndian.Uint32(data))
			}
			if len(data) >= 2 {
				return int(binary.NativeEndian.Uint16(data))
			}
			return 0
		}
		oob = oob[unix.CmsgSpace(int(h.Len)-unix.CmsgLen(0)):]
	}
	return 0
}

// udpGSOControl builds a UDP_SEGMENT control message into buf.
func udpGSOControl(buf []byte, segSize int) []byte {
	need := unix.CmsgSpace(2)
	if cap(buf) < need {
		buf = make([]byte, need)
	}
	buf = buf[:need]
	clear(buf)
	h := (*unix.Cmsghdr)(unsafe.Pointer(&buf[0]))
	h.Level = unix.SOL_UDP
	h.Type = unix.UDP_SEGMENT
	h.SetLen(unix.CmsgLen(2))
	binary.NativeEndian.PutUint16(buf[unix.CmsgLen(0):], uint16(segSize))
	return buf
}

func isUDPGSOError(err error) bool {
	return errors.Is(err, unix.EIO) || errors.Is(err, unix.EINVAL) || errors.Is(err, unix.EMSGSIZE) || errors.Is(err, unix.ENOPROTOOPT) || errors.Is(err, unix.EOPNOTSUPP)
}

func isUDPGSOSizeError(err error) bool {
	return errors.Is(err, unix.EINVAL) || errors.Is(err, unix.EMSGSIZE)
}
