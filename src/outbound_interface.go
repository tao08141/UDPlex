package main

import (
	"fmt"
	"net"
	"strings"
)

type outboundForwarderSpec struct {
	raw           string
	address       string
	interfaceName string
}

type tcpTunnelForwarderSpec struct {
	outboundForwarderSpec
	connectionCount int
}

func formatOutboundRoute(address, interfaceName string) string {
	if strings.TrimSpace(interfaceName) == "" {
		return address
	}
	return address + "@" + interfaceName
}

func parseOutboundForwarderSpec(raw string, defaultInterface string) (outboundForwarderSpec, error) {
	spec := strings.TrimSpace(raw)
	if spec == "" {
		return outboundForwarderSpec{}, fmt.Errorf("forwarder is empty")
	}

	addressPart := spec
	interfaceName := strings.TrimSpace(defaultInterface)

	if idx := strings.LastIndex(spec, "@"); idx >= 0 {
		addressPart = strings.TrimSpace(spec[:idx])
		interfaceName = strings.TrimSpace(spec[idx+1:])
		if addressPart == "" {
			return outboundForwarderSpec{}, fmt.Errorf("missing address before '@' in %q", raw)
		}
		if interfaceName == "" {
			return outboundForwarderSpec{}, fmt.Errorf("missing interface name after '@' in %q", raw)
		}
	}

	return outboundForwarderSpec{
		raw:           spec,
		address:       addressPart,
		interfaceName: interfaceName,
	}, nil
}

func pickInterfaceIPFromAddrs(addrs []net.Addr, remoteIP net.IP) (net.IP, error) {
	type candidate struct {
		ip    net.IP
		score int
	}

	remoteIP = normalizeInterfaceIP(remoteIP)
	remoteIsLoopback := remoteIP != nil && remoteIP.IsLoopback()
	remoteIsLinkLocal := isLinkLocalIP(remoteIP)
	wantFamily := 0
	if remoteIP != nil && !remoteIP.IsUnspecified() {
		if remoteIP.To4() != nil {
			wantFamily = 4
		} else {
			wantFamily = 6
		}
	}

	bestScore := -1
	var bestIP net.IP
	for _, addr := range addrs {
		ip := ipFromNetAddr(addr)
		if ip == nil {
			continue
		}

		ip = normalizeInterfaceIP(ip)
		if ip == nil || ip.IsUnspecified() || ip.IsMulticast() {
			continue
		}

		family := 6
		if ip.To4() != nil {
			family = 4
		}
		if wantFamily != 0 && family != wantFamily {
			continue
		}

		if !remoteIsLoopback && ip.IsLoopback() {
			continue
		}
		if !remoteIsLinkLocal && isLinkLocalIP(ip) {
			continue
		}

		score := 0
		if family == 4 {
			score += 20
		} else {
			score += 10
		}
		if ip.IsGlobalUnicast() {
			score += 100
		}
		if remoteIsLoopback && ip.IsLoopback() {
			score += 80
		}
		if remoteIsLinkLocal && isLinkLocalIP(ip) {
			score += 70
		}

		if score > bestScore {
			bestScore = score
			bestIP = cloneIP(ip)
		}
	}

	if bestIP == nil {
		return nil, fmt.Errorf("no usable local IP matched the remote address family")
	}
	return bestIP, nil
}

func resolveInterfaceLocalIP(interfaceName string, remoteIP net.IP) (net.IP, error) {
	interfaceName = strings.TrimSpace(interfaceName)
	if interfaceName == "" {
		return nil, nil
	}

	iface, err := net.InterfaceByName(interfaceName)
	if err != nil {
		return nil, fmt.Errorf("failed to find interface %q: %w", interfaceName, err)
	}

	addrs, err := iface.Addrs()
	if err != nil {
		return nil, fmt.Errorf("failed to list addresses for interface %q: %w", interfaceName, err)
	}

	ip, err := pickInterfaceIPFromAddrs(addrs, remoteIP)
	if err != nil {
		return nil, fmt.Errorf("failed to choose local IP on interface %q: %w", interfaceName, err)
	}

	return ip, nil
}

func resolveInterfaceLocalUDPAddr(interfaceName string, remoteIP net.IP) (*net.UDPAddr, error) {
	ip, err := resolveInterfaceLocalIP(interfaceName, remoteIP)
	if err != nil || ip == nil {
		return nil, err
	}
	return &net.UDPAddr{IP: ip}, nil
}

func resolveInterfaceLocalTCPAddr(interfaceName string, remoteIP net.IP) (*net.TCPAddr, error) {
	ip, err := resolveInterfaceLocalIP(interfaceName, remoteIP)
	if err != nil || ip == nil {
		return nil, err
	}
	return &net.TCPAddr{IP: ip}, nil
}

func ipFromNetAddr(addr net.Addr) net.IP {
	switch value := addr.(type) {
	case *net.IPNet:
		return value.IP
	case *net.IPAddr:
		return value.IP
	default:
		return nil
	}
}

func normalizeInterfaceIP(ip net.IP) net.IP {
	if ip == nil {
		return nil
	}
	if ip4 := ip.To4(); ip4 != nil {
		return ip4
	}
	return ip.To16()
}

func isLinkLocalIP(ip net.IP) bool {
	ip = normalizeInterfaceIP(ip)
	if ip == nil {
		return false
	}
	return ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast()
}

func cloneIP(ip net.IP) net.IP {
	if ip == nil {
		return nil
	}
	out := make(net.IP, len(ip))
	copy(out, ip)
	return out
}
