package app

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"gopkg.in/yaml.v3"
)

var portFreeTestMu sync.Mutex

func TestStorePersistsSecretButViewDoesNotExposeIt(t *testing.T) {
	withPortFree(t, func(int) bool { return true })

	dir := t.TempDir()
	store, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	item, err := store.Create("HK", "", defaultUserConfig, 28001, 29001)
	if err != nil {
		t.Fatal(err)
	}
	if item.Secret == "" {
		t.Fatal("expected generated secret")
	}

	reloaded, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := reloaded.Get(item.ID)
	if !ok {
		t.Fatal("expected reloaded instance")
	}
	if got.Secret != item.Secret {
		t.Fatal("secret was not persisted")
	}

	profile, _ := reloaded.GetProfile(got.ProfileID)
	view := viewFor(got, profile, "stopped", 0)
	raw, err := json.Marshal(view)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), item.Secret) || strings.Contains(string(raw), "secret") {
		t.Fatalf("view leaked secret: %s", raw)
	}
}

// TestWriteRuntimeConfigInjectsLoopbackFields covers cleanRuntimeConfig's
// full strip list (arch M3, docs/review-2026-07-11-go-architecture.md
// extended it with external-controller-unix/external-controller-pipe) and
// asserts against the parsed runtime config map (testing L10,
// docs/review-2026-07-11-testing-quality.md) instead of substring-matching
// the raw marshaled YAML, which was coupled to yaml.Marshal's exact
// formatting (quoting/indentation) and could be fooled by an unrelated key
// sharing a blocked key's prefix (e.g. a hypothetical "tun-something" would
// have satisfied the old `strings.Contains(got, "tun:")` check either way).
func TestWriteRuntimeConfigInjectsLoopbackFields(t *testing.T) {
	dir := t.TempDir()
	item := &Instance{
		ID:                "test",
		Name:              "Test",
		MixedPort:         28002,
		ControllerPort:    29002,
		Secret:            "secret-token",
		UserConfigPath:    filepath.Join(dir, "config.user.yaml"),
		RuntimeConfigPath: filepath.Join(dir, "config.runtime.yaml"),
	}
	user := "mixed-port: 1\n" +
		"port: 8080\n" +
		"socks-port: 7891\n" +
		"redir-port: 7892\n" +
		"tproxy-port: 7893\n" +
		"external-controller: 0.0.0.0:9999\n" +
		"external-controller-unix: /tmp/other-instance.sock\n" +
		"external-controller-pipe: \\\\.\\pipe\\other-instance\n" +
		"external-ui: ui\n" +
		"tun:\n  enable: true\n" +
		"listeners:\n  - name: test\n" +
		"secret: bad\n" +
		"allow-lan: true\n" +
		"dns:\n  enable: true\n  listen: 0.0.0.0:1053\n"
	if err := os.WriteFile(item.UserConfigPath, []byte(user), 0o644); err != nil {
		t.Fatal(err)
	}
	profile := &Profile{ID: "profile", Name: "Profile", ConfigPath: item.UserConfigPath}
	if _, err := writeRuntimeConfig(item, profile); err != nil {
		t.Fatal(err)
	}
	cfg := readRuntimeConfigMap(t, item.RuntimeConfigPath)

	if cfg["mixed-port"] != 28002 {
		t.Fatalf("mixed-port = %#v, want 28002", cfg["mixed-port"])
	}
	if cfg["external-controller"] != "127.0.0.1:29002" {
		t.Fatalf("external-controller = %#v, want 127.0.0.1:29002", cfg["external-controller"])
	}
	if cfg["secret"] != "secret-token" {
		t.Fatalf("secret = %#v, want secret-token", cfg["secret"])
	}
	if cfg["allow-lan"] != false {
		t.Fatalf("allow-lan = %#v, want false", cfg["allow-lan"])
	}
	if cfg["bind-address"] != "127.0.0.1" {
		t.Fatalf("bind-address = %#v, want 127.0.0.1", cfg["bind-address"])
	}
	// dns.listen is deliberately NOT stripped (arch M3): it may be an
	// intentional single-instance choice, so StartContext logs a warning
	// instead (see TestManagerStartLogsDNSListenWarning in manager_test.go).
	dns, _ := cfg["dns"].(map[string]any)
	if dns["listen"] != "0.0.0.0:1053" {
		t.Fatalf("dns.listen = %#v, want it preserved (0.0.0.0:1053)", dns["listen"])
	}
	for _, blocked := range []string{
		"port", "socks-port", "redir-port", "tproxy-port",
		"external-ui", "external-controller-unix", "external-controller-pipe",
		"tun", "listeners",
	} {
		if _, ok := cfg[blocked]; ok {
			t.Fatalf("runtime config kept conflicting key %q: %#v", blocked, cfg[blocked])
		}
	}
}

func TestWriteRuntimeConfigAllProxyBindUsesMihomoWildcard(t *testing.T) {
	dir := t.TempDir()
	item := &Instance{
		ID:                "test",
		Name:              "Test",
		MixedPort:         28002,
		ProxyBind:         "all",
		ControllerPort:    29002,
		Secret:            "secret-token",
		UserConfigPath:    filepath.Join(dir, "config.user.yaml"),
		RuntimeConfigPath: filepath.Join(dir, "config.runtime.yaml"),
	}
	if err := os.WriteFile(item.UserConfigPath, []byte(defaultUserConfig), 0o644); err != nil {
		t.Fatal(err)
	}
	profile := &Profile{ID: "profile", Name: "Profile", ConfigPath: item.UserConfigPath}
	if _, err := writeRuntimeConfig(item, profile); err != nil {
		t.Fatal(err)
	}
	cfg := readRuntimeConfigMap(t, item.RuntimeConfigPath)
	if cfg["mixed-port"] != 28002 {
		t.Fatalf("mixed-port = %#v, want 28002", cfg["mixed-port"])
	}
	if cfg["allow-lan"] != true {
		t.Fatalf("allow-lan = %#v, want true", cfg["allow-lan"])
	}
	if cfg["bind-address"] != "*" {
		t.Fatalf("bind-address = %#v, want *", cfg["bind-address"])
	}
	if _, ok := cfg["listeners"]; ok {
		t.Fatalf("single wildcard bind should not emit listeners: %#v", cfg["listeners"])
	}
}

func TestWriteRuntimeConfigSingleProxyBindUsesTopLevelBind(t *testing.T) {
	dir := t.TempDir()
	item := &Instance{
		ID:                "test",
		Name:              "Test",
		MixedPort:         28002,
		ProxyBind:         "192.168.64.1",
		ControllerPort:    29002,
		Secret:            "secret-token",
		UserConfigPath:    filepath.Join(dir, "config.user.yaml"),
		RuntimeConfigPath: filepath.Join(dir, "config.runtime.yaml"),
	}
	if err := os.WriteFile(item.UserConfigPath, []byte(defaultUserConfig), 0o644); err != nil {
		t.Fatal(err)
	}
	profile := &Profile{ID: "profile", Name: "Profile", ConfigPath: item.UserConfigPath}
	if _, err := writeRuntimeConfig(item, profile); err != nil {
		t.Fatal(err)
	}
	cfg := readRuntimeConfigMap(t, item.RuntimeConfigPath)
	if cfg["mixed-port"] != 28002 {
		t.Fatalf("mixed-port = %#v, want 28002", cfg["mixed-port"])
	}
	if cfg["allow-lan"] != true {
		t.Fatalf("allow-lan = %#v, want true", cfg["allow-lan"])
	}
	if cfg["bind-address"] != "192.168.64.1" {
		t.Fatalf("bind-address = %#v, want 192.168.64.1", cfg["bind-address"])
	}
	if _, ok := cfg["listeners"]; ok {
		t.Fatalf("single bind should not emit listeners: %#v", cfg["listeners"])
	}
}

func TestWriteRuntimeConfigMultiProxyBindEmitsListeners(t *testing.T) {
	dir := t.TempDir()
	item := &Instance{
		ID:                "test",
		Name:              "Test",
		MixedPort:         28002,
		ProxyBind:         "127.0.0.1,192.168.64.1",
		ControllerPort:    29002,
		Secret:            "secret-token",
		UserConfigPath:    filepath.Join(dir, "config.user.yaml"),
		RuntimeConfigPath: filepath.Join(dir, "config.runtime.yaml"),
	}
	if err := os.WriteFile(item.UserConfigPath, []byte("mixed-port: 1\nlisteners:\n  - name: user\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	profile := &Profile{ID: "profile", Name: "Profile", ConfigPath: item.UserConfigPath}
	if _, err := writeRuntimeConfig(item, profile); err != nil {
		t.Fatal(err)
	}
	cfg := readRuntimeConfigMap(t, item.RuntimeConfigPath)
	if _, ok := cfg["mixed-port"]; ok {
		t.Fatalf("multi bind kept mixed-port: %#v", cfg["mixed-port"])
	}
	if _, ok := cfg["bind-address"]; ok {
		t.Fatalf("multi bind kept bind-address: %#v", cfg["bind-address"])
	}
	if cfg["allow-lan"] != true {
		t.Fatalf("allow-lan = %#v, want true", cfg["allow-lan"])
	}
	listeners, ok := cfg["listeners"].([]any)
	if !ok || len(listeners) != 2 {
		t.Fatalf("listeners = %#v, want two listeners", cfg["listeners"])
	}
	for index, wantListen := range []string{"127.0.0.1", "192.168.64.1"} {
		listener, ok := listeners[index].(map[string]any)
		if !ok {
			t.Fatalf("listener %d = %#v", index, listeners[index])
		}
		if listener["type"] != "mixed" || listener["port"] != 28002 || listener["listen"] != wantListen || listener["udp"] != true {
			t.Fatalf("listener %d = %#v", index, listener)
		}
	}
}

func TestWriteRuntimeConfigGlobalChainBuildsDialerProxy(t *testing.T) {
	dir := t.TempDir()
	item := &Instance{
		ID:                "test",
		Name:              "Test",
		MixedPort:         28003,
		ControllerPort:    29003,
		Secret:            "secret-token",
		UserConfigPath:    filepath.Join(dir, "config.user.yaml"),
		RuntimeConfigPath: filepath.Join(dir, "config.runtime.yaml"),
		Mode:              InstanceModeGlobalChain,
		LocalProxies:      "- name: local-hop\n  type: socks5\n  server: 127.0.0.1\n  port: 1080\n",
		Chain:             []string{"local-hop", globalChainSelectGroupName},
	}
	user := `mixed-port: 1
proxies:
  - name: US-01
    type: ss
    server: example.com
    port: 443
proxy-providers:
  remote:
    type: http
    url: https://example.com/sub
    path: ./remote.yaml
    override:
      udp: true
proxy-groups:
  - name: Old
    type: select
    proxies:
      - DIRECT
rule-providers:
  old:
    type: http
rules:
  - GEOSITE,google,Old
  - MATCH,Old
`
	if err := os.WriteFile(item.UserConfigPath, []byte(user), 0o644); err != nil {
		t.Fatal(err)
	}
	profile := &Profile{ID: "profile", Name: "Profile", ConfigPath: item.UserConfigPath}
	if _, err := writeRuntimeConfig(item, profile); err != nil {
		t.Fatal(err)
	}
	cfg := readRuntimeConfigMap(t, item.RuntimeConfigPath)
	if cfg["mixed-port"] != 28003 || cfg["external-controller"] != "127.0.0.1:29003" {
		t.Fatalf("runtime fields were not injected: %#v", cfg)
	}
	if _, ok := cfg["rule-providers"]; ok {
		t.Fatalf("global-chain config kept rule-providers: %#v", cfg)
	}
	rules, _ := cfg["rules"].([]any)
	if len(rules) != 2 || rules[0] != "NETWORK,UDP,REJECT" || rules[1] != "MATCH,"+globalChainSelectGroupName {
		t.Fatalf("rules = %#v, want UDP reject then MATCH select", rules)
	}
	groups, _ := cfg["proxy-groups"].([]any)
	if len(groups) != 1 {
		t.Fatalf("proxy-groups = %#v, want select only", groups)
	}
	selectGroup := groups[0].(map[string]any)
	if selectGroup["name"] != globalChainSelectGroupName || selectGroup["type"] != "select" {
		t.Fatalf("select group = %#v", selectGroup)
	}
	if got := strings.Join(anyStrings(selectGroup["proxies"]), ","); got != "US-01" {
		t.Fatalf("select proxies = %q", got)
	}
	if got := strings.Join(anyStrings(selectGroup["use"]), ","); got != "remote" {
		t.Fatalf("select use = %q", got)
	}
	proxies, _ := cfg["proxies"].([]any)
	if got := proxyMap(t, proxies, "US-01")["dialer-proxy"]; got != "local-hop" {
		t.Fatalf("US-01 dialer-proxy = %#v, want local-hop", got)
	}
	if _, ok := proxyMap(t, proxies, "local-hop")["dialer-proxy"]; ok {
		t.Fatalf("local-hop should start the chain without dialer-proxy")
	}
	providers, _ := cfg["proxy-providers"].(map[string]any)
	remote, _ := providers["remote"].(map[string]any)
	override, _ := remote["override"].(map[string]any)
	if override["dialer-proxy"] != "local-hop" || override["udp"] != true {
		t.Fatalf("provider override = %#v, want existing override plus dialer-proxy", override)
	}
}

func TestWriteRuntimeConfigGlobalChainSupportsSelectAsDialer(t *testing.T) {
	dir := t.TempDir()
	item := &Instance{
		ID:                "test",
		Name:              "Test",
		MixedPort:         28003,
		ControllerPort:    29003,
		Secret:            "secret-token",
		UserConfigPath:    filepath.Join(dir, "config.user.yaml"),
		RuntimeConfigPath: filepath.Join(dir, "config.runtime.yaml"),
		Mode:              InstanceModeGlobalChain,
		LocalProxies:      "- name: local-hop\n  type: http\n  server: 127.0.0.1\n  port: 7899\n",
		Chain:             []string{globalChainSelectGroupName, "local-hop"},
	}
	user := `proxies:
  - name: US-01
    type: ss
    server: example.com
    port: 443
rules:
  - MATCH,DIRECT
`
	if err := os.WriteFile(item.UserConfigPath, []byte(user), 0o644); err != nil {
		t.Fatal(err)
	}
	profile := &Profile{ID: "profile", Name: "Profile", ConfigPath: item.UserConfigPath}
	if _, err := writeRuntimeConfig(item, profile); err != nil {
		t.Fatal(err)
	}
	cfg := readRuntimeConfigMap(t, item.RuntimeConfigPath)
	rules, _ := cfg["rules"].([]any)
	if len(rules) != 2 || rules[0] != "NETWORK,UDP,REJECT" || rules[1] != "MATCH,local-hop" {
		t.Fatalf("rules = %#v, want UDP reject then MATCH local-hop", rules)
	}
	proxies, _ := cfg["proxies"].([]any)
	if got := proxyMap(t, proxies, "local-hop")["dialer-proxy"]; got != globalChainSelectGroupName {
		t.Fatalf("local-hop dialer-proxy = %#v, want %s", got, globalChainSelectGroupName)
	}
	group := cfg["proxy-groups"].([]any)[0].(map[string]any)
	if got := strings.Join(anyStrings(group["proxies"]), ","); got != "US-01" {
		t.Fatalf("select proxies = %q", got)
	}
}

func TestWriteRuntimeConfigGlobalChainBuildsMultiHopDialerProxy(t *testing.T) {
	dir := t.TempDir()
	item := &Instance{
		ID:                "test",
		Name:              "Test",
		MixedPort:         28003,
		ControllerPort:    29003,
		Secret:            "secret-token",
		UserConfigPath:    filepath.Join(dir, "config.user.yaml"),
		RuntimeConfigPath: filepath.Join(dir, "config.runtime.yaml"),
		Mode:              InstanceModeGlobalChain,
		LocalProxies: "- name: local-a\n  type: socks5\n  server: 127.0.0.1\n  port: 1080\n" +
			"- name: local-b\n  type: http\n  server: 127.0.0.1\n  port: 7899\n",
		Chain: []string{"local-a", "local-b", globalChainSelectGroupName},
	}
	user := `proxies:
  - name: US-01
    type: ss
    server: example.com
    port: 443
rules:
  - MATCH,DIRECT
`
	if err := os.WriteFile(item.UserConfigPath, []byte(user), 0o644); err != nil {
		t.Fatal(err)
	}
	profile := &Profile{ID: "profile", Name: "Profile", ConfigPath: item.UserConfigPath}
	if _, err := writeRuntimeConfig(item, profile); err != nil {
		t.Fatal(err)
	}
	cfg := readRuntimeConfigMap(t, item.RuntimeConfigPath)
	proxies, _ := cfg["proxies"].([]any)
	if got := proxyMap(t, proxies, "local-b")["dialer-proxy"]; got != "local-a" {
		t.Fatalf("local-b dialer-proxy = %#v, want local-a", got)
	}
	if got := proxyMap(t, proxies, "US-01")["dialer-proxy"]; got != "local-b" {
		t.Fatalf("US-01 dialer-proxy = %#v, want local-b", got)
	}
	if got := strings.Join(anyStrings(cfg["proxy-groups"].([]any)[0].(map[string]any)["proxies"]), ","); got != "US-01" {
		t.Fatalf("select proxies = %q", got)
	}
}

func TestWriteRuntimeConfigGlobalChainSingleSelectKeepsLocalProxySelectable(t *testing.T) {
	dir := t.TempDir()
	item := &Instance{
		ID:                "test",
		Name:              "Test",
		MixedPort:         28003,
		ControllerPort:    29003,
		Secret:            "secret-token",
		UserConfigPath:    filepath.Join(dir, "config.user.yaml"),
		RuntimeConfigPath: filepath.Join(dir, "config.runtime.yaml"),
		Mode:              InstanceModeGlobalChain,
		LocalProxies:      "- name: local-hop\n  type: socks5\n  server: 127.0.0.1\n  port: 1080\n",
		Chain:             []string{globalChainSelectGroupName},
	}
	if err := os.WriteFile(item.UserConfigPath, []byte("proxies: []\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	profile := &Profile{ID: "profile", Name: "Profile", ConfigPath: item.UserConfigPath}
	if _, err := writeRuntimeConfig(item, profile); err != nil {
		t.Fatal(err)
	}
	cfg := readRuntimeConfigMap(t, item.RuntimeConfigPath)
	group := cfg["proxy-groups"].([]any)[0].(map[string]any)
	if got := strings.Join(anyStrings(group["proxies"]), ","); got != "local-hop" {
		t.Fatalf("select proxies = %q", got)
	}
	proxies, _ := cfg["proxies"].([]any)
	if _, ok := proxyMap(t, proxies, "local-hop")["dialer-proxy"]; ok {
		t.Fatalf("local-hop should not get dialer-proxy for a single select chain")
	}
}

func TestWriteRuntimeConfigGlobalChainRejectsLocalProxyTopLevelMap(t *testing.T) {
	dir := t.TempDir()
	item := &Instance{
		ID:                "test",
		Name:              "Test",
		MixedPort:         28003,
		ControllerPort:    29003,
		Secret:            "secret-token",
		UserConfigPath:    filepath.Join(dir, "config.user.yaml"),
		RuntimeConfigPath: filepath.Join(dir, "config.runtime.yaml"),
		Mode:              InstanceModeGlobalChain,
		LocalProxies:      "mixed-port: 9999\n",
	}
	if err := os.WriteFile(item.UserConfigPath, []byte(subscriptionConfig), 0o644); err != nil {
		t.Fatal(err)
	}
	profile := &Profile{ID: "profile", Name: "Profile", ConfigPath: item.UserConfigPath}
	if _, err := writeRuntimeConfig(item, profile); err == nil || !strings.Contains(err.Error(), "parse local proxies") {
		t.Fatalf("writeRuntimeConfig() error = %v, want parse local proxies", err)
	}
}

func TestWriteRuntimeConfigGlobalChainRejectsInvalidChainInputs(t *testing.T) {
	tests := []struct {
		name         string
		localProxies string
		chain        []string
		want         string
	}{
		{
			name:         "unknown chain member",
			localProxies: "- name: local-hop\n  type: socks5\n  server: 127.0.0.1\n  port: 1080\n",
			chain:        []string{"missing", globalChainSelectGroupName},
			want:         "chain references unknown proxy or group",
		},
		{
			name:         "builtin direct is not a chain member",
			localProxies: "- name: local-hop\n  type: socks5\n  server: 127.0.0.1\n  port: 1080\n",
			chain:        []string{"DIRECT", globalChainSelectGroupName},
			want:         "chain references unknown proxy or group",
		},
		{
			name:         "duplicate chain member",
			localProxies: "- name: local-hop\n  type: socks5\n  server: 127.0.0.1\n  port: 1080\n",
			chain:        []string{"local-hop", "local-hop", globalChainSelectGroupName},
			want:         "chain contains duplicate member",
		},
		{
			name:         "local conflicts with profile proxy",
			localProxies: "- name: US-01\n  type: socks5\n  server: 127.0.0.1\n  port: 1080\n",
			chain:        []string{"US-01"},
			want:         "conflicts with profile proxy",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			item := &Instance{
				ID:                "test",
				Name:              "Test",
				MixedPort:         28003,
				ControllerPort:    29003,
				Secret:            "secret-token",
				UserConfigPath:    filepath.Join(dir, "config.user.yaml"),
				RuntimeConfigPath: filepath.Join(dir, "config.runtime.yaml"),
				Mode:              InstanceModeGlobalChain,
				LocalProxies:      tt.localProxies,
				Chain:             tt.chain,
			}
			if err := os.WriteFile(item.UserConfigPath, []byte(subscriptionConfig), 0o644); err != nil {
				t.Fatal(err)
			}
			profile := &Profile{ID: "profile", Name: "Profile", ConfigPath: item.UserConfigPath}
			if _, err := writeRuntimeConfig(item, profile); err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("writeRuntimeConfig() error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestStoreSharesProfileAndPersistsPerInstanceSelection(t *testing.T) {
	withPortFree(t, func(int) bool { return true })

	dir := t.TempDir()
	store, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	profile, err := store.CreateProfile("Main subscription", defaultUserConfig)
	if err != nil {
		t.Fatal(err)
	}
	first, err := store.Create("HK", profile.ID, "", 28011, 29011)
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.Create("JP", profile.ID, "", 28012, 29012)
	if err != nil {
		t.Fatal(err)
	}
	if first.UserConfigPath != second.UserConfigPath {
		t.Fatalf("expected shared profile config, got %q and %q", first.UserConfigPath, second.UserConfigPath)
	}
	if _, err := store.SetSelection(first.ID, "Proxy", "HK-01"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SetSelection(second.ID, "Proxy", "JP-01"); err != nil {
		t.Fatal(err)
	}

	reloaded, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	gotFirst, _ := reloaded.Get(first.ID)
	gotSecond, _ := reloaded.Get(second.ID)
	if gotFirst.SelectedProxy != "HK-01" || gotSecond.SelectedProxy != "JP-01" {
		t.Fatalf("selection was not stored per instance: %q %q", gotFirst.SelectedProxy, gotSecond.SelectedProxy)
	}
}

func TestStorePersistsGlobalChainFieldsAndCloneCopiesThem(t *testing.T) {
	withPortFree(t, func(int) bool { return true })

	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	profile, err := store.CreateProfile("Main", subscriptionConfig)
	if err != nil {
		t.Fatal(err)
	}
	local := "- name: local-hop\n  type: socks5\n  server: 127.0.0.1\n  port: 1080\n"
	item, err := store.CreateWithOptions(createInstanceOptions{
		Name:           "Chain",
		ProfileID:      profile.ID,
		MixedPort:      28030,
		ProxyBind:      "127.0.0.1,192.168.64.1",
		ControllerPort: 29030,
		Mode:           InstanceModeGlobalChain,
		LocalProxies:   local,
		Chain:          []string{"local-hop", globalChainSelectGroupName},
	})
	if err != nil {
		t.Fatal(err)
	}
	reloaded, err := NewStore(store.dataDir)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := reloaded.Get(item.ID)
	if !ok {
		t.Fatal("expected reloaded instance")
	}
	if got.Mode != InstanceModeGlobalChain || got.ProxyBind != "127.0.0.1,192.168.64.1" || got.LocalProxies != local || strings.Join(got.Chain, ",") != "local-hop,"+globalChainSelectGroupName {
		t.Fatalf("global-chain fields were not persisted: %+v", got)
	}
	clone, err := reloaded.Clone(item.ID, "", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if clone.Mode != item.Mode || clone.ProxyBind != item.ProxyBind || clone.LocalProxies != local || strings.Join(clone.Chain, ",") != "local-hop,"+globalChainSelectGroupName {
		t.Fatalf("clone lost global-chain fields: %+v", clone)
	}
}

func TestStoreSuggestPortsUsesNextAvailableRange(t *testing.T) {
	withPortFree(t, func(port int) bool { return port != 28003 && port != 29003 })

	dir := t.TempDir()
	store, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Create("US", "", defaultUserConfig, 28001, 29001); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Create("UK", "", defaultUserConfig, 28002, 29002); err != nil {
		t.Fatal(err)
	}

	mixed, controller := store.SuggestPorts()
	if mixed != 28004 || controller != 29004 {
		t.Fatalf("SuggestPorts() = (%d, %d), want (28004, 29004)", mixed, controller)
	}
}

func TestStoreSuggestPortsIgnoresPortsNearUpperLimit(t *testing.T) {
	checked := make(map[int]bool)
	withPortFree(t, func(port int) bool {
		if port > 65535 {
			t.Fatalf("checked invalid port %d", port)
		}
		checked[port] = true
		return port != 28001 && port != 29001
	})

	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Create("High", "", defaultUserConfig, 65000, 65001); err != nil {
		t.Fatal(err)
	}

	mixed, controller := store.SuggestPorts()
	if mixed != 28000 || controller != 29000 {
		t.Fatalf("SuggestPorts() = (%d, %d), want (28000, 29000)", mixed, controller)
	}
	if checked[65002] {
		t.Fatal("expected high port range to be ignored for suggestions")
	}
}

func TestStoreSuggestPortsReturnsDistinctPortsWhenMixedRangeUnavailable(t *testing.T) {
	withPortFree(t, func(port int) bool { return port >= 29000 })

	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	mixed, controller := store.SuggestPorts()
	if mixed == controller {
		t.Fatalf("SuggestPorts() returned duplicate ports: %d", mixed)
	}
	if mixed != 29000 || controller != 29001 {
		t.Fatalf("SuggestPorts() = (%d, %d), want (29000, 29001)", mixed, controller)
	}
}

func TestStoreCloneCopiesProfileSelectionAndAllocatesNextPorts(t *testing.T) {
	withPortFree(t, func(int) bool { return true })

	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	profile, err := store.CreateProfile("Main", defaultUserConfig)
	if err != nil {
		t.Fatal(err)
	}
	source, err := store.Create("US", profile.ID, "", 28010, 29010)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.SetSelection(source.ID, "Proxy", "US-01"); err != nil {
		t.Fatal(err)
	}

	clone, err := store.Clone(source.ID, "", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if clone.ID == source.ID || clone.Secret == source.Secret {
		t.Fatal("clone should have a distinct id and secret")
	}
	if clone.ProfileID != source.ProfileID || clone.UserConfigPath != source.UserConfigPath {
		t.Fatalf("clone profile = %q/%q, want shared %q/%q", clone.ProfileID, clone.UserConfigPath, source.ProfileID, source.UserConfigPath)
	}
	if clone.RuntimeConfigPath == source.RuntimeConfigPath {
		t.Fatal("clone should have a distinct runtime config path")
	}
	if clone.MixedPort != 28011 || clone.ControllerPort != 29011 {
		t.Fatalf("clone ports = (%d, %d), want (28011, 29011)", clone.MixedPort, clone.ControllerPort)
	}
	if clone.SelectedGroup != "Proxy" || clone.SelectedProxy != "US-01" || clone.SelectedProxies["Proxy"] != "US-01" {
		t.Fatalf("clone selections = %#v/%q/%q, want copied selection", clone.SelectedProxies, clone.SelectedGroup, clone.SelectedProxy)
	}
}

func TestStoreCloneRejectsExplicitPortConflict(t *testing.T) {
	withPortFree(t, func(int) bool { return true })

	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	source, err := store.Create("US", "", defaultUserConfig, 28010, 29010)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Clone(source.ID, "US conflict", 28010, 29011); err == nil {
		t.Fatal("expected clone with conflicting mixed port to fail")
	}
}

func TestStoreCloneReportsTypedMissingSource(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Clone("missing", "", 0, 0); !errors.Is(err, errInstanceNotFound) {
		t.Fatalf("Clone() error = %v, want errInstanceNotFound", err)
	}
}

func TestStoreCloneRejectsSameMixedAndControllerPort(t *testing.T) {
	withPortFree(t, func(int) bool { return true })

	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	source, err := store.Create("US", "", defaultUserConfig, 28010, 29010)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Clone(source.ID, "US same port", 28011, 28011); err == nil || !errors.Is(err, errValidation) {
		t.Fatalf("Clone() error = %v, want errValidation", err)
	}
}

func TestStoreRejectsSameMixedAndControllerPort(t *testing.T) {
	withPortFree(t, func(int) bool { return true })

	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Create("Bad", "", defaultUserConfig, 28001, 28001); err == nil {
		t.Fatal("expected same mixed and controller port to be rejected")
	}
}

func TestStoreUpdateRejectsSameMixedAndControllerPort(t *testing.T) {
	withPortFree(t, func(int) bool { return true })

	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	item, err := store.Create("Ports", "", defaultUserConfig, 28001, 29001)
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.Update(item.ID, "", "", "", 0, 28001)
	if err == nil {
		t.Fatal("expected controller port matching existing mixed port to be rejected")
	}
	// H1 (REVIEW-2026-07-04.md): equal mixed/controller ports on update must
	// classify as errValidation (-> HTTP 400), the same as the create path,
	// not errPortUnavailable (-> HTTP 409). Also pin the exact message text,
	// since app.js's errorLabels dictionary matches it verbatim.
	if !errors.Is(err, errValidation) {
		t.Fatalf("Update() error = %v, want errValidation", err)
	}
	if errors.Is(err, errPortUnavailable) {
		t.Fatalf("Update() error = %v, should not also classify as errPortUnavailable", err)
	}
	if err.Error() != "mixed and controller ports must differ" {
		t.Fatalf("Update() error text = %q, want %q", err.Error(), "mixed and controller ports must differ")
	}
	got, ok := store.Get(item.ID)
	if !ok {
		t.Fatal("expected instance to remain")
	}
	if got.MixedPort != 28001 || got.ControllerPort != 29001 {
		t.Fatalf("ports changed after failed update: mixed=%d controller=%d", got.MixedPort, got.ControllerPort)
	}
}

func TestStorePatchProfileRenamePersistsAcrossReload(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	profile, err := store.CreateProfile("Original", defaultUserConfig)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.PatchProfile(profile.ID, ProfilePatch{Name: "Renamed"}); err != nil {
		t.Fatal(err)
	}

	reloaded, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := reloaded.GetProfile(profile.ID)
	if !ok || got.Name != "Renamed" {
		t.Fatalf("profile rename did not persist across reload: %+v", got)
	}
}

func TestStorePatchProfileChangingURLClearsSubscriptionMetadata(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	fetched := &subscriptionFetchResult{
		Config:  subscriptionConfig,
		HomeURL: "https://example.com/home",
		Info:    &SubscriptionInfo{Total: 100},
	}
	profile, err := store.CreateSubscriptionProfile("Provider", "https://example.com/sub", true, 360, fetched)
	if err != nil {
		t.Fatal(err)
	}
	if profile.LastUpdatedAt.IsZero() || profile.HomeURL == "" || profile.SubscriptionInfo == nil {
		t.Fatalf("expected subscription metadata to be populated before the URL change: %+v", profile)
	}

	newURL := "https://example.com/sub2"
	updated, err := store.PatchProfile(profile.ID, ProfilePatch{SubscriptionURL: &newURL})
	if err != nil {
		t.Fatal(err)
	}
	if !updated.LastUpdatedAt.IsZero() || updated.HomeURL != "" || updated.SubscriptionInfo != nil {
		t.Fatalf("expected LastUpdatedAt/HomeURL/SubscriptionInfo to be cleared after URL change: %+v", updated)
	}
}

func TestStorePatchProfileClearingURLDisablesAutoUpdate(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	profile, err := store.CreateSubscriptionProfile("Provider", "https://example.com/sub", true, 360, &subscriptionFetchResult{Config: subscriptionConfig})
	if err != nil {
		t.Fatal(err)
	}
	if !profile.AutoUpdate || profile.UpdateIntervalMinutes == 0 {
		t.Fatalf("expected auto update to start enabled: %+v", profile)
	}

	empty := ""
	updated, err := store.PatchProfile(profile.ID, ProfilePatch{SubscriptionURL: &empty})
	if err != nil {
		t.Fatal(err)
	}
	if updated.SubscriptionURL != "" || updated.AutoUpdate || updated.UpdateIntervalMinutes != 0 {
		t.Fatalf("expected clearing the subscription URL to disable auto update: %+v", updated)
	}
}

func TestStoreUpdateWithOptionsSwitchingProfileClearsSelection(t *testing.T) {
	withPortFree(t, func(int) bool { return true })

	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	profileA, err := store.CreateProfile("A", subscriptionConfig)
	if err != nil {
		t.Fatal(err)
	}
	profileB, err := store.CreateProfile("B", subscriptionConfig)
	if err != nil {
		t.Fatal(err)
	}
	item, err := store.Create("Test", profileA.ID, "", 28040, 29040)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.SetSelection(item.ID, "Proxy", "US-01"); err != nil {
		t.Fatal(err)
	}

	updated, err := store.UpdateWithOptions(item.ID, updateInstanceOptions{ProfileID: profileB.ID})
	if err != nil {
		t.Fatal(err)
	}
	if updated.ProfileID != profileB.ID {
		t.Fatalf("ProfileID = %q, want %q", updated.ProfileID, profileB.ID)
	}
	if updated.SelectedGroup != "" || updated.SelectedProxy != "" || len(updated.SelectedProxies) != 0 {
		t.Fatalf("expected selection to be cleared after switching profile: %+v", updated)
	}
}

func TestStoreUpdateWithOptionsConfigRewritesProfileFile(t *testing.T) {
	withPortFree(t, func(int) bool { return true })

	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	profile, err := store.CreateProfile("Main", defaultUserConfig)
	if err != nil {
		t.Fatal(err)
	}
	item, err := store.Create("Test", profile.ID, "", 28041, 29041)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := store.UpdateWithOptions(item.ID, updateInstanceOptions{Config: subscriptionConfig}); err != nil {
		t.Fatal(err)
	}

	raw, err := os.ReadFile(profile.ConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != subscriptionConfig {
		t.Fatalf("profile config file = %q, want %q", raw, subscriptionConfig)
	}
}

func TestStoreDeleteRemovesInstanceAndDirectoryAndPersists(t *testing.T) {
	withPortFree(t, func(int) bool { return true })

	dir := t.TempDir()
	store, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	item, err := store.Create("Test", "", defaultUserConfig, 28050, 29050)
	if err != nil {
		t.Fatal(err)
	}
	instanceDir := filepath.Join(dir, "instances", item.ID)
	if _, err := os.Stat(instanceDir); err != nil {
		t.Fatalf("expected instance directory to exist before delete: %v", err)
	}

	if err := store.Delete(item.ID); err != nil {
		t.Fatal(err)
	}
	if _, ok := store.Get(item.ID); ok {
		t.Fatal("expected instance record to be removed")
	}
	if _, err := os.Stat(instanceDir); !os.IsNotExist(err) {
		t.Fatalf("expected instance directory to be removed, stat err = %v", err)
	}

	reloaded, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := reloaded.Get(item.ID); ok {
		t.Fatal("deleted instance should not reappear after reload")
	}
}

func TestStoreProfileProxyGroupsCacheInvalidatesOnConfigRewrite(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	profile, err := store.CreateProfile("Main", subscriptionConfig)
	if err != nil {
		t.Fatal(err)
	}

	first, err := store.ProfileProxyGroups(profile.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 1 || strings.Join(first[0].All, ",") != "US-01,JP-01,DIRECT" {
		t.Fatalf("unexpected initial groups: %+v", first)
	}

	nextConfig := strings.Replace(subscriptionConfig, "      - JP-01\n", "", 1)
	if _, err := store.PatchProfile(profile.ID, ProfilePatch{Config: &nextConfig}); err != nil {
		t.Fatal(err)
	}

	second, err := store.ProfileProxyGroups(profile.ID)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(second[0].All, ",") != "US-01,DIRECT" {
		t.Fatalf("cache was not invalidated after config rewrite: %+v", second)
	}
}

// breakStoreSaves makes every future saveLocked() call on a store rooted at
// dataDir fail: writeFileAtomic's os.CreateTemp(dataDir, ...) needs write
// permission on dataDir itself (instances.json lives directly inside it),
// so dropping the write bit is enough to force a write failure without
// touching any file. Subdirectories (instances/<id>, profiles/<id>) keep
// their own 0700 permissions and stay writable, so profile/instance config
// file writes that happen before saveLocked in these tests still succeed --
// only the final saveLocked persistence step fails, which is what exercises
// the rollback branches (H3: PatchProfile, ApplySubscriptionFetchForURL,
// UpdateWithOptions, Delete). Root ignores permission bits entirely, so
// these tests skip under root.
func breakStoreSaves(t *testing.T, dataDir string) {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("running as root: permission bits do not block writes, cannot force a saveLocked failure this way")
	}
	if err := os.Chmod(dataDir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = os.Chmod(dataDir, 0o700)
	})
}

// TestStorePatchProfileRollsBackOnSaveFailure covers the H3 gap: when
// saveLocked fails after PatchProfile has already applied the rename and
// rewritten the profile config file, store.go's rollback branch must put
// both back exactly as they were (see store.go's PatchProfile, the
// `*profile = *original` / writeFileAtomic(previousConfig) block).
func TestStorePatchProfileRollsBackOnSaveFailure(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	profile, err := store.CreateProfile("Original", defaultUserConfig)
	if err != nil {
		t.Fatal(err)
	}

	breakStoreSaves(t, dir)

	newConfig := subscriptionConfig
	if _, err := store.PatchProfile(profile.ID, ProfilePatch{Name: "Renamed", Config: &newConfig}); err == nil {
		t.Fatal("expected PatchProfile to fail when saveLocked cannot persist instances.json")
	}

	got, ok := store.GetProfile(profile.ID)
	if !ok {
		t.Fatal("expected profile to still exist after failed patch")
	}
	if got.Name != "Original" {
		t.Fatalf("profile name = %q, want rollback to %q", got.Name, "Original")
	}
	raw, err := os.ReadFile(profile.ConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != defaultUserConfig {
		t.Fatalf("profile config file was not rolled back: got %q", raw)
	}
}

// TestStoreApplySubscriptionFetchForURLRollsBackOnSaveFailure covers the H3
// gap for ApplySubscriptionFetchForURL's rollback: on saveLocked failure it
// must restore the profile metadata, the rewritten config file, and any
// instance selections it reconciled against the new proxy groups.
func TestStoreApplySubscriptionFetchForURLRollsBackOnSaveFailure(t *testing.T) {
	withPortFree(t, func(int) bool { return true })

	dir := t.TempDir()
	store, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	fetched := &subscriptionFetchResult{
		Config:  subscriptionConfig,
		HomeURL: "https://example.com/home",
		Info:    &SubscriptionInfo{Total: 100},
	}
	profile, err := store.CreateSubscriptionProfile("Provider", "https://example.com/sub", true, 360, fetched)
	if err != nil {
		t.Fatal(err)
	}
	originalConfig, err := os.ReadFile(profile.ConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	originalHomeURL := profile.HomeURL

	item, err := store.Create("Test", profile.ID, "", 28070, 29070)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.SetSelection(item.ID, "Proxy", "US-01"); err != nil {
		t.Fatal(err)
	}

	breakStoreSaves(t, dir)

	nextFetched := &subscriptionFetchResult{Config: defaultUserConfig, HomeURL: "https://example.com/new-home"}
	if _, err := store.ApplySubscriptionFetchForURL(profile.ID, "https://example.com/sub", nextFetched); err == nil {
		t.Fatal("expected ApplySubscriptionFetchForURL to fail when saveLocked cannot persist instances.json")
	}

	gotProfile, ok := store.GetProfile(profile.ID)
	if !ok {
		t.Fatal("expected profile to still exist after failed subscription apply")
	}
	if gotProfile.HomeURL != originalHomeURL {
		t.Fatalf("profile HomeURL = %q, want rollback to %q", gotProfile.HomeURL, originalHomeURL)
	}
	raw, err := os.ReadFile(profile.ConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != string(originalConfig) {
		t.Fatalf("profile config file was not rolled back: got %q", raw)
	}
	gotItem, ok := store.Get(item.ID)
	if !ok {
		t.Fatal("expected instance to remain")
	}
	if gotItem.SelectedProxies["Proxy"] != "US-01" || gotItem.SelectedGroup != "Proxy" || gotItem.SelectedProxy != "US-01" {
		t.Fatalf("instance selection was not rolled back: %#v", gotItem.SelectedProxies)
	}
}

// TestStoreUpdateWithOptionsRollsBackOnSaveFailure covers the H3 gap for
// UpdateWithOptions' rollback: on saveLocked failure both the in-memory
// instance fields and the rewritten profile config file must be restored.
func TestStoreUpdateWithOptionsRollsBackOnSaveFailure(t *testing.T) {
	withPortFree(t, func(int) bool { return true })

	dir := t.TempDir()
	store, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	profile, err := store.CreateProfile("Main", defaultUserConfig)
	if err != nil {
		t.Fatal(err)
	}
	item, err := store.Create("Test", profile.ID, "", 28071, 29071)
	if err != nil {
		t.Fatal(err)
	}
	originalName := item.Name

	breakStoreSaves(t, dir)

	if _, err := store.UpdateWithOptions(item.ID, updateInstanceOptions{Name: "Renamed", Config: subscriptionConfig}); err == nil {
		t.Fatal("expected UpdateWithOptions to fail when saveLocked cannot persist instances.json")
	}

	got, ok := store.Get(item.ID)
	if !ok {
		t.Fatal("expected instance to remain after failed update")
	}
	if got.Name != originalName {
		t.Fatalf("instance name = %q, want rollback to %q", got.Name, originalName)
	}
	raw, err := os.ReadFile(profile.ConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != defaultUserConfig {
		t.Fatalf("profile config file was not rolled back: got %q", raw)
	}
}

// TestStoreDeleteRestoresItemOnSaveFailure covers the H3 gap for Delete's
// rollback: on saveLocked failure the in-memory record must be put back
// (and, since RemoveAll only runs after a successful save, the instance
// directory is never touched).
func TestStoreDeleteRestoresItemOnSaveFailure(t *testing.T) {
	withPortFree(t, func(int) bool { return true })

	dir := t.TempDir()
	store, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	item, err := store.Create("Test", "", defaultUserConfig, 28072, 29072)
	if err != nil {
		t.Fatal(err)
	}

	breakStoreSaves(t, dir)

	if err := store.Delete(item.ID); err == nil {
		t.Fatal("expected Delete to fail when saveLocked cannot persist instances.json")
	}

	got, ok := store.Get(item.ID)
	if !ok {
		t.Fatal("expected instance record to remain after failed delete")
	}
	if got.ID != item.ID || got.Name != item.Name {
		t.Fatalf("instance record = %#v, want unchanged record for %q", got, item.ID)
	}
	instanceDir := filepath.Join(dir, "instances", item.ID)
	if _, err := os.Stat(instanceDir); err != nil {
		t.Fatalf("expected instance directory to survive an aborted delete: %v", err)
	}
}

// withPortFree replaces the package-level isPortFree stub (util.go) for the
// duration of t's run, guarded by portFreeTestMu so tests that use it
// serialize against each other instead of racing on the shared package
// variable.
//
// Do NOT combine with t.Parallel() (testing L4,
// docs/review-2026-07-11-testing-quality.md): portFreeTestMu only
// serializes tests that themselves call withPortFree against each other. A
// parallel test that does NOT call withPortFree could still be scheduled
// concurrently with one that does and observe the stubbed isPortFree
// instead of the real port-probing implementation, since nothing else
// prevents the two from interleaving. None of this package's tests
// currently use t.Parallel, so this has not surfaced in practice -- but
// adding it to a withPortFree-using test (or a test that runs concurrently
// with one) would be a silently flaky trap. A more robust long-term fix
// would thread isPortFree through Store as an injected field instead of a
// package-level var.
func withPortFree(t *testing.T, fn func(int) bool) {
	t.Helper()
	portFreeTestMu.Lock()
	original := isPortFree
	isPortFree = func(port int) bool {
		if port < 1 || port > 65535 {
			return false
		}
		return fn(port)
	}
	t.Cleanup(func() {
		isPortFree = original
		portFreeTestMu.Unlock()
	})
}

func anyStrings(value any) []string {
	items, _ := value.([]any)
	out := make([]string, 0, len(items))
	for _, item := range items {
		if text, ok := item.(string); ok {
			out = append(out, text)
		}
	}
	return out
}

func readRuntimeConfigMap(t *testing.T, path string) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var cfg map[string]any
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	return cfg
}

func proxyMap(t *testing.T, proxies []any, name string) map[string]any {
	t.Helper()
	for _, item := range proxies {
		proxy, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if proxy["name"] == name {
			return proxy
		}
	}
	t.Fatalf("proxy %q not found in %#v", name, proxies)
	return nil
}

// TestCloneInstanceDeepCopiesReferenceFields covers testing M5:
// cloneInstance must never let a caller mutate through to another
// Instance's slice/map/pointer fields. Rather than special-case
// Chain/SelectedProxies by name, this walks Instance's fields via
// reflection so a future field addition of slice/map/pointer kind is
// automatically checked for aliasing (as long as the test below populates
// it with a non-nil value).
func TestCloneInstanceDeepCopiesReferenceFields(t *testing.T) {
	original := &Instance{
		ID:              "id",
		Name:            "name",
		ProfileID:       "profile",
		ProxyBind:       "127.0.0.1",
		Chain:           []string{"a", "b"},
		SelectedProxies: map[string]string{"g": "p"},
	}
	clone := cloneInstance(original)
	if clone == original {
		t.Fatal("cloneInstance returned the same pointer as its input")
	}

	typ := reflect.TypeOf(*original)
	origVal := reflect.ValueOf(original).Elem()
	cloneVal := reflect.ValueOf(clone).Elem()
	for i := 0; i < typ.NumField(); i++ {
		field := typ.Field(i)
		switch field.Type.Kind() {
		case reflect.Slice, reflect.Map, reflect.Ptr:
			of := origVal.Field(i)
			cf := cloneVal.Field(i)
			if of.IsNil() {
				continue
			}
			if of.Pointer() == cf.Pointer() {
				t.Fatalf("field %s was not deep-copied: clone shares underlying storage with the original", field.Name)
			}
		}
	}

	// Belt-and-suspenders: mutate through the original's reference fields
	// and confirm the clone (and vice versa) is unaffected.
	original.Chain[0] = "mutated"
	original.SelectedProxies["g"] = "mutated"
	if clone.Chain[0] == "mutated" {
		t.Fatal("cloneInstance aliased Chain with the original")
	}
	if clone.SelectedProxies["g"] == "mutated" {
		t.Fatal("cloneInstance aliased SelectedProxies with the original")
	}
}

// TestStoreDeleteProfileRemovesUnusedProfileAndPersists covers the M2 happy
// path: an unreferenced profile's record and on-disk config directory are
// both removed, and the removal survives a reload.
func TestStoreDeleteProfileRemovesUnusedProfileAndPersists(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	profile, err := store.CreateProfile("Unused", defaultUserConfig)
	if err != nil {
		t.Fatal(err)
	}
	profileDir := filepath.Dir(profile.ConfigPath)
	if _, err := os.Stat(profileDir); err != nil {
		t.Fatalf("expected profile directory to exist before delete: %v", err)
	}

	if err := store.DeleteProfile(profile.ID); err != nil {
		t.Fatal(err)
	}
	if _, ok := store.GetProfile(profile.ID); ok {
		t.Fatal("expected profile record to be removed")
	}
	if _, err := os.Stat(profileDir); !os.IsNotExist(err) {
		t.Fatalf("expected profile directory to be removed, stat err = %v", err)
	}

	reloaded, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := reloaded.GetProfile(profile.ID); ok {
		t.Fatal("deleted profile should not reappear after reload")
	}
}

// TestStoreDeleteProfileRejectsWhenReferencedByInstance covers M2: deleting
// a profile still referenced by an instance must fail with the exact
// validationError message app.js's errorLabels matches verbatim, and must
// classify as errValidation.
func TestStoreDeleteProfileRejectsWhenReferencedByInstance(t *testing.T) {
	withPortFree(t, func(int) bool { return true })

	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	profile, err := store.CreateProfile("In use", defaultUserConfig)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Create("Test", profile.ID, "", 28080, 29080); err != nil {
		t.Fatal(err)
	}

	err = store.DeleteProfile(profile.ID)
	if err == nil {
		t.Fatal("expected DeleteProfile to reject a profile referenced by an instance")
	}
	if !errors.Is(err, errValidation) {
		t.Fatalf("err = %v, want errValidation", err)
	}
	if err.Error() != "profile is in use by existing instances" {
		t.Fatalf("err.Error() = %q, want exact message %q", err.Error(), "profile is in use by existing instances")
	}
	if _, ok := store.GetProfile(profile.ID); !ok {
		t.Fatal("profile should remain after a rejected delete")
	}
}

// TestStoreDeleteProfileUnknownReturnsNotFoundError covers M2: deleting a
// nonexistent profile ID classifies as errProfileNotFound.
func TestStoreDeleteProfileUnknownReturnsNotFoundError(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	err = store.DeleteProfile("missing")
	if !errors.Is(err, errProfileNotFound) {
		t.Fatalf("err = %v, want errProfileNotFound", err)
	}
}

// TestStoreReplaceProfileSubscriptionPersistsURLAndConfig covers arch M4's
// happy path: URL and fetched config are both applied by a single call.
func TestStoreReplaceProfileSubscriptionPersistsURLAndConfig(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	profile, err := store.CreateSubscriptionProfile("Provider", "https://example.com/old", true, 360, &subscriptionFetchResult{Config: defaultUserConfig})
	if err != nil {
		t.Fatal(err)
	}

	fetched := &subscriptionFetchResult{
		Config:  subscriptionConfig,
		HomeURL: "https://example.com/new-home",
		Info:    &SubscriptionInfo{Total: 500},
	}
	updated, err := store.ReplaceProfileSubscription(profile.ID, "https://example.com/new", fetched)
	if err != nil {
		t.Fatal(err)
	}
	if updated.SubscriptionURL != "https://example.com/new" {
		t.Fatalf("SubscriptionURL = %q, want the new URL", updated.SubscriptionURL)
	}
	if updated.HomeURL != "https://example.com/new-home" {
		t.Fatalf("HomeURL = %q, want the new home URL", updated.HomeURL)
	}
	if updated.SubscriptionInfo == nil || updated.SubscriptionInfo.Total != 500 {
		t.Fatalf("SubscriptionInfo = %+v, want Total 500", updated.SubscriptionInfo)
	}
	if updated.LastUpdatedAt.IsZero() {
		t.Fatal("expected LastUpdatedAt to be set")
	}
	raw, err := os.ReadFile(profile.ConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != subscriptionConfig {
		t.Fatalf("profile config file = %q, want the fetched config", raw)
	}

	reloaded, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	reloadedProfile, ok := reloaded.GetProfile(profile.ID)
	if !ok {
		t.Fatal("expected profile to persist across reload")
	}
	if reloadedProfile.SubscriptionURL != "https://example.com/new" {
		t.Fatalf("reloaded SubscriptionURL = %q, want the new URL", reloadedProfile.SubscriptionURL)
	}
}

// TestStoreReplaceProfileSubscriptionRollsBackOnSaveFailure covers arch M4's
// core guarantee: if saveLocked fails after the new URL and config have
// already been applied in memory/on disk, both must be rolled back together
// so the URL and config.yaml never diverge.
func TestStoreReplaceProfileSubscriptionRollsBackOnSaveFailure(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	profile, err := store.CreateSubscriptionProfile("Provider", "https://example.com/old", true, 360, &subscriptionFetchResult{Config: defaultUserConfig})
	if err != nil {
		t.Fatal(err)
	}
	originalConfig, err := os.ReadFile(profile.ConfigPath)
	if err != nil {
		t.Fatal(err)
	}

	breakStoreSaves(t, dir)

	fetched := &subscriptionFetchResult{Config: subscriptionConfig, HomeURL: "https://example.com/new-home"}
	if _, err := store.ReplaceProfileSubscription(profile.ID, "https://example.com/new", fetched); err == nil {
		t.Fatal("expected ReplaceProfileSubscription to fail when saveLocked cannot persist instances.json")
	}

	gotProfile, ok := store.GetProfile(profile.ID)
	if !ok {
		t.Fatal("expected profile to still exist after failed replace")
	}
	if gotProfile.SubscriptionURL != "https://example.com/old" {
		t.Fatalf("SubscriptionURL = %q, want rollback to the original URL", gotProfile.SubscriptionURL)
	}
	if gotProfile.HomeURL != "" {
		t.Fatalf("HomeURL = %q, want rollback to empty", gotProfile.HomeURL)
	}
	raw, err := os.ReadFile(profile.ConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != string(originalConfig) {
		t.Fatalf("profile config file was not rolled back: got %q", raw)
	}
}

// TestStoreCreateRejectsInvalidChainAtCreateTime covers arch L6: a
// duplicate or unresolvable chain member is rejected at create time (400)
// instead of only surfacing when the instance is later started.
func TestStoreCreateRejectsInvalidChainAtCreateTime(t *testing.T) {
	withPortFree(t, func(int) bool { return true })

	tests := []struct {
		name  string
		chain []string
		want  string
	}{
		{"unknown member", []string{"missing", globalChainSelectGroupName}, `chain references unknown proxy or group "missing"`},
		{"duplicate member", []string{"US-01", "US-01", globalChainSelectGroupName}, `chain contains duplicate member "US-01"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store, err := NewStore(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			profile, err := store.CreateProfile("Main", subscriptionConfig)
			if err != nil {
				t.Fatal(err)
			}
			_, err = store.CreateWithOptions(createInstanceOptions{
				Name:           "Chain gateway",
				ProfileID:      profile.ID,
				MixedPort:      28090,
				ControllerPort: 29090,
				Mode:           InstanceModeGlobalChain,
				Chain:          tt.chain,
			})
			if err == nil {
				t.Fatal("expected CreateWithOptions to reject the invalid chain")
			}
			if !errors.Is(err, errValidation) {
				t.Fatalf("err = %v, want errValidation", err)
			}
			if err.Error() != tt.want {
				t.Fatalf("err.Error() = %q, want %q", err.Error(), tt.want)
			}
			if _, ok := store.Get("chain-gateway"); ok {
				t.Fatal("instance should not have been created")
			}
		})
	}
}

// TestStoreUpdateRejectsInvalidChainAtUpdateTime mirrors the create-time
// coverage above for UpdateWithOptions.
func TestStoreUpdateRejectsInvalidChainAtUpdateTime(t *testing.T) {
	withPortFree(t, func(int) bool { return true })

	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	profile, err := store.CreateProfile("Main", subscriptionConfig)
	if err != nil {
		t.Fatal(err)
	}
	item, err := store.CreateWithOptions(createInstanceOptions{
		Name:           "Chain gateway",
		ProfileID:      profile.ID,
		MixedPort:      28091,
		ControllerPort: 29091,
		Mode:           InstanceModeGlobalChain,
	})
	if err != nil {
		t.Fatal(err)
	}

	badChain := []string{"missing", globalChainSelectGroupName}
	_, err = store.UpdateWithOptions(item.ID, updateInstanceOptions{Chain: &badChain})
	if err == nil {
		t.Fatal("expected UpdateWithOptions to reject the invalid chain")
	}
	if !errors.Is(err, errValidation) {
		t.Fatalf("err = %v, want errValidation", err)
	}
	if err.Error() != `chain references unknown proxy or group "missing"` {
		t.Fatalf("err.Error() = %q", err.Error())
	}
	got, ok := store.Get(item.ID)
	if !ok {
		t.Fatal("instance should still exist")
	}
	if len(got.Chain) != 0 {
		t.Fatalf("Chain = %+v, want unchanged (empty) after rejected update", got.Chain)
	}
}

// TestStoreCreateAutoMixedPortAvoidsExplicitControllerPortConflict covers
// arch L5: when the controller port is explicit and the mixed port is
// auto-allocated, the allocator must not hand back the explicit controller
// port as the mixed port (which would then make the controller port's own
// availability check see it as already used and falsely report a
// conflict).
func TestStoreCreateAutoMixedPortAvoidsExplicitControllerPortConflict(t *testing.T) {
	withPortFree(t, func(int) bool { return true })

	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	item, err := store.Create("Test", "", defaultUserConfig, 0, 28000)
	if err != nil {
		t.Fatal(err)
	}
	if item.ControllerPort != 28000 {
		t.Fatalf("ControllerPort = %d, want the explicit 28000", item.ControllerPort)
	}
	if item.MixedPort == 28000 {
		t.Fatal("auto-allocated MixedPort collided with the explicit ControllerPort")
	}
}

// TestStoreCreateSubscriptionProfileRejectsUnsafeHomeURL covers security
// L-1 / item 6: a HomeURL sourced from a subscription response that isn't
// http(s) is rejected rather than persisted, even though it is currently
// only rendered as plain text.
func TestStoreCreateSubscriptionProfileRejectsUnsafeHomeURL(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	fetched := &subscriptionFetchResult{Config: subscriptionConfig, HomeURL: "javascript:alert(1)"}
	_, err = store.CreateSubscriptionProfile("Provider", "https://example.com/sub", false, 0, fetched)
	if err == nil {
		t.Fatal("expected CreateSubscriptionProfile to reject a non-http(s) HomeURL")
	}
	if !errors.Is(err, errValidation) {
		t.Fatalf("err = %v, want errValidation", err)
	}
	if err.Error() != "home URL must start with http:// or https://" {
		t.Fatalf("err.Error() = %q", err.Error())
	}
}

// TestStoreLoadRecoversFromCorruptInstancesJSON covers L11: a corrupt
// instances.json must not prevent the app from starting. The bad file is
// preserved under a new name and the store starts empty.
func TestStoreLoadRecoversFromCorruptInstancesJSON(t *testing.T) {
	dir := t.TempDir()
	storePath := filepath.Join(dir, "instances.json")
	if err := os.WriteFile(storePath, []byte("{not valid json"), 0o600); err != nil {
		t.Fatal(err)
	}

	store, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore should recover from a corrupt instances.json, got error: %v", err)
	}
	if len(store.List()) != 0 || len(store.ListProfiles()) != 0 {
		t.Fatal("expected an empty store after recovering from corruption")
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var preserved bool
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), "instances.json.corrupt-") {
			continue
		}
		preserved = true
		raw, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if string(raw) != "{not valid json" {
			t.Fatalf("preserved corrupt file content = %q", raw)
		}
	}
	if !preserved {
		t.Fatal("expected the corrupt instances.json to be preserved under a new name")
	}
	if _, err := os.Stat(storePath); err != nil {
		t.Fatalf("expected a fresh instances.json to exist after recovery: %v", err)
	}
}
