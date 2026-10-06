//go:build !linux && !darwin

package main

import "net"

// setTCPNotsentLowat is not supported on this platform.
func setTCPNotsentLowat(conn net.Conn, bytes int) error { return nil }
