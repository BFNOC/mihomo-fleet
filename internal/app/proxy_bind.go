package app

import (
	"fmt"
	"net"
	"strings"
)

const defaultProxyBind = "127.0.0.1"

func instanceProxyBind(value string) string {
	if strings.TrimSpace(value) == "" {
		return defaultProxyBind
	}
	return value
}

func normalizeProxyBind(raw string) (string, error) {
	addrs, err := parseProxyBindAddresses(raw)
	if err != nil {
		return "", err
	}
	return strings.Join(addrs, ","), nil
}

func parseProxyBindAddresses(raw string) ([]string, error) {
	if strings.TrimSpace(raw) == "" {
		raw = defaultProxyBind
	}
	var addrs []string
	seen := make(map[string]struct{})
	for _, part := range strings.Split(raw, ",") {
		addr, err := normalizeProxyBindAddress(part)
		if err != nil {
			return nil, err
		}
		if addr == "" {
			continue
		}
		key := canonicalProxyBindHost(addr)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		addrs = append(addrs, addr)
	}
	if len(addrs) == 0 {
		return []string{defaultProxyBind}, nil
	}
	return coalesceProxyBindAddresses(addrs), nil
}

func normalizeProxyBindAddress(raw string) (string, error) {
	addr := strings.TrimSpace(raw)
	if addr == "" {
		return "", nil
	}
	lower := strings.ToLower(addr)
	if lower == "all" || addr == "*" {
		return "0.0.0.0", nil
	}
	if lower == "localhost" {
		return defaultProxyBind, nil
	}
	if strings.HasPrefix(addr, "[") {
		if parsed, _, err := net.SplitHostPort(addr); err == nil && parsed != "" {
			return "", fmt.Errorf("proxy bind address %q must not include a port; use the mixed port field instead", raw)
		}
		if !strings.HasSuffix(addr, "]") {
			return "", fmt.Errorf("proxy bind address %q has invalid IPv6 brackets", raw)
		}
		addr = strings.TrimPrefix(strings.TrimSuffix(addr, "]"), "[")
	}
	if strings.ContainsAny(addr, "/ \t\r\n") {
		return "", fmt.Errorf("proxy bind address %q is invalid", raw)
	}
	if parsed, _, err := net.SplitHostPort(addr); err == nil && parsed != "" {
		return "", fmt.Errorf("proxy bind address %q must not include a port; use the mixed port field instead", raw)
	}
	if strings.Count(addr, ":") == 1 && net.ParseIP(addr) == nil {
		if host, port, ok := strings.Cut(addr, ":"); ok && host != "" && port != "" {
			return "", fmt.Errorf("proxy bind address %q must not include a port; use the mixed port field instead", raw)
		}
	}
	if addr != "localhost" && net.ParseIP(stripIPv6Zone(addr)) == nil {
		return "", fmt.Errorf("proxy bind address %q must be an IP address, localhost, all, or *", raw)
	}
	if ip := net.ParseIP(stripIPv6Zone(addr)); ip != nil {
		if zone := ipv6Zone(addr); zone != "" {
			return ip.String() + "%" + zone, nil
		}
		return ip.String(), nil
	}
	return addr, nil
}

func coalesceProxyBindAddresses(addrs []string) []string {
	hasIPv4Wildcard := false
	hasIPv6Wildcard := false
	for _, addr := range addrs {
		switch canonicalProxyBindHost(addr) {
		case "0.0.0.0":
			hasIPv4Wildcard = true
		case "::":
			hasIPv6Wildcard = true
		}
	}
	if !hasIPv4Wildcard && !hasIPv6Wildcard {
		return addrs
	}
	coalesced := make([]string, 0, len(addrs))
	for _, addr := range addrs {
		ip := net.ParseIP(stripIPv6Zone(addr))
		if hasIPv4Wildcard && ip != nil && ip.To4() != nil && canonicalProxyBindHost(addr) != "0.0.0.0" {
			continue
		}
		if hasIPv6Wildcard && ip != nil && ip.To4() == nil && canonicalProxyBindHost(addr) != "::" {
			continue
		}
		coalesced = append(coalesced, addr)
	}
	return coalesced
}

func canonicalProxyBindHost(host string) string {
	host = strings.TrimSpace(strings.ToLower(host))
	host = strings.TrimSuffix(host, ".")
	if host == "" {
		return ""
	}
	if host == "localhost" {
		return defaultProxyBind
	}
	if ip := net.ParseIP(stripIPv6Zone(host)); ip != nil {
		if zone := ipv6Zone(host); zone != "" {
			return ip.String() + "%" + zone
		}
		return ip.String()
	}
	return host
}

func stripIPv6Zone(host string) string {
	if i := strings.LastIndex(host, "%"); i >= 0 {
		return host[:i]
	}
	return host
}

func ipv6Zone(host string) string {
	if i := strings.LastIndex(host, "%"); i >= 0 {
		return host[i+1:]
	}
	return ""
}

func mihomoBindAddress(addr string) string {
	if canonicalProxyBindHost(addr) == "0.0.0.0" {
		return "*"
	}
	if ip := net.ParseIP(stripIPv6Zone(addr)); ip != nil && ip.To4() == nil {
		return "[" + addr + "]"
	}
	return addr
}
