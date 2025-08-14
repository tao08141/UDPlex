package filter_test

import (
	"net"
	"testing"
)

// Mock filter structures for testing
type FilterRule struct {
	Pattern string
	Action  string
	Target  string
}

type Filter struct {
	Rules []FilterRule
}

// Mock packet structure
type MockPacket struct {
	Data    []byte
	SrcAddr net.Addr
	Proto   string
}

func (f *Filter) Match(packet *MockPacket) (string, bool) {
	for _, rule := range f.Rules {
		if f.matchRule(rule, packet) {
			return rule.Target, true
		}
	}
	return "", false
}

func (f *Filter) matchRule(rule FilterRule, packet *MockPacket) bool {
	switch rule.Pattern {
	case "http":
		return string(packet.Data[:4]) == "HTTP" || string(packet.Data[:3]) == "GET" || string(packet.Data[:4]) == "POST"
	case "dns":
		return len(packet.Data) > 12 && packet.Data[2]&0x80 == 0 // DNS query flag
	case "any":
		return true
	default:
		return false
	}
}

func TestFilterMatch(t *testing.T) {
	filter := &Filter{
		Rules: []FilterRule{
			{Pattern: "http", Action: "forward", Target: "web_server"},
			{Pattern: "dns", Action: "forward", Target: "dns_server"},
			{Pattern: "any", Action: "forward", Target: "default"},
		},
	}

	tests := []struct {
		name           string
		data           []byte
		expectedTarget string
		shouldMatch    bool
	}{
		{
			name:           "HTTP GET request",
			data:           []byte("GET / HTTP/1.1\r\nHost: example.com\r\n"),
			expectedTarget: "web_server",
			shouldMatch:    true,
		},
		{
			name:           "HTTP POST request",
			data:           []byte("POST /api HTTP/1.1\r\nContent-Length: 10\r\n"),
			expectedTarget: "web_server",
			shouldMatch:    true,
		},
		{
			name:           "HTTP response",
			data:           []byte("HTTP/1.1 200 OK\r\nContent-Type: text/html\r\n"),
			expectedTarget: "web_server",
			shouldMatch:    true,
		},
		{
			name:           "DNS query",
			data:           []byte{0x12, 0x34, 0x01, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x03},
			expectedTarget: "dns_server",
			shouldMatch:    true,
		},
		{
			name:           "Unknown protocol",
			data:           []byte("UNKNOWN_PROTOCOL_DATA"),
			expectedTarget: "default",
			shouldMatch:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			addr, _ := net.ResolveUDPAddr("udp", "127.0.0.1:12345")
			packet := &MockPacket{
				Data:    tt.data,
				SrcAddr: addr,
				Proto:   "udp",
			}

			target, matched := filter.Match(packet)
			
			if matched != tt.shouldMatch {
				t.Errorf("Match() matched = %v, want %v", matched, tt.shouldMatch)
			}
			
			if matched && target != tt.expectedTarget {
				t.Errorf("Match() target = %q, want %q", target, tt.expectedTarget)
			}
		})
	}
}

func TestFilterNoRules(t *testing.T) {
	filter := &Filter{
		Rules: []FilterRule{},
	}

	addr, _ := net.ResolveUDPAddr("udp", "127.0.0.1:12345")
	packet := &MockPacket{
		Data:    []byte("test data"),
		SrcAddr: addr,
		Proto:   "udp",
	}

	target, matched := filter.Match(packet)
	if matched {
		t.Error("Filter with no rules should not match")
	}
	
	if target != "" {
		t.Errorf("Filter with no rules should return empty target, got %q", target)
	}
}

func TestFilterRuleOrder(t *testing.T) {
	// First rule should take precedence
	filter := &Filter{
		Rules: []FilterRule{
			{Pattern: "http", Action: "forward", Target: "http_server"},
			{Pattern: "any", Action: "forward", Target: "default_server"},
		},
	}

	addr, _ := net.ResolveUDPAddr("udp", "127.0.0.1:12345")
	packet := &MockPacket{
		Data:    []byte("GET / HTTP/1.1"),
		SrcAddr: addr,
		Proto:   "udp",
	}

	target, matched := filter.Match(packet)
	if !matched {
		t.Error("Should match HTTP rule")
	}
	
	if target != "http_server" {
		t.Errorf("Should match first rule, got target %q", target)
	}
}

func TestFilterEdgeCases(t *testing.T) {
	filter := &Filter{
		Rules: []FilterRule{
			{Pattern: "http", Action: "forward", Target: "web_server"},
		},
	}

	tests := []struct {
		name    string
		data    []byte
		matched bool
	}{
		{
			name:    "Empty data",
			data:    []byte{},
			matched: false,
		},
		{
			name:    "Short data",
			data:    []byte("HT"),
			matched: false,
		},
		{
			name:    "Just enough data",
			data:    []byte("HTTP"),
			matched: true,
		},
		{
			name:    "Case sensitive match",
			data:    []byte("http"),
			matched: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			addr, _ := net.ResolveUDPAddr("udp", "127.0.0.1:12345")
			packet := &MockPacket{
				Data:    tt.data,
				SrcAddr: addr,
				Proto:   "udp",
			}

			_, matched := filter.Match(packet)
			if matched != tt.matched {
				t.Errorf("Match() = %v, want %v for data %q", matched, tt.matched, string(tt.data))
			}
		})
	}
}

func BenchmarkFilterMatch(b *testing.B) {
	filter := &Filter{
		Rules: []FilterRule{
			{Pattern: "http", Action: "forward", Target: "web_server"},
			{Pattern: "dns", Action: "forward", Target: "dns_server"},
			{Pattern: "any", Action: "forward", Target: "default"},
		},
	}

	addr, _ := net.ResolveUDPAddr("udp", "127.0.0.1:12345")
	packet := &MockPacket{
		Data:    []byte("GET / HTTP/1.1\r\nHost: example.com\r\n"),
		SrcAddr: addr,
		Proto:   "udp",
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		filter.Match(packet)
	}
}