package app

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
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
