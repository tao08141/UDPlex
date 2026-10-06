//go:build linux

package main

import (
	"net"
	"time"

	"golang.org/x/sys/unix"
)

// setTCPCongestion selects the congestion control algorithm of conn.
func setTCPCongestion(conn net.Conn, name string) error {
	return tcpControl(conn, func(fd int) error {
		return unix.SetsockoptString(fd, unix.IPPROTO_TCP, unix.TCP_CONGESTION, name)
	})
}

// setTCPPacingRate caps the send rate of conn in bytes per second; the kernel
// paces packets instead of sending cwnd-sized bursts.
func setTCPPacingRate(conn net.Conn, bytesPerSecond uint64) error {
	return tcpControl(conn, func(fd int) error {
		return unix.SetsockoptUint64(fd, unix.SOL_SOCKET, unix.SO_MAX_PACING_RATE, bytesPerSecond)
	})
}

// getTCPInfo reads the kernel's view of conn.
func getTCPInfo(conn net.Conn) (tcpInfo, bool) {
	var info tcpInfo
	err := tcpControl(conn, func(fd int) error {
		ti, err := unix.GetsockoptTCPInfo(fd, unix.IPPROTO_TCP, unix.TCP_INFO)
		if err != nil {
			return err
		}
		info = tcpInfo{
			RTT:          time.Duration(ti.Rtt) * time.Microsecond,
			MinRTT:       time.Duration(ti.Min_rtt) * time.Microsecond,
			Cwnd:         ti.Snd_cwnd,
			Retrans:      ti.Total_retrans,
			NotsentBytes: ti.Notsent_bytes,
			PacingRate:   ti.Pacing_rate,
			DeliveryRate: ti.Delivery_rate,
		}
		return nil
	})
	return info, err == nil
}
