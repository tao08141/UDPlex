package main

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	geoip2 "github.com/oschwald/geoip2-golang"
)

// IPRouterComponent routes packets based on source IP/CIDR or GeoIP country code.
type IPRouterComponent struct {
	BaseComponent
	// rules: ordered list, first match wins
	rules         []IPRouteRule
	defaultDetour []string

	geoDB     *geoip2.Reader
	geoDBMu   sync.RWMutex
	stopUpd   chan struct{}
	geoIPPath string // local path of mmdb (downloaded or provided)

	geoURL         string
	updateInterval time.Duration
}

type IPRouteRule struct {
	// Match can be one of:
	// - single IP: "192.168.1.10"
	// - CIDR: "192.168.1.0/24" or with non-octet prefix e.g. /21
	// - geo: "geo:US" country code, or "geo:CN"
	Match   string
	Targets []string
	// precompiled helpers
	net     *net.IPNet
	ip      net.IP
	country string
}

type IPRouteComponentConfig struct {
	Type       string                   `json:"type" yaml:"type"`
	Tag        string                   `json:"tag" yaml:"tag"`
	Rules      []LoadBalancerDetourRule `json:"rules" yaml:"rules"`
	DetourMiss []string                 `json:"detour_miss" yaml:"detour_miss"`
	GeoIPMMDB  string                   `json:"geoip_mmdb" yaml:"geoip_mmdb"`
	GeoIPURL   string                   `json:"geoip_url" yaml:"geoip_url"`
	// update interval in seconds; if zero/empty, no background update
	GeoIPUpdateInterval string `json:"geoip_update_interval" yaml:"geoip_update_interval"`
}

func NewIPRouterComponent(cfg IPRouteComponentConfig, router *Router) (*IPRouterComponent, error) {
	c := &IPRouterComponent{
		BaseComponent: NewBaseComponent(cfg.Tag, router, 0),
		defaultDetour: cfg.DetourMiss,
		stopUpd:       make(chan struct{}),
	}

	// Determine GeoIP source
	geoPath := strings.TrimSpace(cfg.GeoIPMMDB)
	geoURL := strings.TrimSpace(cfg.GeoIPURL)
	if geoURL == "" && (strings.HasPrefix(strings.ToLower(geoPath), "http://") || strings.HasPrefix(strings.ToLower(geoPath), "https://")) {
		geoURL = geoPath
		geoPath = ""
	}
	c.geoURL = geoURL
	c.geoIPPath = geoPath

	// parse update interval (supports duration string like "24h" or seconds number)
	if s := strings.TrimSpace(cfg.GeoIPUpdateInterval); s != "" {
		if d, err := time.ParseDuration(s); err == nil {
			c.updateInterval = d
		} else {
			// try as seconds integer
			var sec int64
			for _, ch := range s {
				if ch < '0' || ch > '9' {
					sec = 0
					goto done
				}
			}
			if len(s) > 0 {
				// safe atoi
				var val int64
				for _, ch := range s {
					val = val*10 + int64(ch-'0')
				}
				sec = val
			}
			if sec > 0 {
				c.updateInterval = time.Duration(sec) * time.Second
			}
		}
	}
done:

	// If local file path is provided, open it now (will be replaced if URL updater later)
	if c.geoIPPath != "" {
		db, err := geoip2.Open(c.geoIPPath)
		if err != nil {
			return nil, fmt.Errorf("open geoip db: %w", err)
		}
		c.geoDB = db
	}

	// compile rules
	for _, r := range cfg.Rules {
		rule := IPRouteRule{Match: r.Rule, Targets: r.Targets}
		m := strings.TrimSpace(strings.ToLower(r.Rule))
		if strings.HasPrefix(m, "geo:") {
			rule.country = strings.ToUpper(strings.TrimSpace(r.Rule[4:]))
		} else if strings.Contains(m, "/") {
			if _, ipnet, err := net.ParseCIDR(r.Rule); err == nil {
				rule.net = ipnet
			} else {
				return nil, fmt.Errorf("invalid cidr in rule %q: %v", r.Rule, err)
			}
		} else {
			ip := net.ParseIP(r.Rule)
			if ip == nil {
				return nil, fmt.Errorf("invalid ip in rule %q", r.Rule)
			}
			rule.ip = ip
		}
		c.rules = append(c.rules, rule)
	}
	return c, nil
}

func (c *IPRouterComponent) Start() error {
	// If configured with URL, perform initial download and start updater if interval > 0
	if c.geoURL != "" {
		if err := c.downloadAndSwap(); err != nil {
			logger.Warnf("%s: initial GeoIP download failed: %v", c.tag, err)
		}
		if c.updateInterval > 0 {
			go c.updater()
		}
	}
	return nil
}

func (c *IPRouterComponent) Stop() error {
	if c.stopUpd != nil {
		close(c.stopUpd)
	}
	c.geoDBMu.Lock()
	if c.geoDB != nil {
		_ = c.geoDB.Close()
		c.geoDB = nil
	}
	c.geoDBMu.Unlock()
	close(c.stopCh)
	return nil
}

func (c *IPRouterComponent) SendPacket(_ *Packet, _ any) error { return nil }

func (c *IPRouterComponent) HandlePacket(packet *Packet) error {
	defer packet.Release(1)
	// determine source IP from packet srcAddr if available
	var srcIP net.IP
	if ua, ok := packet.srcAddr.(*net.UDPAddr); ok && ua != nil {
		srcIP = ua.IP
	} else if ta, ok := packet.srcAddr.(*net.TCPAddr); ok && ta != nil {
		srcIP = ta.IP
	}
	if srcIP == nil {
		// cannot determine ip, go default
		return c.router.Route(packet, c.defaultDetour)
	}

	// evaluate rules
	for _, r := range c.rules {
		if r.ip != nil && r.ip.Equal(srcIP) {
			return c.router.Route(packet, r.Targets)
		}
		if r.net != nil && r.net.Contains(srcIP) {
			return c.router.Route(packet, r.Targets)
		}
		if r.country != "" {
			c.geoDBMu.RLock()
			db := c.geoDB
			c.geoDBMu.RUnlock()
			if db != nil {
				rec, err := db.Country(srcIP)
				if err == nil && rec != nil && rec.Country.IsoCode == r.country {
					return c.router.Route(packet, r.Targets)
				}
			}
		}
	}
	return c.router.Route(packet, c.defaultDetour)
}

// downloadAndSwap downloads GeoIP DB from URL to a cache file and swaps reader atomically
func (c *IPRouterComponent) downloadAndSwap() error {
	if c.geoURL == "" {
		return nil
	}
	cacheDir := os.TempDir()
	fileName := "udplex_geoip.mmdb"
	if c.tag != "" {
		fileName = fmt.Sprintf("udplex_geoip_%s.mmdb", c.tag)
	}
	tmpPath := filepath.Join(cacheDir, fileName+".tmp")
	finalPath := filepath.Join(cacheDir, fileName)

	req, err := http.NewRequest("GET", c.geoURL, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("geoip download status %d", resp.StatusCode)
	}
	f, err := os.Create(tmpPath)
	if err != nil {
		return err
	}
	if _, err = io.Copy(f, resp.Body); err != nil {
		_ = f.Close()
		return err
	}
	_ = f.Close()
	if err = os.Rename(tmpPath, finalPath); err != nil {
		return err
	}

	// open new reader
	newDB, err := geoip2.Open(finalPath)
	if err != nil {
		return err
	}

	c.geoDBMu.Lock()
	old := c.geoDB
	c.geoDB = newDB
	c.geoIPPath = finalPath
	c.geoDBMu.Unlock()
	if old != nil {
		_ = old.Close()
	}
	return nil
}

func (c *IPRouterComponent) updater() {
	t := time.NewTicker(c.updateInterval)
	defer t.Stop()
	for {
		select {
		case <-c.stopUpd:
			return
		case <-t.C:
			if err := c.downloadAndSwap(); err != nil {
				logger.Warnf("%s: GeoIP update failed: %v", c.tag, err)
			}
		}
	}
}
