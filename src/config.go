package server

import (
	"fmt"
	"io/ioutil"
	"net"
	"strconv"
	"strings"

	"github.com/miekg/dns"
	"gopkg.in/yaml.v2"
)

type listenConfig struct {
	IPv4 string `yaml:"ipv4"`
	IPv6 string `yaml:"ipv6"`
	Port int    `yaml:"port"`
}

type listenEndpoint struct {
	address string
	network string
	family  string
}

func (c listenConfig) endpoints(mode, override string) ([]listenEndpoint, error) {
	if !strings.EqualFold(mode, "both") {
		endpoint, err := c.endpoint(mode, override)
		return []listenEndpoint{endpoint}, err
	}
	if override != "" {
		ip := net.ParseIP(override)
		if ip == nil {
			return nil, fmt.Errorf("invalid listen IP %q", override)
		}
		if ip.To4() != nil {
			c.IPv4 = override
		} else {
			c.IPv6 = override
		}
	}
	ipv4, err := c.endpoint("ipv4", "")
	if err != nil {
		return nil, err
	}
	ipv6, err := c.endpoint("ipv6", "")
	if err != nil {
		return nil, err
	}
	return []listenEndpoint{ipv4, ipv6}, nil
}

func (c listenConfig) endpoint(mode, override string) (listenEndpoint, error) {
	var ip string
	switch strings.ToLower(mode) {
	case "ipv4":
		ip = c.IPv4
	case "ipv6":
		ip = c.IPv6
	default:
		return listenEndpoint{}, fmt.Errorf("invalid mode %q, expected ipv4, ipv6, or both", mode)
	}
	if override != "" {
		ip = override
	}
	parsed := net.ParseIP(ip)
	if parsed == nil {
		return listenEndpoint{}, fmt.Errorf("invalid listen IP %q for %s", ip, mode)
	}
	ipv4 := parsed.To4() != nil
	if override == "" && ipv4 != strings.EqualFold(mode, "ipv4") {
		return listenEndpoint{}, fmt.Errorf("listen IP %q does not match mode %q", ip, mode)
	}
	if c.Port < 1 || c.Port > 65535 {
		return listenEndpoint{}, fmt.Errorf("invalid listen port %d, expected 1–65535", c.Port)
	}
	endpoint := listenEndpoint{
		address: net.JoinHostPort(parsed.String(), strconv.Itoa(c.Port)),
		network: "udp6",
		family:  "ipv6",
	}
	if ipv4 {
		endpoint.network = "udp4"
		endpoint.family = "ipv4"
	}
	return endpoint, nil
}

type mirrorRecord struct {
	DelayOn string `yaml:"delay_on"`
}

type nameserverConfig struct {
	Name string `yaml:"name"`
	IPv4 string `yaml:"ipv4"`
	IPv6 string `yaml:"ipv6"`
}

type soaConfig struct {
	Mailbox string `yaml:"mailbox"`
	Serial  uint32 `yaml:"serial"`
	Refresh uint32 `yaml:"refresh"`
	Retry   uint32 `yaml:"retry"`
	Expire  uint32 `yaml:"expire"`
	Minimum uint32 `yaml:"minimum"`
	TTL     uint32 `yaml:"ttl"`
}

type fileConfig struct {
	ParentDomain  string                      `yaml:"parent_domain"`
	Listen        listenConfig                `yaml:"listen"`
	ControlIPv4   string                      `yaml:"control_ipv4"`
	ControlIPv6   string                      `yaml:"control_ipv6"`
	AnswerTTL     *uint64                     `yaml:"answer_ttl"`
	Records       map[string]mirrorRecord     `yaml:"records"`
	NSRecords     map[string]string           `yaml:"ns_records"`
	ServeSOA      *bool                       `yaml:"serve_soa"`
	SOARecords    map[string]soaConfig        `yaml:"soa_records"`
	Nameservers   map[string]nameserverConfig `yaml:"nameservers"`
	LoadLog       string                      `yaml:"load_log"`
	QueryLog      string                      `yaml:"query_log"`
	QueryLogMaxMB int64                       `yaml:"query_log_max_mb"`
}

func readConfig(path string) (fileConfig, error) {
	config := fileConfig{LoadLog: DefaultLoadLog, QueryLog: DefaultQueryLog, QueryLogMaxMB: DefaultQueryLogMaxMB}
	data, err := ioutil.ReadFile(path)
	if err != nil {
		return config, err
	}
	if err := yaml.UnmarshalStrict(data, &config); err != nil {
		return config, fmt.Errorf("parse %s: %w", path, err)
	}
	return config, nil
}

func dnsName(name string) (string, error) {
	if _, valid := dns.IsDomainName(name); !valid || name == "" {
		return "", fmt.Errorf("invalid DNS name %q", name)
	}
	return dns.Fqdn(name), nil
}

func configuredDNSName(name, parent string) (string, error) {
	const placeholder = "${parent_domain}"
	if strings.Contains(name, placeholder) {
		if parent == "" {
			return "", fmt.Errorf("parent_domain is required for DNS name %q", name)
		}
		name = strings.ReplaceAll(name, placeholder, strings.TrimSuffix(parent, "."))
	}
	if strings.Contains(name, "${") {
		return "", fmt.Errorf("unknown or incomplete DNS placeholder in %q", name)
	}
	return dnsName(name)
}

func parseIP(value string, ipv4 bool) (net.IP, error) {
	ip := net.ParseIP(value)
	if ip == nil || (ip.To4() != nil) != ipv4 {
		family := "IPv6"
		if ipv4 {
			family = "IPv4"
		}
		return nil, fmt.Errorf("invalid %s address %q", family, value)
	}
	return ip, nil
}
