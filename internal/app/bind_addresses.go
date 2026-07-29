package app

import (
	"net"
	"net/http"
	"sort"
)

// bindAddressCandidate is a raw (address, source interface) pair read off a
// live interface, before dedupe/kind classification/zone handling. Splitting
// collection (collectBindAddressCandidates, which touches the real network
// stack) from assembly (buildBindAddressOptions, pure) lets the latter be
// exercised in tests against synthetic addresses instead of whatever
// interfaces happen to exist on the machine running the test.
type bindAddressCandidate struct {
	ip        net.IP
	ifaceName string
}

// hostBindAddresses enumerates the local machine's up interfaces and returns
// one BindAddressOption per unicast address they carry, plus a synthetic
// "0.0.0.0" wildcard entry. Every returned Address is guaranteed to survive
// normalizeProxyBindAddress (proxy_bind.go) unchanged, since the UI feeds
// these straight into the proxyBind field -- see
// TestHostBindAddressesSurviveNormalizeRoundTrip.
func hostBindAddresses() []BindAddressOption {
	return buildBindAddressOptions(collectBindAddressCandidates())
}

// collectBindAddressCandidates walks net.Interfaces(), keeping only unicast
// addresses from interfaces with FlagUp set and skipping nil/unspecified IPs
// (the 0.0.0.0 wildcard is added later, explicitly and exactly once -- never
// sourced from an interface).
func collectBindAddressCandidates() []bindAddressCandidate {
	var candidates []bindAddressCandidate
	ifaces, _ := net.Interfaces()
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			ipNet, ok := addr.(*net.IPNet)
			if !ok || ipNet.IP == nil || ipNet.IP.IsUnspecified() {
				continue
			}
			candidates = append(candidates, bindAddressCandidate{ip: ipNet.IP, ifaceName: iface.Name})
		}
	}
	return candidates
}

// buildBindAddressOptions turns raw candidates into the deduped, ordered
// response hostBindAddresses returns. Order is deterministic (a browser must
// not see the list reshuffle between polls): loopback, then private, then
// public, then linkLocal, sorted by address string within each kind, with
// the wildcard always last -- it is the widest exposure and the UI lists it
// as 所有网卡.
func buildBindAddressOptions(candidates []bindAddressCandidate) []BindAddressOption {
	options := make([]BindAddressOption, 0, len(candidates)+1)
	seen := make(map[string]bool, len(candidates)+1)
	for _, cand := range candidates {
		ip := cand.ip
		// ipNet.IP (the source of cand.ip) is only the address -- the CIDR
		// mask lives in ipNet.Mask, which the caller already discarded -- so
		// ip.String() alone gives the bare address normalizeProxyBindAddress
		// expects.
		address := ip.String()
		if ip.To4() == nil && ip.IsLinkLocalUnicast() {
			// net.IPNet carries no zone (unlike net.IPAddr), so an IPv6
			// link-local address needs its interface name appended by hand
			// to stay usable -- fe80::/10 addresses are only meaningful
			// scoped to the interface they were read from.
			address = address + "%" + cand.ifaceName
		}
		key := canonicalProxyBindHost(address)
		if seen[key] {
			continue
		}
		seen[key] = true
		options = append(options, BindAddressOption{
			Address:   address,
			Kind:      classifyBindAddressKind(ip),
			Interface: cand.ifaceName,
		})
	}

	sort.SliceStable(options, func(i, j int) bool {
		pi, pj := bindAddressKindOrder(options[i].Kind), bindAddressKindOrder(options[j].Kind)
		if pi != pj {
			return pi < pj
		}
		return options[i].Address < options[j].Address
	})

	// Appended after sorting, unconditionally: unspecified IPs are skipped
	// by collectBindAddressCandidates, so 0.0.0.0 is never produced by the
	// interface scan itself, and appending it here (rather than folding it
	// into the sort) guarantees it lands last regardless of
	// bindAddressKindOrder.
	options = append(options, BindAddressOption{Address: "0.0.0.0", Kind: "wildcard"})
	return options
}

// classifyBindAddressKind buckets ip into the four non-wildcard kinds the
// UI's picker groups addresses by. net.IP.IsPrivate (Go 1.17+) already
// covers both RFC 1918 (10/8, 172.16/12, 192.168/16) and RFC 4193 (fc00::/7),
// so it is reused here instead of hand-rolling the same ranges.
func classifyBindAddressKind(ip net.IP) string {
	switch {
	case ip.IsLoopback():
		return "loopback"
	case ip.IsLinkLocalUnicast():
		return "linkLocal"
	case ip.IsPrivate():
		return "private"
	default:
		return "public"
	}
}

// bindAddressKindOrder gives buildBindAddressOptions' sort its deterministic
// ordering. "wildcard" is never fed through that sort (the wildcard entry is
// appended after sorting), but is listed here for completeness.
func bindAddressKindOrder(kind string) int {
	switch kind {
	case "loopback":
		return 0
	case "private":
		return 1
	case "public":
		return 2
	case "linkLocal":
		return 3
	case "wildcard":
		return 4
	default:
		return 5
	}
}

func (c *Controller) handleBindAddresses(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	writeJSON(w, map[string]any{"addresses": hostBindAddresses()})
}
