package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Access gateway integration tests (examples/gateway_entry.yaml and
// examples/gateway_exit.yaml). The official clients connect to the entry,
// which relays their packets over two lines to the exit, where the clients
// are terminated:
//
//	cli, cli2 --(kernel WireGuard / openvpn)--> entry ==(line A, line B)==> exit --> tgt
//
// tgt only knows its own subnet, so reaching it from a client proves that the
// exit NATs the clients to its default interface. Below the load balancer
// threshold every packet crosses both lines, and the UDP check counts copies
// per datagram to show that the client protocols drop the duplicates, while
// two clients at once show that the lines keep them apart. "Gateway Internet"
// replaces tgt with the real internet through the host and only runs when
// selected explicitly.

const (
	gwCliIP      = "10.201.1.2"
	gwEntryIP    = "10.201.1.1"
	gwCli2IP     = "10.201.5.2"
	gwEntryIP2   = "10.201.5.1"
	gwEntryLineA = "10.201.2.1"
	gwExitLineA  = "10.201.2.2"
	gwEntryLineB = "10.201.4.1"
	gwExitLineB  = "10.201.4.2"
	gwExitTgtIP  = "10.201.3.1"
	gwTgtIP      = "10.201.3.2"
	gwExitNetIP  = "10.201.9.1"
	gwHostNetIP  = "10.201.9.2"
	gwHostNet    = "10.201.9.0/30"

	gwWGServerIP  = "10.8.0.1"
	gwWGPort      = 51821
	gwOpenVPNPort = 1194
	gwUDPEchoPort = 5401

	gwDupDatagrams = 200
	gwDupInterval  = 5 * time.Millisecond
	gwDupSize      = 200

	gwInternetTraceURL    = "https://1.1.1.1/cdn-cgi/trace"
	gwInternetDNSTraceURL = "https://www.cloudflare.com/cdn-cgi/trace"
	gwInternetDownloadURL = "https://speed.cloudflare.com/__down?bytes=10000000"
)

func supportsGatewayIntegration() (bool, string) {
	for _, tool := range []string{"wg", "openvpn", "iptables", "ping"} {
		if _, err := exec.LookPath(tool); err != nil {
			return false, fmt.Sprintf("requires %s", tool)
		}
	}
	return true, ""
}

// gatewayClient is one client namespace and the entry address it reaches.
type gatewayClient struct {
	ns, entryIP string
	index       int
}

type gatewayEnv struct {
	cliNS, cli2NS, entryNS, exitNS, tgtNS string
	internet                              bool
	dir                                   string
	entryConfig, exitConfig               string
	keys                                  map[string]string
	cleanups                              []func()
}

func (e *gatewayEnv) cleanup() {
	for i := len(e.cleanups) - 1; i >= 0; i-- {
		e.cleanups[i]()
	}
}

// setupGatewayEnv creates the namespaces and renders the example configs. With
// internet set, the exit's default route goes to the host, which NATs it.
func setupGatewayEnv(examplesDir string, internet bool) (*gatewayEnv, error) {
	suffix := time.Now().UnixNano() % 100000
	env := &gatewayEnv{
		cliNS:    fmt.Sprintf("gwcli-%d", suffix),
		cli2NS:   fmt.Sprintf("gwcl2-%d", suffix),
		entryNS:  fmt.Sprintf("gwent-%d", suffix),
		exitNS:   fmt.Sprintf("gwext-%d", suffix),
		tgtNS:    fmt.Sprintf("gwtgt-%d", suffix),
		internet: internet,
		keys:     map[string]string{},
	}
	ok := false
	defer func() {
		if !ok {
			env.cleanup()
		}
	}()

	dir, err := os.MkdirTemp("", "udplex-gateway-integration-*")
	if err != nil {
		return nil, err
	}
	env.dir = dir
	env.cleanups = append(env.cleanups, func() { _ = os.RemoveAll(dir) })

	for _, ns := range []string{env.cliNS, env.cli2NS, env.entryNS, env.exitNS, env.tgtNS} {
		if err := runCommand("ip", "netns", "add", ns); err != nil {
			return nil, err
		}
		env.cleanups = append(env.cleanups, func() { _ = runCommand("ip", "netns", "del", ns) })
		if err := runCommand("ip", "-n", ns, "link", "set", "lo", "up"); err != nil {
			return nil, err
		}
	}

	links := []struct{ ns1, if1, ip1, ns2, if2, ip2 string }{
		{env.cliNS, "c0", gwCliIP, env.entryNS, "e0", gwEntryIP},
		{env.cli2NS, "c0", gwCli2IP, env.entryNS, "e3", gwEntryIP2},
		{env.entryNS, "e1", gwEntryLineA, env.exitNS, "x0", gwExitLineA},
		{env.entryNS, "e2", gwEntryLineB, env.exitNS, "x2", gwExitLineB},
		{env.exitNS, "x1", gwExitTgtIP, env.tgtNS, "t0", gwTgtIP},
	}
	for _, l := range links {
		if err := gatewayLink(l.ns1, l.if1, l.ip1+"/24", l.ns2, l.if2, l.ip2+"/24"); err != nil {
			return nil, err
		}
	}
	// A default route lets openvpn find the gateway for redirect-gateway. The
	// entry has no default route, so nothing reaches tgt outside the tunnel.
	for _, c := range env.clients() {
		if err := runCommand("ip", "-n", c.ns, "route", "add", "default", "via", c.entryIP); err != nil {
			return nil, err
		}
	}
	if internet {
		if err := env.connectExitToHost(suffix); err != nil {
			return nil, err
		}
	} else if err := runCommand("ip", "-n", env.exitNS, "route", "add", "default", "via", gwTgtIP); err != nil {
		return nil, err
	}

	if err := env.renderConfigs(examplesDir); err != nil {
		return nil, err
	}
	ok = true
	return env, nil
}

func (e *gatewayEnv) clients() []gatewayClient {
	return []gatewayClient{{e.cliNS, gwEntryIP, 0}, {e.cli2NS, gwEntryIP2, 1}}
}

func gatewayLink(ns1, if1, addr1, ns2, if2, addr2 string) error {
	commands := [][]string{
		{"ip", "link", "add", if1, "netns", ns1, "type", "veth", "peer", "name", if2, "netns", ns2},
		{"ip", "-n", ns1, "addr", "add", addr1, "dev", if1},
		{"ip", "-n", ns2, "addr", "add", addr2, "dev", if2},
		{"ip", "-n", ns1, "link", "set", if1, "up"},
		{"ip", "-n", ns2, "link", "set", if2, "up"},
	}
	for _, cmd := range commands {
		if err := runCommand(cmd[0], cmd[1:]...); err != nil {
			return err
		}
	}
	return nil
}

// connectExitToHost routes the exit to the internet through the host
// namespace and points the client's resolver at a public DNS server.
func (e *gatewayEnv) connectExitToHost(suffix int64) error {
	hostIf := fmt.Sprintf("gwh%d", suffix)
	if err := runCommand("ip", "link", "add", hostIf, "type", "veth", "peer", "name", "x3", "netns", e.exitNS); err != nil {
		return err
	}
	e.cleanups = append(e.cleanups, func() { _ = runCommand("ip", "link", "del", hostIf) })
	commands := [][]string{
		{"ip", "addr", "add", gwHostNetIP + "/30", "dev", hostIf},
		{"ip", "link", "set", hostIf, "up"},
		{"ip", "-n", e.exitNS, "addr", "add", gwExitNetIP + "/30", "dev", "x3"},
		{"ip", "-n", e.exitNS, "link", "set", "x3", "up"},
		{"ip", "-n", e.exitNS, "route", "add", "default", "via", gwHostNetIP},
	}
	for _, cmd := range commands {
		if err := runCommand(cmd[0], cmd[1:]...); err != nil {
			return err
		}
	}

	// Docker Desktop's network hands TCP/UDP packets to the VM with unfilled
	// checksums marked as verified. The kernel keeps that mark while it
	// forwards, but the userspace WireGuard hop loses it and conntrack on the
	// entry then drops the packets. Real NICs deliver valid checksums;
	// recompute them here so the test also runs under Docker Desktop.
	if err := runCommand("tc", "qdisc", "add", "dev", hostIf, "clsact"); err == nil {
		if err := runCommand("tc", "filter", "add", "dev", hostIf, "egress", "matchall", "action", "csum", "ip4h", "tcp", "udp"); err != nil {
			fmt.Printf("Warning: cannot recompute checksums towards the exit: %v\n", err)
		}
	}

	forward, err := os.ReadFile("/proc/sys/net/ipv4/ip_forward")
	if err != nil {
		return err
	}
	if strings.TrimSpace(string(forward)) != "1" {
		if err := os.WriteFile("/proc/sys/net/ipv4/ip_forward", []byte("1"), 0o644); err != nil {
			return err
		}
		e.cleanups = append(e.cleanups, func() { _ = os.WriteFile("/proc/sys/net/ipv4/ip_forward", forward, 0o644) })
	}
	rules := [][]string{
		{"-t", "nat", "POSTROUTING", "-s", gwHostNet, "-j", "MASQUERADE"},
		{"-t", "filter", "FORWARD", "-i", hostIf, "-j", "ACCEPT"},
		{"-t", "filter", "FORWARD", "-o", hostIf, "-j", "ACCEPT"},
	}
	for _, r := range rules {
		args := append([]string{r[0], r[1], "-I", r[2]}, r[3:]...)
		if err := runCommand("iptables", args...); err != nil {
			return err
		}
		del := append([]string{r[0], r[1], "-D", r[2]}, r[3:]...)
		e.cleanups = append(e.cleanups, func() { _ = runCommand("iptables", del...) })
	}

	// ip netns exec bind mounts /etc/netns/<ns>/resolv.conf over /etc/resolv.conf.
	for _, c := range e.clients() {
		resolvDir := filepath.Join("/etc/netns", c.ns)
		if err := os.MkdirAll(resolvDir, 0o755); err != nil {
			return err
		}
		e.cleanups = append(e.cleanups, func() { _ = os.RemoveAll(resolvDir) })
		if err := os.WriteFile(filepath.Join(resolvDir, "resolv.conf"), []byte("nameserver 1.1.1.1\n"), 0o644); err != nil {
			return err
		}
	}
	return nil
}

func (e *gatewayEnv) renderConfigs(examplesDir string) error {
	for _, name := range []string{"access", "client0", "client1"} {
		priv, pub, err := gatewayKeyPair()
		if err != nil {
			return err
		}
		e.keys[name+"_priv"], e.keys[name+"_pub"] = priv, pub
	}
	pkiDir := filepath.Join(e.dir, "pki")
	if err := writeGatewayPKI(pkiDir); err != nil {
		return err
	}
	// The example has one WireGuard client; the second one is added after it.
	replacer := strings.NewReplacer(
		"ACCESS_PRIVATE_KEY", e.keys["access_priv"],
		"CLIENT_PUBLIC_KEY", e.keys["client0_pub"],
		"allowed_ips: [10.8.0.2/32]", "allowed_ips: [10.8.0.2/32]\n      - public_key: "+e.keys["client1_pub"]+"\n        allowed_ips: [10.8.0.3/32]",
		"EXIT_HOST_1", gwExitLineA,
		"EXIT_HOST_2", gwExitLineB,
		"CHANGE_ME", "gateway-integration",
		" pki/", " "+pkiDir+"/",
	)
	var err error
	if e.entryConfig, err = writeRenderedConfig(e.dir, "entry.yaml", filepath.Join(examplesDir, "gateway_entry.yaml"), replacer); err != nil {
		return err
	}
	e.exitConfig, err = writeRenderedConfig(e.dir, "exit.yaml", filepath.Join(examplesDir, "gateway_exit.yaml"), replacer)
	return err
}

func gatewayKeyPair() (string, string, error) {
	privHex, pubHex, err := generateWGKeyPair()
	if err != nil {
		return "", "", err
	}
	priv, _ := hex.DecodeString(privHex)
	pub, _ := hex.DecodeString(pubHex)
	return base64.StdEncoding.EncodeToString(priv), base64.StdEncoding.EncodeToString(pub), nil
}

// writeGatewayPKI writes a CA, the server and client certificates and a
// tls-crypt key, like the access gateway script.
func writeGatewayPKI(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return err
	}
	caTemplate := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "udplex-gateway-test-ca"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caKey.PublicKey, caKey)
	if err != nil {
		return err
	}
	caCert, err := x509.ParseCertificate(caDER)
	if err != nil {
		return err
	}
	if err := writePEM(filepath.Join(dir, "ca.crt"), "CERTIFICATE", caDER); err != nil {
		return err
	}

	issue := func(name string, serial int64, usage x509.ExtKeyUsage) error {
		key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			return err
		}
		template := &x509.Certificate{
			SerialNumber:          big.NewInt(serial),
			Subject:               pkix.Name{CommonName: name},
			NotBefore:             time.Now().Add(-time.Hour),
			NotAfter:              time.Now().Add(24 * time.Hour),
			BasicConstraintsValid: true,
			KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyAgreement,
			ExtKeyUsage:           []x509.ExtKeyUsage{usage},
		}
		der, err := x509.CreateCertificate(rand.Reader, template, caCert, &key.PublicKey, caKey)
		if err != nil {
			return err
		}
		keyDER, err := x509.MarshalPKCS8PrivateKey(key)
		if err != nil {
			return err
		}
		if err := writePEM(filepath.Join(dir, name+".crt"), "CERTIFICATE", der); err != nil {
			return err
		}
		return writePEM(filepath.Join(dir, name+".key"), "PRIVATE KEY", keyDER)
	}
	if err := issue("server", 2, x509.ExtKeyUsageServerAuth); err != nil {
		return err
	}
	for i := range 2 {
		if err := issue(fmt.Sprintf("client%d", i), int64(3+i), x509.ExtKeyUsageClientAuth); err != nil {
			return err
		}
	}

	secret := make([]byte, 256)
	if _, err := rand.Read(secret); err != nil {
		return err
	}
	var tc strings.Builder
	tc.WriteString("-----BEGIN OpenVPN Static key V1-----\n")
	encoded := hex.EncodeToString(secret)
	for i := 0; i < len(encoded); i += 32 {
		tc.WriteString(encoded[i:i+32] + "\n")
	}
	tc.WriteString("-----END OpenVPN Static key V1-----\n")
	return os.WriteFile(filepath.Join(dir, "tc.key"), []byte(tc.String()), 0o600)
}

func writePEM(path, blockType string, der []byte) error {
	return os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: blockType, Bytes: der}), 0o600)
}

// start runs the exit and the entry; the clients wait for their handshakes.
func (e *gatewayEnv) start(projectRoot string) (entry, exit *exec.Cmd, err error) {
	exit = startUDPlexProcessInNamespace(projectRoot, e.exitConfig, e.exitNS, newNamespaceProfileTarget("exit", e.exitNS))
	if exit == nil {
		return nil, nil, fmt.Errorf("failed to start the exit")
	}
	entry = startUDPlexProcessInNamespace(projectRoot, e.entryConfig, e.entryNS, newNamespaceProfileTarget("entry", e.entryNS))
	if entry == nil {
		stopProcess(exit)
		return nil, nil, fmt.Errorf("failed to start the entry")
	}
	return entry, exit, nil
}

// startWireGuardClient sets up a kernel WireGuard interface in the client
// namespace that sends all traffic through the entry, and waits until the
// exit answers through it.
func (e *gatewayEnv) startWireGuardClient(c gatewayClient) (func(), error) {
	keyPath := filepath.Join(e.dir, fmt.Sprintf("client%d.wgkey", c.index))
	if err := os.WriteFile(keyPath, []byte(e.keys[fmt.Sprintf("client%d_priv", c.index)]+"\n"), 0o600); err != nil {
		return nil, err
	}
	clientIP := fmt.Sprintf("10.8.0.%d", 2+c.index)
	stop := func() { _ = runCommand("ip", "-n", c.ns, "link", "del", "wgc") }
	commands := [][]string{
		{"ip", "-n", c.ns, "link", "add", "wgc", "type", "wireguard"},
		{"ip", "netns", "exec", c.ns, "wg", "set", "wgc", "private-key", keyPath,
			"peer", e.keys["access_pub"], "endpoint", fmt.Sprintf("%s:%d", c.entryIP, gwWGPort),
			"allowed-ips", "0.0.0.0/0", "persistent-keepalive", "25"},
		{"ip", "-n", c.ns, "addr", "add", clientIP + "/32", "dev", "wgc"},
		{"ip", "-n", c.ns, "link", "set", "wgc", "mtu", "1420", "up"},
		{"ip", "-n", c.ns, "route", "add", "0.0.0.0/1", "dev", "wgc"},
		{"ip", "-n", c.ns, "route", "add", "128.0.0.0/1", "dev", "wgc"},
	}
	for _, cmd := range commands {
		if err := runCommand(cmd[0], cmd[1:]...); err != nil {
			stop()
			return nil, fmt.Errorf("kernel WireGuard client: %w", err)
		}
	}
	deadline := time.Now().Add(15 * time.Second)
	for exec.Command("ip", "netns", "exec", c.ns, "ping", "-c", "1", "-W", "1", gwWGServerIP).Run() != nil {
		if time.Now().After(deadline) {
			stop()
			return nil, fmt.Errorf("WireGuard client %d: no handshake through the entry", c.index)
		}
	}
	return stop, nil
}

// startOpenVPNClient runs the openvpn client with an inline profile and waits
// for the tunnel; the server pushes redirect-gateway.
func (e *gatewayEnv) startOpenVPNClient(c gatewayClient) (func(), error) {
	pkiDir := filepath.Join(e.dir, "pki")
	name := fmt.Sprintf("client%d", c.index)
	var profile strings.Builder
	fmt.Fprintf(&profile, "client\ndev tun\nproto udp\nremote %s %d\nnobind\nremote-cert-tls server\ntun-mtu 1420\nverb 3\n", c.entryIP, gwOpenVPNPort)
	for _, block := range []struct{ tag, file string }{
		{"ca", "ca.crt"}, {"cert", name + ".crt"}, {"key", name + ".key"}, {"tls-crypt", "tc.key"},
	} {
		content, err := os.ReadFile(filepath.Join(pkiDir, block.file))
		if err != nil {
			return nil, err
		}
		fmt.Fprintf(&profile, "<%s>\n%s</%s>\n", block.tag, content, block.tag)
	}
	profilePath := filepath.Join(e.dir, name+".ovpn")
	logPath := filepath.Join(e.dir, name+".log")
	if err := os.WriteFile(profilePath, []byte(profile.String()), 0o600); err != nil {
		return nil, err
	}

	cmd := exec.Command("ip", "netns", "exec", c.ns, "openvpn", "--config", profilePath, "--log", logPath)
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	stop := func() { stopProcess(cmd) }
	deadline := time.Now().Add(20 * time.Second)
	for {
		log, _ := os.ReadFile(logPath)
		if strings.Contains(string(log), "Initialization Sequence Completed") {
			return stop, nil
		}
		if time.Now().After(deadline) {
			stop()
			return nil, fmt.Errorf("openvpn client %d did not connect:\n%s", c.index, gatewayTail(string(log), 20))
		}
		time.Sleep(250 * time.Millisecond)
	}
}

func gatewayTail(s string, lines int) string {
	all := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(all) > lines {
		all = all[len(all)-lines:]
	}
	return strings.Join(all, "\n")
}

func runGatewayWireGuardIntegration(projectRoot, examplesDir string, config TestConfig, label string, withSleep bool) TestResult {
	return runGatewayTargetIntegration(projectRoot, examplesDir, config, label, withSleep, (*gatewayEnv).startWireGuardClient)
}

func runGatewayOpenVPNIntegration(projectRoot, examplesDir string, config TestConfig, label string, withSleep bool) TestResult {
	return runGatewayTargetIntegration(projectRoot, examplesDir, config, label, withSleep, (*gatewayEnv).startOpenVPNClient)
}

// runGatewayTargetIntegration streams TCP data from a client to tgt. The
// integrity run also sends numbered UDP datagrams from two clients at once
// and checks that stopping UDPlex removes its kernel config.
func runGatewayTargetIntegration(projectRoot, examplesDir string, config TestConfig, label string, withSleep bool, startClient func(*gatewayEnv, gatewayClient) (func(), error)) TestResult {
	result := TestResult{ConfigName: fmt.Sprintf("%s %s", config.Name, label), TotalDuration: config.Duration}

	env, err := setupGatewayEnv(examplesDir, false)
	if err != nil {
		result.Error = fmt.Sprintf("failed to prepare network namespaces: %v", err)
		return result
	}
	defer env.cleanup()

	targetAddr := fmt.Sprintf("%s:%d", gwTgtIP, tcpTargetPort)
	target := startTCPTargetServerInNamespace(env.tgtNS, targetAddr)
	if target == nil {
		result.Error = "failed to start TCP target server"
		return result
	}
	defer stopProcess(target)
	echoAddr := fmt.Sprintf("%s:%d", gwTgtIP, gwUDPEchoPort)
	echo := startWGEchoServerInNamespace(env.tgtNS, echoAddr)
	if echo == nil {
		result.Error = "failed to start UDP echo server"
		return result
	}
	defer stopProcess(echo)

	entry, exit, err := env.start(projectRoot)
	if err != nil {
		result.Error = err.Error()
		return result
	}
	defer stopProcess(entry)
	defer stopProcess(exit)

	clients := env.clients()
	if !withSleep {
		clients = clients[:1]
	}
	for _, c := range clients {
		stopClient, err := startClient(env, c)
		if err != nil {
			result.Error = err.Error()
			return result
		}
		defer stopClient()
	}

	mode := "perf"
	if withSleep {
		mode = "integrity"
	}
	metrics, err := runTCPClientInNamespace(env.cliNS, targetAddr, mode, config.Duration)
	if err != nil {
		result.Error = fmt.Sprintf("TCP client failed: %v", err)
		return result
	}
	populateResultFromTCPMetrics(&result, metrics, config.Duration)
	if !result.Success || !withSleep {
		return result
	}

	if err := env.checkDuplicates(echoAddr); err != nil {
		result.Success, result.Error = false, err.Error()
		return result
	}
	if err := stopGatewayGracefully(entry, exit); err != nil {
		result.Success, result.Error = false, err.Error()
		return result
	}
	if leftover := env.leftoverKernelConfig(); leftover != "" {
		result.Success, result.Error = false, "kernel config left after stop: "+leftover
	}
	return result
}

// gatewayUDPCount is what the UDP check reports: how many datagrams came back
// once, more than once and not at all.
type gatewayUDPCount struct {
	Sent       int `json:"sent"`
	Once       int `json:"once"`
	Duplicates int `json:"duplicates"`
	Lost       int `json:"lost"`
}

// checkDuplicates sends numbered datagrams from both clients at once to the
// echo server. At this rate the load balancer sends every packet over both
// lines, so each datagram crosses each line in both directions; it must still
// come back exactly once, to the client that sent it.
func (e *gatewayEnv) checkDuplicates(echoAddr string) error {
	// The load balancer measures bandwidth over its 3-second window; let the
	// TCP test before this one drop out of it.
	time.Sleep(4 * time.Second)
	lineA, lineB := interfaceRxBytes(e.exitNS, "x0"), interfaceRxBytes(e.exitNS, "x2")
	clients := e.clients()
	counts := make([]*gatewayUDPCount, len(clients))
	errs := make([]error, len(clients))
	var wg sync.WaitGroup
	for i, c := range clients {
		wg.Add(1)
		go func() {
			defer wg.Done()
			counts[i], errs[i] = runGatewayUDPCountInNamespace(c.ns, echoAddr, c.index)
		}()
	}
	wg.Wait()
	for i, count := range counts {
		if errs[i] != nil {
			return fmt.Errorf("UDP check from client %d: %v", i, errs[i])
		}
		fmt.Printf("client %d UDP check: %+v\n", i, *count)
		if count.Duplicates > 0 || count.Lost > count.Sent/100 {
			return fmt.Errorf("client %d: %d datagrams back once, %d more than once, %d lost of %d",
				i, count.Once, count.Duplicates, count.Lost, count.Sent)
		}
	}
	// Both lines carried every datagram of both clients.
	minBytes := int64(len(clients) * gwDupDatagrams * gwDupSize)
	gotA, gotB := interfaceRxBytes(e.exitNS, "x0")-lineA, interfaceRxBytes(e.exitNS, "x2")-lineB
	if gotA < minBytes || gotB < minBytes {
		return fmt.Errorf("lines carried %d and %d bytes, want at least %d each (redundant sending)", gotA, gotB, minBytes)
	}
	return nil
}

func interfaceRxBytes(netns, ifName string) int64 {
	out, err := exec.Command("ip", "netns", "exec", netns, "cat", "/sys/class/net/"+ifName+"/statistics/rx_bytes").Output()
	if err != nil {
		return 0
	}
	n, _ := strconv.ParseInt(strings.TrimSpace(string(out)), 10, 64)
	return n
}

func runGatewayUDPCountInNamespace(netns, target string, client int) (*gatewayUDPCount, error) {
	executable, err := os.Executable()
	if err != nil {
		return nil, err
	}
	out, err := exec.Command("ip", "netns", "exec", netns, executable, "-gw-udp-count", target, strconv.Itoa(client)).Output()
	if err != nil {
		return nil, fmt.Errorf("%v: %s", err, strings.TrimSpace(string(out)))
	}
	var count gatewayUDPCount
	if err := json.Unmarshal(out, &count); err != nil {
		return nil, fmt.Errorf("parse %q: %w", out, err)
	}
	return &count, nil
}

func handleGatewayHelperCommand() bool {
	if len(os.Args) < 4 || os.Args[1] != "-gw-udp-count" {
		return false
	}
	client, _ := strconv.Atoi(os.Args[3])
	count, err := runGatewayUDPCount(os.Args[2], byte(client))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	_ = json.NewEncoder(os.Stdout).Encode(count)
	return true
}

// runGatewayUDPCount sends numbered datagrams tagged with the client and
// counts the copies of each that come back.
func runGatewayUDPCount(target string, client byte) (*gatewayUDPCount, error) {
	conn, err := net.Dial("udp", target)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	copies := make([]int, gwDupDatagrams)
	var foreign int
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		buf := make([]byte, 2048)
		for ctx.Err() == nil {
			_ = conn.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
			n, err := conn.Read(buf)
			if err != nil {
				continue
			}
			if n != gwDupSize || buf[0] != client {
				foreign++
				continue
			}
			if seq := int(buf[1])<<8 | int(buf[2]); seq < len(copies) {
				copies[seq]++
			}
		}
	}()
	payload := make([]byte, gwDupSize)
	payload[0] = client
	for seq := range gwDupDatagrams {
		payload[1], payload[2] = byte(seq>>8), byte(seq)
		if _, err := conn.Write(payload); err != nil {
			cancel()
			return nil, err
		}
		time.Sleep(gwDupInterval)
	}
	time.Sleep(2 * time.Second)
	cancel()
	<-done
	if foreign > 0 {
		return nil, fmt.Errorf("%d datagrams of another client arrived", foreign)
	}
	count := &gatewayUDPCount{Sent: gwDupDatagrams}
	for _, n := range copies {
		switch {
		case n == 0:
			count.Lost++
		case n == 1:
			count.Once++
		default:
			count.Duplicates++
		}
	}
	return count, nil
}

func stopGatewayGracefully(cmds ...*exec.Cmd) error {
	for _, cmd := range cmds {
		_ = cmd.Process.Signal(syscall.SIGTERM)
	}
	for _, cmd := range cmds {
		if err := waitForProcessWithTimeout(cmd, 10*time.Second); err != nil {
			return fmt.Errorf("UDPlex did not stop on SIGTERM: %v", err)
		}
	}
	return nil
}

// leftoverKernelConfig reports iptables rules that are still present after
// UDPlex stopped. The entry only relays, so it must have none at all.
func (e *gatewayEnv) leftoverKernelConfig() string {
	var leftovers []string
	for _, ns := range []string{e.entryNS, e.exitNS} {
		for _, save := range []string{"iptables-legacy-save", "iptables-nft-save", "iptables-save"} {
			if _, err := exec.LookPath(save); err != nil {
				continue
			}
			out, _ := exec.Command("ip", "netns", "exec", ns, save).Output()
			for _, needle := range []string{"MASQUERADE", "TCPMSS", "-j ACCEPT"} {
				if strings.Contains(string(out), needle) {
					leftovers = append(leftovers, fmt.Sprintf("%s %s in %s", needle, save, ns))
				}
			}
		}
	}
	return strings.Join(leftovers, ", ")
}

// runGatewayInternetIntegration checks that both clients reach the internet
// through the gateway, by IP and by name. The performance run measures a
// download instead.
func runGatewayInternetIntegration(projectRoot, examplesDir string, config TestConfig, label string, withSleep bool) TestResult {
	result := TestResult{ConfigName: fmt.Sprintf("%s %s", config.Name, label), TotalDuration: config.Duration}

	env, err := setupGatewayEnv(examplesDir, true)
	if err != nil {
		result.Error = fmt.Sprintf("failed to prepare network namespaces: %v", err)
		return result
	}
	defer env.cleanup()

	entry, exit, err := env.start(projectRoot)
	if err != nil {
		result.Error = err.Error()
		return result
	}
	defer stopProcess(entry)
	defer stopProcess(exit)

	var mbps []float64
	for _, client := range []struct {
		name  string
		start func(*gatewayEnv, gatewayClient) (func(), error)
	}{
		{"wireguard", (*gatewayEnv).startWireGuardClient},
		{"openvpn", (*gatewayEnv).startOpenVPNClient},
	} {
		stopClient, err := client.start(env, env.clients()[0])
		if err != nil {
			result.Error = err.Error()
			return result
		}
		before := interfaceRxBytes(env.exitNS, "x0") + interfaceRxBytes(env.exitNS, "x2")
		if withSleep {
			for _, url := range []string{gwInternetTraceURL, gwInternetDNSTraceURL} {
				out, err := gatewayCurl(env.cliNS, "-sS", url)
				if err != nil || !strings.Contains(out, "ip=") {
					stopClient()
					result.Error = fmt.Sprintf("%s: %s failed: %v %s", client.name, url, err, out)
					return result
				}
				fmt.Printf("%s via the gateway: %s -> %s\n", client.name, url, gatewayTraceField(out, "ip"))
			}
			result.Sent += 2
			result.Received += 2
		} else {
			out, err := gatewayCurl(env.cliNS, "-sS", "-o", "/dev/null", "-w", "%{speed_download}", gwInternetDownloadURL)
			speed, parseErr := strconv.ParseFloat(strings.TrimSpace(out), 64)
			if err != nil || parseErr != nil || speed <= 0 {
				stopClient()
				result.Error = fmt.Sprintf("%s: download failed: %v %s", client.name, err, out)
				return result
			}
			mbps = append(mbps, speed*8/1e6)
			fmt.Printf("%s via the gateway: download %.2f Mbits/s\n", client.name, speed*8/1e6)
		}
		stopClient()
		// The lines carried the requests, so the traffic did not leave some
		// other way.
		if carried := interfaceRxBytes(env.exitNS, "x0") + interfaceRxBytes(env.exitNS, "x2") - before; carried <= 0 {
			result.Error = fmt.Sprintf("%s: no traffic over the lines", client.name)
			return result
		}
	}
	for _, m := range mbps {
		result.Mbps += m / float64(len(mbps))
	}
	result.Success = true
	return result
}

func gatewayCurl(netns string, args ...string) (string, error) {
	args = append([]string{"netns", "exec", netns, "curl", "--max-time", "60"}, args...)
	out, err := exec.Command("ip", args...).CombinedOutput()
	return string(out), err
}

func gatewayTraceField(trace, field string) string {
	for _, line := range strings.Split(trace, "\n") {
		if value, ok := strings.CutPrefix(line, field+"="); ok {
			return value
		}
	}
	return ""
}
