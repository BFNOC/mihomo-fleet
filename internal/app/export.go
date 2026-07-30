package app

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/url"
	"sort"
	"strings"
	"time"
)

// ExportBundle serializes store's entire fleet -- every profile (with its
// config.yaml content inlined) and every instance -- into a single portable
// document (feature #7, docs/feature-roadmap-post-1.3.md #7). See
// ExportBundle (types.go) for exactly what is and isn't carried over.
//
// Profiles/instances are sorted by CreatedAt (their natural creation order)
// so the bundle's field order -- and therefore ImportBundle's creation order
// on the other end -- is deterministic rather than following store's
// internal map iteration order.
func ExportBundle(store *Store) (*FleetBundle, error) {
	profiles := store.ListProfiles()
	sort.Slice(profiles, func(i, j int) bool {
		return profiles[i].CreatedAt.Before(profiles[j].CreatedAt)
	})
	bundle := &FleetBundle{
		Version:    FleetBundleVersion,
		ExportedAt: time.Now().UTC(),
		Profiles:   make([]BundleProfile, 0, len(profiles)),
	}
	for _, profile := range profiles {
		config, err := store.ReadProfileConfig(profile.ID)
		if err != nil {
			return nil, fmt.Errorf("export profile %q: %w", profile.Name, err)
		}
		bundle.Profiles = append(bundle.Profiles, BundleProfile{
			ID:                    profile.ID,
			Name:                  profile.Name,
			Config:                config,
			SubscriptionURL:       profile.SubscriptionURL,
			AutoUpdate:            profile.AutoUpdate,
			UpdateIntervalMinutes: profile.UpdateIntervalMinutes,
		})
	}

	instances := store.List()
	sort.Slice(instances, func(i, j int) bool {
		return instances[i].CreatedAt.Before(instances[j].CreatedAt)
	})
	bundle.Instances = make([]BundleInstance, 0, len(instances))
	for _, item := range instances {
		bundle.Instances = append(bundle.Instances, BundleInstance{
			Name:            item.Name,
			ProfileID:       item.ProfileID,
			MixedPort:       item.MixedPort,
			ProxyBind:       item.ProxyBind,
			ControllerPort:  item.ControllerPort,
			Mode:            item.Mode,
			LocalProxies:    item.LocalProxies,
			Chain:           append([]string{}, item.Chain...),
			SelectedProxies: cloneStringMap(item.SelectedProxies),
			SelectedGroup:   item.SelectedGroup,
			SelectedProxy:   item.SelectedProxy,
			AutoRestart:     item.AutoRestart,
		})
	}
	return bundle, nil
}

// ImportBundle validates data as a FleetBundle and, only if the entire
// document checks out, creates every profile and instance it describes in
// store (feature #7). This is the "validate-then-mutate" contract the
// roadmap calls for: validateBundle runs entirely against the parsed
// in-memory bundle -- no store reads, no store writes, no disk access -- so
// a single malformed or incompatible entry rejects the whole import before
// anything is created. Only once that full pass succeeds does the creation
// loop below start calling store's own create paths (CreateProfile/
// PatchProfile/CreateWithOptions), the same locked paths a normal
// UI-driven create/edit would use -- this function never writes
// instances.json or a config.yaml by hand.
//
// A failure *during* creation (disk I/O, or any other error the upfront
// validation could not have caught) rolls back every profile/instance this
// call itself created, via the same Delete/DeleteProfile paths, so a failed
// import never leaves a partial fleet behind either.
func ImportBundle(store *Store, data []byte) (*ImportResult, error) {
	var bundle FleetBundle
	if err := json.Unmarshal(data, &bundle); err != nil {
		return nil, validationError{msg: fmt.Sprintf("malformed import bundle: %v", err)}
	}
	if err := validateBundle(&bundle); err != nil {
		return nil, err
	}
	return createBundle(store, &bundle)
}

// maxBundleImportEntries caps how many profiles or instances one import
// bundle may carry. A real fleet is a handful; this is generous headroom
// while refusing a crafted bundle that would otherwise drive hundreds of
// thousands of MkdirAll + writeFileAtomic + full-instances.json rewrites
// (O(n^2) disk I/O) inside a single authenticated request.
const maxBundleImportEntries = 500

// validateBundle runs every check createProfileRecordLocked/
// createInstanceLocked would themselves perform at create time, but against
// the bundle's in-memory content only -- see ImportBundle's doc comment for
// why this must not touch the store. It additionally bounds the resources one
// import can consume (entry counts, per-config size) and rejects a
// subscription URL scheme the normal PATCH path would have refused, since
// createBundle's PatchProfile stores it verbatim. Every returned error is
// validationError-wrapped (or already is one, e.g. from normalizeInstanceMode)
// so handleImport classifies it (errors.Is(err, errValidation)) as 400,
// matching every other malformed-request rejection in this package.
func validateBundle(bundle *FleetBundle) error {
	if bundle.Version != FleetBundleVersion {
		return validationError{msg: fmt.Sprintf("unsupported bundle version %d (expected %d)", bundle.Version, FleetBundleVersion)}
	}
	if len(bundle.Profiles) > maxBundleImportEntries {
		return validationError{msg: fmt.Sprintf("bundle has too many profiles (%d, max %d)", len(bundle.Profiles), maxBundleImportEntries)}
	}
	if len(bundle.Instances) > maxBundleImportEntries {
		return validationError{msg: fmt.Sprintf("bundle has too many instances (%d, max %d)", len(bundle.Instances), maxBundleImportEntries)}
	}

	profileByID := make(map[string]*BundleProfile, len(bundle.Profiles))
	for i := range bundle.Profiles {
		profile := &bundle.Profiles[i]
		if strings.TrimSpace(profile.ID) == "" {
			return validationError{msg: fmt.Sprintf("profile %d is missing an id", i)}
		}
		if _, ok := profileByID[profile.ID]; ok {
			return validationError{msg: fmt.Sprintf("profile id %q is duplicated in bundle", profile.ID)}
		}
		// Cap each inlined config.yaml at the ceiling the subscription fetch
		// path enforces (maxSubscriptionBytes, subscription.go): it is written
		// to disk verbatim and re-parsed on every proxies-tab poll, so an
		// unbounded blob is a persistent per-request amplification that bypasses
		// every other ingress cap.
		if len(profile.Config) > maxSubscriptionBytes {
			return validationError{msg: fmt.Sprintf("profile %q config exceeds %d bytes", profile.Name, maxSubscriptionBytes)}
		}
		// Mirror the PATCH handler's subscription-URL check: createBundle's
		// PatchProfile stores the string verbatim, so without this a crafted
		// bundle could persist a file://... or javascript:... URL.
		if raw := strings.TrimSpace(profile.SubscriptionURL); raw != "" {
			parsed, err := url.Parse(raw)
			if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
				return validationError{msg: fmt.Sprintf("profile %q subscription URL must start with http:// or https://", profile.Name)}
			}
		}
		profileByID[profile.ID] = profile
	}

	for i, inst := range bundle.Instances {
		label := inst.Name
		if label == "" {
			label = fmt.Sprintf("#%d", i)
		}
		profile, ok := profileByID[inst.ProfileID]
		if !ok {
			return validationError{msg: fmt.Sprintf("instance %q references unknown profile %q", label, inst.ProfileID)}
		}
		if inst.MixedPort < 0 || inst.ControllerPort < 0 {
			return validationError{msg: fmt.Sprintf("instance %q has a negative port", label)}
		}
		// Reject > 65535 up front with a precise message; otherwise it passes
		// here, fails isPortFree at create, and the reallocation retry's
		// allocatePort (which caps at 65535) returns 0 -> the whole import
		// rolls back with the misleading "unable to allocate local ports".
		if inst.MixedPort > 65535 || inst.ControllerPort > 65535 {
			return validationError{msg: fmt.Sprintf("instance %q has a port above 65535", label)}
		}
		if inst.MixedPort > 0 && inst.MixedPort == inst.ControllerPort {
			return validationError{msg: fmt.Sprintf("instance %q: mixed and controller ports must differ", label)}
		}
		if _, err := normalizeInstanceMode(inst.Mode); err != nil {
			return err
		}
		if _, err := normalizeProxyBind(inst.ProxyBind); err != nil {
			// Mirror createInstanceLocked's wrapping: normalizeProxyBind
			// returns a plain error, classified here as errValidation without
			// altering its message text.
			return validationError{msg: err.Error()}
		}
		if instanceMode(inst.Mode) == InstanceModeGlobalChain {
			if _, _, err := parseLocalProxyItems(inst.LocalProxies); err != nil {
				return err
			}
			if len(normalizeChainNames(inst.Chain)) > 0 {
				candidate := &Instance{LocalProxies: inst.LocalProxies, Chain: inst.Chain}
				if _, err := parseGlobalChainProxyGroups(profile.Config, candidate); err != nil {
					return validationError{msg: err.Error()}
				}
			}
		}
	}
	return nil
}

// createBundle is ImportBundle's mutating half, split out so ImportBundle
// itself stays a thin "parse, validate, then call this" wrapper. bundle has
// already passed validateBundle by the time this runs.
func createBundle(store *Store, bundle *FleetBundle) (*ImportResult, error) {
	existingProfileNames := make(map[string]bool)
	for _, profile := range store.ListProfiles() {
		existingProfileNames[profile.Name] = true
	}
	existingInstanceNames := make(map[string]bool)
	for _, item := range store.List() {
		existingInstanceNames[item.Name] = true
	}

	var createdProfileIDs []string
	var createdInstanceIDs []string
	rollback := func() {
		rollbackImport(store, createdInstanceIDs, createdProfileIDs)
	}

	profileIDMap := make(map[string]string, len(bundle.Profiles))
	profileResults := make([]ImportItemResult, 0, len(bundle.Profiles))
	for _, bp := range bundle.Profiles {
		name := strings.TrimSpace(bp.Name)
		if name == "" {
			name = "Imported profile"
		}
		dedupedName := dedupName(name, existingProfileNames)
		existingProfileNames[dedupedName] = true

		profile, err := store.CreateProfile(dedupedName, bp.Config)
		if err != nil {
			rollback()
			return nil, fmt.Errorf("create profile %q: %w", name, err)
		}
		createdProfileIDs = append(createdProfileIDs, profile.ID)
		profileIDMap[bp.ID] = profile.ID

		if bp.SubscriptionURL != "" {
			url := bp.SubscriptionURL
			autoUpdate := bp.AutoUpdate
			interval := bp.UpdateIntervalMinutes
			if _, err := store.PatchProfile(profile.ID, ProfilePatch{
				SubscriptionURL:       &url,
				AutoUpdate:            &autoUpdate,
				UpdateIntervalMinutes: &interval,
			}); err != nil {
				rollback()
				return nil, fmt.Errorf("set subscription metadata for profile %q: %w", name, err)
			}
		}

		profileResults = append(profileResults, ImportItemResult{
			OriginalName: name,
			Name:         dedupedName,
			ID:           profile.ID,
			Renamed:      dedupedName != name,
		})
	}

	instanceResults := make([]ImportItemResult, 0, len(bundle.Instances))
	for _, bi := range bundle.Instances {
		name := strings.TrimSpace(bi.Name)
		if name == "" {
			name = "Imported instance"
		}
		dedupedName := dedupName(name, existingInstanceNames)
		existingInstanceNames[dedupedName] = true

		opts := createInstanceOptions{
			Name:            dedupedName,
			ProfileID:       profileIDMap[bi.ProfileID],
			MixedPort:       bi.MixedPort,
			ProxyBind:       bi.ProxyBind,
			ControllerPort:  bi.ControllerPort,
			Mode:            bi.Mode,
			LocalProxies:    bi.LocalProxies,
			Chain:           append([]string{}, bi.Chain...),
			SelectedProxies: cloneStringMap(bi.SelectedProxies),
			SelectedGroup:   bi.SelectedGroup,
			SelectedProxy:   bi.SelectedProxy,
			AutoRestart:     bi.AutoRestart,
		}
		item, reallocated, err := createImportedInstance(store, opts)
		if err != nil {
			rollback()
			return nil, fmt.Errorf("create instance %q: %w", name, err)
		}
		createdInstanceIDs = append(createdInstanceIDs, item.ID)

		instanceResults = append(instanceResults, ImportItemResult{
			OriginalName:    name,
			Name:            dedupedName,
			ID:              item.ID,
			Renamed:         dedupedName != name,
			PortReallocated: reallocated,
			MixedPort:       item.MixedPort,
			ControllerPort:  item.ControllerPort,
		})
	}

	return &ImportResult{Profiles: profileResults, Instances: instanceResults}, nil
}

// createImportedInstance creates one bundle instance, re-allocating its
// mixed/controller ports -- via the exact allocatePort scan a fresh create
// already uses, started from the bundle's own port numbers so an instance
// that doesn't actually collide keeps its original ports -- instead of
// failing outright when either port is already claimed by an existing
// instance on this machine. This is the collision rule the roadmap calls
// for: never silently overwrite/reuse a live port, always re-allocate.
func createImportedInstance(store *Store, opts createInstanceOptions) (*Instance, bool, error) {
	item, err := store.CreateWithOptions(opts)
	if err == nil {
		return item, false, nil
	}
	if !errors.Is(err, errPortUnavailable) {
		return nil, false, err
	}
	retry := opts
	retry.MixedStart = opts.MixedPort
	retry.ControllerStart = opts.ControllerPort
	retry.MixedPort = 0
	retry.ControllerPort = 0
	item, err = store.CreateWithOptions(retry)
	if err != nil {
		return nil, false, err
	}
	return item, true, nil
}

// rollbackImport undoes a partially-completed createBundle: instances first
// (reverse creation order), then profiles (also reverse order) -- by the
// time every created instance is gone, every created profile is guaranteed
// unreferenced, exactly what DeleteProfile requires. Both Delete/
// DeleteProfile calls are best-effort: a failure here means the store's own
// save already failed once for this record, so there is nothing more
// meaningful to do than log it and move on to the rest of the rollback.
func rollbackImport(store *Store, instanceIDs, profileIDs []string) {
	for i := len(instanceIDs) - 1; i >= 0; i-- {
		if err := store.Delete(instanceIDs[i]); err != nil {
			log.Printf("import rollback: delete instance %s failed: %v", instanceIDs[i], err)
		}
	}
	for i := len(profileIDs) - 1; i >= 0; i-- {
		if err := store.DeleteProfile(profileIDs[i]); err != nil {
			log.Printf("import rollback: delete profile %s failed: %v", profileIDs[i], err)
		}
	}
}

// dedupName returns name unchanged if it isn't in taken; otherwise appends
// " (2)", " (3)", ... until it finds one that isn't (mirrors uniqueSlug's
// -2/-3 disambiguation in util.go, but for the human-facing Name field
// rather than a slug id). Store itself never enforces Name uniqueness --
// two manually created profiles/instances can already share a display name
// -- but reproducing an entire fleet on top of one that already has a
// same-named entry would otherwise make the two indistinguishable in the
// instance switcher, so ImportBundle enforces it defensively for its own
// output.
func dedupName(name string, taken map[string]bool) string {
	if !taken[name] {
		return name
	}
	for i := 2; ; i++ {
		candidate := fmt.Sprintf("%s (%d)", name, i)
		if !taken[candidate] {
			return candidate
		}
	}
}
