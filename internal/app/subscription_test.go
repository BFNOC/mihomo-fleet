package app

import (
	"context"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
)

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
	if _, err := store.SetSelection(item.ID, "Proxy", "US-01"); err != nil {
		t.Fatal(err)
	}
	nextConfig := strings.Replace(subscriptionConfig, "      - US-01\n", "", 1)
	if _, err := store.ApplySubscriptionFetch(profile.ID, &subscriptionFetchResult{Config: nextConfig}); err != nil {
		t.Fatal(err)
	}
	updated, _ := store.Get(item.ID)
	if updated.SelectedProxy != "" || len(updated.SelectedProxies) != 0 {
		t.Fatalf("removed selection was not cleared: %+v", updated)
	}
}

func TestApplySubscriptionFetchKeepsGlobalChainSelection(t *testing.T) {
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
