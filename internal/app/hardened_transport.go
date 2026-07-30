package app

// SSRF-hardening primitives shared by every outbound downloader in this
// package: subscription fetch (subscription.go), and the mihomo
// core/geodata update downloaders (core_update.go, geo_update.go). All three
// talk to URLs that ultimately came from configuration an operator supplied
// (a subscription link) or from a public API response (GitHub's release/
// asset JSON, which embeds asset download URLs) -- none of it is trusted to
// avoid loopback/private/link-local/reserved targets on its own, and a
// redirect (GitHub always redirects release asset downloads to a CDN host)
// must be re-checked exactly like the original URL, not trusted just because
// the first hop passed.
//
// This file used to be three near-identical copies of the same logic
// (subscription.go had the only one; core/geo update would have needed their
// own). Extracting one implementation here means the SSRF posture cannot
// silently drift between call sites -- every downloader in this package
// dials through hardenedDialContext and validates through
// validateHardenedTarget, so strengthening or loosening the check in one
// place changes it everywhere at once.
//
// Deliberately NOT read here: any HTTP_PROXY/HTTPS_PROXY/NO_PROXY
// environment variable. http.Transport.Proxy is left at its zero value
// (nil) throughout this file -- never set to http.ProxyFromEnvironment --
// so a proxy configured in the environment can never become a path around
// the address checks below.

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// hardenedBlockedNetworks lists reserved/special-purpose ranges beyond what
// net.IP's own IsPrivate/IsLoopback/IsLinkLocal*/IsMulticast predicates
// already cover (blockedHardenedIP checks both). Originally
// subscription.go's blockedSubscriptionNetworks; moved here unchanged so it
// backs every hardened downloader instead of just subscription fetch.
var hardenedBlockedNetworks = []*net.IPNet{
	mustParseCIDR("100.64.0.0/10"),   // CGNAT shared address space
	mustParseCIDR("192.0.0.0/24"),    // IETF protocol assignments
	mustParseCIDR("192.0.2.0/24"),    // TEST-NET-1
	mustParseCIDR("198.18.0.0/15"),   // benchmarking
	mustParseCIDR("198.51.100.0/24"), // TEST-NET-2
	mustParseCIDR("203.0.113.0/24"),  // TEST-NET-3
	mustParseCIDR("240.0.0.0/4"),     // reserved
	mustParseCIDR("100::/64"),        // IPv6 discard-only
	mustParseCIDR("2001:db8::/32"),   // IPv6 documentation
}

func mustParseCIDR(raw string) *net.IPNet {
	_, network, err := net.ParseCIDR(raw)
	if err != nil {
		panic(err)
	}
	return network
}

// blockedHardenedIP reports whether ip must never be dialed: unspecified,
// loopback, private, link-local (unicast or multicast), multicast, or one of
// hardenedBlockedNetworks above.
func blockedHardenedIP(ip net.IP) bool {
	if ip == nil {
		return true
	}
	if v4 := ip.To4(); v4 != nil {
		ip = v4
	}
	if ip.IsUnspecified() ||
		ip.IsLoopback() ||
		ip.IsPrivate() ||
		ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() ||
		ip.IsMulticast() {
		return true
	}
	for _, network := range hardenedBlockedNetworks {
		if network.Contains(ip) {
			return true
		}
	}
	return false
}

// safeHardenedIPs resolves host and rejects the whole result if ANY answer
// is blocked -- not just the first -- so a host that resolves to both a
// public and a private address (attacker-controlled DNS returning multiple
// A/AAAA records) cannot be used to reach the private one by racing which
// address the dialer happens to try. Returns the full address list on
// success for hardenedDialContext to dial from directly, so a DNS answer
// that changes between this check and the actual dial (rebinding) cannot
// introduce an address this call never saw.
func safeHardenedIPs(ctx context.Context, host string) ([]net.IP, error) {
	addrs, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, fmt.Errorf("resolve host: %w", err)
	}
	if len(addrs) == 0 {
		return nil, errors.New("host did not resolve")
	}
	ips := make([]net.IP, 0, len(addrs))
	for _, addr := range addrs {
		if blockedHardenedIP(addr.IP) {
			return nil, fmt.Errorf("host resolves to blocked address %s", addr.IP.String())
		}
		ips = append(ips, addr.IP)
	}
	return ips, nil
}

// validateHardenedTarget checks that parsed is an http(s) URL with a host,
// then resolves and blocklist-checks that host. label is folded into the
// scheme/host error messages only ("subscription URL must start with...",
// "download URL must start with...") so each caller's errors keep reading
// naturally; the resolution/blocklist error is generic since it doesn't
// benefit from that distinction ("host resolves to blocked address ...").
func validateHardenedTarget(ctx context.Context, parsed *url.URL, label string) error {
	if parsed == nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return fmt.Errorf("%s URL must start with http:// or https://", label)
	}
	host := parsed.Hostname()
	if host == "" {
		return fmt.Errorf("%s URL host is required", label)
	}
	_, err := safeHardenedIPs(ctx, host)
	return err
}

// hardenedDialContext returns a DialContext that re-resolves the host itself
// (rather than trusting whatever CheckRedirect/validateHardenedTarget saw a
// moment earlier) and only ever dials an address safeHardenedIPs allows.
// This is what actually closes the DNS-rebinding gap: a pre-flight check
// alone only validates the address at check time, but the dialer here is the
// thing that makes the real connection, so re-checking at dial time is what
// makes a rebind between check and dial harmless.
func hardenedDialContext(dialer *net.Dialer) func(context.Context, string, string) (net.Conn, error) {
	if dialer == nil {
		dialer = &net.Dialer{Timeout: 8 * time.Second, KeepAlive: 30 * time.Second}
	}
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, err
		}
		ips, err := safeHardenedIPs(ctx, host)
		if err != nil {
			return nil, err
		}
		var lastErr error
		for _, ip := range ips {
			conn, err := dialer.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
			if err == nil {
				return conn, nil
			}
			lastErr = err
		}
		if lastErr != nil {
			return nil, lastErr
		}
		return nil, errors.New("host did not resolve")
	}
}

// hardenedCheckRedirect returns an http.Client.CheckRedirect that caps the
// hop count at maxRedirects and re-validates every redirect target through
// validate. GitHub redirects release asset downloads to a CDN host (and the
// GitHub API itself can 301/302 on occasion), so the first URL passing
// validateHardenedTarget is not sufficient -- each hop must be re-checked or
// a redirect response could point at a blocked address the original URL
// never named.
func hardenedCheckRedirect(maxRedirects int, validate func(context.Context, *url.URL) error) func(*http.Request, []*http.Request) error {
	return func(req *http.Request, via []*http.Request) error {
		if len(via) >= maxRedirects {
			return fmt.Errorf("redirect limit exceeded (%d)", maxRedirects)
		}
		return validate(req.Context(), req.URL)
	}
}

// newHardenedTransport builds an *http.Transport that only ever dials
// addresses hardenedDialContext (built from dialer) allows, with HTTP/2
// explicitly re-enabled: http.Transport disables automatic HTTP/2
// negotiation whenever a custom DialContext is set unless ForceAttemptHTTP2
// is set back to true, and GitHub/its CDN are h2-capable. Proxy is left
// unset (nil) -- see this file's header comment.
func newHardenedTransport(dialer *net.Dialer) *http.Transport {
	return &http.Transport{
		DialContext:           hardenedDialContext(dialer),
		ForceAttemptHTTP2:     true,
		ResponseHeaderTimeout: 15 * time.Second,
		IdleConnTimeout:       30 * time.Second,
	}
}

// allowedUpdateHosts pins the mihomo core / geodata update feature's
// outbound requests (core_update.go, geo_update.go) to the exact hosts
// GitHub itself uses for its API and release-asset CDN. This is stricter
// than subscription fetches (arbitrary operator-supplied URLs, which must
// keep allowing plain http and any host) -- every URL fetchBytes/
// downloadToFile ever sees is one this package itself either hardcoded
// (mihomoReleaseAPI/geoReleaseAPI) or read back out of a browser_download_url
// GitHub's own API response gave it, so requiring both https and one of
// these hosts is a belt-and-suspenders check on top of
// validateHardenedTarget's resolve+blocklist, not a workflow restriction.
var allowedUpdateHosts = map[string]bool{
	"api.github.com":                       true,
	"github.com":                           true,
	"objects.githubusercontent.com":        true,
	"release-assets.githubusercontent.com": true,
}

// validateUpdateTarget requires https and a pinned host (allowedUpdateHosts)
// in addition to validateHardenedTarget's own resolve+blocklist check. Used
// for every request the core/geodata update feature makes (initial request
// AND every redirect hop) -- unlike validateHardenedTarget/
// validateSubscriptionTarget, which stay http-or-https for operator-supplied
// subscription links.
func validateUpdateTarget(ctx context.Context, parsed *url.URL, label string) error {
	if parsed == nil || parsed.Scheme != "https" {
		return fmt.Errorf("%s URL must use https", label)
	}
	host := parsed.Hostname()
	if host == "" || !allowedUpdateHosts[strings.ToLower(host)] {
		return fmt.Errorf("%s URL host %q is not an allowed update host", label, host)
	}
	return validateHardenedTarget(ctx, parsed, label)
}

// validateUpdateTargetFn is a package-level indirection over
// validateUpdateTarget, mirroring subscription.go's
// validateSubscriptionTargetFn: core_update_test.go/geo_update_test.go
// substitute a permissive check so fetchBytes/downloadToFile/the update
// client's CheckRedirect can be driven against a loopback httptest.Server,
// which the real https+host-pin check above would otherwise always reject.
var validateUpdateTargetFn = validateUpdateTarget
