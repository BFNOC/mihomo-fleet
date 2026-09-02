package app

import "time"

const (
	InstanceModeRule        = "rule"
	InstanceModeGlobalChain = "global-chain"
)

const defaultUserConfig = `mixed-port: 7890
allow-lan: false
mode: rule
log-level: info
proxies: []
proxy-groups:
  - name: Proxy
    type: select
    proxies:
      - DIRECT
rules:
  - MATCH,DIRECT
`

type Options struct {
	Bind       string
	Port       int
	DataDir    string
	MihomoPath string
	AppVersion string
}

type Instance struct {
	ID                string `json:"id"`
	Name              string `json:"name"`
	ProfileID         string `json:"profileId"`
	MixedPort         int    `json:"mixedPort"`
	ProxyBind         string `json:"proxyBind,omitempty"`
	ControllerPort    int    `json:"controllerPort"`
	Secret            string `json:"secret"`
	UserConfigPath    string `json:"userConfigPath"`
	RuntimeConfigPath string `json:"runtimeConfigPath"`
	Mode              string `json:"mode,omitempty"`
	LocalProxies      string `json:"localProxies,omitempty"`
	// ConfigOverride is per-instance YAML merged onto the profile config
	// when the runtime config is generated (config.go's applyConfigOverride):
	// `prepend-<key>`/`append-<key>` splice lists, nested maps merge, any
	// other key replaces the profile's value. It is what lets two instances
	// share one subscription profile yet differ in a rule or a dns setting.
	ConfigOverride  string            `json:"configOverride,omitempty"`
	Chain           []string          `json:"chain,omitempty"`
	SelectedProxies map[string]string `json:"selectedProxies,omitempty"`
	SelectedGroup   string            `json:"selectedGroup,omitempty"`
	SelectedProxy   string            `json:"selectedProxy,omitempty"`
	CreatedAt       time.Time         `json:"createdAt"`
	UpdatedAt       time.Time         `json:"updatedAt"`
	// ConfigUpdatedAt is the last time a mutation that actually affects the
	// generated runtime config (Mode, Chain, LocalProxies, Config content,
	// ports, ProxyBind, ProfileID) touched this instance -- unlike UpdatedAt,
	// which every mutation bumps, including SetSelection and SetError (N2,
	// docs/review-2026-07-11-fix-verification-round4.md). decorateStatus
	// (manager.go) compares this against the running process's start time to
	// derive PendingRestart; UpdatedAt was doing that job before and produced
	// false positives on every selection change. Old stores predating this
	// field load it as the zero value, which is never After() a start time,
	// so pre-existing instances simply report no pending restart until their
	// first config-affecting edit -- an acceptable, self-healing gap.
	ConfigUpdatedAt time.Time `json:"configUpdatedAt,omitempty"`
	LastError       string    `json:"lastError,omitempty"`
	// AutoRestart opts this instance into the crash watchdog (manager.go):
	// when its mihomo process exits unexpectedly (not via a user Stop, the
	// Stop half of Restart, or a delete-in-flight), Manager relaunches it via
	// the same StartContext path a normal start uses, with exponential
	// backoff and a consecutive-restart cap. Persisted; defaults to false
	// (opt-in) for every existing instance loaded from a store predating
	// this field, matching PRODUCT.md's "explicit runtime evidence... should
	// drive the UI" principle -- a crash only becomes a restart when the
	// operator has explicitly said so.
	AutoRestart bool `json:"autoRestart,omitempty"`
}

type Profile struct {
	ID                    string            `json:"id"`
	Name                  string            `json:"name"`
	ConfigPath            string            `json:"configPath"`
	SubscriptionURL       string            `json:"subscriptionUrl,omitempty"`
	AutoUpdate            bool              `json:"autoUpdate"`
	UpdateIntervalMinutes int               `json:"updateIntervalMinutes,omitempty"`
	LastUpdatedAt         time.Time         `json:"lastUpdatedAt,omitempty"`
	LastUpdateError       string            `json:"lastUpdateError,omitempty"`
	HomeURL               string            `json:"homeUrl,omitempty"`
	SubscriptionInfo      *SubscriptionInfo `json:"subscriptionInfo,omitempty"`
	CreatedAt             time.Time         `json:"createdAt"`
	UpdatedAt             time.Time         `json:"updatedAt"`
}

type InstanceView struct {
	ID                string            `json:"id"`
	Name              string            `json:"name"`
	ProfileID         string            `json:"profileId"`
	ProfileName       string            `json:"profileName,omitempty"`
	ProfileConfigPath string            `json:"profileConfigPath,omitempty"`
	MixedPort         int               `json:"mixedPort"`
	ProxyBind         string            `json:"proxyBind"`
	ControllerPort    int               `json:"controllerPort"`
	UserConfigPath    string            `json:"userConfigPath"`
	RuntimeConfigPath string            `json:"runtimeConfigPath"`
	Mode              string            `json:"mode"`
	LocalProxies      string            `json:"localProxies,omitempty"`
	ConfigOverride    string            `json:"configOverride,omitempty"`
	Chain             []string          `json:"chain,omitempty"`
	SelectedProxies   map[string]string `json:"selectedProxies,omitempty"`
	SelectedGroup     string            `json:"selectedGroup,omitempty"`
	SelectedProxy     string            `json:"selectedProxy,omitempty"`
	CreatedAt         time.Time         `json:"createdAt"`
	UpdatedAt         time.Time         `json:"updatedAt"`
	LastError         string            `json:"lastError,omitempty"`
	Status            string            `json:"status"`
	PID               int               `json:"pid,omitempty"`
	// PendingRestart is true when this (running) instance's stored fields
	// have been updated more recently than the runtime config the live
	// process was actually launched from, i.e. a saved change (Mode/Chain/
	// LocalProxies/Config) has not taken effect yet and won't until the
	// instance is restarted (arch M5,
	// docs/review-2026-07-11-go-architecture.md). Always false/omitted for
	// stopped or starting instances.
	PendingRestart bool `json:"pendingRestart,omitempty"`
	// AutoRestart mirrors Instance.AutoRestart -- whether the crash watchdog
	// is armed for this instance.
	AutoRestart bool `json:"autoRestart,omitempty"`
	// RestartCount/LastExitReason/LastExitAt are the crash watchdog's
	// runtime evidence (manager.go's watchdogState), not persisted on
	// Instance: a lifetime count of successful auto-restarts and a
	// description of the most recent unexpected exit, kept for as long as
	// the Manager process runs (cleared only when the instance itself is
	// deleted, via dropWatchdog). Present even while the instance is
	// currently running again after a successful auto-restart, so the
	// operator can see that a restart happened without having to catch it
	// live. LastExitAt follows the same json convention as
	// Instance.ConfigUpdatedAt (omitempty on a time.Time does not actually
	// collapse the Go zero value -- see that field's doc comment); the
	// frontend gates display on LastExitReason, a plain string, being
	// non-empty instead.
	RestartCount   int       `json:"restartCount,omitempty"`
	LastExitReason string    `json:"lastExitReason,omitempty"`
	LastExitAt     time.Time `json:"lastExitAt,omitempty"`
}

type storedData struct {
	Instances []*Instance `json:"instances"`
	Profiles  []*Profile  `json:"profiles"`
}

type SubscriptionInfo struct {
	Upload   int64 `json:"upload"`
	Download int64 `json:"download"`
	Total    int64 `json:"total"`
	Expire   int64 `json:"expire"`
}

type ProfileProxyGroup struct {
	Name string   `json:"name"`
	Type string   `json:"type,omitempty"`
	All  []string `json:"all"`
	Now  string   `json:"now,omitempty"`
}

type SystemStatus struct {
	Bind         string `json:"bind"`
	Port         int    `json:"port"`
	DataDir      string `json:"dataDir"`
	AppVersion   string `json:"appVersion"`
	MihomoPath   string `json:"mihomoPath"`
	MihomoFound  bool   `json:"mihomoFound"`
	MihomoSource string `json:"mihomoSource"`
	Version      string `json:"version,omitempty"`
}

// BindAddressOption is one entry GET /api/system/bind-addresses returns: an
// address the web UI's proxyBind picker may offer, alongside the kind of
// address it is and -- for anything but the synthetic wildcard entry --
// which interface it came from. See hostBindAddresses (bind_addresses.go).
type BindAddressOption struct {
	Address   string `json:"address"`
	Kind      string `json:"kind"`
	Interface string `json:"interface,omitempty"`
}

// ChainCandidate is one name POST /api/instances/chain-candidates offers the
// web UI's global-chain "chain" picker. See chainCandidates
// (chain_candidates.go).
type ChainCandidate struct {
	Name string `json:"name"`
	Kind string `json:"kind"`
}

// ChainCandidatesResult is POST /api/instances/chain-candidates' response
// body.
type ChainCandidatesResult struct {
	Candidates    []ChainCandidate `json:"candidates"`
	ProviderNames []string         `json:"providerNames,omitempty"`
	LocalError    string           `json:"localError,omitempty"`
	Truncated     bool             `json:"truncated,omitempty"`
}

// CoreUpdateStatus is GET /api/system/core-update's response (feature #3,
// docs/feature-roadmap-post-1.3.md): current vs latest mihomo core version
// for this host's GOOS/GOARCH, and whether the target release actually
// publishes a checksum ApplyCoreUpdate could verify against -- true for
// essentially every current release, since GitHub's own server-computed
// asset.digest field covers it (see core_update.go's CoreUpdateStatus doc
// comment); false only for the rare asset predating that field with no
// sidecar/bundle fallback either.
type CoreUpdateStatus struct {
	// Installed is false when no mihomo binary is currently resolved at
	// all (c.mihomoFound == false) -- e.g. -mihomo was never set and
	// nothing was found alongside mihomo-fleet or on PATH. Every other
	// field is the zero value in that case; this feature updates an
	// already-installed binary in place, it does not bootstrap a fresh one
	// into a location of its own choosing.
	Installed         bool   `json:"installed"`
	CurrentVersion    string `json:"currentVersion,omitempty"`
	LatestVersion     string `json:"latestVersion,omitempty"`
	AssetName         string `json:"assetName,omitempty"`
	UpdateAvailable   bool   `json:"updateAvailable"`
	ChecksumAvailable bool   `json:"checksumAvailable"`
	CheckError        string `json:"checkError,omitempty"`
}

// CoreUpdateResult is POST /api/system/core-update's response after
// ApplyCoreUpdate successfully installs a new mihomo binary.
type CoreUpdateResult struct {
	Version string `json:"version,omitempty"`
}

// GeoFileStatus is one entry of GeoUpdateStatus.Files -- one of the four
// canonical geodata files geodata.go stages into every instance directory
// (GeoIP.dat/GeoSite.dat/Country.mmdb/ASN.mmdb).
type GeoFileStatus struct {
	Name              string `json:"name"`
	Present           bool   `json:"present"`
	SourcePath        string `json:"sourcePath,omitempty"`
	ChecksumAvailable bool   `json:"checksumAvailable"`
	UpdateAvailable   bool   `json:"updateAvailable"`
}

// GeoUpdateStatus is GET /api/system/geo-update's response.
type GeoUpdateStatus struct {
	Files      []GeoFileStatus `json:"files"`
	CheckError string          `json:"checkError,omitempty"`
}

// GeoUpdateResult is ApplyGeoUpdate's return value: the canonical names
// actually installed/updated, and a human-readable message per file that
// was skipped or failed (a partial failure -- e.g. one file's checksum
// unavailable -- never blocks the others, so this is additive information,
// not an error by itself). No longer serialized directly as an HTTP
// response body -- POST /api/system/geo-update now streams Server-Sent
// Events instead (docs/geo-update-enhancements.md P1, GeoDownloadEvent
// below), whose terminal "complete" event carries the same Updated/Errors
// pair. This type still exists as ApplyGeoUpdate's own return shape (used
// directly by geo_update_test.go) and as the value ApplyGeoUpdate itself
// extracts from ApplyGeoUpdateSSE's stream.
type GeoUpdateResult struct {
	Updated []string `json:"updated,omitempty"`
	Errors  []string `json:"errors,omitempty"`
}

// GeoDownloadEvent is one Server-Sent Event frame POST /api/system/geo-update
// streams (docs/geo-update-enhancements.md P1), written by controller.go's
// streamGeoUpdate as "event: <Event>\ndata: <json of this struct>\n\n" and
// produced by geo_update.go's ApplyGeoUpdateSSE. Event distinguishes the
// frame's shape (only the fields relevant to that Event are populated --
// the rest are zero/omitted):
//
//   - "start": a file's download is beginning. File/Index/Total set.
//   - "progress": a download is in flight. File/Index/Total plus
//     Downloaded/TotalSize/Speed set. TotalSize is 0 when the upstream
//     response carried no Content-Length.
//   - "done": one file finished, one way or another. File/Index/Total plus
//     Result ("updated"/"skipped"/"error") set; Message set only when
//     Result is "error".
//   - "complete": the whole update finished. Updated/Errors set, mirroring
//     GeoUpdateResult -- this is the terminal frame of the stream.
type GeoDownloadEvent struct {
	Event      string   `json:"event"`
	File       string   `json:"file,omitempty"`
	Index      int      `json:"index"`
	Total      int      `json:"total,omitempty"`
	Downloaded int64    `json:"downloaded"`
	TotalSize  int64    `json:"totalSize,omitempty"`
	Speed      int64    `json:"speed,omitempty"`
	Result     string   `json:"result,omitempty"`
	Message    string   `json:"message,omitempty"`
	Updated    []string `json:"updated,omitempty"`
	Errors     []string `json:"errors,omitempty"`
}

// ProxyInstanceOption is one entry in GET /api/system/proxy-instances' list
// (P2, docs/geo-update-enhancements.md section 3): the minimal shape the
// frontend's download-source dropdown needs to label an option and, once
// chosen, send back as ApplyGeoUpdateSSE's proxyInstanceId. Deliberately not
// the full InstanceView -- this endpoint's only job is picking an eligible
// (running) proxy instance, not describing it.
type ProxyInstanceOption struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	MixedPort int    `json:"mixedPort"`
}

// FleetBundleVersion is FleetBundle's current schema version (feature #7,
// docs/feature-roadmap-post-1.3.md #7). ImportBundle (export.go) accepts any
// version from fleetBundleMinVersion up to this one -- every bump so far has
// been purely additive, so an older bundle simply loads with the new fields
// at their zero values -- and rejects anything newer, so an older fleet never
// silently drops a field it does not know (encoding/json ignores unknown keys).
//
//   - 1: initial format.
//   - 2: BundleInstance.ConfigOverride.
const FleetBundleVersion = 2

const fleetBundleMinVersion = 1

// FleetBundle is the single-file fleet backup ExportBundle (export.go)
// produces for GET /api/export and ImportBundle consumes for POST
// /api/import. It is deliberately a plain JSON envelope, not a zip: every
// file this format would otherwise need (instances.json plus each profile's
// config.yaml) is inlined as a string field instead, which is what lets
// ImportBundle validate the entire document before creating a single record
// and rules out zip-slip/path-traversal on import entirely -- there is no
// per-file extraction step at all for a malicious path to hide in.
type FleetBundle struct {
	Version    int              `json:"version"`
	ExportedAt time.Time        `json:"exportedAt"`
	Profiles   []BundleProfile  `json:"profiles"`
	Instances  []BundleInstance `json:"instances"`
}

// BundleProfile is one Profile plus its on-disk config.yaml content, inlined
// as Config so the bundle is fully self-contained. ID is carried over
// verbatim from the exporting Store purely as a bundle-local join key --
// BundleInstance.ProfileID references it so ImportBundle can tell which
// profile each instance belongs to -- it is never reused as the imported
// profile's actual ID (a fresh one is minted by Store.CreateProfile, exactly
// as a brand new profile would get).
//
// SubscriptionURL/AutoUpdate/UpdateIntervalMinutes are included: per
// PRODUCT.md and the roadmap, a subscription URL is the operator's own data
// needed to reproduce a working profile, not a secret to redact. What is
// deliberately NOT carried over is SubscriptionInfo (traffic counters),
// LastUpdatedAt/LastUpdateError -- those describe the *exporting* machine's
// last fetch and would be misleading on the importer -- and HomeURL: it is
// the one profile string the UI renders as a clickable href, ProfilePatch
// has no field to plumb it through, and PatchProfile resets it on any
// subscription-URL transition anyway, so exporting it would be a dishonest
// schema (silently dropped on import) with an href-injection footgun if ever
// wired up. ImportBundle leaves the excluded fields at their zero value and
// lets the next refresh (manual or scheduled) repopulate them for real.
type BundleProfile struct {
	ID                    string `json:"id"`
	Name                  string `json:"name"`
	Config                string `json:"config"`
	SubscriptionURL       string `json:"subscriptionUrl,omitempty"`
	AutoUpdate            bool   `json:"autoUpdate,omitempty"`
	UpdateIntervalMinutes int    `json:"updateIntervalMinutes,omitempty"`
}

// BundleInstance is one Instance, minus everything that is either a
// per-machine runtime secret or a local filesystem path meaningless on the
// importing machine:
//
//   - Secret is never exported. It is a controller-only runtime credential,
//     not fleet-portable data; ImportBundle mints a fresh one via the exact
//     same path Store.CreateWithOptions already uses for a brand new
//     instance.
//   - UserConfigPath/RuntimeConfigPath are absolute paths under the
//     exporting machine's data directory; the importer regenerates both from
//     its own data directory when it creates the profile/instance directory.
//   - ID/CreatedAt/UpdatedAt/ConfigUpdatedAt/LastError describe the
//     exporting record's own history, not portable state; the imported
//     instance gets fresh values the same way any newly created instance
//     does.
//
// ProfileID refers to a BundleProfile.ID within this same bundle (see that
// field's doc comment), not a real Store id.
type BundleInstance struct {
	Name            string            `json:"name"`
	ProfileID       string            `json:"profileId"`
	MixedPort       int               `json:"mixedPort"`
	ProxyBind       string            `json:"proxyBind,omitempty"`
	ControllerPort  int               `json:"controllerPort"`
	Mode            string            `json:"mode,omitempty"`
	LocalProxies    string            `json:"localProxies,omitempty"`
	ConfigOverride  string            `json:"configOverride,omitempty"`
	Chain           []string          `json:"chain,omitempty"`
	SelectedProxies map[string]string `json:"selectedProxies,omitempty"`
	SelectedGroup   string            `json:"selectedGroup,omitempty"`
	SelectedProxy   string            `json:"selectedProxy,omitempty"`
	AutoRestart     bool              `json:"autoRestart,omitempty"`
}

// ImportItemResult reports what actually happened to one bundle entry
// (export.go's ImportBundle). Name is what was actually created; it only
// differs from OriginalName when Renamed is true (a name collision with an
// already-existing profile/instance on this machine, or with an earlier
// entry from the same bundle, was resolved by appending " (2)", " (3)", ...
// -- see dedupName). PortReallocated/MixedPort/ControllerPort are only
// meaningful for instances: PortReallocated is true when the bundle's
// original mixed/controller ports collided with a port already in use on
// this machine and had to be re-allocated (never silently overwriting the
// existing instance holding that port); MixedPort/ControllerPort always
// report the port the instance actually ended up with, reallocated or not.
//
// There is deliberately no "skipped" outcome: every profile and instance in
// a validated bundle is always created, with renaming/reallocation applied
// as needed -- see ImportBundle's doc comment for why validate-then-mutate
// makes a partial "some created, some skipped" result impossible in normal
// operation.
type ImportItemResult struct {
	OriginalName    string `json:"originalName"`
	Name            string `json:"name"`
	ID              string `json:"id"`
	Renamed         bool   `json:"renamed,omitempty"`
	PortReallocated bool   `json:"portReallocated,omitempty"`
	MixedPort       int    `json:"mixedPort,omitempty"`
	ControllerPort  int    `json:"controllerPort,omitempty"`
}

// ImportResult is POST /api/import's response body: exactly what was
// created for every profile and instance in the bundle (see
// ImportItemResult), in the same order they appeared in the bundle.
type ImportResult struct {
	Profiles  []ImportItemResult `json:"profiles"`
	Instances []ImportItemResult `json:"instances"`
}
