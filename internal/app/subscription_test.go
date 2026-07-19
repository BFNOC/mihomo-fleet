package app

import (
	"bytes"
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

var subscriptionTargetTestMu sync.Mutex

// withSubscriptionTargetAllowed overrides validateSubscriptionTargetFn so
// fetchSubscription's SSRF pre-check does not reject the loopback
// httptest.Server address these tests use (testing M2 in
// docs/review-2026-07-11-testing-quality.md). It must be paired with a
// plain *http.Client (e.g. server.Client()) rather than
// newSubscriptionHTTPClient's client: that client's custom DialContext
// independently re-checks blockedSubscriptionIP on every connection
// attempt via safeSubscriptionIPs, so it would still refuse loopback even
// with this override in place.
func withSubscriptionTargetAllowed(t *testing.T) {
	t.Helper()
	subscriptionTargetTestMu.Lock()
	original := validateSubscriptionTargetFn
	validateSubscriptionTargetFn = func(context.Context, *url.URL) error { return nil }
	t.Cleanup(func() {
		validateSubscriptionTargetFn = original
		subscriptionTargetTestMu.Unlock()
	})
}

const subscriptionConfig = `mixed-port: 7890
proxies:
  - name: US-01
    type: ss
    server: example.com
    port: 443
  - name: JP-01
    type: ss
    server: example.net
    port: 443
proxy-groups:
  - name: Proxy
    type: select
    proxies:
      - US-01
      - JP-01
      - DIRECT
rules:
  - MATCH,Proxy
`

func TestCreateSubscriptionProfileStoresMetadata(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	fetched := &subscriptionFetchResult{
		Config:                subscriptionConfig,
		Name:                  "Provider",
		UpdateIntervalMinutes: 720,
		HomeURL:               "https://example.com",
		Info:                  &SubscriptionInfo{Upload: 10, Download: 20, Total: 100, Expire: 1893456000},
	}
	profile, err := store.CreateSubscriptionProfile("", "https://example.com/sub", true, 0, fetched)
	if err != nil {
		t.Fatal(err)
	}
	if profile.Name != "Provider" || profile.SubscriptionURL != "https://example.com/sub" {
		t.Fatalf("unexpected subscription profile metadata: %+v", profile)
	}
	if !profile.AutoUpdate || profile.UpdateIntervalMinutes != 720 {
		t.Fatalf("unexpected update settings: %+v", profile)
	}
	if profile.LastUpdatedAt.IsZero() || profile.SubscriptionInfo == nil || profile.SubscriptionInfo.Total != 100 {
		t.Fatalf("subscription metadata was not stored: %+v", profile)
	}
	raw, err := os.ReadFile(profile.ConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "US-01") {
		t.Fatalf("cached subscription config was not written:\n%s", raw)
	}
}

func TestParseProfileProxyGroupsUsesSavedSelection(t *testing.T) {
	groups, err := parseProfileProxyGroups(subscriptionConfig, map[string]string{"Proxy": "JP-01"})
	if err != nil {
		t.Fatal(err)
	}
	if len(groups) != 1 {
		t.Fatalf("got %d groups, want 1", len(groups))
	}
	if groups[0].Name != "Proxy" || groups[0].Now != "JP-01" {
		t.Fatalf("unexpected group selection: %+v", groups[0])
	}
	if strings.Join(groups[0].All, ",") != "US-01,JP-01,DIRECT" {
		t.Fatalf("unexpected group candidates: %+v", groups[0].All)
	}
}

func TestApplySubscriptionFetchClearsRemovedSelection(t *testing.T) {
	withPortFree(t, func(int) bool { return true })

	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	profile, err := store.CreateSubscriptionProfile("Provider", "https://example.com/sub", true, 360, &subscriptionFetchResult{Config: subscriptionConfig})
	if err != nil {
		t.Fatal(err)
	}
	item, err := store.Create("US gateway", profile.ID, "", 28021, 29021)
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.Create("Second gateway", profile.ID, "", 28031, 29031)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.SetSelection(item.ID, "Proxy", "US-01"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ApplySubscriptionFetch(profile.ID, &subscriptionFetchResult{Config: subscriptionConfig}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{item.ID, second.ID} {
		updated, ok := store.Get(id)
		if !ok || !updated.ConfigUpdatedAt.IsZero() {
			t.Fatalf("unchanged subscription marked shared reference %s: %+v", id, updated)
		}
	}
	nextConfig := strings.Replace(subscriptionConfig, "      - US-01\n", "", 1)
	if _, err := store.ApplySubscriptionFetch(profile.ID, &subscriptionFetchResult{Config: nextConfig}); err != nil {
		t.Fatal(err)
	}
	updated, _ := store.Get(item.ID)
	if updated.SelectedProxy != "" || len(updated.SelectedProxies) != 0 {
		t.Fatalf("removed selection was not cleared: %+v", updated)
	}
	for _, id := range []string{item.ID, second.ID} {
		updated, ok := store.Get(id)
		if !ok || updated.ConfigUpdatedAt.IsZero() {
			t.Fatalf("subscription refresh did not mark shared reference %s: %+v", id, updated)
		}
	}
}

func TestApplySubscriptionFetchKeepsGlobalChainSelection(t *testing.T) {
	withPortFree(t, func(int) bool { return true })

	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	profile, err := store.CreateSubscriptionProfile("Provider", "https://example.com/sub", true, 360, &subscriptionFetchResult{Config: subscriptionConfig})
	if err != nil {
		t.Fatal(err)
	}
	item, err := store.CreateWithOptions(createInstanceOptions{
		Name:           "Chain gateway",
		ProfileID:      profile.ID,
		MixedPort:      28022,
		ControllerPort: 29022,
		Mode:           InstanceModeGlobalChain,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.SetSelection(item.ID, globalChainSelectGroupName, "US-01"); err != nil {
		t.Fatal(err)
	}
	nextConfig := strings.Replace(subscriptionConfig, "  - MATCH,Proxy\n", "  - MATCH,DIRECT\n", 1)
	if _, err := store.ApplySubscriptionFetch(profile.ID, &subscriptionFetchResult{Config: nextConfig}); err != nil {
		t.Fatal(err)
	}
	updated, _ := store.Get(item.ID)
	if updated.SelectedProxies[globalChainSelectGroupName] != "US-01" {
		t.Fatalf("global-chain selection was cleared: %+v", updated)
	}
}

func TestStoreGetClonesSelectedProxies(t *testing.T) {
	withPortFree(t, func(int) bool { return true })

	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	profile, err := store.CreateProfile("Main", subscriptionConfig)
	if err != nil {
		t.Fatal(err)
	}
	item, err := store.Create("US gateway", profile.ID, "", 28021, 29021)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.SetSelection(item.ID, "Proxy", "US-01"); err != nil {
		t.Fatal(err)
	}
	first, ok := store.Get(item.ID)
	if !ok {
		t.Fatal("created instance was not found")
	}
	first.SelectedProxies["Proxy"] = "JP-01"
	second, _ := store.Get(item.ID)
	if second.SelectedProxies["Proxy"] != "US-01" {
		t.Fatalf("Get returned aliased SelectedProxies map: %+v", second.SelectedProxies)
	}
}

func TestParseProfileProxyGroupsSynthesizesFallbackGroupWhenNoGroupsPresent(t *testing.T) {
	config := `proxies:
  - name: US-01
    type: ss
    server: example.com
    port: 443
  - name: JP-01
    type: ss
    server: example.net
    port: 443
`
	groups, err := parseProfileProxyGroups(config, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(groups) != 1 {
		t.Fatalf("got %d groups, want 1", len(groups))
	}
	if groups[0].Name != "Proxy" || groups[0].Type != "select" {
		t.Fatalf("unexpected fallback group: %+v", groups[0])
	}
	if strings.Join(groups[0].All, ",") != "US-01,JP-01,DIRECT" {
		t.Fatalf("unexpected fallback members: %+v", groups[0].All)
	}
	if groups[0].Now != "US-01" {
		t.Fatalf("fallback Now = %q, want first candidate US-01", groups[0].Now)
	}
}

func TestParseProfileProxyGroupsEmptyInputReturnsNil(t *testing.T) {
	groups, err := parseProfileProxyGroups("mixed-port: 1\n", nil)
	if err != nil {
		t.Fatal(err)
	}
	if groups != nil {
		t.Fatalf("groups = %+v, want nil", groups)
	}
}

func TestSubscriptionHeaderParsing(t *testing.T) {
	header := http.Header{}
	header.Set("profile-update-interval", "12")
	header.Set("subscription-userinfo", "upload=1; download=2; total=3; expire=4")
	if got := subscriptionUpdateInterval(header); got != 720 {
		t.Fatalf("subscriptionUpdateInterval() = %d, want 720", got)
	}
	info := subscriptionInfo(header)
	if info == nil || info.Upload != 1 || info.Download != 2 || info.Total != 3 || info.Expire != 4 {
		t.Fatalf("subscriptionInfo() = %+v", info)
	}
}

func TestValidateSubscriptionTargetBlocksLoopback(t *testing.T) {
	parsed, err := url.Parse("http://127.0.0.1/sub.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if err := validateSubscriptionTarget(context.Background(), parsed); err == nil {
		t.Fatal("expected loopback subscription target to be blocked")
	}
}

func TestBlockedSubscriptionIPIncludesReservedRanges(t *testing.T) {
	blocked := []string{
		"100.64.0.1",
		"169.254.169.254",
		"198.18.0.1",
		"240.0.0.1",
		"::ffff:127.0.0.1",
		"2001:db8::1",
	}
	for _, raw := range blocked {
		if !blockedSubscriptionIP(net.ParseIP(raw)) {
			t.Fatalf("expected %s to be blocked", raw)
		}
	}
}

func TestProfileSubscriptionDueUsesLastAttempt(t *testing.T) {
	now := time.Date(2026, 5, 31, 12, 0, 0, 0, time.UTC)
	profile := &Profile{
		SubscriptionURL:       "https://example.com/sub",
		AutoUpdate:            true,
		UpdateIntervalMinutes: 60,
		LastUpdatedAt:         now.Add(-2 * time.Hour),
		LastUpdateError:       "temporary failure",
		UpdatedAt:             now.Add(-10 * time.Minute),
	}
	if profileSubscriptionDue(profile, now) {
		t.Fatal("expected failed recent attempt to delay the next auto update")
	}
	profile.UpdatedAt = now.Add(-61 * time.Minute)
	if !profileSubscriptionDue(profile, now) {
		t.Fatal("expected profile to be due after interval since last attempt")
	}
}

// TestSanitizeSubscriptionErrorStripsCredentialsAndQuery covers review
// finding M-2: fetchSubscription must never let a subscription URL's
// userinfo or query-string tokens (where subscription tokens typically
// live) reach the error text that gets persisted to instances.json
// (Profile.LastUpdateError) or printed via log.Printf. This exercises
// sanitizeSubscriptionError directly against a *url.Error shaped like the
// one http.Client.Do (or a CheckRedirect failure it wraps) returns, since
// fetchSubscription's own dial path can't be driven through httptest here
// (the SSRF guard rejects loopback targets by design -- see M2 in
// docs/review-2026-07-11-testing-quality.md).
func TestSanitizeSubscriptionErrorStripsCredentialsAndQuery(t *testing.T) {
	rawURL := "https://user:pass@example.com/sub?token=secret123"
	simulated := &url.Error{Op: "Get", URL: rawURL, Err: errors.New("dial tcp: connect: connection refused")}

	sanitized := sanitizeSubscriptionError(simulated)
	if sanitized == nil {
		t.Fatal("sanitizeSubscriptionError(non-nil) returned nil")
	}
	msg := sanitized.Error()
	if strings.Contains(msg, "pass") {
		t.Fatalf("sanitized error still contains the password: %q", msg)
	}
	if strings.Contains(msg, "secret123") {
		t.Fatalf("sanitized error still contains the query token: %q", msg)
	}
	if strings.Contains(msg, "token=secret123") || strings.Contains(msg, "?") {
		t.Fatalf("sanitized error still contains the query string: %q", msg)
	}
	if !strings.Contains(msg, "example.com") {
		t.Fatalf("sanitized error lost the (non-sensitive) host: %q", msg)
	}
}

// TestSanitizeSubscriptionErrorFallsBackForUnparsableURL covers the
// url.Parse-failure path in fetchSubscription, where the *url.Error's URL
// field is the raw (invalid) input string itself and so cannot be
// re-parsed to build a redacted form -- sanitizeSubscriptionError must fall
// back to a textual strip instead of leaving it untouched.
func TestSanitizeSubscriptionErrorFallsBackForUnparsableURL(t *testing.T) {
	rawURL := "https://user:pass@example.com/sub?token=secret123\x7f"
	simulated := &url.Error{Op: "parse", URL: rawURL, Err: errors.New("net/url: invalid control character in URL")}

	sanitized := sanitizeSubscriptionError(simulated)
	msg := sanitized.Error()
	if strings.Contains(msg, "pass") {
		t.Fatalf("sanitized error still contains the password: %q", msg)
	}
	if strings.Contains(msg, "secret123") {
		t.Fatalf("sanitized error still contains the query token: %q", msg)
	}
}

// TestFetchSubscriptionStripsBOM covers testing M2: fetchSubscription must
// strip a leading UTF-8 BOM from the response body before it is treated as
// YAML/persisted as the profile config.
func TestFetchSubscriptionStripsBOM(t *testing.T) {
	withSubscriptionTargetAllowed(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("\ufeff" + subscriptionConfig))
	}))
	defer server.Close()

	fetched, err := fetchSubscription(context.Background(), server.Client(), server.URL, "test-agent")
	if err != nil {
		t.Fatal(err)
	}
	if strings.HasPrefix(fetched.Config, "\ufeff") {
		t.Fatalf("BOM was not stripped, config starts with: %q", fetched.Config[:10])
	}
	if !strings.Contains(fetched.Config, "US-01") {
		t.Fatalf("config content was lost: %q", fetched.Config)
	}
}

// TestFetchSubscriptionRejectsOversizedBody covers testing M2: a response
// larger than the 16MB cap must be rejected rather than buffered in full.
func TestFetchSubscriptionRejectsOversizedBody(t *testing.T) {
	withSubscriptionTargetAllowed(t)
	huge := bytes.Repeat([]byte("a"), maxSubscriptionBytes+1024)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(huge)
	}))
	defer server.Close()

	_, err := fetchSubscription(context.Background(), server.Client(), server.URL, "test-agent")
	if err == nil || !strings.Contains(err.Error(), "larger than") {
		t.Fatalf("err = %v, want a size-limit error", err)
	}
}

// TestFetchSubscriptionNonSuccessStatus covers testing M2: a non-2xx
// response must surface as an error rather than being parsed as a config.
func TestFetchSubscriptionNonSuccessStatus(t *testing.T) {
	withSubscriptionTargetAllowed(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer server.Close()

	_, err := fetchSubscription(context.Background(), server.Client(), server.URL, "test-agent")
	if err == nil || !strings.Contains(err.Error(), "subscription server returned") {
		t.Fatalf("err = %v, want a status error", err)
	}
}

// TestFetchSubscriptionParsesHeaderMetadata covers testing M2: the
// update-interval, subscription-userinfo, and profile-web-page-url response
// headers must be parsed into the fetch result.
func TestFetchSubscriptionParsesHeaderMetadata(t *testing.T) {
	withSubscriptionTargetAllowed(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("profile-update-interval", "12")
		w.Header().Set("subscription-userinfo", "upload=1; download=2; total=3; expire=4")
		w.Header().Set("profile-web-page-url", " https://example.com/home ")
		_, _ = w.Write([]byte(subscriptionConfig))
	}))
	defer server.Close()

	fetched, err := fetchSubscription(context.Background(), server.Client(), server.URL, "test-agent")
	if err != nil {
		t.Fatal(err)
	}
	if fetched.UpdateIntervalMinutes != 720 {
		t.Fatalf("UpdateIntervalMinutes = %d, want 720", fetched.UpdateIntervalMinutes)
	}
	if fetched.HomeURL != "https://example.com/home" {
		t.Fatalf("HomeURL = %q, want trimmed home URL", fetched.HomeURL)
	}
	if fetched.Info == nil || fetched.Info.Upload != 1 || fetched.Info.Download != 2 || fetched.Info.Total != 3 || fetched.Info.Expire != 4 {
		t.Fatalf("Info = %+v, want upload/download/total/expire 1/2/3/4", fetched.Info)
	}
}

// TestFetchSubscriptionUsesFilenameStarNameWithLanguageTag exercises the
// 07-04 review L5 fix end to end through a real httptest server: an RFC
// 5987 filename* value with a non-empty language tag
// (charset'language'value, as opposed to the empty-language
// charset”value form) must still have its name extracted correctly.
func TestFetchSubscriptionUsesFilenameStarNameWithLanguageTag(t *testing.T) {
	withSubscriptionTargetAllowed(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Disposition", `attachment; filename*=UTF-8'en'my-nodes.yaml`)
		_, _ = w.Write([]byte(subscriptionConfig))
	}))
	defer server.Close()

	fetched, err := fetchSubscription(context.Background(), server.Client(), server.URL, "test-agent")
	if err != nil {
		t.Fatal(err)
	}
	if fetched.Name != "my-nodes" {
		t.Fatalf("Name = %q, want %q", fetched.Name, "my-nodes")
	}
}

// TestSubscriptionNameParsesContentDisposition table-tests subscriptionName
// directly (testing M2's "immediately doable" pure-function suggestion),
// including the RFC 5987 filename* forms with an empty and a non-empty
// language tag (07-04 review L5).
func TestSubscriptionNameParsesContentDisposition(t *testing.T) {
	parsedURL, err := url.Parse("https://example.com/path/sub.yaml?token=x")
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name        string
		disposition string
		want        string
	}{
		{"plain filename", `attachment; filename="nodes.yaml"`, "nodes"},
		{"filename* empty language tag", `attachment; filename*=UTF-8''nodes.yaml`, "nodes"},
		{"filename* non-empty language tag", `attachment; filename*=UTF-8'en'nodes.yaml`, "nodes"},
		{"filename* percent-encoded value", `attachment; filename*=UTF-8''%E8%AE%A2%E9%98%85.yaml`, "订阅"},
		{"no content-disposition falls back to URL path", "", "sub"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			header := http.Header{}
			if tt.disposition != "" {
				header.Set("Content-Disposition", tt.disposition)
			}
			if got := subscriptionName(header, parsedURL); got != tt.want {
				t.Fatalf("subscriptionName() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestNormalizeSubscriptionIntervalClampsAutoUpdate(t *testing.T) {
	if got := normalizeSubscriptionInterval(1, true); got != minSubscriptionUpdateIntervalMinutes {
		t.Fatalf("normalizeSubscriptionInterval(1, true) = %d, want %d", got, minSubscriptionUpdateIntervalMinutes)
	}
	if got := normalizeSubscriptionInterval(0, true); got != defaultSubscriptionUpdateIntervalMinutes {
		t.Fatalf("normalizeSubscriptionInterval(0, true) = %d, want %d", got, defaultSubscriptionUpdateIntervalMinutes)
	}
	if got := normalizeSubscriptionInterval(1, false); got != 1 {
		t.Fatalf("normalizeSubscriptionInterval(1, false) = %d, want 1", got)
	}
}
