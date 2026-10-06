package main

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// stubNetCommands records commands instead of running them. Rule checks
// (iptables -C) fail so rules are always added.
func stubNetCommands(t *testing.T) *[]string {
	t.Helper()
	var commands []string
	oldRun, oldSysctl, oldDefault := runNetCommand, ensureSysctl, lookupDefaultInterface
	runNetCommand = func(name string, args ...string) error {
		line := name + " " + strings.Join(args, " ")
		if strings.Contains(line, " -C ") {
			return errors.New("missing")
		}
		commands = append(commands, line)
		return nil
	}
	ensureSysctl = func(name string) error {
		commands = append(commands, "sysctl "+name)
		return nil
	}
	lookupDefaultInterface = func(ipv6 bool) (string, error) { return "eth0", nil }
	t.Cleanup(func() { runNetCommand, ensureSysctl, lookupDefaultInterface = oldRun, oldSysctl, oldDefault })
	return &commands
}

func requireCommands(t *testing.T, got []string, want ...string) {
	t.Helper()
	for _, line := range want {
		found := false
		for _, command := range got {
			if command == line {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("missing command %q in:\n%s", line, strings.Join(got, "\n"))
		}
	}
}

func TestTunNetSetupAppliesAccessGateway(t *testing.T) {
	commands := stubNetCommands(t)
	setup := newTunNetSetup("wg_access", "wg_access")
	cfg := TunNetConfig{
		IPForward: true,
		MSSClamp:  true,
		PolicyRoutes: []PolicyRouteConfig{
			{From: []string{"10.8.0.1/24"}, Table: 100, Priority: 1000, Dev: "wg_udplex"},
		},
		Masquerade: []MasqueradeConfig{{Source: "10.8.0.0/24", OutInterface: "wg_udplex"}},
	}

	if err := setup.applyAll(cfg, true, false); err != nil {
		t.Fatalf("apply: %v", err)
	}

	requireCommands(t, *commands,
		"sysctl net/ipv4/ip_forward",
		"iptables -t filter -I FORWARD -i wg_access -j ACCEPT",
		"iptables -t filter -I FORWARD -o wg_access -j ACCEPT",
		"iptables -t mangle -I FORWARD -i wg_access -p tcp --tcp-flags SYN,RST SYN -j TCPMSS --clamp-mss-to-pmtu",
		"ip route replace 0.0.0.0/0 dev wg_udplex table 100",
		"ip route replace throw 10.8.0.0/24 table 100",
		"ip rule add from 10.8.0.0/24 lookup 100 priority 1000",
		"iptables -t nat -A POSTROUTING -s 10.8.0.0/24 -o wg_udplex -j MASQUERADE",
	)
	for _, command := range *commands {
		if strings.HasPrefix(command, "ip6tables") || strings.HasPrefix(command, "ip -6") {
			t.Fatalf("unexpected ipv6 command %q", command)
		}
	}

	*commands = nil
	setup.teardown()
	requireCommands(t, *commands,
		"iptables -t nat -D POSTROUTING -s 10.8.0.0/24 -o wg_udplex -j MASQUERADE",
		"ip rule del from 10.8.0.0/24 lookup 100 priority 1000",
		"ip route del throw 10.8.0.0/24 table 100",
		"ip route del 0.0.0.0/0 dev wg_udplex table 100",
		"iptables -t filter -D FORWARD -i wg_access -j ACCEPT",
	)
	if (*commands)[0] != "iptables -t nat -D POSTROUTING -s 10.8.0.0/24 -o wg_udplex -j MASQUERADE" {
		t.Fatalf("teardown must run in reverse order, got first %q", (*commands)[0])
	}
}

func TestTunNetSetupMasqueradeAutoAndAny(t *testing.T) {
	commands := stubNetCommands(t)
	setup := newTunNetSetup("wg_udplex", "wg_udplex")
	cfg := TunNetConfig{Masquerade: []MasqueradeConfig{
		{Source: "10.0.0.0/24", OutInterface: "auto"},
		{Source: "fd00::/64"},
	}}

	if err := setup.applyAll(cfg, true, true); err != nil {
		t.Fatalf("apply: %v", err)
	}
	requireCommands(t, *commands,
		"iptables -t nat -A POSTROUTING -s 10.0.0.0/24 -o eth0 -j MASQUERADE",
		"ip6tables -t nat -A POSTROUTING -s fd00::/64 ! -d fd00::/64 -j MASQUERADE",
	)
}

func TestTunNetSetupSkipsExistingRule(t *testing.T) {
	var commands []string
	oldRun := runNetCommand
	runNetCommand = func(name string, args ...string) error {
		commands = append(commands, name+" "+strings.Join(args, " "))
		return nil // every -C check succeeds
	}
	t.Cleanup(func() { runNetCommand = oldRun })

	setup := newTunNetSetup("wg", "wg0")
	if err := setup.applyAll(TunNetConfig{MSSClamp: true}, true, false); err != nil {
		t.Fatalf("apply: %v", err)
	}
	for _, command := range commands {
		if strings.Contains(command, " -I ") {
			t.Fatalf("rule added although it exists: %q", command)
		}
	}
	if len(setup.undo) != 2 {
		t.Fatalf("existing rules must still be removed on teardown, undo=%v", setup.undo)
	}
}

func TestTunNetSetupRejectsBadPolicyRoute(t *testing.T) {
	stubNetCommands(t)
	setup := newTunNetSetup("wg", "wg0")
	if err := setup.applyAll(TunNetConfig{PolicyRoutes: []PolicyRouteConfig{{From: []string{"10.8.0.0/24"}}}}, true, false); err == nil {
		t.Fatal("expected error for missing table")
	}
	if err := setup.applyAll(TunNetConfig{PolicyRoutes: []PolicyRouteConfig{{Table: 100}}}, true, false); err == nil {
		t.Fatal("expected error for missing from")
	}
}

func TestTunNetConfigUnmarshalsIntoWireGuardConfig(t *testing.T) {
	cfg := WireGuardComponentConfig{}
	raw := `{"type":"wg","bind_mode":"native","ip_forward":true,"policy_routes":[{"from":["10.8.0.0/24"],"table":100,"dev":"wg_udplex"}],"masquerade":[{"source":"10.8.0.0/24","out_interface":"wg_udplex"}]}`
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if cfg.BindMode != "native" || !cfg.IPForward || len(cfg.PolicyRoutes) != 1 || cfg.PolicyRoutes[0].Dev != "wg_udplex" || len(cfg.Masquerade) != 1 {
		t.Fatalf("unexpected config: %+v", cfg)
	}
}
