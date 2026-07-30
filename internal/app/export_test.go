package app

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestExportImportRoundTripReproducesFleet covers the acceptance criterion in
// docs/feature-roadmap-post-1.3.md #7: exporting a fleet (a manual profile
// with a rule-mode instance holding a proxy selection, plus a subscription
// profile with a global-chain instance) and importing the bundle into a
// fresh, empty store reproduces both profiles and both instances with their
// fields intact -- and, since the target store has no pre-existing names or
// ports to collide with, nothing gets renamed or reallocated.
func TestExportImportRoundTripReproducesFleet(t *testing.T) {
	withPortFree(t, func(int) bool { return true })

	source, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	manualProfile, err := source.CreateProfile("Manual", defaultUserConfig)
	if err != nil {
		t.Fatal(err)
	}
	ruleInstance, err := source.CreateWithOptions(createInstanceOptions{
		Name:            "HK",
		ProfileID:       manualProfile.ID,
		MixedPort:       28001,
		ControllerPort:  29001,
		SelectedProxies: map[string]string{"Proxy": "US-01"},
		SelectedGroup:   "Proxy",
		SelectedProxy:   "US-01",
		AutoRestart:     true,
	})
	if err != nil {
		t.Fatal(err)
	}

	fetched := &subscriptionFetchResult{Config: subscriptionConfig, HomeURL: "https://example.com/home", Info: &SubscriptionInfo{Total: 100}}
	subProfile, err := source.CreateSubscriptionProfile("Provider", "https://example.com/sub", true, 360, fetched)
	if err != nil {
		t.Fatal(err)
	}
	local := "- name: local-hop\n  type: socks5\n  server: 127.0.0.1\n  port: 1080\n"
	chainInstance, err := source.CreateWithOptions(createInstanceOptions{
		Name:           "Chain",
		ProfileID:      subProfile.ID,
		MixedPort:      28002,
		ControllerPort: 29002,
		ProxyBind:      "127.0.0.1,192.168.64.1",
		Mode:           InstanceModeGlobalChain,
		LocalProxies:   local,
		Chain:          []string{"local-hop", globalChainSelectGroupName},
	})
	if err != nil {
		t.Fatal(err)
	}

	bundle, err := ExportBundle(source)
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(bundle)
	if err != nil {
		t.Fatal(err)
	}

	target, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	result, err := ImportBundle(target, data)
	if err != nil {
		t.Fatal(err)
	}

	if len(result.Profiles) != 2 || len(result.Instances) != 2 {
		t.Fatalf("unexpected result shape: %+v", result)
	}
	for _, p := range result.Profiles {
		if p.Renamed {
			t.Fatalf("did not expect a rename into a fresh store: %+v", p)
		}
	}
	for _, i := range result.Instances {
		if i.Renamed || i.PortReallocated {
			t.Fatalf("did not expect a rename/reallocation into a fresh store: %+v", i)
		}
	}

	targetProfiles := target.ListProfiles()
	if len(targetProfiles) != 2 {
		t.Fatalf("expected 2 profiles, got %d", len(targetProfiles))
	}
	targetInstances := target.List()
	if len(targetInstances) != 2 {
		t.Fatalf("expected 2 instances, got %d", len(targetInstances))
	}

	newRule, ok := target.Get(result.Instances[0].ID)
	if !ok {
		t.Fatal("expected the imported rule instance to exist")
	}
	if newRule.Name != ruleInstance.Name || newRule.MixedPort != ruleInstance.MixedPort ||
		newRule.ControllerPort != ruleInstance.ControllerPort || newRule.SelectedGroup != "Proxy" ||
		newRule.SelectedProxy != "US-01" || !newRule.AutoRestart {
		t.Fatalf("rule instance fields did not round-trip: %+v", newRule)
	}
	newRuleProfile, ok := target.GetProfile(newRule.ProfileID)
	if !ok {
		t.Fatal("imported rule instance's profileId does not resolve to an imported profile")
	}
	newRuleConfig, err := target.ReadProfileConfig(newRuleProfile.ID)
	if err != nil {
		t.Fatal(err)
	}
	if newRuleConfig != defaultUserConfig {
		t.Fatalf("profile config did not round-trip: %q", newRuleConfig)
	}

	newChain, ok := target.Get(result.Instances[1].ID)
	if !ok {
		t.Fatal("expected the imported global-chain instance to exist")
	}
	if newChain.Mode != InstanceModeGlobalChain || newChain.ProxyBind != "127.0.0.1,192.168.64.1" ||
		newChain.LocalProxies != local || strings.Join(newChain.Chain, ",") != "local-hop,"+globalChainSelectGroupName {
		t.Fatalf("global-chain instance fields did not round-trip: %+v", newChain)
	}
	newSubProfile, ok := target.GetProfile(newChain.ProfileID)
	if !ok {
		t.Fatal("imported chain instance's profileId does not resolve to an imported profile")
	}
	if newSubProfile.SubscriptionURL != "https://example.com/sub" || !newSubProfile.AutoUpdate || newSubProfile.UpdateIntervalMinutes != 360 {
		t.Fatalf("subscription metadata did not round-trip: %+v", newSubProfile)
	}
	_ = chainInstance
}

// TestImportBundleRegeneratesControllerSecret covers the roadmap's explicit
// security requirement: a controller secret is a per-instance runtime
// credential, never exported, and a fresh one must be minted on import (the
// same as any brand new instance) rather than silently defaulting to the
// zero value or somehow surviving the round trip.
func TestImportBundleRegeneratesControllerSecret(t *testing.T) {
	withPortFree(t, func(int) bool { return true })

	source, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	original, err := source.Create("HK", "", defaultUserConfig, 28001, 29001)
	if err != nil {
		t.Fatal(err)
	}
	if original.Secret == "" {
		t.Fatal("expected the source instance to have a generated secret")
	}

	bundle, err := ExportBundle(source)
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(bundle)
	if err != nil {
		t.Fatal(err)
	}
	raw := string(data)
	if strings.Contains(raw, original.Secret) {
		t.Fatal("exported bundle must not contain the instance's controller secret")
	}

	target, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	result, err := ImportBundle(target, data)
	if err != nil {
		t.Fatal(err)
	}
	imported, ok := target.Get(result.Instances[0].ID)
	if !ok {
		t.Fatal("expected the imported instance to exist")
	}
	if imported.Secret == "" || imported.Secret == original.Secret {
		t.Fatalf("expected a freshly generated secret, got %q (original %q)", imported.Secret, original.Secret)
	}
}

// TestImportBundleReallocatesCollidingPort covers the roadmap's collision
// rule: an imported instance whose bundle ports are already claimed by an
// existing instance on the target machine must have its ports re-allocated,
// never silently reused/overwritten.
func TestImportBundleReallocatesCollidingPort(t *testing.T) {
	withPortFree(t, func(int) bool { return true })

	target, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	existing, err := target.Create("Existing", "", defaultUserConfig, 28001, 29001)
	if err != nil {
		t.Fatal(err)
	}

	bundle := &FleetBundle{
		Version: FleetBundleVersion,
		Profiles: []BundleProfile{
			{ID: "p1", Name: "Imported", Config: defaultUserConfig},
		},
		Instances: []BundleInstance{
			{Name: "Incoming", ProfileID: "p1", MixedPort: 28001, ControllerPort: 29001},
		},
	}
	data, err := json.Marshal(bundle)
	if err != nil {
		t.Fatal(err)
	}
	result, err := ImportBundle(target, data)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Instances) != 1 {
		t.Fatalf("expected exactly one imported instance, got %+v", result.Instances)
	}
	inst := result.Instances[0]
	if !inst.PortReallocated {
		t.Fatalf("expected the colliding port to be reported as reallocated: %+v", inst)
	}
	if inst.MixedPort == 28001 && inst.ControllerPort == 29001 {
		t.Fatalf("expected at least one port to actually move, got %+v", inst)
	}

	// The pre-existing instance must be untouched -- never clobbered.
	stillThere, ok := target.Get(existing.ID)
	if !ok || stillThere.MixedPort != 28001 || stillThere.ControllerPort != 29001 {
		t.Fatalf("existing instance was clobbered by import: %+v", stillThere)
	}
}

// TestImportBundleDedupesDuplicateNames covers the roadmap's other collision
// rule: a profile/instance name that already exists on the target machine
// must be de-duplicated (never silently merged/overwritten).
func TestImportBundleDedupesDuplicateNames(t *testing.T) {
	withPortFree(t, func(int) bool { return true })

	target, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := target.CreateProfile("Main", defaultUserConfig); err != nil {
		t.Fatal(err)
	}
	if _, err := target.Create("HK", "", defaultUserConfig, 28001, 29001); err != nil {
		t.Fatal(err)
	}

	bundle := &FleetBundle{
		Version: FleetBundleVersion,
		Profiles: []BundleProfile{
			{ID: "p1", Name: "Main", Config: defaultUserConfig},
		},
		Instances: []BundleInstance{
			{Name: "HK", ProfileID: "p1", MixedPort: 28002, ControllerPort: 29002},
		},
	}
	data, err := json.Marshal(bundle)
	if err != nil {
		t.Fatal(err)
	}
	result, err := ImportBundle(target, data)
	if err != nil {
		t.Fatal(err)
	}

	profileResult := result.Profiles[0]
	if !profileResult.Renamed || profileResult.Name == "Main" || profileResult.OriginalName != "Main" {
		t.Fatalf("expected the duplicate profile name to be renamed: %+v", profileResult)
	}
	instanceResult := result.Instances[0]
	if !instanceResult.Renamed || instanceResult.Name == "HK" || instanceResult.OriginalName != "HK" {
		t.Fatalf("expected the duplicate instance name to be renamed: %+v", instanceResult)
	}

	names := make(map[string]bool)
	for _, p := range target.ListProfiles() {
		names[p.Name] = true
	}
	if !names["Main"] || !names[profileResult.Name] {
		t.Fatalf("expected both the original and renamed profile to exist, got %+v", names)
	}
}

// TestImportBundleRejectsDanglingProfileReference covers "a profile
// referenced by an instance is imported before/with it (no dangling
// ProfileID)" from the other direction: a bundle whose instance references a
// profile id absent from the bundle's own Profiles list must be rejected
// outright, before anything is created.
func TestImportBundleRejectsDanglingProfileReference(t *testing.T) {
	target, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	bundle := &FleetBundle{
		Version: FleetBundleVersion,
		Instances: []BundleInstance{
			{Name: "Orphan", ProfileID: "missing", MixedPort: 28001, ControllerPort: 29001},
		},
	}
	data, err := json.Marshal(bundle)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ImportBundle(target, data); err == nil {
		t.Fatal("expected an error for a dangling profile reference")
	} else if !errors.Is(err, errValidation) {
		t.Fatalf("expected errValidation, got %v", err)
	}
	if len(target.List()) != 0 || len(target.ListProfiles()) != 0 {
		t.Fatal("expected nothing to be created for a rejected import")
	}
}

// TestImportBundleRejectsMalformedJSON covers the "malformed envelope
// rejected with nothing mutated" requirement for outright unparseable input.
func TestImportBundleRejectsMalformedJSON(t *testing.T) {
	target, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ImportBundle(target, []byte("{not valid json")); err == nil {
		t.Fatal("expected an error for malformed JSON")
	} else if !errors.Is(err, errValidation) {
		t.Fatalf("expected errValidation, got %v", err)
	}
	if len(target.List()) != 0 || len(target.ListProfiles()) != 0 {
		t.Fatal("expected nothing to be created for a rejected import")
	}
}

// TestImportBundleRejectsIncompatibleVersion covers the "versions
// compatible" validation requirement.
func TestImportBundleRejectsIncompatibleVersion(t *testing.T) {
	target, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	bundle := &FleetBundle{Version: FleetBundleVersion + 1}
	data, err := json.Marshal(bundle)
	if err != nil {
		t.Fatal(err)
	}
	_, err = ImportBundle(target, data)
	if err == nil {
		t.Fatal("expected an error for an incompatible bundle version")
	}
	if !errors.Is(err, errValidation) {
		t.Fatalf("expected errValidation, got %v", err)
	}
	if !strings.Contains(err.Error(), "unsupported bundle version") {
		t.Fatalf("unexpected error message: %v", err)
	}
	if len(target.List()) != 0 || len(target.ListProfiles()) != 0 {
		t.Fatal("expected nothing to be created for a rejected import")
	}
}

// TestImportBundleRejectsInvalidInstanceMode ensures a single bad instance
// (here: an unrecognized Mode) rejects the whole bundle rather than
// importing everything else and skipping just that one -- the validation
// pass runs over the entire document before any mutation starts.
func TestImportBundleRejectsInvalidInstanceMode(t *testing.T) {
	target, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	bundle := &FleetBundle{
		Version: FleetBundleVersion,
		Profiles: []BundleProfile{
			{ID: "p1", Name: "Main", Config: defaultUserConfig},
		},
		Instances: []BundleInstance{
			{Name: "Good", ProfileID: "p1", MixedPort: 28001, ControllerPort: 29001},
			{Name: "Bad", ProfileID: "p1", MixedPort: 28002, ControllerPort: 29002, Mode: "bogus-mode"},
		},
	}
	data, err := json.Marshal(bundle)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ImportBundle(target, data); err == nil {
		t.Fatal("expected an error for an invalid instance mode")
	} else if !errors.Is(err, errValidation) {
		t.Fatalf("expected errValidation, got %v", err)
	}
	if len(target.List()) != 0 || len(target.ListProfiles()) != 0 {
		t.Fatal("expected nothing to be created -- including the otherwise-valid 'Good' instance -- for a rejected import")
	}
}

// TestImportBundleRejectsOversizedConfig covers the resource bound added after
// the #7 security review: an inlined config.yaml above maxSubscriptionBytes is
// rejected before anything is written to disk.
func TestImportBundleRejectsOversizedConfig(t *testing.T) {
	target, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	bundle := &FleetBundle{
		Version: FleetBundleVersion,
		Profiles: []BundleProfile{
			{ID: "p1", Name: "Big", Config: strings.Repeat("a", maxSubscriptionBytes+1)},
		},
	}
	data, err := json.Marshal(bundle)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ImportBundle(target, data); err == nil {
		t.Fatal("expected an error for an oversized profile config")
	} else if !errors.Is(err, errValidation) {
		t.Fatalf("expected errValidation, got %v", err)
	}
	if len(target.ListProfiles()) != 0 {
		t.Fatal("expected nothing to be created for a rejected import")
	}
}

// TestImportBundleRejectsTooManyEntries covers the entry-count cap that bounds
// the O(n^2) disk-I/O amplification a huge bundle would otherwise drive.
func TestImportBundleRejectsTooManyEntries(t *testing.T) {
	target, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	profiles := make([]BundleProfile, maxBundleImportEntries+1)
	for i := range profiles {
		profiles[i] = BundleProfile{ID: fmt.Sprintf("p%d", i), Name: "x", Config: defaultUserConfig}
	}
	data, err := json.Marshal(&FleetBundle{Version: FleetBundleVersion, Profiles: profiles})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ImportBundle(target, data); err == nil {
		t.Fatal("expected an error for too many profiles")
	} else if !errors.Is(err, errValidation) {
		t.Fatalf("expected errValidation, got %v", err)
	}
	if len(target.ListProfiles()) != 0 {
		t.Fatal("expected nothing to be created for a rejected import")
	}
}

// TestImportBundleRejectsBadSubscriptionURL proves a crafted bundle cannot
// persist a file:// / javascript: subscription URL the normal PATCH path would
// have refused.
func TestImportBundleRejectsBadSubscriptionURL(t *testing.T) {
	target, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	bundle := &FleetBundle{
		Version: FleetBundleVersion,
		Profiles: []BundleProfile{
			{ID: "p1", Name: "Evil", Config: defaultUserConfig, SubscriptionURL: "file:///etc/passwd"},
		},
	}
	data, err := json.Marshal(bundle)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ImportBundle(target, data); err == nil {
		t.Fatal("expected an error for a non-http(s) subscription URL")
	} else if !errors.Is(err, errValidation) {
		t.Fatalf("expected errValidation, got %v", err)
	}
	if len(target.ListProfiles()) != 0 {
		t.Fatal("expected nothing to be created for a rejected import")
	}
}

// TestImportBundleRejectsPortAbove65535 covers the upper-bound port check that
// replaces the misleading "unable to allocate local ports" failure.
func TestImportBundleRejectsPortAbove65535(t *testing.T) {
	target, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	bundle := &FleetBundle{
		Version: FleetBundleVersion,
		Profiles: []BundleProfile{
			{ID: "p1", Name: "Main", Config: defaultUserConfig},
		},
		Instances: []BundleInstance{
			{Name: "Bad", ProfileID: "p1", MixedPort: 70000, ControllerPort: 29001},
		},
	}
	data, err := json.Marshal(bundle)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ImportBundle(target, data); err == nil {
		t.Fatal("expected an error for a port above 65535")
	} else if !errors.Is(err, errValidation) {
		t.Fatalf("expected errValidation, got %v", err)
	}
	if len(target.List()) != 0 || len(target.ListProfiles()) != 0 {
		t.Fatal("expected nothing to be created for a rejected import")
	}
}

// TestHandleImportSingleFlightRejectsConcurrent proves POST /api/import returns
// 409 (and creates nothing) while another import holds importMu.
func TestHandleImportSingleFlightRejectsConcurrent(t *testing.T) {
	target, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	c := &Controller{store: target}
	c.importMu.Lock() // simulate an import already in flight
	defer c.importMu.Unlock()

	bundle := &FleetBundle{
		Version:  FleetBundleVersion,
		Profiles: []BundleProfile{{ID: "p1", Name: "X", Config: defaultUserConfig}},
	}
	data, err := json.Marshal(bundle)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/import", bytes.NewReader(data))
	rec := httptest.NewRecorder()
	c.handleImport(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409 while an import is in flight, got %d", rec.Code)
	}
	if len(target.ListProfiles()) != 0 {
		t.Fatal("a rejected concurrent import must not create anything")
	}
}
