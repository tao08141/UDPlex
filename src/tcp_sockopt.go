package main

import "time"

// tcpInfo is the subset of the kernel TCP_INFO reported by the API.
type tcpInfo struct {
	RTT          time.Duration
	MinRTT       time.Duration
	Cwnd         uint32
	Retrans      uint32
	NotsentBytes uint32
	PacingRate   uint64 // bytes per second
	DeliveryRate uint64 // bytes per second
}
