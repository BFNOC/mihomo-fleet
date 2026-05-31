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
		return nil, fmt.Errorf("parse subscription URL: %w", err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, errors.New("subscription URL must start with http:// or https://")
	}
	if parsed.Host == "" {
		return nil, errors.New("subscription URL host is required")
	}
	if err := validateSubscriptionTarget(ctx, parsed); err != nil {
		return nil, err
	}
	if userAgent == "" {
		userAgent = "clash-verge/vdev"
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "text/yaml, application/yaml, text/plain, */*")

	res, err := client.Do(req)
	if err != nil {
		return nil, err
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

func newSubscriptionHTTPClient() *http.Client {
	dialer := &net.Dialer{
		Timeout:   8 * time.Second,
		KeepAlive: 30 * time.Second,
	}
	client := &http.Client{
		Transport: &http.Transport{
			DialContext:           subscriptionDialContext(dialer),
			ResponseHeaderTimeout: 15 * time.Second,
			IdleConnTimeout:       30 * time.Second,
		},
		Timeout: 25 * time.Second,
	}
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= 5 {
			return errors.New("subscription redirect limit exceeded")
		}
		return validateSubscriptionTarget(req.Context(), req.URL)
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
		if strings.EqualFold(key, "filename") || strings.EqualFold(key, "filename*") {
			value = strings.Trim(value, `"`)
			if idx := strings.LastIndex(value, "''"); idx >= 0 {
				value = value[idx+2:]
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
		out.Now = selectionForGroup(out.Name, out.All, selections)
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
		Now:  selectionForGroup("Proxy", names, selections),
	}}, nil
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
