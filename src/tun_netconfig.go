package main

import (
	"fmt"
	"net/netip"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync"
)

// runNetCommand runs ip/iptables. Tests replace it to record commands.
var runNetCommand = func(name string, args ...string) error {
	if name == "ip" {
		return runIP(args...)
	}
	if name == "iptables" || name == "ip6tables" {
		name = iptablesBinary(name)
	}
	return runCommand(name, args...)
}

// iptablesBackend is the backend (legacy or nft) whose rules the kernel
// already uses. Packets traverse the chains of both, so an ACCEPT added to one
// cannot override a DROP policy in the other: rules must go where Docker or
// the distribution put theirs.
var iptablesBackend = sync.OnceValue(func() string {
	switch backend := strings.ToLower(strings.TrimSpace(os.Getenv("UDPLEX_IPTABLES_BACKEND"))); backend {
	case "legacy", "nft":
		return backend
	}
	legacy := countIptablesRules("iptables-legacy-save")
	nft := countIptablesRules("iptables-nft-save")
	backend := "nft"
	switch {
	case legacy < 0 && nft < 0:
		return ""
	case legacy > nft:
		backend = "legacy"
	}
	logger.Infof("Using the iptables %s backend (%d legacy rules, %d nft rules)", backend, legacy, nft)
	return backend
})

// countIptablesRules returns the number of rules listed by save, or -1 when
// save is not available.
func countIptablesRules(save string) int {
	if _, err := exec.LookPath(save); err != nil {
		return -1
	}
	output, err := commandOutput(save)
	if err != nil {
		return -1
	}
	count := 0
	for _, line := range strings.Split(output, "\n") {
		if strings.HasPrefix(line, "-A ") {
			count++
		}
	}
	return count
}

// iptablesBinary returns the iptables or ip6tables binary of the detected backend.
func iptablesBinary(name string) string {
	backend := iptablesBackend()
	if backend == "" {
		return name
	}
	binary := name + "-" + backend
	if _, err := exec.LookPath(binary); err != nil {
		return name
	}
	return binary
}

// lookupDefaultInterface returns the interface of the default route of a family.
var lookupDefaultInterface = func(ipv6 bool) (string, error) {
	args := []string{"route", "show", "default"}
	if ipv6 {
		args = append([]string{"-6"}, args...)
	}
	output, err := commandOutput("ip", args...)
	if err != nil {
		return "", err
	}
	fields := strings.Fields(output)
	for i := 0; i+1 < len(fields); i++ {
		if fields[i] == "dev" {
			return fields[i+1], nil
		}
	}
	return "", fmt.Errorf("no default route")
}

// configureTunInterface sets MTU, addresses, link state and routes of a TUN interface.
func configureTunInterface(tag, iface string, mtu int, addresses, routes []string) error {
	if runtime.GOOS != "linux" {
		return fmt.Errorf("%s: interface setup is only implemented on linux, set setup_interface=false to manage %s manually", tag, iface)
	}

	if iface == "" {
		return fmt.Errorf("%s: interface name is empty", tag)
	}

	if mtu > 0 {
		if err := runIP("link", "set", "dev", iface, "mtu", strconv.Itoa(mtu)); err != nil {
			return fmt.Errorf("%s: failed to set MTU on %s: %w", tag, iface, err)
		}
	}

	for _, address := range addresses {
		address = strings.TrimSpace(address)
		if address == "" {
			continue
		}
		if err := runIP("address", "add", address, "dev", iface); err != nil && !isIPAlreadyExists(err) {
			return fmt.Errorf("%s: failed to add address %s to %s: %w", tag, address, iface, err)
		}
	}

	if err := runIP("link", "set", "dev", iface, "up"); err != nil {
		return fmt.Errorf("%s: failed to bring interface %s up: %w", tag, iface, err)
	}

	for _, route := range routes {
		args := []string{"route", "replace", route, "dev", iface}
		if strings.Contains(route, ":") {
			args = []string{"-6", "route", "replace", route, "dev", iface}
		}
		if err := runIP(args...); err != nil {
			return fmt.Errorf("%s: failed to add route %s via %s: %w", tag, route, iface, err)
		}
	}

	return nil
}

// tunNetSetup applies a TunNetConfig and remembers how to undo it.
type tunNetSetup struct {
	tag   string
	iface string
	undo  [][]string
}

func (cfg TunNetConfig) empty() bool {
	return !cfg.IPForward && !cfg.MSSClamp && len(cfg.PolicyRoutes) == 0 && len(cfg.Masquerade) == 0
}

// families reports which IP families the config and the interface addresses use.
func (cfg TunNetConfig) families(addresses []string) (v4, v6 bool) {
	mark := func(raw string) {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			return
		}
		if strings.Contains(raw, ":") {
			v6 = true
		} else {
			v4 = true
		}
	}
	for _, address := range addresses {
		mark(address)
	}
	for _, route := range cfg.PolicyRoutes {
		for _, from := range route.From {
			mark(from)
		}
	}
	for _, masquerade := range cfg.Masquerade {
		mark(masquerade.Source)
	}
	if !v4 && !v6 {
		v4 = true
	}
	return v4, v6
}

func newTunNetSetup(tag, iface string) *tunNetSetup {
	return &tunNetSetup{tag: tag, iface: iface}
}

// apply configures the kernel. On failure everything applied so far is undone.
func (s *tunNetSetup) apply(cfg TunNetConfig, addresses []string) error {
	if cfg.empty() {
		return nil
	}
	if runtime.GOOS != "linux" {
		return fmt.Errorf("%s: ip_forward, mss_clamp, policy_routes and masquerade are only implemented on linux", s.tag)
	}

	v4, v6 := cfg.families(addresses)
	err := s.applyAll(cfg, v4, v6)
	if err != nil {
		s.teardown()
		return fmt.Errorf("%s: %w", s.tag, err)
	}
	return nil
}

func (s *tunNetSetup) applyAll(cfg TunNetConfig, v4, v6 bool) error {
	if cfg.IPForward {
		if v4 {
			if err := ensureSysctl("net/ipv4/ip_forward"); err != nil {
				return err
			}
		}
		if v6 {
			if err := ensureSysctl("net/ipv6/conf/all/forwarding"); err != nil {
				return err
			}
		}
		for _, bin := range iptablesBinaries(v4, v6) {
			if err := s.insertRule(bin, "filter", "FORWARD", "-i", s.iface, "-j", "ACCEPT"); err != nil {
				return err
			}
			if err := s.insertRule(bin, "filter", "FORWARD", "-o", s.iface, "-j", "ACCEPT"); err != nil {
				return err
			}
		}
	}

	if cfg.MSSClamp {
		for _, bin := range iptablesBinaries(v4, v6) {
			for _, direction := range []string{"-i", "-o"} {
				if err := s.insertRule(bin, "mangle", "FORWARD", direction, s.iface,
					"-p", "tcp", "--tcp-flags", "SYN,RST", "SYN", "-j", "TCPMSS", "--clamp-mss-to-pmtu"); err != nil {
					return err
				}
			}
		}
	}

	for _, route := range cfg.PolicyRoutes {
		if err := s.applyPolicyRoute(route); err != nil {
			return err
		}
	}

	for _, masquerade := range cfg.Masquerade {
		if err := s.applyMasquerade(masquerade); err != nil {
			return err
		}
	}

	return nil
}

func (s *tunNetSetup) applyPolicyRoute(route PolicyRouteConfig) error {
	if route.Table <= 0 {
		return fmt.Errorf("policy route table must be a positive id")
	}
	if len(route.From) == 0 {
		return fmt.Errorf("policy route for table %d has no from prefixes", route.Table)
	}
	dev := strings.TrimSpace(route.Dev)
	if dev == "" {
		dev = s.iface
	}
	table := strconv.Itoa(route.Table)

	froms := make([]netip.Prefix, 0, len(route.From))
	for _, raw := range route.From {
		prefix, err := netip.ParsePrefix(strings.TrimSpace(raw))
		if err != nil {
			return fmt.Errorf("invalid policy route source %q: %w", raw, err)
		}
		froms = append(froms, prefix.Masked())
	}

	destinations := route.Routes
	if len(destinations) == 0 {
		seen := map[bool]bool{}
		for _, from := range froms {
			if seen[from.Addr().Is6()] {
				continue
			}
			seen[from.Addr().Is6()] = true
			if from.Addr().Is6() {
				destinations = append(destinations, "::/0")
			} else {
				destinations = append(destinations, "0.0.0.0/0")
			}
		}
	}

	for _, destination := range destinations {
		destination = strings.TrimSpace(destination)
		family := ipFamilyArgs(destination)
		add := append(family, "route", "replace", destination, "dev", dev, "table", table)
		if err := runNetCommand("ip", add...); err != nil {
			return fmt.Errorf("failed to add route %s dev %s table %s: %w", destination, dev, table, err)
		}
		s.undo = append(s.undo, append([]string{"ip"}, append(family, "route", "del", destination, "dev", dev, "table", table)...))
	}

	for _, from := range froms {
		family := ipFamilyArgs(from.String())
		// Traffic between sources, e.g. two clients of one pool, falls back to
		// the next rule instead of leaving through dev.
		throw := append(family, "route", "replace", "throw", from.String(), "table", table)
		if err := runNetCommand("ip", throw...); err != nil {
			return fmt.Errorf("failed to add throw route %s table %s: %w", from, table, err)
		}
		s.undo = append(s.undo, append([]string{"ip"}, append(family, "route", "del", "throw", from.String(), "table", table)...))

		rule := append(family, "rule", "add", "from", from.String(), "lookup", table)
		if route.Priority > 0 {
			rule = append(rule, "priority", strconv.Itoa(route.Priority))
		}
		del := append([]string{"ip"}, rule...)
		del[len(family)+2] = "del"
		// Remove a rule left behind by an unclean exit so it is not duplicated.
		_ = runNetCommand(del[0], del[1:]...)
		if err := runNetCommand("ip", rule...); err != nil {
			return fmt.Errorf("failed to add rule from %s lookup %s: %w", from, table, err)
		}
		s.undo = append(s.undo, del)
	}

	return nil
}

func (s *tunNetSetup) applyMasquerade(masquerade MasqueradeConfig) error {
	prefix, err := netip.ParsePrefix(strings.TrimSpace(masquerade.Source))
	if err != nil {
		return fmt.Errorf("invalid masquerade source %q: %w", masquerade.Source, err)
	}
	prefix = prefix.Masked()
	ipv6 := prefix.Addr().Is6()

	out := strings.TrimSpace(masquerade.OutInterface)
	if out == "auto" {
		out, err = lookupDefaultInterface(ipv6)
		if err != nil {
			return fmt.Errorf("failed to find the default route interface for masquerade of %s: %w", prefix, err)
		}
	}

	rule := []string{"-s", prefix.String()}
	if out != "" {
		rule = append(rule, "-o", out)
	} else {
		rule = append(rule, "!", "-d", prefix.String())
	}
	rule = append(rule, "-j", "MASQUERADE")

	bin := "iptables"
	if ipv6 {
		bin = "ip6tables"
	}
	return s.appendRule(bin, "nat", "POSTROUTING", rule...)
}

// insertRule puts a rule first in chain unless it is already there.
func (s *tunNetSetup) insertRule(bin, table, chain string, rule ...string) error {
	return s.addRule(bin, table, chain, "-I", rule)
}

// appendRule puts a rule last in chain unless it is already there.
func (s *tunNetSetup) appendRule(bin, table, chain string, rule ...string) error {
	return s.addRule(bin, table, chain, "-A", rule)
}

func (s *tunNetSetup) addRule(bin, table, chain, op string, rule []string) error {
	check := append([]string{"-t", table, "-C", chain}, rule...)
	if runNetCommand(bin, check...) != nil {
		add := append([]string{"-t", table, op, chain}, rule...)
		if err := runNetCommand(bin, add...); err != nil {
			return fmt.Errorf("%s %s: %w", bin, strings.Join(add, " "), err)
		}
	}
	s.undo = append(s.undo, append([]string{bin, "-t", table, "-D", chain}, rule...))
	return nil
}

// teardown undoes the applied configuration in reverse order.
func (s *tunNetSetup) teardown() {
	for i := len(s.undo) - 1; i >= 0; i-- {
		command := s.undo[i]
		if err := runNetCommand(command[0], command[1:]...); err != nil {
			logger.Debugf("%s: failed to undo %s: %v", s.tag, strings.Join(command, " "), err)
		}
	}
	s.undo = nil
}

func iptablesBinaries(v4, v6 bool) []string {
	var binaries []string
	if v4 {
		binaries = append(binaries, "iptables")
	}
	if v6 {
		binaries = append(binaries, "ip6tables")
	}
	return binaries
}

func ipFamilyArgs(prefix string) []string {
	if strings.Contains(prefix, ":") {
		return []string{"-6"}
	}
	return []string{}
}

// ensureSysctl sets a boolean sysctl under /proc/sys to 1.
var ensureSysctl = func(name string) error {
	path := "/proc/sys/" + name
	current, err := os.ReadFile(path)
	if err == nil && strings.TrimSpace(string(current)) == "1" {
		return nil
	}
	if err := os.WriteFile(path, []byte("1"), 0o644); err != nil {
		key := strings.ReplaceAll(name, "/", ".")
		return fmt.Errorf("failed to enable %s (run `sysctl -w %s=1` on the host when /proc/sys is read-only): %w", key, key, err)
	}
	return nil
}
