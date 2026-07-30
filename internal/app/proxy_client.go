package app

// Feature P2 (docs/geo-update-enhancements.md, section 3 "通过托管实例代理下载"):
// lets an operator route a core/geodata download through one of their own
// running mihomo instances instead of dialing GitHub/its CDN directly --
// useful when the machine's own direct path is slow or blocked, but the
// managed instance's outbound line is not. Only mihomo-fleet's own managed
// instances are ever eligible (never an arbitrary address the caller
// supplies), which is what keeps this from reopening the SSRF side channel
// hardened_transport.go's whole design exists to close.

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// proxyDialTimeout/proxyClientTimeout size proxyClientForInstance's
// *http.Client: 30s to establish the TCP connection to the instance's own
// mixed-port (a loopback/LAN hop, so generous headroom costs nothing), 10min
// overall to cover the largest geodata file (GeoIP.dat, ~20MB) over a slow
// proxied path. streamGeoUpdate's context caps at 5min, so the client
// timeout is a secondary backstop rather than the primary bound.
const (
	proxyDialTimeout   = 30 * time.Second
	proxyClientTimeout = 10 * time.Minute
)

// proxyDialAddress picks a dialable host out of an instance's ProxyBind
// field. ProxyBind can be a comma-separated multi-address list and/or a
// wildcard ("0.0.0.0"/"::", or the pre-normalization "all"/"*" spelling,
// see proxy_bind.go) -- none of which is itself a valid address to dial.
// The controller and every instance it manages always run on the same host
// (docs/geo-update-enhancements.md's "仅限本机实例" constraint), so a
// wildcard bind is resolved to loopback here exactly like handleMihomoProxy
// already does unconditionally for the controller port (mihomoProxyFor
// hardcodes "127.0.0.1" there for the identical reason): binding "all
// interfaces" already includes loopback, it does not exclude it.
func proxyDialAddress(proxyBind string) string {
	first := proxyBind
	if idx := strings.IndexByte(proxyBind, ','); idx >= 0 {
		first = proxyBind[:idx]
	}
	first = strings.TrimSpace(first)
	switch first {
	case "", "0.0.0.0", "::", "*", "all":
		return defaultProxyBind
	default:
		return first
	}
}

// proxyClientForInstance builds an *http.Client whose Transport routes every
// request through instance id's own mixed-port instead of dialing the
// target directly. The instance must exist and currently be running -- a
// stopped instance has nothing listening on its mixed-port.
//
// Deliberately does NOT reuse hardenedDialContext/newHardenedTransport: this
// client's only ever dials the proxy address itself, which is always a
// loopback/local bind mihomo-fleet itself configured (never operator input),
// so hardenedDialContext's loopback/private-address rejection would be
// checking the wrong hop and would in fact reject the very address this
// feature needs to reach. The instance's own mihomo process resolves and
// dials the real target (GitHub/its CDN) on the other side of that proxy
// connection and is responsible for that hop; on this side, the URL
// whitelist (validateUpdateTargetFn, enforced inside downloadToFile
// regardless of which *http.Client it is given) still constrains which URL
// this client is ever asked to fetch, and the downloaded content's SHA-256
// is still verified exactly like a direct download -- see geoDownloadAndInstall.
func (c *Controller) proxyClientForInstance(id string) (*http.Client, error) {
	view, ok := c.manager.View(id)
	if !ok {
		return nil, fmt.Errorf("instance %q not found", id)
	}
	if view.Status != "running" {
		return nil, fmt.Errorf("instance %q is not running (status: %s)", id, view.Status)
	}

	proxyAddr := net.JoinHostPort(proxyDialAddress(view.ProxyBind), strconv.Itoa(view.MixedPort))
	proxyURL, err := url.Parse("http://" + proxyAddr)
	if err != nil {
		return nil, fmt.Errorf("build proxy URL for instance %q: %w", id, err)
	}

	transport := &http.Transport{
		Proxy: http.ProxyURL(proxyURL),
		// Note: intentionally not hardenedDialContext -- see doc comment above.
		DialContext: (&net.Dialer{
			Timeout:   proxyDialTimeout,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		TLSHandshakeTimeout: 15 * time.Second,
	}

	return &http.Client{
		Transport: transport,
		Timeout:   proxyClientTimeout,
		CheckRedirect: hardenedCheckRedirect(5, func(ctx context.Context, u *url.URL) error {
			return validateUpdateTargetFn(ctx, u, "download")
		}),
	}, nil
}
