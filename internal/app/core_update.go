package app

// Feature #3 (docs/feature-roadmap-post-1.3.md): mihomo core binary
// auto-update. Queries MetaCubeX/mihomo's GitHub releases, resolves the
// asset for this host's GOOS/GOARCH, and -- ONLY after verifying its
// SHA-256 against an upstream-published checksum -- atomically swaps it
// into place at the same path the fleet already execs (c.mihomoPath).
//
// ASSET LAYOUT (confirmed live against api.github.com on 2026-07-30, not
// assumed from docs): a release's assets are named
// "mihomo-{goos}-{arch}-{tag}.{gz|zip}", e.g.
// "mihomo-linux-amd64-v1.19.29.gz" or "mihomo-windows-amd64-v1.19.29.zip".
// unix/darwin assets are a plain gzip of the raw executable (no tar
// wrapper); windows assets are a zip containing exactly one .exe entry.
// Both were verified by actually downloading one of each and inspecting
// the decompressed content (`gzip -l`, Python's zipfile) during this
// feature's development.
//
// CHECKSUM SCHEME -- CORRECTED, confirmed live against api.github.com on
// 2026-07-30 (a first pass here mistakenly concluded "no checksum" after
// only searching for a checksums.txt/*.sha256 FILE among the release's
// assets -- that search terms out empty for MetaCubeX/mihomo's normal
// tagged releases, but it was the wrong signal to look at). GitHub's REST
// API attaches a server-computed "digest" field directly on every release
// asset object -- format "sha256:<hex>" -- independent of whatever files
// the release itself publishes. Checked on v1.19.29: all 127 assets carry a
// populated digest. That field, not a sidecar/bundle file, is this file's
// PRIMARY checksum source (resolveChecksum below); a sidecar/bundle search
// remains as a secondary fallback for the genuine edge case of an asset
// uploaded before GitHub started computing this field. Only when NEITHER
// source yields a well-formed digest does resolveChecksum return
// errChecksumUnavailable and ApplyCoreUpdate refuse to install -- which,
// given the above, should now be rare against current stable releases, not
// the common case a first pass here wrongly assumed.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"
)

// mihomoReleaseAPI is a var (not a const) so core_update_test.go can point
// it at a local httptest.Server -- the same "package-level indirection"
// pattern subscription.go's validateSubscriptionTargetFn already uses for
// the same reason (testing M2, docs/review-2026-07-11-testing-quality.md).
var mihomoReleaseAPI = "https://api.github.com/repos/MetaCubeX/mihomo/releases/latest"

// errMihomoNotFound/errCoreUpdateBusy are sentinels handleCoreUpdate
// (controller.go) checks with errors.Is to pick an HTTP status, mirroring
// the errAlreadyRunning/errReloadNetworkChanged pattern manager.go already
// uses for the same reason (classify without parsing error text).
var (
	errMihomoNotFound = errors.New("mihomo binary not found; set -mihomo or place it alongside mihomo-fleet, then retry")
	errCoreUpdateBusy = errors.New("stop all instances before updating the mihomo core")
)

// githubReleaseAsset/githubRelease mirror the subset of GitHub's release
// API response this package reads. Read as the narrow shape it actually
// needs (name/size/download URL, tag name, asset list) rather than the
// full response GitHub sends.
type githubReleaseAsset struct {
	Name               string `json:"name"`
	Size               int64  `json:"size"`
	BrowserDownloadURL string `json:"browser_download_url"`
	// Digest is GitHub's own server-computed checksum for this asset,
	// "sha256:<hex>" -- see this file's header comment. Populated by the
	// real API for every asset checked during this feature's development;
	// parseAssetDigest treats an empty/malformed value as unavailable
	// rather than erroring, so an older asset predating this field falls
	// through to resolveChecksum's sidecar/bundle fallback instead of
	// breaking outright.
	Digest string `json:"digest"`
}

type githubRelease struct {
	TagName string               `json:"tag_name"`
	Assets  []githubReleaseAsset `json:"assets"`
}

// newUpdateHTTPClient builds the SSRF-hardened client core/geo update
// downloads share (hardened_transport.go). Unlike newSubscriptionHTTPClient
// (25s total, sized for one small YAML fetch), this client sets NO overall
// Client.Timeout -- a legitimate mihomo binary or geoip.dat download can
// take well over 25s on a slow connection, and callers bound duration with
// a per-call context deadline instead (see handleCoreUpdate/handleGeoUpdate
// in controller.go), so a genuinely slow-but-completing transfer is not cut
// off by an arbitrary global timer while a truly stuck one still cannot
// hang forever.
func newUpdateHTTPClient() *http.Client {
	dialer := &net.Dialer{Timeout: 8 * time.Second, KeepAlive: 30 * time.Second}
	client := &http.Client{Transport: newHardenedTransport(dialer)}
	// validateUpdateTargetFn (hardened_transport.go), not the plain
	// validateHardenedTarget subscription fetches use: every redirect hop
	// here must additionally be https and one of GitHub's own hosts.
	client.CheckRedirect = hardenedCheckRedirect(5, func(ctx context.Context, u *url.URL) error {
		return validateUpdateTargetFn(ctx, u, "download")
	})
	return client
}

func fetchGitHubRelease(ctx context.Context, client *http.Client, apiURL string) (*githubRelease, error) {
	data, err := fetchBytes(ctx, client, apiURL, 4<<20, "application/vnd.github+json")
	if err != nil {
		return nil, fmt.Errorf("fetch release metadata: %w", err)
	}
	var release githubRelease
	if err := json.Unmarshal(data, &release); err != nil {
		return nil, fmt.Errorf("parse release metadata: %w", err)
	}
	if strings.TrimSpace(release.TagName) == "" {
		return nil, errors.New("release metadata has no tag_name")
	}
	return &release, nil
}

func findAssetByName(assets []githubReleaseAsset, name string) *githubReleaseAsset {
	for i := range assets {
		if assets[i].Name == name {
			return &assets[i]
		}
	}
	return nil
}

// checksumBundleNames lists the filenames a release might publish a
// combined checksum bundle under. Matched case-insensitively against every
// asset name.
var checksumBundleNames = map[string]bool{
	"checksums.txt":  true,
	"checksum.txt":   true,
	"sha256sum.txt":  true,
	"sha256sums.txt": true,
	"sha256sums":     true,
}

// findChecksumAsset looks for a checksum source among a release's assets: a
// per-asset sidecar ("assetName.sha256"/"assetName.sha256sum") is name-
// specific and unambiguous; a combined bundle (checksums.txt/
// sha256sum.txt/...) is a single shared file whose presence does NOT by
// itself prove it actually contains a line for assetName -- only
// resolveChecksum's parse of its content can confirm that. CoreUpdateStatus
// uses this function's return alone (no download) for a cheap
// "ChecksumAvailable" hint that can occasionally be a false positive for a
// bundle that turns out not to cover this specific asset; ApplyCoreUpdate
// always calls resolveChecksum for real before ever downloading the
// asset itself, so that false positive can only ever surface as "abort,
// nothing changed", never as a false pass.
//
// Returns (nil, nil) when neither exists -- callers must treat that as
// errChecksumUnavailable, not as "nothing to verify against so skip
// verification".
func findChecksumAsset(assets []githubReleaseAsset, assetName string) (bundle, sidecar *githubReleaseAsset) {
	for i := range assets {
		a := &assets[i]
		switch {
		case strings.EqualFold(a.Name, assetName+".sha256"), strings.EqualFold(a.Name, assetName+".sha256sum"):
			sidecar = a
		case checksumBundleNames[strings.ToLower(a.Name)]:
			bundle = a
		}
	}
	return bundle, sidecar
}

// assetDigestPrefix is the format GitHub's server-computed asset.digest
// field uses: "sha256:<64 lowercase hex chars>".
const assetDigestPrefix = "sha256:"

// parseAssetDigest extracts the hex digest from a githubReleaseAsset.Digest
// value ("sha256:<hex>"), returning errChecksumUnavailable for anything
// else (empty, a different algorithm prefix, or a malformed hex length) --
// treated identically to "no checksum published", never as a weaker partial
// check.
func parseAssetDigest(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if !strings.HasPrefix(raw, assetDigestPrefix) {
		return "", errChecksumUnavailable
	}
	digest := strings.ToLower(strings.TrimPrefix(raw, assetDigestPrefix))
	if len(digest) != 64 {
		return "", errChecksumUnavailable
	}
	return digest, nil
}

// resolveChecksum returns the expected SHA-256 hex digest for assetName.
// PRIMARY source: the matching asset's own GitHub-computed .Digest field
// (no extra request -- it is already in the release metadata this package
// fetched to find the asset in the first place). FALLBACK, only when the
// asset has no usable digest: whichever checksum sidecar/bundle
// findChecksumAsset finds (a sidecar is preferred over a bundle), covering
// the edge case of an asset uploaded before GitHub started computing this
// field. Returns errChecksumUnavailable when neither source yields a
// well-formed digest -- see this file's header comment.
func resolveChecksum(ctx context.Context, client *http.Client, assets []githubReleaseAsset, assetName string) (string, error) {
	if asset := findAssetByName(assets, assetName); asset != nil {
		if digest, err := parseAssetDigest(asset.Digest); err == nil {
			return digest, nil
		}
	}

	bundle, sidecar := findChecksumAsset(assets, assetName)
	if sidecar != nil {
		data, err := fetchBytes(ctx, client, sidecar.BrowserDownloadURL, maxChecksumBundleBytes, "")
		if err != nil {
			return "", fmt.Errorf("fetch checksum sidecar: %w", err)
		}
		return parseChecksumSidecar(data)
	}
	if bundle != nil {
		data, err := fetchBytes(ctx, client, bundle.BrowserDownloadURL, maxChecksumBundleBytes, "")
		if err != nil {
			return "", fmt.Errorf("fetch checksum bundle: %w", err)
		}
		return parseChecksumBundle(data, assetName)
	}
	return "", errChecksumUnavailable
}

// mihomoAssetArch maps a Go GOARCH to the architecture token mihomo's
// release asset names use. amd64/arm64/386/mips64/mips64le/riscv64/ppc64le/
// s390x match Go's own GOARCH string verbatim; arm does not (Go's GOARCH
// alone does not expose the GOARM variant, which mihomo's asset names
// encode as armv5/armv6/armv7) -- armv7 is used as the documented
// best-effort default, matching the most common 32-bit ARM Linux target.
func mihomoAssetArch(goarch string) (string, error) {
	switch goarch {
	case "amd64", "arm64", "386", "mips64", "mips64le", "riscv64", "ppc64le", "s390x":
		return goarch, nil
	case "arm":
		return "armv7", nil
	default:
		return "", fmt.Errorf("unsupported GOARCH %q for mihomo core update", goarch)
	}
}

// mihomoAssetExt maps a Go GOOS to the compression format mihomo publishes
// its release asset in: a zip archive on windows (holding one .exe entry),
// a plain gzip of the raw executable everywhere else this function
// supports.
func mihomoAssetExt(goos string) (string, error) {
	switch goos {
	case "windows":
		return "zip", nil
	case "linux", "darwin", "freebsd":
		return "gz", nil
	default:
		return "", fmt.Errorf("unsupported GOOS %q for mihomo core update", goos)
	}
}

// mihomoAssetName builds the asset filename mihomo's release process uses
// for goos/goarch/tag, e.g. mihomoAssetName("linux", "amd64", "v1.19.29") ->
// "mihomo-linux-amd64-v1.19.29.gz".
func mihomoAssetName(goos, goarch, tag string) (string, error) {
	arch, err := mihomoAssetArch(goarch)
	if err != nil {
		return "", err
	}
	ext, err := mihomoAssetExt(goos)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("mihomo-%s-%s-%s.%s", goos, arch, tag, ext), nil
}

// goToolchainToken/mihomoVersionPattern mirror format.ts's
// shortMihomoVersion (internal/app/web-src/format.ts): mihomo's "-v" output
// embeds a go toolchain token ("Mihomo Meta v1.19.29 linux amd64 with
// go1.24.5 ...", confirmed against strings(1) output on the mihomo binary
// checked into this repo) that would otherwise be mistaken for the
// mihomo version itself, so it is stripped first.
var (
	goToolchainToken     = regexp.MustCompile(`(?i)\bgo\d+(\.\d+)*`)
	mihomoVersionPattern = regexp.MustCompile(`(?i)v?(\d+\.\d+(?:\.\d+)?(?:[-+][0-9a-zA-Z.]+)?)`)
)

// extractMihomoVersion pulls the bare version token (no "v" prefix) out of
// detectVersion's raw probe output, or "" if it doesn't contain one this
// pattern recognizes.
func extractMihomoVersion(raw string) string {
	cleaned := goToolchainToken.ReplaceAllString(raw, " ")
	match := mihomoVersionPattern.FindStringSubmatch(cleaned)
	if match == nil {
		return ""
	}
	return match[1]
}

// CoreUpdateStatus is the read-only "check for update" half of feature #3:
// current vs latest mihomo core version for this host's GOOS/GOARCH, and
// whether the release actually publishes a checksum this feature could
// verify against -- true for essentially every current release, since
// GitHub's own asset.digest field covers it (see this file's header
// comment); false only for the rare asset predating that field with no
// sidecar/bundle fallback either. Never returns an error -- a check failure
// (no network, GitHub API hiccup, unsupported GOOS/GOARCH) is reported via
// CheckError in the result instead, the same way a subscription profile's
// LastUpdateError is surfaced rather than failing the whole request.
func (c *Controller) CoreUpdateStatus(ctx context.Context) CoreUpdateStatus {
	status := CoreUpdateStatus{Installed: c.mihomoFound && c.mihomoPath != ""}
	if !status.Installed {
		return status
	}
	status.CurrentVersion = extractMihomoVersion(c.currentMihomoVersion())

	release, err := fetchGitHubRelease(ctx, c.updateClient, mihomoReleaseAPI)
	if err != nil {
		status.CheckError = err.Error()
		return status
	}
	status.LatestVersion = strings.TrimPrefix(release.TagName, "v")

	assetName, err := mihomoAssetName(runtime.GOOS, runtime.GOARCH, release.TagName)
	if err != nil {
		status.CheckError = err.Error()
		return status
	}
	status.AssetName = assetName
	status.UpdateAvailable = status.CurrentVersion != "" && status.LatestVersion != "" && status.CurrentVersion != status.LatestVersion

	// Cheap check only -- no extra request. The primary source
	// (asset.Digest) is already sitting in release.Assets from the fetch
	// above; the sidecar/bundle fallback is only a name-existence check
	// here (resolveChecksum's actual fetch+parse of THAT happens at apply
	// time, when the digest value itself is needed).
	asset := findAssetByName(release.Assets, assetName)
	if asset != nil {
		if _, err := parseAssetDigest(asset.Digest); err == nil {
			status.ChecksumAvailable = true
		}
	}
	if !status.ChecksumAvailable {
		if bundle, sidecar := findChecksumAsset(release.Assets, assetName); bundle != nil || sidecar != nil {
			status.ChecksumAvailable = true
		}
	}
	return status
}

// ApplyCoreUpdate downloads, verifies, and installs the latest mihomo core
// release for this host's GOOS/GOARCH. Ordering (matches this file's header
// comment): fetch release metadata -> resolve checksum -> ONLY THEN
// download the actual binary -> re-verify its SHA-256 against that
// checksum -> decompress -> atomicSwap. The new binary is never executed
// (not even to "probe its version") until after atomicSwap has
// successfully installed it; detectVersion is called on c.mihomoPath again
// only once the swap itself has returned nil.
//
// Binary-in-use safety spans the WHOLE call, not just the instant of the
// swap: c.manager.BeginCoreUpdate (checked-and-armed atomically) both
// refuses if an instance is already running/starting AND arms a fleet-wide
// gate that makes StartContext itself refuse for as long as this function
// runs -- closing the TOCTOU window a single AnyRunning() check at the top
// alone would leave open for the entire download (which can legitimately
// take minutes). The AnyRunning() re-check immediately before atomicSwap is
// deliberate belt-and-suspenders on top of that gate, not a replacement for
// it -- see BeginCoreUpdate's own doc comment.
func (c *Controller) ApplyCoreUpdate(ctx context.Context) (CoreUpdateResult, error) {
	if !c.mihomoFound || c.mihomoPath == "" {
		return CoreUpdateResult{}, errMihomoNotFound
	}
	if err := c.manager.BeginCoreUpdate(); err != nil {
		return CoreUpdateResult{}, fmt.Errorf("%w: %v", errCoreUpdateBusy, err)
	}
	defer c.manager.EndCoreUpdate()

	release, err := fetchGitHubRelease(ctx, c.updateClient, mihomoReleaseAPI)
	if err != nil {
		return CoreUpdateResult{}, fmt.Errorf("check latest release: %w", err)
	}
	assetName, err := mihomoAssetName(runtime.GOOS, runtime.GOARCH, release.TagName)
	if err != nil {
		return CoreUpdateResult{}, err
	}
	asset := findAssetByName(release.Assets, assetName)
	if asset == nil {
		return CoreUpdateResult{}, fmt.Errorf("release %s has no asset named %q", release.TagName, assetName)
	}

	// MANDATORY, before any byte of the actual binary is downloaded: no
	// checksum, no download. See errChecksumUnavailable's doc comment and
	// this file's header comment.
	checksum, err := resolveChecksum(ctx, c.updateClient, release.Assets, assetName)
	if err != nil {
		return CoreUpdateResult{}, fmt.Errorf("refusing unverified core update: %w", err)
	}

	targetDir := filepath.Dir(c.mihomoPath)
	downloadPath, err := downloadToFile(ctx, c.updateClient, asset.BrowserDownloadURL, targetDir, maxDownloadBytes)
	if err != nil {
		return CoreUpdateResult{}, fmt.Errorf("download %s: %w", assetName, err)
	}
	defer os.Remove(downloadPath)

	// Checksum covers the downloaded archive itself (confirmed against a
	// real checksums.txt: it hashes the .gz/.zip release artifact, not its
	// decompressed content), so verification happens BEFORE decompression.
	if err := verifyChecksum(downloadPath, checksum); err != nil {
		return CoreUpdateResult{}, fmt.Errorf("core update aborted, nothing was changed: %w", err)
	}

	var binaryPath string
	if strings.HasSuffix(assetName, ".zip") {
		binaryPath, err = extractZipExecutableToFile(downloadPath, targetDir)
	} else {
		binaryPath, err = decompressGzipToFile(downloadPath, targetDir)
	}
	if err != nil {
		return CoreUpdateResult{}, fmt.Errorf("extract %s: %w", assetName, err)
	}
	defer os.Remove(binaryPath)

	// Re-check immediately before the point of no return. BeginCoreUpdate's
	// gate should already make this unreachable, but re-verifying right
	// here is cheap insurance against any future lifecycle path that might
	// launch a process without going through StartContext's gate check.
	if id, running := c.manager.AnyRunning(); running {
		return CoreUpdateResult{}, fmt.Errorf("%w: instance %q started during the update", errCoreUpdateBusy, id)
	}

	if err := atomicSwap(binaryPath, c.mihomoPath, true); err != nil {
		return CoreUpdateResult{}, fmt.Errorf("install new mihomo binary: %w", err)
	}

	// Only now -- after the verified binary is the one actually installed
	// at c.mihomoPath -- is it ever executed, to read back its own "-v"
	// output for display. This is the same detectVersion NewController
	// already runs at startup, just re-run post-swap.
	newVersion := detectVersion(c.mihomoPath)
	c.setMihomoVersion(newVersion)
	return CoreUpdateResult{Version: extractMihomoVersion(newVersion)}, nil
}
