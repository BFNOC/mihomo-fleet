package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

// sixExitLocalProxies is the owner's shape: one server and port, six exits
// told apart only by uuid (synthetic here).
func sixExitLocalProxies() string {
	var b strings.Builder
	for i, name := range []string{"us-globel", "sg-globel", "jp-globel", "uk-globel", "nz-globel", "au-globel"} {
		fmt.Fprintf(&b, `- name: %s
  type: vless
  server: 192.0.2.10
  port: 17548
  uuid: 00000000-0000-4000-8000-00000000000%d
  udp: true
  tls: true
  reality-opts:
    public-key: test-key
    short-id: "0123456789abcdef"
`, name, i+1)
	}
	return b.String()
}

func sixExitMappings() []PortMapping {
	names := []string{"us-globel", "sg-globel", "jp-globel", "uk-globel", "nz-globel", "au-globel"}
	out := make([]PortMapping, len(names))
	for i, name := range names {
		out[i] = PortMapping{Port: 10001 + i, Proxy: name}
	}
	return out
}

const ruleProfileWithGroup = `proxies:
  - {name: a, type: socks5, server: 127.0.0.1, port: 1}
  - {name: b, type: socks5, server: 127.0.0.1, port: 2}
proxy-groups:
  - {name: G, type: select, proxies: [a, b]}
rules:
  - MATCH,G
`

func runtimeListeners(t *testing.T, cfg map[string]any) []map[string]any {
	t.Helper()
	// Round-trip through YAML so the assertions see what mihomo reads.
	out, err := yaml.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	var parsed struct {
		MixedPort int              `yaml:"mixed-port"`
		Listeners []map[string]any `yaml:"listeners"`
	}
	if err := yaml.Unmarshal(out, &parsed); err != nil {
		t.Fatal(err)
	}
	return parsed.Listeners
}

func TestPortMappingsGenerateOwnerSixExitListeners(t *testing.T) {
	for _, chainUDP := range []bool{false, true} {
		item := &Instance{
			MixedPort: 28000, ControllerPort: 29000, Mode: InstanceModeGlobalChain,
			LocalProxies: sixExitLocalProxies(), ChainUDP: chainUDP, PortMappings: sixExitMappings(),
		}
		cfg, _, err := buildRuntimeConfig(item, []byte(defaultUserConfig))
		if err != nil {
			t.Fatalf("chainUDP=%t: buildRuntimeConfig() error = %v", chainUDP, err)
		}
		if cfg["mixed-port"] != 28000 {
			t.Fatalf("mixed-port = %v, want the main port kept", cfg["mixed-port"])
		}
		listeners := runtimeListeners(t, cfg)
		if len(listeners) != 6 {
			t.Fatalf("listeners = %d, want 6: %v", len(listeners), listeners)
		}
		names := map[string]bool{}
		for i, l := range listeners {
			want := sixExitMappings()[i]
			if l["type"] != "mixed" || l["port"] != want.Port || l["proxy"] != want.Proxy || l["listen"] != "127.0.0.1" || l["udp"] != chainUDP {
				t.Fatalf("chainUDP=%t listener %d = %v, want mixed %d -> %s on 127.0.0.1 udp=%t", chainUDP, i, l, want.Port, want.Proxy, chainUDP)
			}
			names[l["name"].(string)] = true
		}
		if len(names) != 6 {
			t.Fatalf("listener names not unique: %v", names)
		}
		// Each mapped exit keeps its own uuid on the shared server and port:
		// that uuid is the only thing telling the six exits apart.
		uuids := map[string]bool{}
		for _, p := range cfg["proxies"].([]any) {
			entry := p.(map[string]any)
			if entry["server"] != "192.0.2.10" || entry["port"] != 17548 {
				t.Fatalf("proxy %v lost its shared server/port", entry["name"])
			}
			uuids[entry["uuid"].(string)] = true
		}
		if len(uuids) != 6 {
			t.Fatalf("uuids = %v, want six distinct", uuids)
		}
	}
}

func TestPortMappingsRuleModeTargetsGroupsWithUDP(t *testing.T) {
	item := &Instance{MixedPort: 28000, ControllerPort: 29000, PortMappings: []PortMapping{{Port: 10001, Proxy: "a"}, {Port: 10002, Proxy: "G"}}}
	cfg, _, err := buildRuntimeConfig(item, []byte(ruleProfileWithGroup))
	if err != nil {
		t.Fatalf("buildRuntimeConfig() error = %v", err)
	}
	listeners := runtimeListeners(t, cfg)
	if len(listeners) != 2 || listeners[1]["proxy"] != "G" || listeners[0]["udp"] != true {
		t.Fatalf("listeners = %v, want a and G with udp", listeners)
	}
}

func TestPortMappingsMultiBindExpandsPerAddress(t *testing.T) {
	item := &Instance{MixedPort: 28000, ControllerPort: 29000, ProxyBind: "127.0.0.1,192.168.64.1", PortMappings: []PortMapping{{Port: 10001, Proxy: "a"}}}
	cfg, _, err := buildRuntimeConfig(item, []byte(ruleProfileWithGroup))
	if err != nil {
		t.Fatalf("buildRuntimeConfig() error = %v", err)
	}
	listeners := runtimeListeners(t, cfg)
	if len(listeners) != 4 {
		t.Fatalf("listeners = %d, want 2 main + 2 mapping: %v", len(listeners), listeners)
	}
	seen := map[string]bool{}
	for _, l := range listeners {
		seen[l["name"].(string)] = true
	}
	mapped := listeners[2:]
	if len(seen) != 4 || mapped[0]["listen"] != "127.0.0.1" || mapped[1]["listen"] != "192.168.64.1" || mapped[1]["proxy"] != "a" {
		t.Fatalf("mapping listeners = %v, want one per bind address with unique names", mapped)
	}
}

// Negative control: no mappings means no listeners key on a single bind, and
// listeners the profile or override write are still stripped.
func TestPortMappingsAbsentLeavesRuntimeConfigAlone(t *testing.T) {
	profile := ruleProfileWithGroup + "listeners:\n  - {name: rogue, type: mixed, port: 10009, proxy: a}\n"
	item := &Instance{MixedPort: 28000, ControllerPort: 29000, ConfigOverride: "append-listeners:\n  - {name: rogue2, type: mixed, port: 10010}\n"}
	cfg, _, err := buildRuntimeConfig(item, []byte(profile))
	if err != nil {
		t.Fatalf("buildRuntimeConfig() error = %v", err)
	}
	if _, ok := cfg["listeners"]; ok {
		t.Fatalf("listeners = %v, want none without mappings", cfg["listeners"])
	}
}

func TestPortMappingsUnknownTargetNamesWhatIsAvailable(t *testing.T) {
	item := &Instance{MixedPort: 28000, ControllerPort: 29000, PortMappings: []PortMapping{{Port: 10001, Proxy: "missing"}}}
	_, _, err := buildRuntimeConfig(item, []byte(ruleProfileWithGroup+"proxy-providers:\n  sub: {type: file, path: ./x.yaml}\n"))
	if !errors.Is(err, errValidation) {
		t.Fatalf("error = %v, want validation error", err)
	}
	for _, want := range []string{"10001", `"missing"`, "a, b, G", "proxy-provider"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q does not mention %q", err, want)
		}
	}
	// Global-chain replaces the groups, so a profile group is no target there.
	chain := &Instance{MixedPort: 28000, ControllerPort: 29000, Mode: InstanceModeGlobalChain, PortMappings: []PortMapping{{Port: 10001, Proxy: "G"}}}
	if _, _, err := buildRuntimeConfig(chain, []byte(ruleProfileWithGroup)); err == nil || !strings.Contains(err.Error(), "节点选择") {
		t.Fatalf("global-chain error = %v, want unknown G listing 节点选择", err)
	}
}

func TestNormalizePortMappingsRejectsBadShapes(t *testing.T) {
	tooMany := make([]PortMapping, maxPortMappings+1)
	for i := range tooMany {
		tooMany[i] = PortMapping{Port: 20000 + i, Proxy: "a"}
	}
	cases := []struct {
		name string
		in   []PortMapping
		want string
	}{
		{"zero port", []PortMapping{{Port: 0, Proxy: "a"}}, "port 0 is out of range"},
		{"high port", []PortMapping{{Port: 65536, Proxy: "a"}}, "port 65536 is out of range"},
		{"empty target", []PortMapping{{Port: 10001, Proxy: "  "}}, "port 10001): proxy name is empty"},
		{"duplicate", []PortMapping{{Port: 10001, Proxy: "a"}, {Port: 10001, Proxy: "b"}}, "port 10001 is duplicated"},
		{"mixed", []PortMapping{{Port: 28000, Proxy: "a"}}, "28000 equals this instance's mixed proxy port"},
		{"controller", []PortMapping{{Port: 29000, Proxy: "a"}}, "29000 equals this instance's controller port"},
		{"too many", tooMany, fmt.Sprintf("too many port mappings (%d, max %d)", maxPortMappings+1, maxPortMappings)},
	}
	for _, tc := range cases {
		_, err := normalizePortMappings(tc.in, 28000, 29000)
		if !errors.Is(err, errValidation) || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("%s: error = %v, want validation error containing %q", tc.name, err, tc.want)
		}
	}
	// Kept as written: mihomo matches proxy names exactly, padding included.
	got, err := normalizePortMappings([]PortMapping{{Port: 10001, Proxy: " a "}}, 28000, 29000)
	if err != nil || got[0].Proxy != " a " {
		t.Fatalf("padded name: got %v, %v", got, err)
	}
}

func withMappingProbes(t *testing.T, busy map[int]bool) {
	t.Helper()
	probe := func(address string) error {
		for port := range busy {
			if strings.HasSuffix(address, fmt.Sprintf(":%d", port)) {
				return errors.New("address already in use")
			}
		}
		return nil
	}
	withProxyBindProbe(t, probe)
	original := tcpListenProbe
	tcpListenProbe = probe
	t.Cleanup(func() { tcpListenProbe = original })
}

func createMappedInstance(t *testing.T, store *Store, name string, mixed, controller int, mappings []PortMapping) (*Instance, error) {
	t.Helper()
	return store.CreateWithOptions(createInstanceOptions{
		Name: name, Config: ruleProfileWithGroup, MixedPort: mixed, ControllerPort: controller, PortMappings: mappings,
	})
}

func TestStorePortMappingsAreFleetWideUnique(t *testing.T) {
	withPortFree(t, func(int) bool { return true })
	withMappingProbes(t, nil)
	store := newManagerTestStore(t)
	if _, err := createMappedInstance(t, store, "first", 28001, 29001, []PortMapping{{Port: 10001, Proxy: "a"}}); err != nil {
		t.Fatal(err)
	}
	_, err := createMappedInstance(t, store, "second", 28002, 29002, []PortMapping{{Port: 10001, Proxy: "b"}})
	if !errors.Is(err, errPortUnavailable) || !strings.Contains(err.Error(), `instance "first" port mapping 10001 -> "a"`) {
		t.Fatalf("mapping clash error = %v, want 409 naming the holder", err)
	}
	_, err = createMappedInstance(t, store, "third", 10001, 29003, nil)
	if !errors.Is(err, errPortUnavailable) {
		t.Fatalf("mixed port on a mapped port: error = %v, want unavailable", err)
	}
	_, err = createMappedInstance(t, store, "fourth", 28004, 29004, []PortMapping{{Port: 28001, Proxy: "a"}})
	if !errors.Is(err, errPortUnavailable) || !strings.Contains(err.Error(), "mixed proxy port") {
		t.Fatalf("mapping on another mixed port: error = %v", err)
	}
	// Auto-allocation must step over a mapped port.
	auto, err := store.CreateWithOptions(createInstanceOptions{Name: "auto", Config: ruleProfileWithGroup, MixedStart: 10001, ControllerStart: 29100})
	if err != nil || auto.MixedPort == 10001 {
		t.Fatalf("auto mixed port = %v (err %v), want anything but the mapped 10001", auto, err)
	}
	if _, err := createMappedInstance(t, store, "unknown", 28005, 29005, []PortMapping{{Port: 10005, Proxy: "nope"}}); !errors.Is(err, errValidation) {
		t.Fatalf("unknown target at create: error = %v, want validation", err)
	}
}

func TestStorePortMappingProbeFailureReportsCause(t *testing.T) {
	withPortFree(t, func(int) bool { return true })
	withMappingProbes(t, map[int]bool{10001: true})
	store := newManagerTestStore(t)
	_, err := createMappedInstance(t, store, "probe", 28001, 29001, []PortMapping{{Port: 10001, Proxy: "a"}})
	if !errors.Is(err, errPortUnavailable) || !strings.Contains(err.Error(), "address already in use") {
		t.Fatalf("error = %v, want 409 carrying the bind error", err)
	}
}

func TestStoreUpdatePortMappingsSemantics(t *testing.T) {
	withPortFree(t, func(int) bool { return true })
	withMappingProbes(t, nil)
	store := newManagerTestStore(t)
	item, err := createMappedInstance(t, store, "edit", 28001, 29001, []PortMapping{{Port: 10001, Proxy: "a"}})
	if err != nil {
		t.Fatal(err)
	}
	stamp := item.ConfigUpdatedAt

	echo := []PortMapping{{Port: 10001, Proxy: "a"}}
	got, err := store.UpdateWithOptions(item.ID, updateInstanceOptions{PortMappings: &echo})
	if err != nil || !got.ConfigUpdatedAt.Equal(stamp) {
		t.Fatalf("echo: err %v, ConfigUpdatedAt moved %v -> %v", err, stamp, got.ConfigUpdatedAt)
	}
	got, err = store.UpdateWithOptions(item.ID, updateInstanceOptions{Name: "renamed"})
	if err != nil || !portMappingsEqual(got.PortMappings, item.PortMappings) {
		t.Fatalf("omitted field: mappings = %v (err %v), want kept", got.PortMappings, err)
	}
	retarget := []PortMapping{{Port: 10001, Proxy: "G"}}
	got, err = store.UpdateWithOptions(item.ID, updateInstanceOptions{PortMappings: &retarget})
	if err != nil || got.PortMappings[0].Proxy != "G" || !got.ConfigUpdatedAt.After(stamp) {
		t.Fatalf("retarget: %v err %v", got, err)
	}
	if _, err := store.UpdateWithOptions(item.ID, updateInstanceOptions{MixedPort: 10001}); !errors.Is(err, errValidation) {
		t.Fatalf("mixed port onto own mapping: error = %v, want validation", err)
	}
	bad := []PortMapping{{Port: 10001, Proxy: "gone"}}
	if _, err := store.UpdateWithOptions(item.ID, updateInstanceOptions{PortMappings: &bad}); !errors.Is(err, errValidation) {
		t.Fatalf("unknown target at update: error = %v", err)
	}
	// The same request's new profile YAML is what targets are checked against.
	newConfig := strings.Replace(ruleProfileWithGroup, "{name: G,", "{name: H,", 1)
	newConfig = strings.Replace(newConfig, "MATCH,G", "MATCH,H", 1)
	if _, err := store.UpdateWithOptions(item.ID, updateInstanceOptions{Config: newConfig}); !errors.Is(err, errValidation) || !strings.Contains(err.Error(), `"G"`) {
		t.Fatalf("config dropping the mapped group: error = %v", err)
	}
	empty := []PortMapping{}
	got, err = store.UpdateWithOptions(item.ID, updateInstanceOptions{PortMappings: &empty})
	if err != nil || len(got.PortMappings) != 0 {
		t.Fatalf("clear: %v err %v", got.PortMappings, err)
	}

	reopened, err := NewStore(store.dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if loaded, _ := reopened.Get(item.ID); len(loaded.PortMappings) != 0 {
		t.Fatalf("reloaded mappings = %v, want cleared", loaded.PortMappings)
	}
}

func TestStoreClonePortMappingsMovesPorts(t *testing.T) {
	withPortFree(t, func(int) bool { return true })
	withMappingProbes(t, nil)
	store := newManagerTestStore(t)
	source, err := createMappedInstance(t, store, "src", 28001, 29001, []PortMapping{{Port: 10001, Proxy: "a"}, {Port: 10002, Proxy: "b"}})
	if err != nil {
		t.Fatal(err)
	}
	clone, err := store.Clone(source.ID, "", 0, 0)
	if err != nil {
		t.Fatalf("Clone() error = %v", err)
	}
	if len(clone.PortMappings) != 2 || clone.PortMappings[0].Proxy != "a" || clone.PortMappings[1].Proxy != "b" {
		t.Fatalf("clone mappings = %v, want same targets", clone.PortMappings)
	}
	for _, m := range clone.PortMappings {
		if m.Port == 10001 || m.Port == 10002 || m.Port == clone.MixedPort || m.Port == clone.ControllerPort {
			t.Fatalf("clone mapping port %d collides", m.Port)
		}
	}
}

func TestBundlePortMappingsRoundTripAndReallocate(t *testing.T) {
	withPortFree(t, func(int) bool { return true })
	withMappingProbes(t, nil)
	store := newManagerTestStore(t)
	if _, err := createMappedInstance(t, store, "exp", 28001, 29001, []PortMapping{{Port: 10001, Proxy: "a"}, {Port: 10002, Proxy: "G"}}); err != nil {
		t.Fatal(err)
	}
	bundle, err := ExportBundle(store)
	if err != nil {
		t.Fatal(err)
	}
	if bundle.Version != 4 || len(bundle.Instances[0].PortMappings) != 2 {
		t.Fatalf("bundle version %d mappings %v", bundle.Version, bundle.Instances[0].PortMappings)
	}
	data, _ := json.Marshal(bundle)

	// Same store: every port collides, so the mappings must move and be reported.
	result, err := ImportBundle(store, data)
	if err != nil {
		t.Fatalf("ImportBundle() error = %v", err)
	}
	got := result.Instances[0]
	if !got.PortReallocated || len(got.PortMappings) != 2 || got.PortMappings[0].Port == 10001 || got.PortMappings[1].Proxy != "G" {
		t.Fatalf("import result = %+v, want reallocated mappings keeping targets", got)
	}

	// A fresh store keeps the original ports.
	fresh := newManagerTestStore(t)
	result, err = ImportBundle(fresh, data)
	if err != nil || result.Instances[0].PortReallocated || result.Instances[0].PortMappings[0].Port != 10001 {
		t.Fatalf("fresh import = %+v err %v", result, err)
	}

	// A target the profile no longer has (a refresh dropped it) still
	// restores as it was held; start is where it is reported.
	bundle.Instances[0].PortMappings[0].Proxy = "gone"
	data, _ = json.Marshal(bundle)
	empty := newManagerTestStore(t)
	if _, err := ImportBundle(empty, data); err != nil {
		t.Fatalf("dangling target import: %v", err)
	}
	restored := empty.List()[0]
	if restored.PortMappings[0].Proxy != "gone" {
		t.Fatalf("restored mappings = %v", restored.PortMappings)
	}
	raw, _ := os.ReadFile(restored.UserConfigPath)
	if _, _, err := buildRuntimeConfig(restored, raw); err == nil || !strings.Contains(err.Error(), `"gone"`) {
		t.Fatalf("start-time generation error = %v, want the dangling target named", err)
	}
	// Shape problems are still refused before anything is created.
	bundle.Instances[0].PortMappings[0].Port = 0
	data, _ = json.Marshal(bundle)
	shape := newManagerTestStore(t)
	if _, err := ImportBundle(shape, data); !errors.Is(err, errValidation) || len(shape.List()) != 0 {
		t.Fatalf("bad shape import: err %v, created %d", err, len(shape.List()))
	}
}

// Every Instance field must reach InstanceView, BundleInstance and back, or be
// listed here as deliberately dropped.
func TestPortMappingsCarriedAcrossEveryBoundary(t *testing.T) {
	// Every source field must arrive with its own value (distinct sentinels,
	// so a field fed from the wrong source fails too), or be listed as
	// deliberately dropped or rebuilt.
	assertCarried := func(t *testing.T, label string, src, dst any, dropped ...string) {
		t.Helper()
		skip := stringSet(dropped)
		sv, dv := reflect.ValueOf(src), reflect.ValueOf(dst)
		for i := 0; i < sv.NumField(); i++ {
			name := sv.Type().Field(i).Name
			if skip[name] {
				continue
			}
			sf, df := sv.Field(i), dv.FieldByName(name)
			if !df.IsValid() {
				t.Fatalf("%s: field %s has no counterpart", label, name)
			}
			if sf.IsZero() {
				t.Fatalf("%s: fixture leaves %s zero; give it a sentinel", label, name)
			}
			if !reflect.DeepEqual(sf.Interface(), df.Interface()) {
				t.Fatalf("%s: field %s = %v, want %v", label, name, df.Interface(), sf.Interface())
			}
		}
	}
	item := &Instance{
		ID: "id", Name: "n", ProfileID: "p", MixedPort: 1, ProxyBind: "127.0.0.1", ControllerPort: 2,
		Secret: "s", UserConfigPath: "u", RuntimeConfigPath: "r", Mode: InstanceModeGlobalChain,
		LocalProxies: "l", ConfigOverride: "o", Chain: []string{"c"}, ChainUDP: true,
		PortMappings: []PortMapping{{Port: 3, Proxy: "x"}}, SelectedProxies: map[string]string{"g": "x"},
		SelectedGroup: "g", SelectedProxy: "x", LastError: "e", AutoRestart: true,
		CreatedAt: time.Unix(10, 0).UTC(), UpdatedAt: time.Unix(20, 0).UTC(), ConfigUpdatedAt: time.Unix(30, 0).UTC(),
	}
	view := viewFor(item, nil, "stopped", 0)
	assertCarried(t, "Instance->InstanceView", *item, view, "Secret", "ConfigUpdatedAt")

	withPortFree(t, func(int) bool { return true })
	withMappingProbes(t, nil)
	store := newManagerTestStore(t)
	created, err := store.CreateWithOptions(createInstanceOptions{
		Name: "all", Config: defaultUserConfig, MixedPort: 28001, ProxyBind: "127.0.0.1", ControllerPort: 29001,
		Mode: InstanceModeGlobalChain, LocalProxies: sixExitLocalProxies(), ConfigOverride: "dns: {enable: false}",
		Chain: []string{"节点选择"}, ChainUDP: true, PortMappings: []PortMapping{{Port: 10001, Proxy: "us-globel"}},
		SelectedProxies: map[string]string{"节点选择": "us-globel"}, SelectedGroup: "节点选择", SelectedProxy: "us-globel", AutoRestart: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	bundle, err := ExportBundle(store)
	if err != nil {
		t.Fatal(err)
	}
	// Iterates the Instance fields, so a new one missing from BundleInstance
	// fails here unless it is listed as machine-local.
	assertCarried(t, "Instance->BundleInstance", *created, bundle.Instances[0],
		"ID", "Secret", "UserConfigPath", "RuntimeConfigPath", "CreatedAt", "UpdatedAt", "ConfigUpdatedAt", "LastError")
	data, _ := json.Marshal(bundle)
	fresh := newManagerTestStore(t)
	if _, err := ImportBundle(fresh, data); err != nil {
		t.Fatal(err)
	}
	imported := fresh.List()[0]
	back := BundleInstance{
		Name: imported.Name, ProfileID: imported.ProfileID, MixedPort: imported.MixedPort, ProxyBind: imported.ProxyBind,
		ControllerPort: imported.ControllerPort, Mode: imported.Mode, LocalProxies: imported.LocalProxies,
		ConfigOverride: imported.ConfigOverride, Chain: imported.Chain, ChainUDP: imported.ChainUDP,
		PortMappings: imported.PortMappings, SelectedProxies: imported.SelectedProxies, SelectedGroup: imported.SelectedGroup,
		SelectedProxy: imported.SelectedProxy, AutoRestart: imported.AutoRestart,
	}
	// ProfileID is rebuilt on import (a fresh profile id); the rest must match.
	assertCarried(t, "BundleInstance->Instance", bundle.Instances[0], back, "ProfileID")
	if back.ProfileID == "" {
		t.Fatal("imported instance has no profile")
	}
	if !reflect.DeepEqual(imported.PortMappings, created.PortMappings) {
		t.Fatalf("imported mappings %v, want %v", imported.PortMappings, created.PortMappings)
	}
}

// withMappingDial replaces the readiness dial and reports mihomo's listeners
// as applied (no controller runs in these tests).
func withMappingDial(t *testing.T, fn func(ctx context.Context, address string) error) {
	t.Helper()
	original, originalTimeout, originalApplied := portMappingDial, portMappingReadyTimeout, mihomoListenersApplied
	portMappingDial = fn
	mihomoListenersApplied = func(context.Context, *Instance) (bool, string, error) { return true, "", nil }
	t.Cleanup(func() {
		portMappingDial, portMappingReadyTimeout, mihomoListenersApplied = original, originalTimeout, originalApplied
	})
}

func TestManagerStartFailsWhenMappedPortNeverListens(t *testing.T) {
	withPortFree(t, func(int) bool { return true })
	withMappingProbes(t, nil)
	withMappingDial(t, func(context.Context, string) error { return errors.New("connection refused") })
	portMappingReadyTimeout = 300 * time.Millisecond
	store := newManagerTestStore(t)
	manager := NewManager(store, writeFakeMihomo(t, true, 0))
	item, err := createMappedInstance(t, store, "never", 28101, 29101, []PortMapping{{Port: 10101, Proxy: "a"}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Stop(item.ID) })
	err = manager.Start(item.ID)
	if err == nil || !strings.Contains(err.Error(), `10101 -> "a" on 127.0.0.1:10101 (connection refused)`) {
		t.Fatalf("Start() error = %v, want the endpoint and dial error", err)
	}
	if manager.state(item.ID) != nil {
		t.Fatal("process still registered after a failed mapping check")
	}
	if got, _ := store.Get(item.ID); got.LastError != err.Error() {
		t.Fatalf("LastError = %q, want %q", got.LastError, err.Error())
	}
}

func TestRunningInstanceRefusesMappingEditsAndReload(t *testing.T) {
	withPortFree(t, func(int) bool { return true })
	withMappingProbes(t, nil)
	withMappingDial(t, func(context.Context, string) error { return nil })
	c, err := NewController(Options{DataDir: t.TempDir(), MihomoPath: writeFakeMihomo(t, true, 0), AppVersion: "test"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Shutdown(context.Background()) })
	item, err := createMappedInstance(t, c.store, "live", 28201, 29201, []PortMapping{{Port: 10201, Proxy: "a"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.manager.Start(item.ID); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	put := func(body string) int {
		req := httptest.NewRequest(http.MethodPut, "/api/instances/"+item.ID, strings.NewReader(body))
		rec := httptest.NewRecorder()
		c.handleInstanceRoot(rec, req, item.ID)
		return rec.Code
	}
	if code := put(`{"portMappings":[{"port":10201,"proxy":"a"}]}`); code != http.StatusOK {
		t.Fatalf("echo while running: status %d, want 200", code)
	}
	req := httptest.NewRequest(http.MethodPut, "/api/instances/"+item.ID, strings.NewReader(`{"portMappings":[{"port":10201,"proxy":"b"}]}`))
	rec := httptest.NewRecorder()
	c.handleInstanceRoot(rec, req, item.ID)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), `running: 10201 -\u003e a; requested: 10201 -\u003e b`) {
		t.Fatalf("retarget while running: status %d body %s, want 409 naming both lists", rec.Code, rec.Body.String())
	}
	// Bypass the handler guard to prove reload refuses a listener change.
	retarget := []PortMapping{{Port: 10201, Proxy: "b"}}
	if _, err := c.store.UpdateWithOptions(item.ID, updateInstanceOptions{PortMappings: &retarget}); err != nil {
		t.Fatal(err)
	}
	if err := c.manager.ReloadContext(context.Background(), item.ID); !errors.Is(err, errReloadNetworkChanged) {
		t.Fatalf("ReloadContext() error = %v, want errReloadNetworkChanged", err)
	}
}

func bundleWithMappings(t *testing.T, bind string, mappings []PortMapping) []byte {
	t.Helper()
	bundle := FleetBundle{
		Version:  FleetBundleVersion,
		Profiles: []BundleProfile{{ID: "p", Name: "p", Config: ruleProfileWithGroup}},
		Instances: []BundleInstance{{
			Name: "imp", ProfileID: "p", MixedPort: 28050, ControllerPort: 29050, ProxyBind: bind, PortMappings: mappings,
		}},
	}
	data, err := json.Marshal(bundle)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// Only the mapping whose port is taken moves; its free siblings keep theirs.
func TestImportReallocatesOnlyTakenMappingPorts(t *testing.T) {
	withPortFree(t, func(int) bool { return true })
	withMappingProbes(t, nil)
	store := newManagerTestStore(t)
	if _, err := createMappedInstance(t, store, "holder", 28001, 29001, []PortMapping{{Port: 10001, Proxy: "a"}}); err != nil {
		t.Fatal(err)
	}
	result, err := ImportBundle(store, bundleWithMappings(t, "127.0.0.1", []PortMapping{{Port: 10001, Proxy: "a"}, {Port: 10002, Proxy: "b"}, {Port: 10003, Proxy: "G"}}))
	if err != nil {
		t.Fatal(err)
	}
	got := result.Instances[0]
	if got.PortMappings[1].Port != 10002 || got.PortMappings[2].Port != 10003 {
		t.Fatalf("free mapping ports moved: %v", got.PortMappings)
	}
	if moved := got.PortMappings[0].Port; moved == 10001 || moved == 10002 || moved == 10003 || moved == got.MixedPort || moved == got.ControllerPort {
		t.Fatalf("taken mapping moved onto a used port: %+v", got)
	}
	if got.MixedPort != 28050 || got.ControllerPort != 29050 {
		t.Fatalf("free mixed/controller ports moved: %d/%d", got.MixedPort, got.ControllerPort)
	}
}

// A backup from another machine names a bind address this host lacks. The
// import must still succeed with the ports as they were; start reports the
// address.
func TestImportKeepsMappingsWhenBindAddressIsNotOnHost(t *testing.T) {
	withPortFree(t, func(int) bool { return true })
	foreign := "192.0.2.77"
	probe := func(address string) error {
		if strings.HasPrefix(address, foreign) {
			return errors.New("bind: cannot assign requested address")
		}
		return nil
	}
	withProxyBindProbe(t, probe)
	original := tcpListenProbe
	tcpListenProbe = probe
	t.Cleanup(func() { tcpListenProbe = original })
	if onHostBindAddresses(foreign) != "" {
		t.Skip("192.0.2.77 unexpectedly configured on this host")
	}

	store := newManagerTestStore(t)
	result, err := ImportBundle(store, bundleWithMappings(t, foreign, []PortMapping{{Port: 10001, Proxy: "a"}}))
	if err != nil {
		t.Fatalf("ImportBundle() error = %v", err)
	}
	if got := result.Instances[0]; len(got.PortMappings) != 1 || got.PortMappings[0].Port != 10001 || got.PortReallocated {
		t.Fatalf("result = %+v, want 10001 kept and nothing reported as moved", got)
	}
	// A plain create still refuses straight away, naming the address.
	_, err = store.CreateWithOptions(createInstanceOptions{Name: "direct", Config: ruleProfileWithGroup, ProxyBind: foreign, PortMappings: []PortMapping{{Port: 10009, Proxy: "a"}}})
	if !errors.Is(err, errPortUnavailable) || !strings.Contains(err.Error(), `proxy bind address "192.0.2.77" is not available on this host`) {
		t.Fatalf("create error = %v", err)
	}
}

// Stop while a start waits for its mapped ports: the wait is cancelled at
// once, the process is gone, and a user-requested stop leaves no error behind.
func TestStopDuringMappingWaitIsPromptAndQuiet(t *testing.T) {
	withPortFree(t, func(int) bool { return true })
	withMappingProbes(t, nil)
	withMappingDial(t, func(context.Context, string) error { return errors.New("connection refused") })
	portMappingReadyTimeout = 30 * time.Second
	store := newManagerTestStore(t)
	manager := NewManager(store, writeFakeMihomo(t, true, 0))
	item, err := createMappedInstance(t, store, "stopwait", 28301, 29301, []PortMapping{{Port: 10301, Proxy: "a"}})
	if err != nil {
		t.Fatal(err)
	}
	startErr := make(chan error, 1)
	go func() { startErr <- manager.Start(item.ID) }()
	deadline := time.Now().Add(5 * time.Second)
	for manager.state(item.ID) == nil && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if manager.state(item.ID) == nil {
		t.Fatal("process never registered")
	}
	began := time.Now()
	if err := manager.Stop(item.ID); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
	if took := time.Since(began); took > 5*time.Second {
		t.Fatalf("Stop took %s, want it to cut the 30s wait short", took)
	}
	if err := <-startErr; !errors.Is(err, context.Canceled) || !strings.Contains(err.Error(), "10301") {
		t.Fatalf("Start() error = %v, want cancelled naming the pending port", err)
	}
	if got, _ := store.Get(item.ID); got.LastError != "" {
		t.Fatalf("LastError = %q, want empty after a requested stop", got.LastError)
	}
	if manager.state(item.ID) != nil {
		t.Fatal("process still registered after Stop")
	}
}

// mihomo dying inside the mapping wait fails that start; it is not a crash for
// the watchdog to relaunch behind the caller's back.
func TestExitDuringMappingWaitDoesNotArmWatchdog(t *testing.T) {
	withPortFree(t, func(int) bool { return true })
	withMappingProbes(t, nil)
	withMappingDial(t, func(context.Context, string) error { return errors.New("connection refused") })
	withWatchdogTiming(t, 20*time.Millisecond, 40*time.Millisecond, 5, time.Minute)
	store := newManagerTestStore(t)
	manager := NewManager(store, writeCrashingFakeMihomo(t, 999))
	item, err := store.CreateWithOptions(createInstanceOptions{
		Name: "crashy", Config: ruleProfileWithGroup, MixedPort: 28401, ControllerPort: 29401,
		PortMappings: []PortMapping{{Port: 10401, Proxy: "a"}}, AutoRestart: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Stop(item.ID) })
	err = manager.Start(item.ID)
	if err == nil || !strings.Contains(err.Error(), "mihomo exited before port mappings were ready") {
		t.Fatalf("Start() error = %v", err)
	}
	time.Sleep(400 * time.Millisecond)
	raw, _ := os.ReadFile(filepath.Join(filepath.Dir(item.RuntimeConfigPath), ".start-count"))
	if strings.TrimSpace(string(raw)) != "1" {
		t.Fatalf("mihomo launched %q times, want exactly 1 (no watchdog relaunch)", strings.TrimSpace(string(raw)))
	}
	if got, _ := store.Get(item.ID); !strings.Contains(got.LastError, "mihomo exited before port mappings were ready") {
		t.Fatalf("LastError = %q", got.LastError)
	}
}

func TestReloadNamesThePortMappingChange(t *testing.T) {
	err := reloadMappingsChangedError{msg: "x"}
	if !errors.Is(err, errReloadNetworkChanged) {
		t.Fatal("reloadMappingsChangedError must still classify as errReloadNetworkChanged (409)")
	}
}

// Two bundle instances where only the first one's port is taken: moving the
// first must not take the second's port, which would silently send that
// port's clients through a different exit.
func TestImportAcrossInstancesKeepsEachOthersPorts(t *testing.T) {
	withPortFree(t, func(int) bool { return true })
	withMappingProbes(t, nil)
	store := newManagerTestStore(t)
	if _, err := createMappedInstance(t, store, "holder", 28001, 29001, []PortMapping{{Port: 10001, Proxy: "a"}}); err != nil {
		t.Fatal(err)
	}
	bundle := FleetBundle{
		Version:  FleetBundleVersion,
		Profiles: []BundleProfile{{ID: "p", Name: "p", Config: ruleProfileWithGroup}},
		Instances: []BundleInstance{
			{Name: "A", ProfileID: "p", MixedPort: 28060, ControllerPort: 29060, PortMappings: []PortMapping{{Port: 10001, Proxy: "a"}}},
			{Name: "B", ProfileID: "p", MixedPort: 28061, ControllerPort: 29061, PortMappings: []PortMapping{{Port: 10002, Proxy: "b"}}},
		},
	}
	data, _ := json.Marshal(bundle)
	result, err := ImportBundle(store, data)
	if err != nil {
		t.Fatal(err)
	}
	a, b := result.Instances[0], result.Instances[1]
	if b.PortReallocated || b.PortMappings[0].Port != 10002 {
		t.Fatalf("B = %+v, want its free 10002 kept", b)
	}
	if !a.PortReallocated || a.PortMappings[0].Port == 10001 || a.PortMappings[0].Port == 10002 {
		t.Fatalf("A = %+v, want moved off 10001 without taking B's 10002", a)
	}
}

func TestClonePortMappingAtTopPortWraps(t *testing.T) {
	withPortFree(t, func(int) bool { return true })
	withMappingProbes(t, nil)
	store := newManagerTestStore(t)
	source, err := createMappedInstance(t, store, "top", 28001, 29001, []PortMapping{{Port: 65535, Proxy: "a"}})
	if err != nil {
		t.Fatal(err)
	}
	clone, err := store.Clone(source.ID, "", 0, 0)
	if err != nil {
		t.Fatalf("Clone() error = %v", err)
	}
	if port := clone.PortMappings[0].Port; port < 1024 || port >= 65535 {
		t.Fatalf("clone mapping port = %d, want wrapped into the unprivileged range", port)
	}
}

func TestSharedProfileEditRefusedWhenItDropsAnotherInstancesTarget(t *testing.T) {
	withPortFree(t, func(int) bool { return true })
	withMappingProbes(t, nil)
	store := newManagerTestStore(t)
	a, err := createMappedInstance(t, store, "A", 28001, 29001, nil)
	if err != nil {
		t.Fatal(err)
	}
	b, err := store.CreateWithOptions(createInstanceOptions{
		Name: "B", ProfileID: a.ProfileID, MixedPort: 28002, ControllerPort: 29002, PortMappings: []PortMapping{{Port: 10001, Proxy: "G"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	withoutG := "proxies:\n  - {name: a, type: socks5, server: 127.0.0.1, port: 1}\nrules:\n  - MATCH,a\n"
	if _, err := store.UpdateWithOptions(a.ID, updateInstanceOptions{Config: withoutG}); !errors.Is(err, errValidation) ||
		!strings.Contains(err.Error(), `config would break instance "B": port mapping 10001 -> "G"`) {
		t.Fatalf("instance PUT config: error = %v", err)
	}
	if _, err := store.UpdateProfile(a.ProfileID, "", withoutG); !errors.Is(err, errValidation) || !strings.Contains(err.Error(), `instance "B"`) {
		t.Fatalf("profile PUT config: error = %v", err)
	}
	raw, _ := os.ReadFile(b.UserConfigPath)
	if !strings.Contains(string(raw), "name: G") {
		t.Fatal("refused edit still rewrote the shared profile")
	}
	// Negative control: an edit keeping G goes through.
	keepsG := ruleProfileWithGroup + "# edited\n"
	if _, err := store.UpdateProfile(a.ProfileID, "", keepsG); err != nil {
		t.Fatalf("edit keeping G: %v", err)
	}
}

// One PUT replacing the profile config, the chain and the mapping is checked
// against the new config throughout, not partly against the file on disk.
func TestUpdateChecksChainAndMappingsAgainstSameRequestConfig(t *testing.T) {
	withPortFree(t, func(int) bool { return true })
	withMappingProbes(t, nil)
	store := newManagerTestStore(t)
	item, err := store.CreateWithOptions(createInstanceOptions{
		Name: "gc", Config: "proxies:\n  - {name: a, type: socks5, server: 127.0.0.1, port: 1}\n", MixedPort: 28001, ControllerPort: 29001,
		Mode: InstanceModeGlobalChain,
	})
	if err != nil {
		t.Fatal(err)
	}
	newConfig := "proxies:\n  - {name: a, type: socks5, server: 127.0.0.1, port: 1}\n  - {name: b, type: socks5, server: 127.0.0.1, port: 2}\n"
	chain := []string{"b", globalChainSelectGroupName}
	mappings := []PortMapping{{Port: 10001, Proxy: "b"}}
	if _, err := store.UpdateWithOptions(item.ID, updateInstanceOptions{Config: newConfig, Chain: &chain, PortMappings: &mappings}); err != nil {
		t.Fatalf("UpdateWithOptions() error = %v", err)
	}
}

func TestPortMappingTargetsMatchRawNames(t *testing.T) {
	profile := "proxies:\n  - {name: \" us \", type: socks5, server: 127.0.0.1, port: 1}\n"
	item := &Instance{MixedPort: 28000, ControllerPort: 29000, PortMappings: []PortMapping{{Port: 10001, Proxy: "us"}}}
	if _, _, err := buildRuntimeConfig(item, []byte(profile)); err == nil || !strings.Contains(err.Error(), `available: " us "`) {
		t.Fatalf("error = %v, want the padded name refused and listed as written", err)
	}
}

func TestWaitPortMappingsReadyFailsOnLoggedBindError(t *testing.T) {
	withMappingDial(t, func(context.Context, string) error { return nil })
	line := "stderr: level=error msg=\"Listener fleet-map-10001 listen err: listen tcp 127.0.0.1:10001: bind: address already in use\""
	if !isPortMappingBindError(line) || isPortMappingBindError("stderr: Listener fleet-mixed-1 listen err: x") || isPortMappingBindError("stdout: Mixed(http+socks)[fleet-map-10001] proxy listening at: 127.0.0.1:10001") {
		t.Fatal("isPortMappingBindError matches the wrong lines")
	}
	// The dial succeeds (another process accepts), but mihomo logged that it
	// never bound the port: that is a failed start.
	appliedNow := func(context.Context) (bool, string, error) { return true, "", nil }
	err := waitPortMappingsReady(context.Background(), "127.0.0.1", []PortMapping{{Port: 10001, Proxy: "a"}}, make(chan struct{}),
		func() string { return line }, appliedNow)
	if err == nil || !strings.Contains(err.Error(), "mihomo could not bind a port mapping") || !strings.Contains(err.Error(), "address already in use") {
		t.Fatalf("waitPortMappingsReady() error = %v", err)
	}
}

// The foreign process accepts at once, but mihomo has not reached its listeners
// yet; the bind error is logged only later. Success must wait for mihomo's own
// "listeners applied" signal, by which time the error is in the log.
func TestWaitPortMappingsReadyWaitsForMihomoBeforeTrustingTheDial(t *testing.T) {
	withMappingDial(t, func(context.Context, string) error { return nil })
	calls := 0
	logged := ""
	applied := func(context.Context) (bool, string, error) {
		calls++
		if calls < 4 {
			return false, "controller reports mixed-port=0, want 28000", nil
		}
		// mihomo logs the bind error before SetAllowLan/ReCreateMixed flip the signal.
		logged = "Listener fleet-map-10001 listen err: bind: address already in use"
		return true, "", nil
	}
	err := waitPortMappingsReady(context.Background(), "127.0.0.1", []PortMapping{{Port: 10001, Proxy: "a"}}, make(chan struct{}),
		func() string { return logged }, applied)
	if err == nil || !strings.Contains(err.Error(), "address already in use") {
		t.Fatalf("waitPortMappingsReady() error = %v, want the late bind error", err)
	}
	// Never applied: times out naming that state.
	portMappingReadyTimeout = 300 * time.Millisecond
	err = waitPortMappingsReady(context.Background(), "127.0.0.1", []PortMapping{{Port: 10001, Proxy: "a"}}, make(chan struct{}),
		func() string { return "" }, func(context.Context) (bool, string, error) {
			return false, "controller reports mixed-port=0, want 28000", nil
		})
	if err == nil || !strings.Contains(err.Error(), "mihomo has not finished applying its listeners (controller reports mixed-port=0, want 28000)") {
		t.Fatalf("never applied: error = %v", err)
	}
}

func TestRunningInstanceRefusesMappingUDPFlip(t *testing.T) {
	withPortFree(t, func(int) bool { return true })
	withMappingProbes(t, nil)
	withMappingDial(t, func(context.Context, string) error { return nil })
	c, err := NewController(Options{DataDir: t.TempDir(), MihomoPath: writeFakeMihomo(t, true, 0), AppVersion: "test"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Shutdown(context.Background()) })
	item, err := createMappedInstance(t, c.store, "udp", 28501, 29501, []PortMapping{{Port: 10501, Proxy: "a"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.manager.Start(item.ID); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPut, "/api/instances/"+item.ID, strings.NewReader(`{"mode":"global-chain","chainUdp":false}`))
	rec := httptest.NewRecorder()
	c.handleInstanceRoot(rec, req, item.ID)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "running udp=true, requested udp=false") {
		t.Fatalf("status %d body %s, want 409 naming both udp values", rec.Code, rec.Body.String())
	}
	// Negative control: a chainUdp toggle that leaves rule mode's udp alone passes.
	req = httptest.NewRequest(http.MethodPut, "/api/instances/"+item.ID, strings.NewReader(`{"chainUdp":true}`))
	rec = httptest.NewRecorder()
	c.handleInstanceRoot(rec, req, item.ID)
	if rec.Code != http.StatusOK {
		t.Fatalf("rule-mode chainUdp toggle: status %d body %s", rec.Code, rec.Body.String())
	}
}

// Decision 2 ("follow the chain"): a mapped target keeps the dialer-proxy the
// chain gave it, and the start log spells out the hops.
func TestPortMappingFollowsChainAndLogsRoute(t *testing.T) {
	profile := "proxies:\n  - {name: front, type: socks5, server: 127.0.0.1, port: 1}\n"
	item := &Instance{
		MixedPort: 28000, ControllerPort: 29000, Mode: InstanceModeGlobalChain, LocalProxies: sixExitLocalProxies(),
		Chain: []string{"front", globalChainSelectGroupName}, PortMappings: []PortMapping{{Port: 10001, Proxy: "us-globel"}},
	}
	cfg, _, err := buildRuntimeConfig(item, []byte(profile))
	if err != nil {
		t.Fatal(err)
	}
	dialer := ""
	for _, p := range cfg["proxies"].([]any) {
		if entry := p.(map[string]any); entry["name"] == "us-globel" {
			dialer, _ = entry["dialer-proxy"].(string)
		}
	}
	if dialer != "front" || runtimeListeners(t, cfg)[0]["proxy"] != "us-globel" {
		t.Fatalf("us-globel dialer-proxy = %q, want the mapping to go through front", dialer)
	}
	out, _ := yaml.Marshal(cfg)
	path := filepath.Join(t.TempDir(), "config.runtime.yaml")
	if err := os.WriteFile(path, out, 0o600); err != nil {
		t.Fatal(err)
	}
	notes := portMappingRuntimeNotes(path, item.PortMappings)
	if len(notes) != 1 || notes[0] != "port mapping 10001 -> us-globel goes through front > us-globel" {
		t.Fatalf("notes = %q", notes)
	}
	// Negative control: a direct target logs no route; silent logging warns.
	if err := os.WriteFile(path, []byte("log-level: silent\nproxies:\n  - {name: a, type: socks5, server: 127.0.0.1, port: 1}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	notes = portMappingRuntimeNotes(path, []PortMapping{{Port: 10001, Proxy: "a"}})
	if len(notes) != 1 || !strings.Contains(notes[0], "log-level is silent") {
		t.Fatalf("silent notes = %q", notes)
	}
}

// Removing a chain member that a dependent instance's mapping also relies on
// fails that instance on the chain check first; the edit is still refused.
func TestSharedProfileEditRefusedWhenItDropsAChainMember(t *testing.T) {
	withPortFree(t, func(int) bool { return true })
	withMappingProbes(t, nil)
	store := newManagerTestStore(t)
	profile := "proxies:\n  - {name: us, type: socks5, server: 127.0.0.1, port: 1}\n  - {name: sg, type: socks5, server: 127.0.0.1, port: 2}\n"
	a, err := store.CreateWithOptions(createInstanceOptions{Name: "A", Config: profile, MixedPort: 28001, ControllerPort: 29001})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateWithOptions(createInstanceOptions{
		Name: "B", ProfileID: a.ProfileID, MixedPort: 28002, ControllerPort: 29002, Mode: InstanceModeGlobalChain,
		Chain: []string{"us", globalChainSelectGroupName}, PortMappings: []PortMapping{{Port: 10001, Proxy: "us"}},
	}); err != nil {
		t.Fatal(err)
	}
	withoutUS := "proxies:\n  - {name: sg, type: socks5, server: 127.0.0.1, port: 2}\n"
	if _, err := store.UpdateProfile(a.ProfileID, "", withoutUS); !errors.Is(err, errValidation) || !strings.Contains(err.Error(), `instance "B"`) {
		t.Fatalf("error = %v, want the edit refused naming B", err)
	}
}

// One bind address missing on this host must not switch off probing on the
// others: a port taken on 127.0.0.1 still moves.
func TestImportStillProbesOnHostAddressesWhenOneIsForeign(t *testing.T) {
	withPortFree(t, func(int) bool { return true })
	probe := func(address string) error {
		if strings.HasPrefix(address, "192.0.2.77") || address == "127.0.0.1:10001" {
			return errors.New("bind: unavailable")
		}
		return nil
	}
	withProxyBindProbe(t, probe)
	original := tcpListenProbe
	tcpListenProbe = probe
	t.Cleanup(func() { tcpListenProbe = original })
	if onHostBindAddresses("192.0.2.77") != "" {
		t.Skip("192.0.2.77 unexpectedly configured on this host")
	}
	store := newManagerTestStore(t)
	result, err := ImportBundle(store, bundleWithMappings(t, "127.0.0.1,192.0.2.77", []PortMapping{{Port: 10001, Proxy: "a"}}))
	if err != nil {
		t.Fatal(err)
	}
	if got := result.Instances[0]; got.PortMappings[0].Port == 10001 || !got.PortReallocated {
		t.Fatalf("result = %+v, want 10001 moved off the taken loopback port", got)
	}
}

// Pre-existing: an import retry auto-allocating both ports let mixed take the
// instance's own free controller port.
func TestImportRetryKeepsOwnFreeControllerPort(t *testing.T) {
	withPortFree(t, func(port int) bool { return port != 28070 })
	withMappingProbes(t, nil)
	store := newManagerTestStore(t)
	bundle := FleetBundle{
		Version:   FleetBundleVersion,
		Profiles:  []BundleProfile{{ID: "p", Name: "p", Config: ruleProfileWithGroup}},
		Instances: []BundleInstance{{Name: "x", ProfileID: "p", MixedPort: 28070, ControllerPort: 28071}},
	}
	data, _ := json.Marshal(bundle)
	result, err := ImportBundle(store, data)
	if err != nil {
		t.Fatal(err)
	}
	if got := result.Instances[0]; got.ControllerPort != 28071 || got.MixedPort == 28070 || got.MixedPort == 28071 {
		t.Fatalf("result = %+v, want controller 28071 kept and mixed moved elsewhere", got)
	}
}

// The create entry point end to end: POST body -> stored instance -> view.
func TestCreateHandlerCarriesPortMappings(t *testing.T) {
	withPortFree(t, func(int) bool { return true })
	withMappingProbes(t, nil)
	c, err := NewController(Options{DataDir: t.TempDir(), MihomoPath: writeFakeMihomo(t, true, 0), AppVersion: "test"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Shutdown(context.Background()) })
	body := `{"name":"post","config":` + strconvQuote(ruleProfileWithGroup) + `,"mixedPort":28601,"controllerPort":29601,` +
		`"portMappings":[{"port":10601,"proxy":"a"},{"port":10602,"proxy":"G"}]}`
	req := httptest.NewRequest(http.MethodPost, "/api/instances", strings.NewReader(body))
	rec := httptest.NewRecorder()
	c.handleInstances(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	var view InstanceView
	if err := json.Unmarshal(rec.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	want := []PortMapping{{Port: 10601, Proxy: "a"}, {Port: 10602, Proxy: "G"}}
	stored, _ := c.store.Get(view.ID)
	if !reflect.DeepEqual(view.PortMappings, want) || !reflect.DeepEqual(stored.PortMappings, want) {
		t.Fatalf("view %v stored %v, want %v", view.PortMappings, stored.PortMappings, want)
	}
}

func strconvQuote(s string) string {
	out, _ := json.Marshal(s)
	return string(out)
}

func TestMihomoListenersAppliedReadsTheControllersState(t *testing.T) {
	state := `{"mixed-port":0,"allow-lan":false}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/configs" || r.Header.Get("Authorization") != "Bearer s3" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(state))
	}))
	t.Cleanup(server.Close)
	port := server.Listener.Addr().(*net.TCPAddr).Port
	single := &Instance{ControllerPort: port, Secret: "s3", MixedPort: 28000, ProxyBind: "127.0.0.1"}
	multi := &Instance{ControllerPort: port, Secret: "s3", MixedPort: 28000, ProxyBind: "127.0.0.1,192.168.64.1"}
	for _, tc := range []struct {
		state        string
		single, many bool
	}{
		{`{"mixed-port":0,"allow-lan":false}`, false, false},
		{`{"mixed-port":28000,"allow-lan":false}`, true, false},
		{`{"mixed-port":0,"allow-lan":true}`, false, true},
	} {
		state = tc.state
		if got, _, err := mihomoListenersApplied(context.Background(), single); err != nil || got != tc.single {
			t.Fatalf("%s single: %v %v", tc.state, got, err)
		}
		if got, _, err := mihomoListenersApplied(context.Background(), multi); err != nil || got != tc.many {
			t.Fatalf("%s multi: %v %v", tc.state, got, err)
		}
	}
	wrong := *single
	wrong.Secret = "nope"
	if _, _, err := mihomoListenersApplied(context.Background(), &wrong); err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatalf("wrong secret: error = %v, want the status reported", err)
	}
}

// A wildcard in the bind list makes mapping listeners dual-stack on that port
// already; a second address would collide with it and fail every start.
func TestPortMappingListenersCollapseOnWildcardBind(t *testing.T) {
	// A stored mixed-family bind is refused at generation too, not only on
	// save (H2, round 4): the instance fails to start naming the bind.
	for _, bind := range []string{"::,127.0.0.1", "0.0.0.0,::1"} {
		item := &Instance{MixedPort: 28000, ControllerPort: 29000, ProxyBind: bind, PortMappings: []PortMapping{{Port: 10001, Proxy: "a"}}}
		if _, _, err := buildRuntimeConfig(item, []byte(ruleProfileWithGroup)); !errors.Is(err, errValidation) || !strings.Contains(err.Error(), bind) {
			t.Fatalf("bind %q: error = %v, want refused at generation", bind, err)
		}
	}
	for _, bind := range []string{"0.0.0.0,::"} {
		item := &Instance{MixedPort: 28000, ControllerPort: 29000, ProxyBind: bind, PortMappings: []PortMapping{{Port: 10001, Proxy: "a"}}}
		cfg, _, err := buildRuntimeConfig(item, []byte(ruleProfileWithGroup))
		if err != nil {
			t.Fatal(err)
		}
		var mapped []map[string]any
		for _, l := range runtimeListeners(t, cfg) {
			if strings.HasPrefix(l["name"].(string), "fleet-map-") {
				mapped = append(mapped, l)
			}
		}
		addrs, _ := parseProxyBindAddresses(bind)
		if len(mapped) != 1 || mapped[0]["listen"] != addrs[0] {
			t.Fatalf("bind %q: mapping listeners = %v, want one on %s", bind, mapped, addrs[0])
		}
	}
}

// The owner's shape (local exits only, chain left empty) gives each exit its
// own direct connection: no target carries dialer-proxy.
func TestOwnerShapeExitsStayIndependent(t *testing.T) {
	item := &Instance{MixedPort: 28000, ControllerPort: 29000, Mode: InstanceModeGlobalChain, LocalProxies: sixExitLocalProxies(), PortMappings: sixExitMappings()}
	cfg, _, err := buildRuntimeConfig(item, []byte(defaultUserConfig))
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range cfg["proxies"].([]any) {
		if dialer, ok := p.(map[string]any)["dialer-proxy"]; ok {
			t.Fatalf("%v carries dialer-proxy %v", p.(map[string]any)["name"], dialer)
		}
	}
}

// Restart while a start sits in its mapping wait: Stop must wait for that
// start to finish unwinding, or the Start half finds it still "starting",
// skips, and the instance ends up stopped behind a successful Restart.
// Selection pushes during the wait are refused like any mid-launch instance.
func TestRestartDuringMappingWaitLeavesInstanceRunning(t *testing.T) {
	withPortFree(t, func(int) bool { return true })
	withMappingProbes(t, nil)
	var ready atomic.Bool
	// Refuses until the first start has returned (cancelled by Restart's
	// Stop), then accepts -- so only the second start can succeed.
	withMappingDial(t, func(context.Context, string) error {
		if ready.Load() {
			return nil
		}
		return errors.New("connection refused")
	})
	portMappingReadyTimeout = 30 * time.Second
	c, err := NewController(Options{DataDir: t.TempDir(), MihomoPath: writeFakeMihomo(t, true, 0), AppVersion: "test"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Shutdown(context.Background()) })
	item, err := createMappedInstance(t, c.store, "restart", 28701, 29701, []PortMapping{{Port: 10701, Proxy: "a"}})
	if err != nil {
		t.Fatal(err)
	}
	startErr := make(chan error, 1)
	go func() { startErr <- c.manager.Start(item.ID) }()
	deadline := time.Now().Add(5 * time.Second)
	for c.manager.state(item.ID) == nil && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/instances/"+item.ID+"/selection", strings.NewReader(`{"group":"G","proxy":"a","apply":true}`))
	rec := httptest.NewRecorder()
	c.handleSelection(rec, req, item.ID)
	if rec.Code != http.StatusConflict {
		t.Fatalf("selection during mapping wait: status %d body %s, want 409", rec.Code, rec.Body.String())
	}
	restartErr := make(chan error, 1)
	go func() { restartErr <- c.manager.Restart(item.ID) }()
	if err := <-startErr; !errors.Is(err, context.Canceled) {
		t.Fatalf("first Start() error = %v, want cancelled by Restart's Stop", err)
	}
	ready.Store(true)
	if err := <-restartErr; err != nil {
		t.Fatalf("Restart() error = %v", err)
	}
	if c.manager.state(item.ID) == nil || c.manager.isStarting(item.ID) {
		t.Fatal("instance not running after a successful Restart")
	}
}

func TestPortMappingsRefuseMixedFamilyWildcardBind(t *testing.T) {
	withPortFree(t, func(int) bool { return true })
	withMappingProbes(t, nil)
	store := newManagerTestStore(t)
	_, err := store.CreateWithOptions(createInstanceOptions{
		Name: "mixfam", Config: ruleProfileWithGroup, ProxyBind: "0.0.0.0,::1", MixedPort: 28801, ControllerPort: 29801,
		PortMappings: []PortMapping{{Port: 10801, Proxy: "a"}},
	})
	if !errors.Is(err, errValidation) || !strings.Contains(err.Error(), `"0.0.0.0,::1"`) || !strings.Contains(err.Error(), "::1") {
		t.Fatalf("create error = %v, want the bind refused naming the addresses", err)
	}
	// Negative controls: wildcards only, or specific addresses only, are fine.
	for i, bind := range []string{"0.0.0.0,::", "127.0.0.1,::1"} {
		if _, err := store.CreateWithOptions(createInstanceOptions{
			Name: "ok" + bind, Config: ruleProfileWithGroup, ProxyBind: bind, MixedPort: 28810 + i, ControllerPort: 29810 + i,
			PortMappings: []PortMapping{{Port: 10810 + i, Proxy: "a"}},
		}); err != nil {
			t.Fatalf("bind %q: %v", bind, err)
		}
	}
	// Adding mappings to an existing instance with such a bind is refused too.
	plain, err := store.CreateWithOptions(createInstanceOptions{Name: "plain", Config: ruleProfileWithGroup, ProxyBind: "0.0.0.0,::1", MixedPort: 28820, ControllerPort: 29820})
	if err != nil {
		t.Fatal(err)
	}
	add := []PortMapping{{Port: 10820, Proxy: "a"}}
	if _, err := store.UpdateWithOptions(plain.ID, updateInstanceOptions{PortMappings: &add}); !errors.Is(err, errValidation) {
		t.Fatalf("update error = %v", err)
	}
}

// A panel through the fleet's mihomo passthrough must not replace the config
// of an instance whose listeners the fleet manages; PATCH stays allowed.
func TestPassthroughRefusesFullConfigReplaceWithMappings(t *testing.T) {
	withPortFree(t, func(int) bool { return true })
	withMappingProbes(t, nil)
	withMappingDial(t, func(context.Context, string) error { return nil })
	c, err := NewController(Options{DataDir: t.TempDir(), MihomoPath: writeFakeMihomo(t, true, 0), AppVersion: "test"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Shutdown(context.Background()) })
	item, err := createMappedInstance(t, c.store, "pass", 28901, 29901, []PortMapping{{Port: 10901, Proxy: "a"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.manager.Start(item.ID); err != nil {
		t.Fatal(err)
	}
	call := func(method, path string) int {
		req := httptest.NewRequest(method, "/api/mihomo/"+item.ID+path, strings.NewReader(`{"path":"/x"}`))
		rec := httptest.NewRecorder()
		c.handleMihomoProxy(rec, req)
		return rec.Code
	}
	if code := call(http.MethodPut, "/configs"); code != http.StatusConflict {
		t.Fatalf("PUT /configs: status %d, want 409", code)
	}
	// The fake mihomo has no controller, so a forwarded request ends in 502:
	// what matters is that it was forwarded rather than refused.
	if code := call(http.MethodPatch, "/configs"); code == http.StatusConflict {
		t.Fatalf("PATCH /configs refused, want it forwarded")
	}
}

func TestSubscriptionURLReplaceRefusedWhenItDropsAMappedTarget(t *testing.T) {
	withPortFree(t, func(int) bool { return true })
	withMappingProbes(t, nil)
	store := newManagerTestStore(t)
	first := &subscriptionFetchResult{Config: ruleProfileWithGroup}
	item, err := store.CreateWithOptions(createInstanceOptions{
		Name: "sub", SubscriptionURL: "https://example.com/a", SubscriptionFetch: first, MixedPort: 28951, ControllerPort: 29951,
		PortMappings: []PortMapping{{Port: 10951, Proxy: "G"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	other := &subscriptionFetchResult{Config: "proxies:\n  - {name: z, type: socks5, server: 127.0.0.1, port: 1}\n"}
	_, _, err = store.ReplaceProfileSubscription(item.ProfileID, "https://example.com/a", "https://example.com/b", other)
	if !errors.Is(err, errValidation) || !strings.Contains(err.Error(), `instance "sub"`) {
		t.Fatalf("error = %v, want the replacement refused naming the instance", err)
	} // A scheduled refresh of the same URL cannot be refused: it applies, and
	// start is where the dangling target is reported.
	if _, _, err := store.ApplySubscriptionFetchForURL(item.ProfileID, "https://example.com/a", other); err != nil {
		t.Fatalf("scheduled refresh refused: %v", err)
	}
}

// A stopped instance switching bind address or UDP re-probes every mapping
// endpoint, not only added ports.
func TestStoreReprobesMappingsWhenUDPTurnsOn(t *testing.T) {
	withPortFree(t, func(int) bool { return true })
	udpBusy := false
	withProxyBindProbe(t, func(address string) error { // TCP+UDP probe
		if udpBusy && strings.HasSuffix(address, ":10961") {
			return errors.New("udp: address already in use")
		}
		return nil
	})
	original := tcpListenProbe
	tcpListenProbe = func(string) error { return nil }
	t.Cleanup(func() { tcpListenProbe = original })
	store := newManagerTestStore(t)
	item, err := store.CreateWithOptions(createInstanceOptions{
		Name: "udp", Config: "proxies:\n  - {name: a, type: socks5, server: 127.0.0.1, port: 1}\n", MixedPort: 28961, ControllerPort: 29961,
		Mode: InstanceModeGlobalChain, PortMappings: []PortMapping{{Port: 10961, Proxy: "a"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	udpBusy = true
	on := true
	if _, err := store.UpdateWithOptions(item.ID, updateInstanceOptions{ChainUDP: &on}); !errors.Is(err, errPortUnavailable) || !strings.Contains(err.Error(), "10961") {
		t.Fatalf("ChainUDP on: error = %v, want the mapping's UDP half reported taken", err)
	}
}

// A second Start while the first waits for its mappings reports the first's
// outcome instead of claiming success early.
func TestConcurrentStartReportsTheInFlightOutcome(t *testing.T) {
	withPortFree(t, func(int) bool { return true })
	withMappingProbes(t, nil)
	withMappingDial(t, func(context.Context, string) error { return errors.New("connection refused") })
	portMappingReadyTimeout = 500 * time.Millisecond
	store := newManagerTestStore(t)
	manager := NewManager(store, writeFakeMihomo(t, true, 0))
	item, err := createMappedInstance(t, store, "twice", 28971, 29971, []PortMapping{{Port: 10971, Proxy: "a"}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Stop(item.ID) })
	first := make(chan error, 1)
	go func() { first <- manager.Start(item.ID) }()
	deadline := time.Now().Add(5 * time.Second)
	for !manager.isStarting(item.ID) && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	second := manager.Start(item.ID)
	if err := <-first; err == nil || second == nil || second.Error() != err.Error() {
		t.Fatalf("first = %v, second = %v; want the same failure from both", err, second)
	}
}

// Editing a stopped instance's mappings over HTTP: retarget, omit, clear.
func TestPutHandlerCarriesPortMappings(t *testing.T) {
	withPortFree(t, func(int) bool { return true })
	withMappingProbes(t, nil)
	c, err := NewController(Options{DataDir: t.TempDir(), MihomoPath: writeFakeMihomo(t, true, 0), AppVersion: "test"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Shutdown(context.Background()) })
	item, err := createMappedInstance(t, c.store, "put", 28981, 29981, []PortMapping{{Port: 10981, Proxy: "a"}})
	if err != nil {
		t.Fatal(err)
	}
	put := func(body string) []PortMapping {
		t.Helper()
		req := httptest.NewRequest(http.MethodPut, "/api/instances/"+item.ID, strings.NewReader(body))
		rec := httptest.NewRecorder()
		c.handleInstanceRoot(rec, req, item.ID)
		if rec.Code != http.StatusOK {
			t.Fatalf("PUT %s: status %d body %s", body, rec.Code, rec.Body.String())
		}
		got, _ := c.store.Get(item.ID)
		return got.PortMappings
	}
	if got := put(`{"portMappings":[{"port":10982,"proxy":"G"}]}`); !reflect.DeepEqual(got, []PortMapping{{Port: 10982, Proxy: "G"}}) {
		t.Fatalf("retarget: %v", got)
	}
	if got := put(`{"name":"renamed"}`); !reflect.DeepEqual(got, []PortMapping{{Port: 10982, Proxy: "G"}}) {
		t.Fatalf("omitted: %v", got)
	}
	if got := put(`{"portMappings":[]}`); len(got) != 0 {
		t.Fatalf("clear: %v", got)
	}
}

// Reallocation scans the whole range: a free port beyond the first 5000
// candidates is still found.
func TestReallocationScansPastThousandsOfTakenPorts(t *testing.T) {
	withMappingProbes(t, nil)
	store := newManagerTestStore(t)
	used := make(map[int]bool)
	for port := 10000; port <= 15120; port++ {
		used[port] = true
	}
	mappings := []PortMapping{{Port: 10000, Proxy: "a"}}
	store.mu.Lock()
	err := store.claimPortMappingsLocked("", mappings, used, "127.0.0.1", true, true)
	store.mu.Unlock()
	if err != nil || mappings[0].Port != 15121 {
		t.Fatalf("claim = %v, port %d; want 15121", err, mappings[0].Port)
	}
}

// Clone mirrors import: a target a refresh dropped is copied as held, and the
// copy reports it at start like the source does.
func TestCloneCopiesADanglingTargetAsHeld(t *testing.T) {
	withPortFree(t, func(int) bool { return true })
	withMappingProbes(t, nil)
	store := newManagerTestStore(t)
	first := &subscriptionFetchResult{Config: ruleProfileWithGroup}
	source, err := store.CreateWithOptions(createInstanceOptions{
		Name: "src", SubscriptionURL: "https://example.com/a", SubscriptionFetch: first, MixedPort: 28991, ControllerPort: 29991,
		PortMappings: []PortMapping{{Port: 10991, Proxy: "G"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	dropped := &subscriptionFetchResult{Config: "proxies:\n  - {name: a, type: socks5, server: 127.0.0.1, port: 1}\n"}
	if _, _, err := store.ApplySubscriptionFetchForURL(source.ProfileID, "https://example.com/a", dropped); err != nil {
		t.Fatal(err)
	}
	clone, err := store.Clone(source.ID, "", 0, 0)
	if err != nil || clone.PortMappings[0].Proxy != "G" {
		t.Fatalf("Clone() = %v, %v; want the mapping copied as held", clone, err)
	}
}

// While a start waits for its mappings, the passthrough and the IP check
// answer "starting" instead of reaching a controller that may not be ready.
func TestStartingInstanceRefusesPassthroughAndIPCheck(t *testing.T) {
	withPortFree(t, func(int) bool { return true })
	withMappingProbes(t, nil)
	withMappingDial(t, func(context.Context, string) error { return errors.New("connection refused") })
	portMappingReadyTimeout = 30 * time.Second
	c, err := NewController(Options{DataDir: t.TempDir(), MihomoPath: writeFakeMihomo(t, true, 0), AppVersion: "test"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Shutdown(context.Background()) })
	item, err := createMappedInstance(t, c.store, "wait", 28995, 29995, []PortMapping{{Port: 10995, Proxy: "a"}})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- c.manager.Start(item.ID) }()
	deadline := time.Now().Add(5 * time.Second)
	for c.manager.state(item.ID) == nil && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	for _, tc := range []struct {
		name string
		call func(*httptest.ResponseRecorder)
	}{
		{"passthrough", func(rec *httptest.ResponseRecorder) {
			c.handleMihomoProxy(rec, httptest.NewRequest(http.MethodPatch, "/api/mihomo/"+item.ID+"/configs", strings.NewReader(`{}`)))
		}},
		{"ip check", func(rec *httptest.ResponseRecorder) {
			c.handleIPCheck(rec, httptest.NewRequest(http.MethodPost, "/api/instances/"+item.ID+"/ip", strings.NewReader(`{}`)), item.ID)
		}},
	} {
		rec := httptest.NewRecorder()
		tc.call(rec)
		if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "instance is starting") {
			t.Fatalf("%s: status %d body %s", tc.name, rec.Code, rec.Body.String())
		}
	}
	_ = c.manager.Stop(item.ID)
	<-done
}

// Connections made before mihomo confirms its listeners never count: another
// process that held the port briefly and then left must not pass the check.
func TestWaitPortMappingsReadyIgnoresDialsBeforeConfirmation(t *testing.T) {
	confirmed := false
	var dialsBefore int
	withMappingDial(t, func(context.Context, string) error {
		if !confirmed {
			dialsBefore++
			return nil
		}
		return errors.New("connection refused")
	})
	portMappingReadyTimeout = 400 * time.Millisecond
	calls := 0
	applied := func(context.Context) (bool, string, error) {
		calls++
		confirmed = calls >= 3
		return confirmed, "controller reports mixed-port=0, want 28000", nil
	}
	err := waitPortMappingsReady(context.Background(), "127.0.0.1", []PortMapping{{Port: 10001, Proxy: "a"}}, make(chan struct{}), func() string { return "" }, applied)
	if err == nil || !strings.Contains(err.Error(), "connection refused") || dialsBefore != 0 {
		t.Fatalf("error = %v, dials before confirmation = %d; want a refused endpoint and no early dials", err, dialsBefore)
	}
}

// Both wildcards collapse to one listener, but readiness dials each family.
func TestWaitPortMappingsReadyDialsEachRequestedFamily(t *testing.T) {
	var dialed []string
	withMappingDial(t, func(_ context.Context, address string) error {
		dialed = append(dialed, address)
		return nil
	})
	err := waitPortMappingsReady(context.Background(), "0.0.0.0,::", []PortMapping{{Port: 10001, Proxy: "a"}}, make(chan struct{}),
		func() string { return "" }, func(context.Context) (bool, string, error) { return true, "", nil })
	if err != nil {
		t.Fatal(err)
	}
	seen := stringSet(dialed)
	if !seen["127.0.0.1:10001"] || !seen["[::1]:10001"] {
		t.Fatalf("dialed %v, want both loopbacks", dialed)
	}
}

// Two editors on one stopped instance: A removes 10001; B, still showing the
// old list, changes only 10002. B's save must be refused, not silently bring
// 10001 back.
func TestStaleMappingListIsRefused(t *testing.T) {
	withPortFree(t, func(int) bool { return true })
	withMappingProbes(t, nil)
	c, err := NewController(Options{DataDir: t.TempDir(), MihomoPath: writeFakeMihomo(t, true, 0), AppVersion: "test"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Shutdown(context.Background()) })
	item, err := createMappedInstance(t, c.store, "two", 28501, 29501, []PortMapping{{Port: 10001, Proxy: "a"}, {Port: 10002, Proxy: "b"}})
	if err != nil {
		t.Fatal(err)
	}
	put := func(body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPut, "/api/instances/"+item.ID, strings.NewReader(body))
		rec := httptest.NewRecorder()
		c.handleInstanceRoot(rec, req, item.ID)
		return rec
	}
	loaded := `[{"port":10001,"proxy":"a"},{"port":10002,"proxy":"b"}]`
	if rec := put(`{"portMappings":[{"port":10002,"proxy":"b"}],"expectedPortMappings":` + loaded + `}`); rec.Code != http.StatusOK {
		t.Fatalf("A: status %d body %s", rec.Code, rec.Body.String())
	}
	rec := put(`{"portMappings":[{"port":10001,"proxy":"a"},{"port":10002,"proxy":"G"}],"expectedPortMappings":` + loaded + `}`)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "port mappings changed since they were loaded") {
		t.Fatalf("B: status %d body %s, want 409", rec.Code, rec.Body.String())
	}
	if got, _ := c.store.Get(item.ID); !reflect.DeepEqual(got.PortMappings, []PortMapping{{Port: 10002, Proxy: "b"}}) {
		t.Fatalf("stored = %v, want A's list kept", got.PortMappings)
	}
}
