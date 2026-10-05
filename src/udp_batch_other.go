//go:build !linux

package main

import "net"

const udpBatchSupported = false

var udpGROControlSize = 0

func enableUDPGRO(conn *net.UDPConn) bool { return false }

func udpGROSegmentSize(oob []byte) int { return 0 }

func udpGSOControl(buf []byte, segSize int) []byte { return buf }

func isUDPGSOError(err error) bool { return false }

func isUDPGSOSizeError(err error) bool { return false }
