package main

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
)

func buildTestWireGuardComponent(router *Router) *WireGuardComponent {
	component := NewWireGuardComponent(WireGuardComponentConfig{
		Type:            "wg",
		Tag:             "wg_edge",
		InterfaceName:   "wg-test0",
		Detour:          []string{"forward_a"},
		PrivateKey:      "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		ListenPort:      51820,
		Addresses:       []string{"10.6.0.2/24"},
		Routes:          []string{"10.7.0.0/24"},
		RouteAllowedIPs: boolPtr(true),
		MTU:             1380,
		Peers: []WireGuardPeerConfig{
			{
				PublicKey:           "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789",
				PresharedKey:        "fedcba9876543210fedcba9876543210fedcba9876543210fedcba9876543210",
				Endpoint:            "server.example.com:51820",
				AllowedIPs:          []string{"10.6.0.1/32"},
				PersistentKeepalive: 25,
			},
		},
		SetupInterface:      boolPtr(false),
		ReuseIncomingDetour: boolPtr(false),
		SendTimeout:         750,
	}, router)
	component.actualInterfaceName = "wg-actual0"
	component.bind = NewWireGuardBind(component)
	return component
}

func TestGetComponentInfoIncludesWireGuardFields(t *testing.T) {
	router := NewRouter(Config{
		Services: []map[string]any{
			{
				"type":      "wg",
				"tag":       "wg_edge",
				"detour":    []string{"forward_a"},
				"addresses": []string{"10.6.0.2/24"},
			},
		},
	})
	component := buildTestWireGuardComponent(router)
	if err := router.Register(component); err != nil {
		t.Fatalf("register component: %v", err)
	}

	apiServer := NewAPIServer(APIConfig{}, router)
	info := apiServer.getComponentInfo("wg_edge")

	if got, ok := info["type"].(string); !ok || got != "wg" {
		t.Fatalf("unexpected type: %#v", info["type"])
	}
	if got, ok := info["actual_interface_name"].(string); !ok || got != "wg-actual0" {
		t.Fatalf("unexpected actual_interface_name: %#v", info["actual_interface_name"])
	}
	if got, ok := info["mtu"].(int); !ok || got != 1380 {
		t.Fatalf("unexpected mtu: %#v", info["mtu"])
	}
	if got, ok := info["route_allowed_ips"].(bool); !ok || !got {
		t.Fatalf("unexpected route_allowed_ips: %#v", info["route_allowed_ips"])
	}
	if got, ok := info["setup_interface"].(bool); !ok || got {
		t.Fatalf("unexpected setup_interface: %#v", info["setup_interface"])
	}
	if got, ok := info["reuse_incoming_detour"].(bool); !ok || got {
		t.Fatalf("unexpected reuse_incoming_detour: %#v", info["reuse_incoming_detour"])
	}
	if got, ok := info["peer_count"].(int); !ok || got != 1 {
		t.Fatalf("unexpected peer_count: %#v", info["peer_count"])
	}
	if got, ok := info["send_timeout_ms"].(int); !ok || got != 750 {
		t.Fatalf("unexpected send_timeout_ms: %#v", info["send_timeout_ms"])
	}

	effectiveRoutes, ok := info["effective_routes"].([]string)
	if !ok {
		t.Fatalf("unexpected effective_routes type: %#v", info["effective_routes"])
	}
	if len(effectiveRoutes) != 2 || effectiveRoutes[0] != "10.7.0.0/24" || effectiveRoutes[1] != "10.6.0.1/32" {
		t.Fatalf("unexpected effective_routes: %#v", effectiveRoutes)
	}
}

func TestParseWireGuardIPCState(t *testing.T) {
	state := "" +
		"listen_port=51820\n" +
		"public_key=abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789\n" +
		"endpoint=198.51.100.10:51820\n" +
		"allowed_ip=10.6.0.1/32\n" +
		"persistent_keepalive_interval=25\n" +
		"last_handshake_time_sec=1710000000\n" +
		"last_handshake_time_nsec=123000000\n" +
		"rx_bytes=1234\n" +
		"tx_bytes=5678\n" +
		"protocol_version=1\n"

	peers := parseWireGuardIPCState(state)
	peer, ok := peers["abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789"]
	if !ok {
		t.Fatalf("expected peer to be parsed, got %#v", peers)
	}
	if peer.endpoint != "198.51.100.10:51820" {
		t.Fatalf("unexpected endpoint: %s", peer.endpoint)
	}
	if len(peer.allowedIPs) != 1 || peer.allowedIPs[0] != "10.6.0.1/32" {
		t.Fatalf("unexpected allowedIPs: %#v", peer.allowedIPs)
	}
	if peer.persistentKeepalive != 25 || peer.rxBytes != 1234 || peer.txBytes != 5678 || peer.protocolVersion != 1 {
		t.Fatalf("unexpected runtime peer state: %#v", peer)
	}
}

func TestHandleGetWireGuardInfo(t *testing.T) {
	router := NewRouter(Config{
		Services: []map[string]any{
			{
				"type":   "wg",
				"tag":    "wg_edge",
				"detour": []string{"forward_a"},
			},
		},
	})
	component := buildTestWireGuardComponent(router)
	if err := router.Register(component); err != nil {
		t.Fatalf("register component: %v", err)
	}

	apiServer := NewAPIServer(APIConfig{}, router)
	req := httptest.NewRequest("GET", "/api/wg/wg_edge", nil)
	rec := httptest.NewRecorder()

	apiServer.handleGetWireGuardInfo(rec, req)

	if rec.Code != 200 {
		t.Fatalf("unexpected status code: %d", rec.Code)
	}

	var payload map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	if got, ok := payload["type"].(string); !ok || got != "wg" {
		t.Fatalf("unexpected type: %#v", payload["type"])
	}
	if got, ok := payload["is_running"].(bool); !ok || got {
		t.Fatalf("unexpected is_running: %#v", payload["is_running"])
	}
	if got, ok := payload["actual_interface_name"].(string); !ok || got != "wg-actual0" {
		t.Fatalf("unexpected actual_interface_name: %#v", payload["actual_interface_name"])
	}
	if got, ok := payload["rx_queue_capacity"].(float64); !ok || got <= 0 {
		t.Fatalf("unexpected rx_queue_capacity: %#v", payload["rx_queue_capacity"])
	}

	peers, ok := payload["peers"].([]any)
	if !ok || len(peers) != 1 {
		t.Fatalf("unexpected peers payload: %#v", payload["peers"])
	}

	peerInfo, ok := peers[0].(map[string]any)
	if !ok {
		t.Fatalf("unexpected peer entry: %#v", peers[0])
	}
	if got, ok := peerInfo["runtime_present"].(bool); !ok || got {
		t.Fatalf("unexpected runtime_present: %#v", peerInfo["runtime_present"])
	}
	if got, ok := peerInfo["has_preshared_key"].(bool); !ok || !got {
		t.Fatalf("unexpected has_preshared_key: %#v", peerInfo["has_preshared_key"])
	}
}

func boolPtr(v bool) *bool {
	return &v
}
