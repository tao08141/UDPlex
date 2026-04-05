package main

import (
	"net"
	"testing"
)

func TestParseOutboundForwarderSpec(t *testing.T) {
	spec, err := parseOutboundForwarderSpec("203.0.113.10:9000@eth1", "")
	if err != nil {
		t.Fatalf("parseOutboundForwarderSpec returned error: %v", err)
	}
	if spec.address != "203.0.113.10:9000" {
		t.Fatalf("unexpected address: %s", spec.address)
	}
	if spec.interfaceName != "eth1" {
		t.Fatalf("unexpected interface name: %s", spec.interfaceName)
	}

	spec, err = parseOutboundForwarderSpec("203.0.113.11:9001", "eth9")
	if err != nil {
		t.Fatalf("parseOutboundForwarderSpec with default interface returned error: %v", err)
	}
	if spec.interfaceName != "eth9" {
		t.Fatalf("expected default interface eth9, got %s", spec.interfaceName)
	}
}

func TestParseOutboundForwarderSpecRejectsMissingInterfaceName(t *testing.T) {
	if _, err := parseOutboundForwarderSpec("203.0.113.10:9000@", ""); err == nil {
		t.Fatal("expected parse error for missing interface name")
	}
}

func TestParseTCPForwarderAddress(t *testing.T) {
	spec, err := parseTCPForwarderAddress("[2001:db8::10]:9000:6@eth2", "")
	if err != nil {
		t.Fatalf("parseTCPForwarderAddress returned error: %v", err)
	}
	if spec.address != "[2001:db8::10]:9000" {
		t.Fatalf("unexpected address: %s", spec.address)
	}
	if spec.connectionCount != 6 {
		t.Fatalf("unexpected connection count: %d", spec.connectionCount)
	}
	if spec.interfaceName != "eth2" {
		t.Fatalf("unexpected interface name: %s", spec.interfaceName)
	}

	spec, err = parseTCPForwarderAddress("198.51.100.20:9100", "eth3")
	if err != nil {
		t.Fatalf("parseTCPForwarderAddress with default interface returned error: %v", err)
	}
	if spec.connectionCount != 4 {
		t.Fatalf("expected default connection count 4, got %d", spec.connectionCount)
	}
	if spec.interfaceName != "eth3" {
		t.Fatalf("expected default interface eth3, got %s", spec.interfaceName)
	}
}

func TestPickInterfaceIPFromAddrsPrefersMatchingFamily(t *testing.T) {
	addrs := []net.Addr{
		&net.IPNet{IP: net.IPv4(192, 0, 2, 10), Mask: net.CIDRMask(24, 32)},
		&net.IPNet{IP: net.ParseIP("2001:db8::10"), Mask: net.CIDRMask(64, 128)},
	}

	ip, err := pickInterfaceIPFromAddrs(addrs, net.IPv4(203, 0, 113, 10))
	if err != nil {
		t.Fatalf("pickInterfaceIPFromAddrs returned error for IPv4: %v", err)
	}
	if got := ip.String(); got != "192.0.2.10" {
		t.Fatalf("expected IPv4 candidate, got %s", got)
	}

	ip, err = pickInterfaceIPFromAddrs(addrs, net.ParseIP("2001:db8::99"))
	if err != nil {
		t.Fatalf("pickInterfaceIPFromAddrs returned error for IPv6: %v", err)
	}
	if got := ip.String(); got != "2001:db8::10" {
		t.Fatalf("expected IPv6 candidate, got %s", got)
	}
}

func TestPickInterfaceIPFromAddrsRejectsLoopbackForNonLoopbackRemote(t *testing.T) {
	addrs := []net.Addr{
		&net.IPNet{IP: net.IPv4(127, 0, 0, 1), Mask: net.CIDRMask(8, 32)},
	}

	if _, err := pickInterfaceIPFromAddrs(addrs, net.IPv4(203, 0, 113, 10)); err == nil {
		t.Fatal("expected error when only loopback is available for non-loopback remote")
	}
}
