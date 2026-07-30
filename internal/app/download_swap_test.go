package app

import (
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"sync"
	"testing"
	"time"
)

func writeTestFile(t *testing.T, path string, content []byte, perm os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, content, perm); err != nil {
		t.Fatal(err)
	}
}

var updateTargetTestMu sync.Mutex

// withUpdateTargetAllowed overrides validateUpdateTargetFn so
// fetchBytes/downloadToFile and the update client's CheckRedirect do not
// reject the loopback httptest.Server address these tests use -- mirroring
// subscription_test.go's withSubscriptionTargetAllowed for the same reason
// (testing M2, docs/review-2026-07-11-testing-quality.md). Every
// core/geo-update test that points mihomoReleaseAPI/geoReleaseAPI at an
// httptest.Server needs this: validateUpdateTargetFn's real implementation
// requires https and a pinned github.com/githubusercontent.com host, which
// a plain http://127.0.0.1:PORT test server can never satisfy.
func withUpdateTargetAllowed(t *testing.T) {
	t.Helper()
	updateTargetTestMu.Lock()
	original := validateUpdateTargetFn
	validateUpdateTargetFn = func(context.Context, *url.URL, string) error { return nil }
	t.Cleanup(func() {
		validateUpdateTargetFn = original
		updateTargetTestMu.Unlock()
	})
}

func TestSha256FileMatchesKnownDigest(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "content")
	writeTestFile(t, path, []byte("hello world"), 0o644)

	got, err := sha256File(path)
	if err != nil {
		t.Fatal(err)
	}
	// sha256("hello world"), independently computed with Python's hashlib
	// during this test's development (not hand-copied from memory).
	const want = "b94d27b9934d3e08a52e52d7da7dabfac484efe37a5380ee9088f7ace2efcde9"
	if got != want {
		t.Fatalf("digest = %q, want %q", got, want)
	}
}

func TestVerifyChecksumPassesAndFails(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "content")
	writeTestFile(t, path, []byte("verified content"), 0o644)

	digest, err := sha256File(path)
	if err != nil {
		t.Fatal(err)
	}

	if err := verifyChecksum(path, digest); err != nil {
		t.Fatalf("matching digest: %v, want nil", err)
	}
	if err := verifyChecksum(path, "0000000000000000000000000000000000000000000000000000000000000000"[:64]); err == nil {
		t.Fatal("mismatched digest: want error, got nil")
	}
	if err := verifyChecksum(path, ""); err != errChecksumUnavailable {
		t.Fatalf("empty digest = %v, want errChecksumUnavailable", err)
	}
	if err := verifyChecksum(path, "short"); err != errChecksumUnavailable {
		t.Fatalf("malformed digest = %v, want errChecksumUnavailable", err)
	}
}

func TestParseChecksumBundleMatchesByNameAndSuffix(t *testing.T) {
	digest := "abcd1234abcd1234abcd1234abcd1234abcd1234abcd1234abcd1234abcd1234"[:64]
	bundle := []byte(digest + "  ./mihomo-linux-amd64-v1.19.29.gz\n" +
		"deadbeef00000000000000000000000000000000000000000000000000000\n\n" +
		"# comment line\n")

	got, err := parseChecksumBundle(bundle, "mihomo-linux-amd64-v1.19.29.gz")
	if err != nil {
		t.Fatal(err)
	}
	if got != digest {
		t.Fatalf("digest = %q, want %q", got, digest)
	}

	if _, err := parseChecksumBundle(bundle, "not-in-bundle.gz"); err != errChecksumUnavailable {
		t.Fatalf("missing entry = %v, want errChecksumUnavailable", err)
	}
}

func TestParseChecksumSidecar(t *testing.T) {
	digest := "1111111111111111111111111111111111111111111111111111111111111111"[:64]
	got, err := parseChecksumSidecar([]byte(digest + "  geoip.dat\n"))
	if err != nil {
		t.Fatal(err)
	}
	if got != digest {
		t.Fatalf("digest = %q, want %q", got, digest)
	}

	if _, err := parseChecksumSidecar([]byte("")); err != errChecksumUnavailable {
		t.Fatalf("empty sidecar = %v, want errChecksumUnavailable", err)
	}
	if _, err := parseChecksumSidecar([]byte("not-a-hex-digest\n")); err != errChecksumUnavailable {
		t.Fatalf("malformed sidecar = %v, want errChecksumUnavailable", err)
	}
}

// stepReader hands back one fixed chunk per Read call, then io.EOF -- lets
// a test control exactly when each Read happens (and how long it sleeps in
// between) without depending on how a real network reader happens to chunk
// its data.
type stepReader struct {
	chunks [][]byte
	i      int
}

func (r *stepReader) Read(buf []byte) (int, error) {
	if r.i >= len(r.chunks) {
		return 0, io.EOF
	}
	n := copy(buf, r.chunks[r.i])
	r.i++
	return n, nil
}

// TestProgressReaderReportsPeriodicallyAndOnFinalRead covers progressReader
// directly: a report within the same progressReportInterval window is
// suppressed, a report is emitted once that interval has elapsed, and the
// terminal EOF read always forces one last report reflecting the true
// cumulative total even if the interval has not elapsed since the previous
// report.
func TestProgressReaderReportsPeriodicallyAndOnFinalRead(t *testing.T) {
	src := &stepReader{chunks: [][]byte{[]byte("aaaaa"), []byte("bb"), []byte("ccc")}} // 5, 2, 3 bytes
	type report struct{ downloaded, totalSize, speed int64 }
	var reports []report
	pr := newProgressReader(src, 42, func(downloaded, totalSize, bytesPerSec int64) {
		reports = append(reports, report{downloaded, totalSize, bytesPerSec})
	})
	buf := make([]byte, 16)

	// First read, immediately after construction: well under
	// progressReportInterval, so no report yet.
	if n, err := pr.Read(buf); n != 5 || err != nil {
		t.Fatalf("first Read = (%d, %v), want (5, nil)", n, err)
	}
	if len(reports) != 0 {
		t.Fatalf("reports after first Read = %d, want 0 (interval not elapsed)", len(reports))
	}

	// Wait past the interval, then read again: must report now, with the
	// CUMULATIVE total (5+2=7), not just the latest chunk's size.
	time.Sleep(progressReportInterval + 30*time.Millisecond)
	if n, err := pr.Read(buf); n != 2 || err != nil {
		t.Fatalf("second Read = (%d, %v), want (2, nil)", n, err)
	}
	if len(reports) != 1 {
		t.Fatalf("reports after second Read = %d, want 1 (interval elapsed)", len(reports))
	}
	if reports[0].downloaded != 7 {
		t.Fatalf("reports[0].downloaded = %d, want 7", reports[0].downloaded)
	}
	if reports[0].totalSize != 42 {
		t.Fatalf("reports[0].totalSize = %d, want 42 (passed through from caller)", reports[0].totalSize)
	}
	if reports[0].speed <= 0 {
		t.Fatalf("reports[0].speed = %d, want > 0", reports[0].speed)
	}

	// Read the final chunk immediately (no sleep): interval has not
	// elapsed since the last report, so still no new report.
	if n, err := pr.Read(buf); n != 3 || err != nil {
		t.Fatalf("third Read = (%d, %v), want (3, nil)", n, err)
	}
	if len(reports) != 1 {
		t.Fatalf("reports after third Read = %d, want still 1 (interval not elapsed)", len(reports))
	}

	// Terminal EOF read: forces one final report with the true total (10),
	// regardless of interval timing.
	if n, err := pr.Read(buf); n != 0 || err != io.EOF {
		t.Fatalf("fourth Read = (%d, %v), want (0, io.EOF)", n, err)
	}
	if len(reports) != 2 {
		t.Fatalf("reports after EOF Read = %d, want 2 (terminal report forced)", len(reports))
	}
	if reports[1].downloaded != 10 {
		t.Fatalf("final report downloaded = %d, want 10 (true total)", reports[1].downloaded)
	}
}

// TestDownloadToFileReportsProgressAndFinalTotals covers downloadToFile's
// onProgress wiring end to end: totalSize comes from the response's
// Content-Length, and the last callback observed reflects the fully
// downloaded file, not a partial read.
func TestDownloadToFileReportsProgressAndFinalTotals(t *testing.T) {
	withUpdateTargetAllowed(t)
	const body = "0123456789abcdef"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		_, _ = w.Write([]byte(body))
	}))
	defer server.Close()

	dir := t.TempDir()
	var calls int
	var lastDownloaded, lastTotalSize int64
	path, err := downloadToFile(context.Background(), server.Client(), server.URL, dir, maxDownloadBytes, func(downloaded, totalSize, bytesPerSec int64) {
		calls++
		lastDownloaded = downloaded
		lastTotalSize = totalSize
	})
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(path)

	if calls == 0 {
		t.Fatal("onProgress was never called")
	}
	if lastDownloaded != int64(len(body)) {
		t.Fatalf("final reported downloaded = %d, want %d", lastDownloaded, len(body))
	}
	if lastTotalSize != int64(len(body)) {
		t.Fatalf("final reported totalSize = %d, want %d (from Content-Length)", lastTotalSize, len(body))
	}
}

// TestDownloadToFileNilProgressSkipsWrapping covers the "nil onProgress
// behaves exactly as before" contract: downloadToFile must still succeed
// and write the full content when no callback is supplied at all.
func TestDownloadToFileNilProgressSkipsWrapping(t *testing.T) {
	withUpdateTargetAllowed(t)
	const body = "no progress callback here"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
	defer server.Close()

	dir := t.TempDir()
	path, err := downloadToFile(context.Background(), server.Client(), server.URL, dir, maxDownloadBytes, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(path)

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != body {
		t.Fatalf("downloaded content = %q, want %q", got, body)
	}
}

func TestDecompressGzipToFileRoundTrips(t *testing.T) {
	dir := t.TempDir()
	srcPath := filepath.Join(dir, "asset.gz")
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	if _, err := gz.Write([]byte("fake mihomo binary content")); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, srcPath, buf.Bytes(), 0o644)

	outPath, err := decompressGzipToFile(srcPath, dir)
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(outPath)
	got, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "fake mihomo binary content" {
		t.Fatalf("decompressed content = %q, want %q", got, "fake mihomo binary content")
	}
}

func TestExtractZipExecutableRequiresExactlyOneEntry(t *testing.T) {
	dir := t.TempDir()

	// Exactly one entry: succeeds and returns its content.
	onePath := filepath.Join(dir, "one.zip")
	var oneBuf bytes.Buffer
	zw := zip.NewWriter(&oneBuf)
	w, err := zw.Create("mihomo-windows-amd64.exe")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte("fake exe content")); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, onePath, oneBuf.Bytes(), 0o644)

	outPath, err := extractZipExecutableToFile(onePath, dir)
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(outPath)
	got, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "fake exe content" {
		t.Fatalf("extracted content = %q, want %q", got, "fake exe content")
	}

	// Two entries: rejected outright rather than guessed at.
	twoPath := filepath.Join(dir, "two.zip")
	var twoBuf bytes.Buffer
	zw2 := zip.NewWriter(&twoBuf)
	for _, name := range []string{"a.exe", "b.txt"} {
		w, err := zw2.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte("x")); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw2.Close(); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, twoPath, twoBuf.Bytes(), 0o644)

	if _, err := extractZipExecutableToFile(twoPath, dir); err == nil {
		t.Fatal("two-entry zip: want error, got nil")
	}
}

// TestAtomicSwapReplacesTargetAndKeepsBackup covers the required "atomic
// swap + rollback: swap replaces target and keeps .bak" case.
func TestAtomicSwapReplacesTargetAndKeepsBackup(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "mihomo")
	writeTestFile(t, target, []byte("old binary"), 0o644)

	newPath := filepath.Join(dir, ".new-binary")
	writeTestFile(t, newPath, []byte("new binary"), 0o644)

	if err := atomicSwap(newPath, target, true); err != nil {
		t.Fatalf("atomicSwap: %v", err)
	}

	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "new binary" {
		t.Fatalf("target content = %q, want %q", got, "new binary")
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(target)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm()&0o100 == 0 {
			t.Fatalf("target mode = %v, want executable bit set", info.Mode())
		}
	}

	bak, err := os.ReadFile(target + ".bak")
	if err != nil {
		t.Fatalf("read .bak: %v", err)
	}
	if string(bak) != "old binary" {
		t.Fatalf(".bak content = %q, want %q", bak, "old binary")
	}
	if _, err := os.Stat(newPath); !os.IsNotExist(err) {
		t.Fatalf("newPath still exists after swap (err=%v), want it renamed away", err)
	}
}

// TestAtomicSwapNoPreviousFile covers installing into a path with nothing
// there yet (no rollback possible/needed, no .bak expected).
func TestAtomicSwapNoPreviousFile(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "geoip.dat")
	newPath := filepath.Join(dir, ".new-geoip")
	writeTestFile(t, newPath, []byte("geodata"), 0o644)

	if err := atomicSwap(newPath, target, false); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "geodata" {
		t.Fatalf("target content = %q, want %q", got, "geodata")
	}
	if _, err := os.Stat(target + ".bak"); !os.IsNotExist(err) {
		t.Fatal("want no .bak when there was nothing to roll back to")
	}
}

// TestAtomicSwapRollsBackOnSimulatedFailure covers "a simulated failure
// rolls back". newPath is never created, simulating the final
// os.Rename(newPath, targetPath) failing for any reason (disk full,
// cross-device, an in-use exe on Windows) without depending on a
// platform-specific failure mode -- the rename simply has no source to
// move. atomicSwap must restore the original file from .bak rather than
// leaving targetPath missing or corrupt. executable=false skips the chmod
// pre-check, which would otherwise fail first (on the missing newPath)
// before atomicSwap ever reaches the rename this test wants to exercise.
func TestAtomicSwapRollsBackOnSimulatedFailure(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "mihomo")
	writeTestFile(t, target, []byte("old binary"), 0o644)
	missingNewPath := filepath.Join(dir, ".new-binary-does-not-exist")

	err := atomicSwap(missingNewPath, target, false)
	if err == nil {
		t.Fatal("want an error when the final rename has nothing to install")
	}

	got, readErr := os.ReadFile(target)
	if readErr != nil {
		t.Fatalf("target missing after failed swap: %v (swap error was %v)", readErr, err)
	}
	if string(got) != "old binary" {
		t.Fatalf("target content after rollback = %q, want %q (original preserved)", got, "old binary")
	}
	if _, statErr := os.Stat(target + ".bak"); !os.IsNotExist(statErr) {
		t.Fatalf(".bak stat error = %v, want IsNotExist (rollback should have renamed it back to target)", statErr)
	}
}

// TestHardenedUpdateClientRejectsLoopbackByHostPin covers the required
// "SSRF: a download URL resolving to loopback/private is rejected" case at
// the https+host-pin layer (validateUpdateTarget, hardened_transport.go):
// a plain http://127.0.0.1 URL is neither https nor an allowed GitHub host,
// so fetchBytes must refuse it before ever attempting to dial.
func TestHardenedUpdateClientRejectsLoopbackByHostPin(t *testing.T) {
	server := httptest.NewServer(nil)
	defer server.Close()

	client := newUpdateHTTPClient()
	_, err := fetchBytes(context.Background(), client, server.URL, maxChecksumBundleBytes, "")
	if err == nil {
		t.Fatal("want an error fetching a loopback httptest.Server through the hardened update client")
	}
}

// TestHardenedUpdateClientRejectsLoopbackByDialTimeCheck covers the SAME
// requirement one layer deeper: even with the https+host-pin check bypassed
// (withUpdateTargetAllowed, simulating a scenario where the URL somehow
// carried an allowed host string), the REAL production transport
// (newUpdateHTTPClient -> newHardenedTransport -> hardenedDialContext)
// still independently re-resolves and blocklist-checks the address at
// actual dial time and refuses the loopback connection -- proving the
// dial-time layer is not just dead code shadowed by the upfront check.
func TestHardenedUpdateClientRejectsLoopbackByDialTimeCheck(t *testing.T) {
	withUpdateTargetAllowed(t)
	server := httptest.NewServer(nil)
	defer server.Close()

	client := newUpdateHTTPClient()
	_, err := fetchBytes(context.Background(), client, server.URL, maxChecksumBundleBytes, "")
	if err == nil {
		t.Fatal("want an error dialing a loopback httptest.Server through the hardened update client's own DialContext")
	}
}
