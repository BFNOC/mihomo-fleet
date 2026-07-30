package app

// Shared download/verify/swap primitives for feature #3 (mihomo core +
// geodata auto-update, docs/feature-roadmap-post-1.3.md). Both
// core_update.go (the mihomo binary itself) and geo_update.go (geoip.dat/
// geosite.dat/Country.mmdb/ASN.mmdb) go through exactly this file for the
// download -> verify -> (decompress) -> atomic swap sequence, so the
// security-critical ordering (verify BEFORE anything touches the real
// target path) lives in one place rather than being reimplemented per
// artifact type.
//
// Non-negotiable ordering enforced by every caller in this package (not by
// this file alone, since a caller composes these calls): downloadToFile ->
// verifyChecksum -> (decompressGzipToFile/extractZipExecutableToFile) ->
// atomicSwap. Nothing before atomicSwap ever touches targetPath; nothing
// after downloadToFile is ever treated as trustworthy until verifyChecksum
// has passed against the upstream-published digest.

import (
	"archive/zip"
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// errChecksumUnavailable is returned by checksum resolution when the
// upstream release publishes no checksum mihomo-fleet can parse for the
// asset in question. Every caller treats this identically to a checksum
// MISMATCH: abort, touch nothing on disk, return a clear error. There is no
// "best effort" install of an unverified artifact.
var errChecksumUnavailable = errors.New("upstream did not publish a checksum for this asset")

const (
	// maxDownloadBytes caps any single binary/geodata download this package
	// performs. The mihomo binary's compressed release asset is currently
	// under 25MB and geoip.dat is under 20MB (checked live against the real
	// releases this feature was built against); this cap gives generous
	// headroom while still bounding an unbounded/misbehaving response.
	maxDownloadBytes = 200 << 20
	// maxChecksumBundleBytes caps the small checksums.txt/sha256sum.txt/
	// *.sha256sum sidecar files specifically -- a combined bundle covering
	// 100+ assets is still only a few KB.
	maxChecksumBundleBytes = 1 << 20
)

// fetchBytes GETs url through client and returns the full body, capped at
// maxBytes. Used for the small JSON/checksum-text responses (GitHub API
// release metadata, checksum bundles/sidecars) where buffering the whole
// body in memory is fine -- downloadToFile below is for the actual
// multi-megabyte artifacts, which stream straight to disk instead.
func fetchBytes(ctx context.Context, client *http.Client, rawURL string, maxBytes int64, accept string) ([]byte, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("parse URL: %w", err)
	}
	if err := validateUpdateTargetFn(ctx, parsed, "download"); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "mihomo-fleet")
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	res, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return nil, fmt.Errorf("server returned %s", res.Status)
	}
	limited := io.LimitReader(res.Body, maxBytes+1)
	data, err := io.ReadAll(limited)
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxBytes {
		return nil, fmt.Errorf("response exceeds %d byte limit", maxBytes)
	}
	return data, nil
}

// progressReportInterval bounds how often progressReader invokes its
// callback. 200ms keeps controller.go's SSE geodata-update stream from
// turning a single multi-second download into a firehose of near-identical
// events, while still feeling live to a human watching a progress bar.
const progressReportInterval = 200 * time.Millisecond

// progressReader wraps an io.Reader, invoking onProgress with the
// cumulative bytes read so far, totalSize (as given by the caller --
// typically an HTTP response's Content-Length, 0 when unknown), and the
// average download speed in bytes/sec measured from when the reader was
// created. onProgress fires at most once per progressReportInterval, PLUS
// exactly once more on the terminal Read that returns a non-nil error (EOF
// or otherwise), so the last event a caller observes always reflects the
// true final byte count rather than whatever fell inside the last interval.
type progressReader struct {
	reader     io.Reader
	onProgress func(downloaded, totalSize, bytesPerSec int64)
	totalSize  int64
	downloaded int64
	start      time.Time
	lastReport time.Time
}

func newProgressReader(r io.Reader, totalSize int64, onProgress func(downloaded, totalSize, bytesPerSec int64)) *progressReader {
	now := time.Now()
	return &progressReader{reader: r, onProgress: onProgress, totalSize: totalSize, start: now, lastReport: now}
}

func (p *progressReader) Read(buf []byte) (int, error) {
	n, err := p.reader.Read(buf)
	if n > 0 {
		p.downloaded += int64(n)
	}
	if n > 0 || err != nil {
		now := time.Now()
		if err != nil || now.Sub(p.lastReport) >= progressReportInterval {
			p.lastReport = now
			var bps int64
			if elapsed := now.Sub(p.start).Seconds(); elapsed > 0 {
				bps = int64(float64(p.downloaded) / elapsed)
			}
			p.onProgress(p.downloaded, p.totalSize, bps)
		}
	}
	return n, err
}

// downloadToFile GETs url through client and streams the response body into
// a new temp file created in dir (so a later os.Rename onto a target in the
// same dir is a same-filesystem, near-atomic rename), capped at maxBytes.
// Returns the temp file's path; the caller owns removing it once it is no
// longer needed (whether because it was renamed into place by atomicSwap,
// or because a later verification/extraction step failed).
//
// onProgress, when non-nil, is wrapped around the response body via
// progressReader so the caller gets periodic downloaded/totalSize/speed
// callbacks (docs/geo-update-enhancements.md P1). Passing nil (every
// pre-existing caller until that feature) skips the wrapping entirely --
// no progressReader allocation, no extra time.Now() calls per chunk --
// so behavior and cost are identical to before this parameter existed.
func downloadToFile(ctx context.Context, client *http.Client, rawURL string, dir string, maxBytes int64, onProgress func(downloaded, totalSize, bytesPerSec int64)) (string, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return "", fmt.Errorf("parse URL: %w", err)
	}
	if err := validateUpdateTargetFn(ctx, parsed, "download"); err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "mihomo-fleet")
	res, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return "", fmt.Errorf("server returned %s", res.Status)
	}

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	tmp, err := os.CreateTemp(dir, ".mihomo-fleet-download-*")
	if err != nil {
		return "", err
	}
	tmpPath := tmp.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmpPath)
		}
	}()

	var body io.Reader = res.Body
	if onProgress != nil {
		totalSize := res.ContentLength
		if totalSize < 0 {
			totalSize = 0
		}
		body = newProgressReader(res.Body, totalSize, onProgress)
	}
	limited := io.LimitReader(body, maxBytes+1)
	written, err := io.Copy(tmp, limited)
	if err != nil {
		_ = tmp.Close()
		return "", err
	}
	if written > maxBytes {
		_ = tmp.Close()
		return "", fmt.Errorf("download exceeds %d byte limit", maxBytes)
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}
	cleanup = false
	return tmpPath, nil
}

// sha256File returns the lowercase hex SHA-256 digest of the file at path.
func sha256File(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// verifyChecksum computes path's SHA-256 and compares it (case-
// insensitively) against expectedHex. expectedHex must be a 64-hex-char
// digest; anything else (including empty) is treated as unavailable rather
// than silently skipped -- a bug in a checksum-bundle parser must degrade
// to the same ABORT path as a genuinely missing checksum, never to
// accidentally bypassing verification.
func verifyChecksum(path string, expectedHex string) error {
	expectedHex = strings.ToLower(strings.TrimSpace(expectedHex))
	if len(expectedHex) != 64 {
		return errChecksumUnavailable
	}
	actual, err := sha256File(path)
	if err != nil {
		return err
	}
	if actual != expectedHex {
		return fmt.Errorf("checksum mismatch: expected %s, got %s", expectedHex, actual)
	}
	return nil
}

// parseChecksumBundle scans a sha256sum(1)-style combined checksum file
// ("<hex64>␠␠<name>" per line, optionally "*"-prefixed for binary mode and/
// or "./"-prefixed) for the line naming assetName, and returns its hex
// digest. Matches on an exact name or a "/assetName" suffix (in case the
// bundle records a relative subdirectory path), after stripping a leading
// "./" and "*" marker -- exactly the shape confirmed against a real
// MetaCubeX/mihomo checksums.txt during this feature's development.
func parseChecksumBundle(data []byte, assetName string) (string, error) {
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 0, 4096), 1<<20)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		digest := fields[0]
		name := strings.TrimPrefix(strings.TrimPrefix(fields[1], "*"), "./")
		if name == assetName || strings.HasSuffix(name, "/"+assetName) {
			return digest, nil
		}
	}
	if err := scanner.Err(); err != nil {
		return "", err
	}
	return "", errChecksumUnavailable
}

// parseChecksumSidecar reads a per-asset "<name>.sha256"/"<name>.sha256sum"
// file, which is either a bare hex digest or "<hex64>␠␠<name>" -- either
// way the first whitespace-separated field is the digest.
func parseChecksumSidecar(data []byte) (string, error) {
	fields := strings.Fields(string(data))
	if len(fields) == 0 {
		return "", errChecksumUnavailable
	}
	digest := fields[0]
	if len(digest) != 64 {
		return "", errChecksumUnavailable
	}
	return digest, nil
}

// decompressGzipToFile decompresses the single gzip member at srcPath into
// a new temp file in dir, returning its path. mihomo's unix/darwin release
// assets are a plain gzip of the raw executable -- confirmed against a real
// release download during this feature's development (`gzip -l` reports the
// original filename/size and the decompressed content is a stripped ELF
// binary with no tar wrapper), not a .tar.gz.
func decompressGzipToFile(srcPath, dir string) (string, error) {
	in, err := os.Open(srcPath)
	if err != nil {
		return "", err
	}
	defer in.Close()
	gz, err := gzip.NewReader(in)
	if err != nil {
		return "", fmt.Errorf("open gzip: %w", err)
	}
	defer gz.Close()
	return copyLimitedToTemp(io.LimitReader(gz, maxDownloadBytes+1), dir)
}

// extractZipExecutableToFile extracts the single .exe file from the zip
// archive at srcPath into a new temp file in dir, returning its path.
// mihomo's windows release assets are a zip containing exactly one .exe
// entry (confirmed against a real release download); an archive that does
// not contain exactly one regular file, or whose one file is not named
// *.exe, is rejected outright rather than guessed at -- there is no safe
// heuristic for "which entry" that still holds once the file is about to
// become an executable target.
func extractZipExecutableToFile(srcPath, dir string) (string, error) {
	zr, err := zip.OpenReader(srcPath)
	if err != nil {
		return "", fmt.Errorf("open zip: %w", err)
	}
	defer zr.Close()

	var entry *zip.File
	count := 0
	for _, f := range zr.File {
		if f.FileInfo().IsDir() {
			continue
		}
		count++
		entry = f
	}
	if count != 1 || entry == nil {
		return "", fmt.Errorf("zip archive contains %d files, expected exactly 1", count)
	}
	if !strings.EqualFold(filepath.Ext(entry.Name), ".exe") {
		return "", fmt.Errorf("zip archive's single entry %q is not a .exe file", entry.Name)
	}
	if entry.UncompressedSize64 > uint64(maxDownloadBytes) {
		return "", fmt.Errorf("zip entry %q exceeds %d byte limit", entry.Name, maxDownloadBytes)
	}

	rc, err := entry.Open()
	if err != nil {
		return "", err
	}
	defer rc.Close()
	return copyLimitedToTemp(io.LimitReader(rc, maxDownloadBytes+1), dir)
}

func copyLimitedToTemp(r io.Reader, dir string) (string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	out, err := os.CreateTemp(dir, ".mihomo-fleet-extract-*")
	if err != nil {
		return "", err
	}
	outPath := out.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = out.Close()
			_ = os.Remove(outPath)
		}
	}()
	written, err := io.Copy(out, r)
	if err != nil {
		return "", err
	}
	if written > maxDownloadBytes {
		return "", fmt.Errorf("extracted content exceeds %d byte limit", maxDownloadBytes)
	}
	if err := out.Close(); err != nil {
		return "", err
	}
	cleanup = false
	return outPath, nil
}

// atomicSwap installs newPath (already fully written and, by the time any
// caller in this package reaches here, checksum-verified) at targetPath,
// keeping whatever previously lived at targetPath as targetPath+".bak":
//
//  1. remove any stale targetPath+".bak" from an earlier swap
//  2. if targetPath exists, rename it to targetPath+".bak", freeing the name
//  3. rename newPath (already in targetPath's directory, so this is a
//     same-filesystem rename) onto targetPath
//  4. if step 3 fails, attempt to restore step 2's rename (bak -> target)
//     and return an error describing whether that rollback succeeded
//
// The only file ever written to with arbitrary new content is newPath,
// fully created by the caller before atomicSwap is even invoked; targetPath
// itself is only ever the subject of whole-file renames here, so a crash at
// any point leaves it either fully absent, fully the old file, or fully the
// new file -- never truncated or partially written. This is not a true
// atomic exchange (Go's stdlib has no portable rename-swap syscall): between
// steps 2 and 3, targetPath does not exist. That window is a single
// directory-entry rename, the same residual risk essentially every
// self-updating application accepts.
//
// executable controls whether newPath is chmod'd to 0755 before the swap
// (the mihomo binary, unix only -- meaningless on windows, where a rename
// changing the file's contents behind a fixed .exe path already makes it
// executable regardless of a POSIX mode bit). Pass false for geodata files.
func atomicSwap(newPath, targetPath string, executable bool) error {
	if executable {
		if err := os.Chmod(newPath, 0o755); err != nil {
			return fmt.Errorf("chmod new file: %w", err)
		}
	}

	bakPath := targetPath + ".bak"
	_ = os.Remove(bakPath) // best-effort; a stale .bak from a previous swap is expected to be replaced

	hadPrevious := false
	if _, err := os.Lstat(targetPath); err == nil {
		if err := os.Rename(targetPath, bakPath); err != nil {
			// Binary-in-use safety: on Windows, renaming an exe a process
			// still has open (or that was JUST replaced and reopened) can
			// fail with ERROR_ACCESS_DENIED, which os.Rename surfaces as a
			// plain *LinkError here. Nothing has moved yet -- targetPath is
			// untouched -- so simply surfacing the error is already a safe
			// abort, not a corruption.
			return fmt.Errorf("move previous file aside: %w", err)
		}
		hadPrevious = true
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("stat target: %w", err)
	}

	if err := os.Rename(newPath, targetPath); err != nil {
		if !hadPrevious {
			return fmt.Errorf("install new file: %w", err)
		}
		if rollbackErr := os.Rename(bakPath, targetPath); rollbackErr != nil {
			// Both the swap and the rollback failed -- e.g. Windows refusing
			// the second rename too. targetPath may now be MISSING rather
			// than corrupt (this function never truncates/overwrites it in
			// place), and bakPath still holds the previous good file, so
			// nothing is lost -- but the operator needs to know to restore
			// it by hand, hence surfacing both errors instead of just one.
			return fmt.Errorf("install new file failed (%v) AND rollback failed (%v); previous file preserved at %s", err, rollbackErr, bakPath)
		}
		return fmt.Errorf("install new file failed, rolled back to previous version: %w", err)
	}
	return nil
}
