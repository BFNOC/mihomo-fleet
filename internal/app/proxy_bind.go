package app

import (
	"errors"
	"fmt"
	"net"
	"strconv"
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

// proxyBindListenProbe is the seam tests replace, mirroring util.go's
// isPortFree. It binds and immediately releases, so a successful probe proves
// only that the address existed and the port was free at that instant -- the
// same best-effort guarantee isPortFree gives.
//
// Both transports, not just TCP: a mixed listener serves UDP too
// (config.go's generated listeners set `udp: true`, and mihomo's single-address
// mixed-port path binds both). Probing TCP alone let an instance start against
// an address whose UDP half was already taken, which is exactly the silent
// half-broken listener this check exists to prevent -- the controller is on
// loopback, so the health check would still report the instance running.
// TCP is held open across the UDP probe so a port free on one transport and
// busy on the other cannot slip through the gap between the two binds.
var proxyBindListenProbe = func(address string) error {
	ln, err := net.Listen("tcp", address)
	if err != nil {
		return err
	}
	defer ln.Close()
	packet, err := net.ListenPacket("udp", address)
	if err != nil {
		return err
	}
	return packet.Close()
}

// mixedPortFreeOn reports whether every address in `bind` can take `port`, for
// callers that only need a yes/no and supply their own error (Store's save-time
// port check). checkProxyBindAvailable is the variant that explains itself.
//
// A malformed bind string answers true: the address is validated separately and
// reported with its own message, and failing the port check for it would hide
// that reason behind a port conflict that is not the actual problem.
func mixedPortFreeOn(bind string, port int) bool {
	err := checkProxyBindAvailable(bind, port)
	if err == nil {
		return true
	}
	var unusable proxyBindUnavailableError
	return !errors.As(err, &unusable)
}

// proxyBindUnavailableError marks the failures that mean "this address and port
// cannot be bound right now", as opposed to "this bind string is not valid".
type proxyBindUnavailableError struct{ msg string }

func (e proxyBindUnavailableError) Error() string { return e.msg }

// checkProxyBindAvailable verifies every address in an instance's ProxyBind
// list can actually be bound with its mixed port, before mihomo is launched.
//
// Without this the failure is silent. mihomo's external-controller is always
// on 127.0.0.1, so the post-launch health check passes even when the mixed
// listener never came up -- the instance reports "running" while the proxy it
// exists to serve refuses every connection. The way in is mundane: a fleet
// backup restored onto another machine, or a DHCP lease that moved, leaves a
// stored bind address this host no longer owns.
func checkProxyBindAvailable(bind string, mixedPort int) error {
	addrs, err := parseProxyBindAddresses(bind)
	if err != nil {
		return err
	}
	for _, addr := range addrs {
		if err := proxyBindListenProbe(net.JoinHostPort(addr, strconv.Itoa(mixedPort))); err != nil {
			// Two causes, two different fixes: the address is gone from this
			// host (choose another one) or something else already holds the
			// port (stop it). Told apart by asking the host what it currently
			// has, rather than by matching errno -- EADDRNOTAVAIL lives behind
			// a different syscall package per GOOS, and this package builds for
			// Windows too.
			if !proxyBindAddressOnHost(addr) {
				return proxyBindUnavailableError{msg: fmt.Sprintf("proxy bind address %q is not available on this host", addr)}
			}
			// Same wording as the loopback check this replaced, so the UI's
			// existing translation still matches.
			return proxyBindUnavailableError{msg: fmt.Sprintf("mixed proxy port %d is already in use", mixedPort)}
		}
	}
	return nil
}

// proxyBindAddressOnHost reports whether addr is still one of this machine's
// addresses. Only consulted after a failed probe, so the wildcards -- which
// are never in hostBindAddresses()' interface scan -- answering true here just
// routes a wildcard failure to the "port in use" branch, which is the only
// thing a 0.0.0.0 bind can fail for.
func proxyBindAddressOnHost(addr string) bool {
	key := canonicalProxyBindHost(addr)
	if key == "0.0.0.0" || key == "::" {
		return true
	}
	for _, option := range hostBindAddresses() {
		if canonicalProxyBindHost(option.Address) == key {
			return true
		}
	}
	return false
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
