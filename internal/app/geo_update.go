package app

// Feature #3 (docs/feature-roadmap-post-1.3.md): geodata (GeoIP.dat/
// GeoSite.dat/Country.mmdb/ASN.mmdb) auto-update, sourced from
// MetaCubeX/meta-rules-dat's "latest" release -- confirmed live against
// api.github.com on 2026-07-30 to actually publish all four files this
// mihomo-fleet installation cares about (geodata.go's geodataFiles), each
// with a per-asset "<name>.sha256sum" sidecar in plain
// "<hex64>␠␠<name>" (sha256sum(1)) format. Unlike the mihomo core binary
// (core_update.go), this repo/release DOES publish verifiable checksums for
// every file, so this half of feature #3 can actually complete an update
// end to end, not just report "unavailable".
//
// MetaCubeX/geoip (the other repo the roadmap mentions) returns 404 on
// /releases/latest -- it does not publish releases the way meta-rules-dat
// does -- so this file uses meta-rules-dat exclusively, which alone covers
// every file mihomo-fleet stages.
//
// Unlike the core binary, geodata files have no version number to compare
// (meta-rules-dat's "latest" release is a single, continuously-updated tag,
// not a series of dated releases). "Update available" is therefore
// determined by content, not by a version string: the locally-installed
// file's own SHA-256 is compared directly against the upstream-published
// digest. This is arguably a *better* signal than a version number would
// be -- it can never fall out of sync with what upstream actually shipped.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// geoReleaseAPI is a var for the same testability reason as
// core_update.go's mihomoReleaseAPI.
var geoReleaseAPI = "https://api.github.com/repos/MetaCubeX/meta-rules-dat/releases/latest"

// geoAssetMap maps meta-rules-dat's upstream asset filenames to the
// canonical local filenames geodata.go already recognizes and stages into
// every instance directory (its geodataFiles table). mihomo-fleet's own
// naming predates this feature; GeoLite2-ASN.mmdb is the one file whose
// upstream name does not already match a canonical/alias pair geodata.go
// looks for.
var geoAssetMap = []struct {
	upstream  string
	canonical string
}{
	{upstream: "geoip.dat", canonical: "GeoIP.dat"},
	{upstream: "geosite.dat", canonical: "GeoSite.dat"},
	{upstream: "country.mmdb", canonical: "Country.mmdb"},
	{upstream: "GeoLite2-ASN.mmdb", canonical: "ASN.mmdb"},
}

// geoDataDir is the data-dir "geo/" directory geo_update.go downloads into.
// geodataSourceDirs() (geodata.go) already checks filepath.Join(dataDir,
// "geo") FIRST among every instance's geodata source candidates, so a file
// landing here is picked up by the next prepareGeodata call (next instance
// start, or the next time an already-running instance's geodata is
// (re-)staged) with no further wiring needed.
func (c *Controller) geoDataDir() string {
	return filepath.Join(c.opts.DataDir, "geo")
}

// GeoUpdateStatus reports, per canonical geodata file: whether it is
// currently present in the data-dir geo/ directory, whether
// meta-rules-dat's latest release publishes a checksum for it, and whether
// that checksum differs from (or the file is simply missing versus) the
// locally-installed copy. Never returns an error for a single file's own
// resolution problem -- see CheckError, populated only for a release-wide
// failure (couldn't reach the API at all).
func (c *Controller) GeoUpdateStatus(ctx context.Context) GeoUpdateStatus {
	release, err := fetchGitHubRelease(ctx, c.updateClient, geoReleaseAPI)
	if err != nil {
		return GeoUpdateStatus{CheckError: err.Error()}
	}

	geoDir := c.geoDataDir()
	status := GeoUpdateStatus{Files: make([]GeoFileStatus, 0, len(geoAssetMap))}
	for _, mapping := range geoAssetMap {
		file := GeoFileStatus{Name: mapping.canonical}
		localSHA, statErr := sha256File(filepath.Join(geoDir, mapping.canonical))
		file.Present = statErr == nil

		checksum, err := resolveChecksum(ctx, c.updateClient, release.Assets, mapping.upstream)
		switch {
		case err == nil:
			file.ChecksumAvailable = true
			file.UpdateAvailable = !file.Present || !strings.EqualFold(localSHA, checksum)
		case err == errChecksumUnavailable:
			// Leave ChecksumAvailable/UpdateAvailable at false: no
			// verifiable content exists to compare against, so "no update"
			// is the honest default instead of guessing either way.
		default:
			if status.CheckError == "" {
				status.CheckError = err.Error()
			}
		}
		status.Files = append(status.Files, file)
	}
	return status
}

// ApplyGeoUpdate downloads, verifies, and installs every geodata file whose
// upstream checksum differs from (or is simply missing versus) the local
// copy. Each file is independent: one file's download/verification failure
// does not block the others from updating, and is reported in
// GeoUpdateResult.Errors rather than aborting the whole call -- matching
// GeoUpdateStatus's per-file shape. ApplyGeoUpdate itself only returns a
// non-nil error when the release list could not be fetched at all, so there
// is nothing to iterate.
func (c *Controller) ApplyGeoUpdate(ctx context.Context) (GeoUpdateResult, error) {
	release, err := fetchGitHubRelease(ctx, c.updateClient, geoReleaseAPI)
	if err != nil {
		return GeoUpdateResult{}, fmt.Errorf("check latest geodata release: %w", err)
	}
	geoDir := c.geoDataDir()
	if err := os.MkdirAll(geoDir, 0o755); err != nil {
		return GeoUpdateResult{}, fmt.Errorf("create geo data directory: %w", err)
	}

	result := GeoUpdateResult{}
	for _, mapping := range geoAssetMap {
		asset := findAssetByName(release.Assets, mapping.upstream)
		if asset == nil {
			continue // this upstream file isn't published this release -- nothing to do, not an error
		}

		// MANDATORY, before any byte of the actual file is downloaded: no
		// checksum, no download. Same ordering as core_update.go's
		// ApplyCoreUpdate.
		checksum, err := resolveChecksum(ctx, c.updateClient, release.Assets, mapping.upstream)
		if err != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("%s: refusing unverified update: %v", mapping.canonical, err))
			continue
		}

		downloadPath, err := downloadToFile(ctx, c.updateClient, asset.BrowserDownloadURL, geoDir, maxDownloadBytes)
		if err != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("%s: download failed: %v", mapping.canonical, err))
			continue
		}
		if err := verifyChecksum(downloadPath, checksum); err != nil {
			_ = os.Remove(downloadPath)
			result.Errors = append(result.Errors, fmt.Sprintf("%s: aborted, nothing was changed: %v", mapping.canonical, err))
			continue
		}

		// Geodata assets are published uncompressed (confirmed against the
		// real release: geoip.dat/geosite.dat/country.mmdb/
		// GeoLite2-ASN.mmdb carry no .gz/.zip extension and are the final
		// content directly) -- no decompression step, straight to the swap.
		target := filepath.Join(geoDir, mapping.canonical)
		if err := atomicSwap(downloadPath, target, false); err != nil {
			_ = os.Remove(downloadPath)
			result.Errors = append(result.Errors, fmt.Sprintf("%s: install failed: %v", mapping.canonical, err))
			continue
		}
		result.Updated = append(result.Updated, mapping.canonical)
	}
	return result, nil
}
