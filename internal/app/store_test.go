package app

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"gopkg.in/yaml.v3"
)

var portFreeTestMu sync.Mutex

func TestStorePersistsSecretButViewDoesNotExposeIt(t *testing.T) {
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
	user := "mixed-port: 1\nport: 8080\nsocks-port: 7891\nredir-port: 7892\ntproxy-port: 7893\nexternal-controller: 0.0.0.0:9999\nexternal-ui: ui\ntun:\n  enable: true\nlisteners:\n  - name: test\nsecret: bad\nallow-lan: true\n"
	if err := os.WriteFile(item.UserConfigPath, []byte(user), 0o644); err != nil {
		t.Fatal(err)
	}
	profile := &Profile{ID: "profile", Name: "Profile", ConfigPath: item.UserConfigPath}
	if err := writeRuntimeConfig(item, profile); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(item.RuntimeConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	got := string(raw)
	for _, want := range []string{
		"mixed-port: 28002",
		"external-controller: 127.0.0.1:29002",
		"secret: secret-token",
		"allow-lan: false",
		"bind-address: 127.0.0.1",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("runtime config missing %q:\n%s", want, got)
		}
	}
	for _, blocked := range []string{"port: 8080", "socks-port:", "redir-port:", "tproxy-port:", "external-ui:", "tun:", "listeners:"} {
		if strings.Contains(got, blocked) {
			t.Fatalf("runtime config kept conflicting port key %q:\n%s", blocked, got)
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
	if err := writeRuntimeConfig(item, profile); err != nil {
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
	if err := writeRuntimeConfig(item, profile); err != nil {
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
	if err := writeRuntimeConfig(item, profile); err != nil {
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
	if err := writeRuntimeConfig(item, profile); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(item.RuntimeConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	var cfg map[string]any
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg["mixed-port"] != 28003 || cfg["external-controller"] != "127.0.0.1:29003" {
		t.Fatalf("runtime fields were not injected: %#v", cfg)
	}
	if _, ok := cfg["rule-providers"]; ok {
		t.Fatalf("global-chain config kept rule-providers:\n%s", raw)
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
	if err := writeRuntimeConfig(item, profile); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(item.RuntimeConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	var cfg map[string]any
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
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
	if err := writeRuntimeConfig(item, profile); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(item.RuntimeConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	var cfg map[string]any
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
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
	if err := writeRuntimeConfig(item, profile); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(item.RuntimeConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	var cfg map[string]any
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
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
	if err := writeRuntimeConfig(item, profile); err == nil || !strings.Contains(err.Error(), "parse local proxies") {
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
			if err := writeRuntimeConfig(item, profile); err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("writeRuntimeConfig() error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestStoreSharesProfileAndPersistsPerInstanceSelection(t *testing.T) {
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
	if _, err := store.Clone(source.ID, "US same port", 28011, 28011); err == nil || err.Error() != "mixed and controller ports must differ" {
		t.Fatalf("Clone() error = %v, want mixed/controller differ error", err)
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
	if _, err := store.Update(item.ID, "", "", "", 0, 28001); err == nil {
		t.Fatal("expected controller port matching existing mixed port to be rejected")
	}
	got, ok := store.Get(item.ID)
	if !ok {
		t.Fatal("expected instance to remain")
	}
	if got.MixedPort != 28001 || got.ControllerPort != 29001 {
		t.Fatalf("ports changed after failed update: mixed=%d controller=%d", got.MixedPort, got.ControllerPort)
	}
}

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
