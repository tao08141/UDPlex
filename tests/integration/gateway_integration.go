package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"math/big"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Access gateway integration tests (examples/gateway_entry.yaml and
// examples/gateway_exit.yaml). The official clients connect to the entry:
//
//	cli --(kernel WireGuard / openvpn)--> entry ==(line A, line B)==> exit --> tgt
//
// tgt only knows its own subnet, so reaching it from a client proves that the
// entry NATs the clients into the inner tunnel and the exit NATs the tunnel to
// its default interface. "Gateway Internet" replaces tgt with the real
// internet through the host and only runs when selected explicitly.

const (
	gwCliIP      = "10.201.1.2"
	gwEntryIP    = "10.201.1.1"
	gwEntryLineA = "10.201.2.1"
	gwExitLineA  = "10.201.2.2"
	gwEntryLineB = "10.201.4.1"
	gwExitLineB  = "10.201.4.2"
	gwExitTgtIP  = "10.201.3.1"
	gwTgtIP      = "10.201.3.2"
	gwExitNetIP  = "10.201.9.1"
	gwHostNetIP  = "10.201.9.2"
	gwHostNet    = "10.201.9.0/30"

	gwInnerExitIP = "10.0.0.2"
	gwWGClientIP  = "10.8.0.2"
	gwWGPort      = 51821
	gwOpenVPNPort = 1194
	gwPolicyTable = "7100"

	gwInternetTraceURL    = "https://1.1.1.1/cdn-cgi/trace"
	gwInternetDNSTraceURL = "https://www.cloudflare.com/cdn-cgi/trace"
	gwInternetDownloadURL = "https://speed.cloudflare.com/__down?bytes=25000000"
)

func supportsGatewayIntegration() (bool, string) {
	for _, tool := range []string{"wg", "openvpn", "iptables", "ping"} {
		if _, err := exec.LookPath(tool); err != nil {
			return false, fmt.Sprintf("requires %s", tool)
		}
	}
	return true, ""
}

type gatewayEnv struct {
	cliNS, entryNS, exitNS, tgtNS string
	internet                      bool
	dir                           string
	entryConfig, exitConfig       string
	keys                          map[string]string
	cleanups                      []func()
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

	for _, ns := range []string{env.cliNS, env.entryNS, env.exitNS, env.tgtNS} {
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
	if err := runCommand("ip", "-n", env.cliNS, "route", "add", "default", "via", gwEntryIP); err != nil {
		return nil, err
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
	resolvDir := filepath.Join("/etc/netns", e.cliNS)
	if err := os.MkdirAll(resolvDir, 0o755); err != nil {
		return err
	}
	e.cleanups = append(e.cleanups, func() { _ = os.RemoveAll(resolvDir) })
	return os.WriteFile(filepath.Join(resolvDir, "resolv.conf"), []byte("nameserver 1.1.1.1\n"), 0o644)
}

func (e *gatewayEnv) renderConfigs(examplesDir string) error {
	for _, name := range []string{"entry_inner", "exit_inner", "access", "client"} {
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
	replacer := strings.NewReplacer(
		"ENTRY_INNER_PRIVATE_KEY", e.keys["entry_inner_priv"],
		"ENTRY_INNER_PUBLIC_KEY", e.keys["entry_inner_pub"],
		"EXIT_INNER_PRIVATE_KEY", e.keys["exit_inner_priv"],
		"EXIT_INNER_PUBLIC_KEY", e.keys["exit_inner_pub"],
		"ACCESS_PRIVATE_KEY", e.keys["access_priv"],
		"CLIENT_PUBLIC_KEY", e.keys["client_pub"],
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
	if err := issue("client", 3, x509.ExtKeyUsageClientAuth); err != nil {
		return err
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

// start runs the exit and the entry and waits for the inner tunnel.
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
	deadline := time.Now().Add(15 * time.Second)
	for {
		if exec.Command("ip", "netns", "exec", e.entryNS, "ping", "-c", "1", "-W", "1", gwInnerExitIP).Run() == nil {
			return entry, exit, nil
		}
		if time.Now().After(deadline) {
			stopProcess(entry)
			stopProcess(exit)
			return nil, nil, fmt.Errorf("inner tunnel to %s did not come up", gwInnerExitIP)
		}
		time.Sleep(500 * time.Millisecond)
	}
}

// startWireGuardClient sets up a kernel WireGuard interface in the client
// namespace that sends all traffic through the entry.
func (e *gatewayEnv) startWireGuardClient() (func(), error) {
	keyPath := filepath.Join(e.dir, "client.key")
	if err := os.WriteFile(keyPath, []byte(e.keys["client_priv"]+"\n"), 0o600); err != nil {
		return nil, err
	}
	stop := func() { _ = runCommand("ip", "-n", e.cliNS, "link", "del", "wgc") }
	commands := [][]string{
		{"ip", "-n", e.cliNS, "link", "add", "wgc", "type", "wireguard"},
		{"ip", "netns", "exec", e.cliNS, "wg", "set", "wgc", "private-key", keyPath,
			"peer", e.keys["access_pub"], "endpoint", fmt.Sprintf("%s:%d", gwEntryIP, gwWGPort),
			"allowed-ips", "0.0.0.0/0", "persistent-keepalive", "25"},
		{"ip", "-n", e.cliNS, "addr", "add", gwWGClientIP + "/32", "dev", "wgc"},
		{"ip", "-n", e.cliNS, "link", "set", "wgc", "mtu", "1420", "up"},
		{"ip", "-n", e.cliNS, "route", "add", "0.0.0.0/1", "dev", "wgc"},
		{"ip", "-n", e.cliNS, "route", "add", "128.0.0.0/1", "dev", "wgc"},
	}
	for _, cmd := range commands {
		if err := runCommand(cmd[0], cmd[1:]...); err != nil {
			stop()
			return nil, fmt.Errorf("kernel WireGuard client: %w", err)
		}
	}
	return stop, nil
}

// startOpenVPNClient runs the openvpn client with an inline profile and waits
// for the tunnel; the server pushes redirect-gateway.
func (e *gatewayEnv) startOpenVPNClient() (func(), error) {
	pkiDir := filepath.Join(e.dir, "pki")
	var profile strings.Builder
	fmt.Fprintf(&profile, "client\ndev tun\nproto udp\nremote %s %d\nnobind\nremote-cert-tls server\ntun-mtu 1420\nverb 3\n", gwEntryIP, gwOpenVPNPort)
	for _, block := range []struct{ tag, file string }{
		{"ca", "ca.crt"}, {"cert", "client.crt"}, {"key", "client.key"}, {"tls-crypt", "tc.key"},
	} {
		content, err := os.ReadFile(filepath.Join(pkiDir, block.file))
		if err != nil {
			return nil, err
		}
		fmt.Fprintf(&profile, "<%s>\n%s</%s>\n", block.tag, content, block.tag)
	}
	profilePath := filepath.Join(e.dir, "client.ovpn")
	logPath := filepath.Join(e.dir, "openvpn.log")
	if err := os.WriteFile(profilePath, []byte(profile.String()), 0o600); err != nil {
		return nil, err
	}

	cmd := exec.Command("ip", "netns", "exec", e.cliNS, "openvpn", "--config", profilePath, "--log", logPath)
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
			return nil, fmt.Errorf("openvpn client did not connect:\n%s", gatewayTail(string(log), 20))
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
// integrity run also checks that stopping UDPlex removes its kernel config.
func runGatewayTargetIntegration(projectRoot, examplesDir string, config TestConfig, label string, withSleep bool, startClient func(*gatewayEnv) (func(), error)) TestResult {
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

	entry, exit, err := env.start(projectRoot)
	if err != nil {
		result.Error = err.Error()
		return result
	}
	defer stopProcess(entry)
	defer stopProcess(exit)

	stopClient, err := startClient(env)
	if err != nil {
		result.Error = err.Error()
		return result
	}
	defer stopClient()

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

	if err := stopGatewayGracefully(entry, exit); err != nil {
		result.Success, result.Error = false, err.Error()
		return result
	}
	if leftover := env.leftoverKernelConfig(); leftover != "" {
		result.Success, result.Error = false, "kernel config left after stop: "+leftover
	}
	return result
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

// leftoverKernelConfig reports policy routing and iptables rules that are
// still present after UDPlex stopped.
func (e *gatewayEnv) leftoverKernelConfig() string {
	var leftovers []string
	if out, _ := exec.Command("ip", "-n", e.entryNS, "rule", "show").Output(); strings.Contains(string(out), "lookup "+gwPolicyTable) {
		leftovers = append(leftovers, "entry ip rule")
	}
	if out, _ := exec.Command("ip", "-n", e.entryNS, "route", "show", "table", gwPolicyTable).Output(); strings.TrimSpace(string(out)) != "" {
		leftovers = append(leftovers, "entry routes in table "+gwPolicyTable)
	}
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
		start func(*gatewayEnv) (func(), error)
	}{
		{"wireguard", (*gatewayEnv).startWireGuardClient},
		{"openvpn", (*gatewayEnv).startOpenVPNClient},
	} {
		stopClient, err := client.start(env)
		if err != nil {
			result.Error = err.Error()
			return result
		}
		before := interfaceTxBytes(env.exitNS, "wg_gw")
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
		// The exit's inner tunnel carried the replies, so the traffic did not
		// leave some other way.
		if sent := interfaceTxBytes(env.exitNS, "wg_gw") - before; sent <= 0 {
			result.Error = fmt.Sprintf("%s: no traffic through the inner tunnel", client.name)
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
	args = append([]string{"netns", "exec", netns, "curl", "--max-time", "30"}, args...)
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
