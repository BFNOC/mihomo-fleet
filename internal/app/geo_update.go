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
//
// This is now a thin wrapper around ApplyGeoUpdateSSE (docs/
// geo-update-enhancements.md P1): the download/verify/install sequence
// lives in exactly one place, and ApplyGeoUpdate just collects that stream's
// terminal "complete" event into the GeoUpdateResult shape callers already
// depend on (both the Go test suite and, historically, the JSON POST
// response) instead of duplicating the loop.
func (c *Controller) ApplyGeoUpdate(ctx context.Context) (GeoUpdateResult, error) {
	var result GeoUpdateResult
	err := c.ApplyGeoUpdateSSE(ctx, nil, func(evt GeoDownloadEvent) {
		if evt.Event == "complete" {
			result = GeoUpdateResult{Updated: evt.Updated, Errors: evt.Errors}
		}
	})
	return result, err
}

// ApplyGeoUpdateSSE performs the same download/verify/install sequence as
// ApplyGeoUpdate, but reports progress as a sequence of GeoDownloadEvent
// values via onEvent instead of only returning a single result at the end
// (docs/geo-update-enhancements.md P1). onEvent is invoked synchronously
// from this goroutine, in strict per-file "start" -> zero or more
// "progress" -> "done" order, followed by exactly one final "complete"
// event once every file in geoAssetMap has been considered; the caller
// (controller.go's streamGeoUpdate) turns each call into one SSE frame.
//
// A file already up to date locally is reported as "done"/"skipped"
// without a "start"/"progress" pair, since no download happens for it. A
// file the current release simply does not publish (see
// TestGeoUpdateStatusHandlesUnpublishedFile) produces no event at all,
// matching ApplyGeoUpdate's pre-SSE behavior of silently leaving it out of
// both Updated and Errors.
//
// Like ApplyGeoUpdate, this only returns a non-nil error when the release
// list itself could not be fetched -- in that case no per-file events and
// no "complete" event are ever sent, since there is nothing to iterate;
// streamGeoUpdate is responsible for surfacing that error to the client
// itself.
//
// downloadClient is P2's hook (docs/geo-update-enhancements.md, section 3):
// when nil, every request -- the GitHub API call above included -- goes
// through c.updateClient exactly as before. When non-nil (built by
// proxyClientForInstance from a caller-selected running instance),
// downloadClient is used ONLY for the actual asset downloads
// (geoDownloadAndInstall below); the release-metadata and checksum-sidecar
// requests (fetchGitHubRelease/resolveChecksum) always stay on
// c.updateClient. Routing the small, latency-sensitive metadata calls
// through an extra local hop would only add a point of failure for no
// bandwidth benefit -- it's the multi-megabyte asset bodies a slow direct
// path actually struggles with.
func (c *Controller) ApplyGeoUpdateSSE(ctx context.Context, downloadClient *http.Client, onEvent func(GeoDownloadEvent)) error {
	release, err := fetchGitHubRelease(ctx, c.updateClient, geoReleaseAPI)
	if err != nil {
		return fmt.Errorf("check latest geodata release: %w", err)
	}
	geoDir := c.geoDataDir()
	if err := os.MkdirAll(geoDir, 0o755); err != nil {
		return fmt.Errorf("create geo data directory: %w", err)
	}

	client := downloadClient
	if client == nil {
		client = c.updateClient
	}

	sourceDirs := c.manager.geodataSourceDirs()
	total := len(geoAssetMap)
	var updated, errs []string
	for index, mapping := range geoAssetMap {
		asset := findAssetByName(release.Assets, mapping.upstream)
		if asset == nil {
			continue
		}

		checksum, err := resolveChecksum(ctx, c.updateClient, release.Assets, mapping.upstream)
		if err != nil {
			msg := fmt.Sprintf("%s: refusing unverified update: %v", mapping.canonical, err)
			errs = append(errs, msg)
			onEvent(GeoDownloadEvent{Event: "done", File: mapping.canonical, Index: index, Total: total, Result: "error", Message: msg})
			continue
		}

		target := resolveLocalGeoFile(geoDir, sourceDirs, mapping.canonical)
		if localSHA, err := sha256File(target); err == nil && strings.EqualFold(localSHA, checksum) {
			onEvent(GeoDownloadEvent{Event: "done", File: mapping.canonical, Index: index, Total: total, Result: "skipped"})
			continue
		}

		onEvent(GeoDownloadEvent{Event: "start", File: mapping.canonical, Index: index, Total: total})
		onProgress := func(downloaded, totalSize, bytesPerSec int64) {
			onEvent(GeoDownloadEvent{
				Event: "progress", File: mapping.canonical, Index: index, Total: total,
				Downloaded: downloaded, TotalSize: totalSize, Speed: bytesPerSec,
			})
		}

		if err := geoDownloadAndInstall(ctx, client, asset.BrowserDownloadURL, target, checksum, onProgress); err != nil {
			// H1: if target dir is read-only (e.g. exe in /usr/local/bin),
			// fall back to geoDir which is always writable.
			fallback := filepath.Join(geoDir, mapping.canonical)
			if fallback != target {
				if err2 := geoDownloadAndInstall(ctx, client, asset.BrowserDownloadURL, fallback, checksum, onProgress); err2 != nil {
					msg := fmt.Sprintf("%s: %v (fallback: %v)", mapping.canonical, err, err2)
					errs = append(errs, msg)
					onEvent(GeoDownloadEvent{Event: "done", File: mapping.canonical, Index: index, Total: total, Result: "error", Message: msg})
					continue
				}
			} else {
				msg := fmt.Sprintf("%s: %v", mapping.canonical, err)
				errs = append(errs, msg)
				onEvent(GeoDownloadEvent{Event: "done", File: mapping.canonical, Index: index, Total: total, Result: "error", Message: msg})
				continue
			}
		}
		updated = append(updated, mapping.canonical)
		onEvent(GeoDownloadEvent{Event: "done", File: mapping.canonical, Index: index, Total: total, Result: "updated"})
	}
	onEvent(GeoDownloadEvent{Event: "complete", Updated: updated, Errors: errs})
	return nil
}

func geoDownloadAndInstall(ctx context.Context, client *http.Client, url, target, checksum string, onProgress func(downloaded, totalSize, bytesPerSec int64)) error {
	targetDir := filepath.Dir(target)
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		return fmt.Errorf("prepare directory: %w", err)
	}
	downloadPath, err := downloadToFile(ctx, client, url, targetDir, maxDownloadBytes, onProgress)
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
