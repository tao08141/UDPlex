//go:build linux || darwin

package main

import (
	"net"

	"golang.org/x/sys/unix"
)

// setTCPNotsentLowat limits the unsent bytes the kernel buffers for conn.
func setTCPNotsentLowat(conn net.Conn, bytes int) error {
	return tcpSetsockoptInt(conn, unix.IPPROTO_TCP, unix.TCP_NOTSENT_LOWAT, bytes)
}

func tcpSetsockoptInt(conn net.Conn, level, opt, value int) error {
	return tcpControl(conn, func(fd int) error { return unix.SetsockoptInt(fd, level, opt, value) })
}

// tcpControl runs fn on the socket of a TCP connection; other connections are ignored.
func tcpControl(conn net.Conn, fn func(fd int) error) error {
	tc, ok := conn.(*net.TCPConn)
	if !ok {
		return nil
	}
	raw, err := tc.SyscallConn()
	if err != nil {
		return err
	}
	var ferr error
	if err := raw.Control(func(fd uintptr) { ferr = fn(int(fd)) }); err != nil {
		return err
	}
	return ferr
}
