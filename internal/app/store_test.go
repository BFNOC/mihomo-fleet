package app

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

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
