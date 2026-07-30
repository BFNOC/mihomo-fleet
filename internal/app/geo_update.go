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
	"net/http"
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

// geoDataDir is the data-dir "geo/" subdirectory that serves as the
// default download destination when no existing copy is found elsewhere.
// geodataSourceDirs() (geodata.go) lists it FIRST among every instance's
// geodata source candidates, so it wins over exe-dir/cwd copies.
func (c *Controller) geoDataDir() string {
	return filepath.Join(c.opts.DataDir, "geo")
}

// GeoUpdateStatus reports, per canonical geodata file: whether it is
// currently present in any geodata source directory (dataDir/geo, exe dir,
// cwd, mihomo dir — same search as runtime staging), whether
// meta-rules-dat's latest release publishes a checksum for it, and whether
// that checksum differs from the locally-installed copy. SourcePath
// records the resolved path when present. Never returns an error for a
// single file's resolution problem -- see CheckError, populated only for
// a release-wide failure (couldn't reach the API at all).
func (c *Controller) GeoUpdateStatus(ctx context.Context) GeoUpdateStatus {
	release, err := fetchGitHubRelease(ctx, c.updateClient, geoReleaseAPI)
	if err != nil {
		return GeoUpdateStatus{CheckError: err.Error()}
	}

	geoDir := c.geoDataDir()
	sourceDirs := c.manager.geodataSourceDirs()
	status := GeoUpdateStatus{Files: make([]GeoFileStatus, 0, len(geoAssetMap))}
	for _, mapping := range geoAssetMap {
		file := GeoFileStatus{Name: mapping.canonical}
		localPath := resolveLocalGeoFile(geoDir, sourceDirs, mapping.canonical)
		localSHA, statErr := sha256File(localPath)
		file.Present = statErr == nil
		if file.Present {
			file.SourcePath = localPath
		}

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

	sourceDirs := c.manager.geodataSourceDirs()
	result := GeoUpdateResult{}
	for _, mapping := range geoAssetMap {
		asset := findAssetByName(release.Assets, mapping.upstream)
		if asset == nil {
			continue
		}

		checksum, err := resolveChecksum(ctx, c.updateClient, release.Assets, mapping.upstream)
		if err != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("%s: refusing unverified update: %v", mapping.canonical, err))
			continue
		}

		target := resolveLocalGeoFile(geoDir, sourceDirs, mapping.canonical)
		if localSHA, err := sha256File(target); err == nil && strings.EqualFold(localSHA, checksum) {
			continue
		}

		if err := geoDownloadAndInstall(ctx, c.updateClient, asset.BrowserDownloadURL, target, checksum); err != nil {
			// H1: if target dir is read-only (e.g. exe in /usr/local/bin),
			// fall back to geoDir which is always writable.
			fallback := filepath.Join(geoDir, mapping.canonical)
			if fallback != target {
				if err2 := geoDownloadAndInstall(ctx, c.updateClient, asset.BrowserDownloadURL, fallback, checksum); err2 != nil {
					result.Errors = append(result.Errors, fmt.Sprintf("%s: %v (fallback: %v)", mapping.canonical, err, err2))
					continue
				}
			} else {
				result.Errors = append(result.Errors, fmt.Sprintf("%s: %v", mapping.canonical, err))
				continue
			}
		}
		result.Updated = append(result.Updated, mapping.canonical)
	}
	return result, nil
}

func geoDownloadAndInstall(ctx context.Context, client *http.Client, url, target, checksum string) error {
	targetDir := filepath.Dir(target)
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		return fmt.Errorf("prepare directory: %w", err)
	}
	downloadPath, err := downloadToFile(ctx, client, url, targetDir, maxDownloadBytes)
	if err != nil {
		return fmt.Errorf("download: %w", err)
	}
	if err := verifyChecksum(downloadPath, checksum); err != nil {
		_ = os.Remove(downloadPath)
		return fmt.Errorf("checksum mismatch: %w", err)
	}
	if err := atomicSwap(downloadPath, target, false); err != nil {
		_ = os.Remove(downloadPath)
		return fmt.Errorf("install: %w", err)
	}
	return nil
}

func resolveLocalGeoFile(geoDir string, sourceDirs []string, canonical string) string {
	primary := filepath.Join(geoDir, canonical)
	if info, err := os.Stat(primary); err == nil && !info.IsDir() {
		return primary
	}
	for _, g := range geodataFiles {
		if g.canonical == canonical {
			if found := findGeodataSource(sourceDirs, g.aliases); found != "" {
				return found
			}
			break
		}
	}
	return primary
}
