package main

import (
	"encoding/base64"
	"strings"
	"testing"
)

func TestWireGuardBuildIPCConfig(t *testing.T) {
	component := NewWireGuardComponent(WireGuardComponentConfig{
		Type:          "wg",
		Tag:           "wg_client",
		InterfaceName: "wg-test0",
		PrivateKey:    "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		ListenPort:    51820,
		Peers: []WireGuardPeerConfig{
			{
				PublicKey:           "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789",
				PresharedKey:        "fedcba9876543210fedcba9876543210fedcba9876543210fedcba9876543210",
				Endpoint:            "server.example.com:51820",
				AllowedIPs:          []string{"10.0.0.2/32", "fd00::2/128"},
				PersistentKeepalive: 25,
			},
		},
	}, NewRouter(Config{}))

	config := component.buildIPCConfig()

	requiredLines := []string{
		"private_key=0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		"listen_port=51820",
		"replace_peers=true",
		"public_key=abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789",
		"preshared_key=fedcba9876543210fedcba9876543210fedcba9876543210fedcba9876543210",
		"endpoint=server.example.com:51820",
		"persistent_keepalive_interval=25",
		"replace_allowed_ips=true",
		"allowed_ip=10.0.0.2/32",
		"allowed_ip=fd00::2/128",
	}

	for _, line := range requiredLines {
		if !strings.Contains(config, line+"\n") {
			t.Fatalf("expected config to contain %q, got:\n%s", line, config)
		}
	}
}

func TestWireGuardEndpointFromPacketUsesIncomingSourceTag(t *testing.T) {
	router := NewRouter(Config{})
	component := NewWireGuardComponent(WireGuardComponentConfig{
		Type:          "wg",
		Tag:           "wg_server",
		InterfaceName: "wg-test1",
		PrivateKey:    "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		Detour:        []string{"fallback_forward"},
	}, router)

	packet := router.GetPacket("tcp_server")
	packet.SetConnID(ConnIDFromUint64(42))

	endpoint := component.endpointFromPacket(&packet)
	if endpoint == nil {
		t.Fatal("expected endpoint")
	}
	if endpoint.connID != ConnIDFromUint64(42) {
		t.Fatalf("unexpected connID: %x", endpoint.connID)
	}
	if len(endpoint.routeTags) != 1 || endpoint.routeTags[0] != "tcp_server" {
		t.Fatalf("unexpected route tags: %#v", endpoint.routeTags)
	}
	packet.Release(1)
}

func TestNormalizeWGKeyAcceptsBase64(t *testing.T) {
	raw := [32]byte{}
	for i := range raw {
		raw[i] = byte(i)
	}

	base64Key := base64.StdEncoding.EncodeToString(raw[:])
	expected := "000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f"

	if got := normalizeWGKey(base64Key); got != expected {
		t.Fatalf("unexpected normalized key: got %s want %s", got, expected)
	}
}
