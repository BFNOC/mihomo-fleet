package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

const (
	defaultSubscriptionUpdateIntervalMinutes = 360
	minSubscriptionUpdateIntervalMinutes     = 15
	maxSubscriptionBytes                     = 16 << 20
)

type subscriptionFetchResult struct {
	Config                string
	Name                  string
	UpdateIntervalMinutes int
	HomeURL               string
	Info                  *SubscriptionInfo
}

func fetchSubscription(ctx context.Context, client *http.Client, subscriptionURL, userAgent string) (*subscriptionFetchResult, error) {
	parsed, err := url.Parse(strings.TrimSpace(subscriptionURL))
	if err != nil {
		return nil, fmt.Errorf("parse subscription URL: %w", sanitizeSubscriptionError(err))
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, errors.New("subscription URL must start with http:// or https://")
	}
	if parsed.Host == "" {
		return nil, errors.New("subscription URL host is required")
	}
	if err := validateSubscriptionTargetFn(ctx, parsed); err != nil {
		return nil, sanitizeSubscriptionError(err)
	}
	if userAgent == "" {
		userAgent = "clash-verge/vdev"
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return nil, sanitizeSubscriptionError(err)
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "text/yaml, application/yaml, text/plain, */*")

	res, err := client.Do(req)
	if err != nil {
		// client.Do's error (and any redirect failure from CheckRedirect
		// below, which http.Client wraps into the same *url.Error) embeds
		// the full request URL -- including userinfo and any query-string
		// tokens the subscription link carries. This error is persisted to
		// instances.json (Profile.LastUpdateError) and printed via
		// log.Printf by callers, so it must be sanitized here, at the
		// source, rather than by every consumer.
		return nil, sanitizeSubscriptionError(err)
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return nil, fmt.Errorf("subscription server returned %s", res.Status)
	}

	limited := io.LimitReader(res.Body, maxSubscriptionBytes+1)
	raw, err := io.ReadAll(limited)
	if err != nil {
		return nil, err
	}
	if len(raw) > maxSubscriptionBytes {
		return nil, fmt.Errorf("subscription is larger than %d bytes", maxSubscriptionBytes)
	}
	config := strings.TrimPrefix(string(raw), "\ufeff")
	if err := validateSubscriptionConfig(config); err != nil {
		return nil, err
	}

	return &subscriptionFetchResult{
		Config:                config,
		Name:                  subscriptionName(res.Header, parsed),
		UpdateIntervalMinutes: subscriptionUpdateInterval(res.Header),
		HomeURL:               strings.TrimSpace(res.Header.Get("profile-web-page-url")),
		Info:                  subscriptionInfo(res.Header),
	}, nil
}

// sanitizeSubscriptionError redacts any URL embedded in err before it is
// returned from fetchSubscription. *url.Error (returned by url.Parse,
// http.NewRequestWithContext, and http.Client.Do -- including redirect
// failures from CheckRedirect, which the client wraps into the same type)
// carries the exact request/parse URL in its Error() text, userinfo and
// query string included. Since these errors flow into
// Profile.LastUpdateError (persisted to instances.json) and log.Printf
// (controller.go's refreshDueSubscriptions), any credential or token in the
// subscription link would otherwise leak to disk and stdout. Non-*url.Error
// values are returned unchanged: fetchSubscription's other error paths
// (size limits, HTTP status, YAML validation) never embed the URL.
func sanitizeSubscriptionError(err error) error {
	if err == nil {
		return nil
	}
	var uerr *url.Error
	if errors.As(err, &uerr) {
		sanitized := *uerr
		sanitized.URL = redactSubscriptionURLText(uerr.URL)
		sanitized.Err = sanitizeSubscriptionError(uerr.Err)
		return &sanitized
	}
	return err
}

// redactSubscriptionURLText reduces a URL-shaped string to a bare
// "scheme://host/path" form: no userinfo, no query string, no fragment.
// It prefers net/url.Redacted() (which masks -- but does not remove --
// userinfo) when raw parses cleanly, then additionally strips the query
// string/fragment, since subscription tokens are typically carried in the
// query. If raw does not parse as a URL at all (e.g. it *is* the malformed
// input that made url.Parse fail in the first place, so re-parsing it here
// would fail identically), it falls back to a best-effort textual strip of
// the same two things directly on raw.
func redactSubscriptionURLText(raw string) string {
	if parsed, err := url.Parse(raw); err == nil {
		redacted := parsed.Redacted()
		if idx := strings.IndexAny(redacted, "?#"); idx >= 0 {
			redacted = redacted[:idx]
		}
		return redacted
	}
	text := raw
	if idx := strings.IndexAny(text, "?#"); idx >= 0 {
		text = text[:idx]
	}
	if schemeEnd := strings.Index(text, "://"); schemeEnd >= 0 {
		afterScheme := schemeEnd + 3
		if at := strings.IndexByte(text[afterScheme:], '@'); at >= 0 {
			text = text[:afterScheme] + text[afterScheme+at+1:]
		}
	}
	return text
}

func newSubscriptionHTTPClient() *http.Client {
	dialer := &net.Dialer{
		Timeout:   8 * time.Second,
		KeepAlive: 30 * time.Second,
	}
	client := &http.Client{
		Transport: &http.Transport{
			DialContext: subscriptionDialContext(dialer),
			// L-4 (docs/review-2026-07-11-go-concurrency-performance.md):
			// http.Transport disables automatic HTTP/2 negotiation whenever a
			// custom DialContext is set, unless explicitly re-enabled here.
			// Subscription hosts are frequently CDN-fronted (h2-capable), so
			// this was silently pinning every subscription fetch to HTTP/1.1.
			ForceAttemptHTTP2:     true,
			ResponseHeaderTimeout: 15 * time.Second,
			IdleConnTimeout:       30 * time.Second,
		},
		Timeout: 25 * time.Second,
	}
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= 5 {
			return errors.New("subscription redirect limit exceeded")
		}
		return validateSubscriptionTargetFn(req.Context(), req.URL)
	}
	return client
}

func subscriptionDialContext(dialer *net.Dialer) func(context.Context, string, string) (net.Conn, error) {
	if dialer == nil {
		dialer = &net.Dialer{Timeout: 8 * time.Second, KeepAlive: 30 * time.Second}
	}
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, err
		}
		ips, err := safeSubscriptionIPs(ctx, host)
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
		return nil, errors.New("subscription host did not resolve")
	}
}

// validateSubscriptionTargetFn is a package-level indirection over
// validateSubscriptionTarget, following the same pattern as util.go's
// isPortFree: fetchSubscription and newSubscriptionHTTPClient's
// CheckRedirect call the var (never the function directly) so tests can
// substitute a permissive check and drive fetchSubscription against a
// loopback httptest.Server, which the real SSRF guard below would otherwise
// always reject (testing M2 in docs/review-2026-07-11-testing-quality.md).
var validateSubscriptionTargetFn = validateSubscriptionTarget

func validateSubscriptionTarget(ctx context.Context, parsed *url.URL) error {
	if parsed == nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return errors.New("subscription URL must start with http:// or https://")
	}
	host := parsed.Hostname()
	if host == "" {
		return errors.New("subscription URL host is required")
	}
	_, err := safeSubscriptionIPs(ctx, host)
	return err
}

func safeSubscriptionIPs(ctx context.Context, host string) ([]net.IP, error) {
	addrs, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, fmt.Errorf("resolve subscription host: %w", err)
	}
	if len(addrs) == 0 {
		return nil, errors.New("subscription host did not resolve")
	}
	ips := make([]net.IP, 0, len(addrs))
	for _, addr := range addrs {
		if blockedSubscriptionIP(addr.IP) {
			return nil, fmt.Errorf("subscription host resolves to blocked address %s", addr.IP.String())
		}
		ips = append(ips, addr.IP)
	}
	return ips, nil
}

func blockedSubscriptionIP(ip net.IP) bool {
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
	for _, network := range blockedSubscriptionNetworks {
		if network.Contains(ip) {
			return true
		}
	}
	return false
}

var blockedSubscriptionNetworks = []*net.IPNet{
	mustParseCIDR("100.64.0.0/10"),
	mustParseCIDR("192.0.0.0/24"),
	mustParseCIDR("192.0.2.0/24"),
	mustParseCIDR("198.18.0.0/15"),
	mustParseCIDR("198.51.100.0/24"),
	mustParseCIDR("203.0.113.0/24"),
	mustParseCIDR("240.0.0.0/4"),
	mustParseCIDR("100::/64"),
	mustParseCIDR("2001:db8::/32"),
}

func mustParseCIDR(raw string) *net.IPNet {
	_, network, err := net.ParseCIDR(raw)
	if err != nil {
		panic(err)
	}
	return network
}

func validateSubscriptionConfig(config string) error {
	var cfg map[string]any
	if err := yaml.Unmarshal([]byte(config), &cfg); err != nil {
		return fmt.Errorf("remote profile data is invalid yaml: %w", err)
	}
	if cfg == nil {
		return errors.New("remote profile is empty")
	}
	if _, ok := cfg["proxies"]; ok {
		return nil
	}
	if _, ok := cfg["proxy-providers"]; ok {
		return nil
	}
	return errors.New("remote profile must contain proxies or proxy-providers")
}

func subscriptionName(header http.Header, parsed *url.URL) string {
	disposition := header.Get("Content-Disposition")
	for _, part := range strings.Split(disposition, ";") {
		key, value, ok := strings.Cut(strings.TrimSpace(part), "=")
		if !ok {
			continue
		}
		isExtValue := strings.EqualFold(key, "filename*")
		if isExtValue || strings.EqualFold(key, "filename") {
			value = strings.Trim(value, `"`)
			if isExtValue {
				// RFC 5987 ext-value = charset "'" [ language ] "'" value-chars.
				// Splitting on the first two single quotes strips the
				// charset/language prefix whether or not a language tag is
				// present (e.g. both "UTF-8''name.yaml" and
				// "UTF-8'en'name.yaml"). The previous implementation only
				// searched for a literal "''" (adjacent quotes), which only
				// matches the empty-language-tag form and left a non-empty
				// language tag (07-04 review, L5) unstripped in the parsed
				// name.
				if parts := strings.SplitN(value, "'", 3); len(parts) == 3 {
					value = parts[2]
				}
			}
			if unescaped, err := url.QueryUnescape(value); err == nil {
				value = unescaped
			}
			value = strings.TrimSpace(value)
			if value != "" {
				return strings.TrimSuffix(value, path.Ext(value))
			}
		}
	}
	name := path.Base(parsed.Path)
	if name == "." || name == "/" || name == "" {
		return "Remote subscription"
	}
	if unescaped, err := url.PathUnescape(name); err == nil {
		name = unescaped
	}
	name = strings.TrimSuffix(name, path.Ext(name))
	if name == "" {
		return "Remote subscription"
	}
	return name
}

func subscriptionUpdateInterval(header http.Header) int {
	raw := strings.TrimSpace(header.Get("profile-update-interval"))
	if raw == "" {
		return 0
	}
	hours, err := strconv.Atoi(raw)
	if err != nil || hours <= 0 {
		return 0
	}
	return hours * 60
}

func subscriptionInfo(header http.Header) *SubscriptionInfo {
	for key, values := range header {
		lower := strings.ToLower(key)
		if lower != "subscription-userinfo" && !strings.HasSuffix(lower, "-subscription-userinfo") {
			continue
		}
		if len(values) == 0 {
			continue
		}
		return &SubscriptionInfo{
			Upload:   parseSubscriptionInfoValue(values[0], "upload"),
			Download: parseSubscriptionInfoValue(values[0], "download"),
			Total:    parseSubscriptionInfoValue(values[0], "total"),
			Expire:   parseSubscriptionInfoValue(values[0], "expire"),
		}
	}
	return nil
}

func parseSubscriptionInfoValue(raw, key string) int64 {
	for _, part := range strings.Split(raw, ";") {
		name, value, ok := strings.Cut(strings.TrimSpace(part), "=")
		if !ok || !strings.EqualFold(strings.TrimSpace(name), key) {
			continue
		}
		parsed, _ := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
		return parsed
	}
	return 0
}

func parseProfileProxyGroups(config string, selections map[string]string) ([]ProfileProxyGroup, error) {
	groups, err := parseProfileProxyGroupsBase(config)
	if err != nil {
		return nil, err
	}
	return applyProfileProxySelections(groups, selections), nil
}

// parseProfileProxyGroupsBase performs the actual (potentially expensive,
// full-document) YAML parse of a profile config into proxy groups, without
// applying any per-instance selection (each group's Now is left at its
// first candidate). Its result only depends on the config content, which is
// exactly what makes it safe for Store.ProfileProxyGroups to cache keyed by
// the config file's (path, modTime, size) -- see profileProxyGroupCache in
// store.go. Callers that need a specific selection applied (e.g. an
// instance's saved SelectedProxies) should call applyProfileProxySelections
// on the result instead of re-parsing.
func parseProfileProxyGroupsBase(config string) ([]ProfileProxyGroup, error) {
	var cfg struct {
		Proxies []struct {
			Name string `yaml:"name"`
		} `yaml:"proxies"`
		ProxyGroups []struct {
			Name    string   `yaml:"name"`
			Type    string   `yaml:"type"`
			Proxies []string `yaml:"proxies"`
		} `yaml:"proxy-groups"`
	}
	if err := yaml.Unmarshal([]byte(config), &cfg); err != nil {
		return nil, fmt.Errorf("parse profile config: %w", err)
	}

	groups := make([]ProfileProxyGroup, 0, len(cfg.ProxyGroups))
	for _, group := range cfg.ProxyGroups {
		if strings.TrimSpace(group.Name) == "" || len(group.Proxies) == 0 {
			continue
		}
		out := ProfileProxyGroup{
			Name: group.Name,
			Type: group.Type,
			All:  dedupeStrings(group.Proxies),
		}
		out.Now = selectionForGroup(out.Name, out.All, nil)
		groups = append(groups, out)
	}
	if len(groups) > 0 {
		return groups, nil
	}

	names := make([]string, 0, len(cfg.Proxies)+1)
	for _, proxy := range cfg.Proxies {
		if proxy.Name != "" {
			names = append(names, proxy.Name)
		}
	}
	if len(names) == 0 {
		return nil, nil
	}
	names = append(names, "DIRECT")
	names = dedupeStrings(names)
	return []ProfileProxyGroup{{
		Name: "Proxy",
		Type: "select",
		All:  names,
		Now:  selectionForGroup("Proxy", names, nil),
	}}, nil
}

// applyProfileProxySelections returns a copy of groups with Now recomputed
// per the given per-group selections (falling back to each group's first
// candidate, same rule as parseProfileProxyGroupsBase). It does not mutate
// groups or alias-write into the All slices it shares with them, since
// groups may be a slice owned by profileProxyGroupCache.
func applyProfileProxySelections(groups []ProfileProxyGroup, selections map[string]string) []ProfileProxyGroup {
	if len(groups) == 0 {
		return groups
	}
	out := make([]ProfileProxyGroup, len(groups))
	for i, group := range groups {
		group.Now = selectionForGroup(group.Name, group.All, selections)
		out[i] = group
	}
	return out
}

func selectionForGroup(group string, names []string, selections map[string]string) string {
	if selected := selections[group]; selected != "" {
		for _, name := range names {
			if name == selected {
				return selected
			}
		}
	}
	if len(names) == 0 {
		return ""
	}
	return names[0]
}

func dedupeStrings(in []string) []string {
	out := make([]string, 0, len(in))
	seen := make(map[string]bool, len(in))
	for _, value := range in {
		if strings.TrimSpace(value) == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	return out
}

func profileSubscriptionDue(profile *Profile, now time.Time) bool {
	if profile == nil || profile.SubscriptionURL == "" || !profile.AutoUpdate || profile.UpdateIntervalMinutes <= 0 {
		return false
	}
	lastAttempt := profile.LastUpdatedAt
	if profile.LastUpdateError != "" && profile.UpdatedAt.After(lastAttempt) {
		lastAttempt = profile.UpdatedAt
	}
	if lastAttempt.IsZero() {
		return true
	}
	return !lastAttempt.Add(time.Duration(profile.UpdateIntervalMinutes) * time.Minute).After(now)
}

func normalizeSubscriptionInterval(minutes int, autoUpdate bool) int {
	if minutes <= 0 {
		if autoUpdate {
			return defaultSubscriptionUpdateIntervalMinutes
		}
		return 0
	}
	if autoUpdate && minutes < minSubscriptionUpdateIntervalMinutes {
		return minSubscriptionUpdateIntervalMinutes
	}
	return minutes
}
