//go:build !linux

package main

import (
	"errors"
	"net"
)

var errTCPOptionUnsupported = errors.New("not supported on this platform")

func setTCPCongestion(conn net.Conn, name string) error { return errTCPOptionUnsupported }

func setTCPPacingRate(conn net.Conn, bytesPerSecond uint64) error { return errTCPOptionUnsupported }

func getTCPInfo(conn net.Conn) (tcpInfo, bool) { return tcpInfo{}, false }
