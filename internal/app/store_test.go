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
