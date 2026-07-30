package app

import (
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestMihomoAssetName(t *testing.T) {
	cases := []struct {
		goos, goarch, tag, want string
		wantErr                 bool
	}{
		{goos: "linux", goarch: "amd64", tag: "v1.19.29", want: "mihomo-linux-amd64-v1.19.29.gz"},
		{goos: "windows", goarch: "amd64", tag: "v1.19.29", want: "mihomo-windows-amd64-v1.19.29.zip"},
		{goos: "darwin", goarch: "arm64", tag: "v1.19.29", want: "mihomo-darwin-arm64-v1.19.29.gz"},
		{goos: "linux", goarch: "arm", tag: "v1.19.29", want: "mihomo-linux-armv7-v1.19.29.gz"},
		{goos: "linux", goarch: "386", tag: "v1.19.29", want: "mihomo-linux-386-v1.19.29.gz"},
		{goos: "plan9", goarch: "amd64", tag: "v1.19.29", wantErr: true},
		{goos: "linux", goarch: "wasm", tag: "v1.19.29", wantErr: true},
	}
	for _, tc := range cases {
		got, err := mihomoAssetName(tc.goos, tc.goarch, tc.tag)
		if tc.wantErr {
			if err == nil {
				t.Errorf("mihomoAssetName(%q,%q,%q) = %q, want error", tc.goos, tc.goarch, tc.tag, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("mihomoAssetName(%q,%q,%q) unexpected error: %v", tc.goos, tc.goarch, tc.tag, err)
			continue
		}
		if got != tc.want {
			t.Errorf("mihomoAssetName(%q,%q,%q) = %q, want %q", tc.goos, tc.goarch, tc.tag, got, tc.want)
		}
	}
}

func TestExtractMihomoVersion(t *testing.T) {
	cases := map[string]string{
		"Mihomo Meta v1.19.29 linux amd64 with go1.24.5 2026-07-18T12:24:55Z": "1.19.29",
		"v1.19.29":      "1.19.29",
		"1.19.29":       "1.19.29",
		"":              "",
		"not a version": "",
	}
	for raw, want := range cases {
		if got := extractMihomoVersion(raw); got != want {
			t.Errorf("extractMihomoVersion(%q) = %q, want %q", raw, got, want)
		}
	}
}

func TestFindChecksumAssetPrefersSidecarOverBundle(t *testing.T) {
	assets := []githubReleaseAsset{
		{Name: "mihomo-linux-amd64-v1.19.29.gz"},
		{Name: "mihomo-linux-amd64-v1.19.29.gz.sha256sum"},
		{Name: "checksums.txt"},
	}
	bundle, sidecar := findChecksumAsset(assets, "mihomo-linux-amd64-v1.19.29.gz")
	if sidecar == nil || sidecar.Name != "mihomo-linux-amd64-v1.19.29.gz.sha256sum" {
		t.Fatalf("sidecar = %v, want the .sha256sum asset", sidecar)
	}
	if bundle == nil || bundle.Name != "checksums.txt" {
		t.Fatalf("bundle = %v, want checksums.txt", bundle)
	}

	// A bundle's presence is name-independent (it is a single shared file
	// that may or may not actually cover any given asset -- resolveChecksum
	// is what actually parses it to find out), so it is still returned even
	// when asked about an asset with no sidecar of its own.
	if b, s := findChecksumAsset(assets, "nothing-published.gz"); b == nil || s != nil {
		t.Fatalf("got bundle=%v sidecar=%v, want bundle=checksums.txt sidecar=nil", b, s)
	}
	// A release with no checksum assets of any kind: both nil.
	if b, s := findChecksumAsset([]githubReleaseAsset{{Name: "mihomo-linux-amd64-v1.19.29.gz"}}, "mihomo-linux-amd64-v1.19.29.gz"); b != nil || s != nil {
		t.Fatalf("release with no checksum assets: got bundle=%v sidecar=%v, want both nil", b, s)
	}
}

// checksumMode selects which checksum source fakeReleaseServer's release
// JSON advertises for the asset, mirroring resolveChecksum's own priority
// order (core_update.go): checksumDigest sets the asset object's own
// "digest" field (GitHub's server-computed primary source), checksumSidecar
// publishes a separate "<name>.sha256sum" asset instead (the fallback path
// for an asset predating the digest field), and checksumNone publishes
// neither.
type checksumMode int

const (
	checksumNone checksumMode = iota
	checksumDigest
	checksumSidecar
)

// fakeReleaseServer builds an httptest.Server that serves a GitHub-shaped
// release JSON at "/latest" plus whatever asset/checksum content mode
// describes, for the given assetName. Returns the server and the resolved
// asset name (this host's GOOS/GOARCH baked into the filename, mirroring
// mihomoAssetName's own choice of compression -- gzip everywhere but
// windows, zip on windows).
func fakeReleaseServer(t *testing.T, tag string, rawContent []byte, mode checksumMode, wrongChecksum bool) (*httptest.Server, string) {
	t.Helper()
	assetName, err := mihomoAssetName(runtime.GOOS, runtime.GOARCH, tag)
	if err != nil {
		t.Skipf("unsupported test host GOOS/GOARCH for mihomo asset naming: %v", err)
	}

	served := compressForAsset(t, assetName, rawContent)
	digestSum := sha256.Sum256(served)
	checksumHex := hex.EncodeToString(digestSum[:])
	if wrongChecksum {
		checksumHex = strings.Repeat("0", 64)
	}

	// server is assigned below, but the "/latest" handler (which needs
	// server.URL to build absolute asset download URLs) is only ever
	// invoked once an actual HTTP request arrives -- strictly after
	// httptest.NewServer has returned and this variable is set -- so the
	// closure capturing it by reference is safe despite server being
	// declared before it is assigned.
	var server *httptest.Server

	mux := http.NewServeMux()
	mux.HandleFunc("/latest", func(w http.ResponseWriter, r *http.Request) {
		asset := map[string]any{
			"name": assetName, "size": len(served), "browser_download_url": server.URL + "/assets/" + assetName,
		}
		if mode == checksumDigest {
			asset["digest"] = "sha256:" + checksumHex
		}
		assets := []map[string]any{asset}
		if mode == checksumSidecar {
			assets = append(assets, map[string]any{
				"name": assetName + ".sha256sum", "size": 0,
				"browser_download_url": server.URL + "/assets/" + assetName + ".sha256sum",
			})
		}
		release := map[string]any{"tag_name": tag, "assets": assets}
		_ = json.NewEncoder(w).Encode(release)
	})
	mux.HandleFunc("/assets/"+assetName, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(served)
	})
	mux.HandleFunc("/assets/"+assetName+".sha256sum", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(checksumHex + "  " + assetName + "\n"))
	})

	server = httptest.NewServer(mux)
	t.Cleanup(server.Close)

	return server, assetName
}

func compressForAsset(t *testing.T, assetName string, raw []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	if strings.HasSuffix(assetName, ".zip") {
		zw := zip.NewWriter(&buf)
		entryName := strings.TrimSuffix(assetName, ".zip") + ".exe"
		w, err := zw.Create(entryName)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write(raw); err != nil {
			t.Fatal(err)
		}
		if err := zw.Close(); err != nil {
			t.Fatal(err)
		}
		return buf.Bytes()
	}
	gz := gzip.NewWriter(&buf)
	if _, err := gz.Write(raw); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// newCoreTestController builds a Controller whose mihomo binary is a real,
// executable (chmod +x) file on disk -- so c.mihomoFound is true and
// ApplyCoreUpdate has a real same-directory target to swap -- without it
// needing to actually be a runnable mihomo (detectVersion's exec attempt
// against fake content simply fails fast and returns "", which these tests
// account for rather than depend on).
func newCoreTestController(t *testing.T, initialContent []byte) (*Controller, string) {
	t.Helper()
	binDir := t.TempDir()
	mihomoPath := filepath.Join(binDir, "mihomo")
	if err := os.WriteFile(mihomoPath, initialContent, 0o755); err != nil {
		t.Fatal(err)
	}
	c, err := NewController(Options{
		DataDir:    t.TempDir(),
		MihomoPath: mihomoPath,
		AppVersion: "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Shutdown(context.Background()) })
	if !c.mihomoFound {
		t.Fatalf("mihomoFound = false, want true (resolved path %q)", c.mihomoPath)
	}
	return c, mihomoPath
}

// setupCoreUpdateServer wires mihomoReleaseAPI/c.updateClient/withUpdateTargetAllowed
// for a fakeReleaseServer, saving the boilerplate every ApplyCoreUpdate test
// below repeats.
func setupCoreUpdateServer(t *testing.T, c *Controller, server *httptest.Server) {
	t.Helper()
	withUpdateTargetAllowed(t)
	original := mihomoReleaseAPI
	mihomoReleaseAPI = server.URL + "/latest"
	t.Cleanup(func() { mihomoReleaseAPI = original })
	c.updateClient = server.Client()
}

// TestApplyCoreUpdateHappyPathViaAssetDigest covers the required "a release
// asset WITH a populated digest drives a real verified core update
// happy-path (matching digest -> swap)" case: GitHub's server-computed
// asset.digest field, not a sidecar file, is resolveChecksum's PRIMARY
// checksum source (core_update.go's header comment) -- this is the path a
// real update against MetaCubeX/mihomo's actual current releases takes,
// confirmed live (every asset on v1.19.29 carries a populated digest).
func TestApplyCoreUpdateHappyPathViaAssetDigest(t *testing.T) {
	c, mihomoPath := newCoreTestController(t, []byte("old fake mihomo"))
	server, _ := fakeReleaseServer(t, "v9.9.9", []byte("new fake mihomo content"), checksumDigest, false)
	setupCoreUpdateServer(t, c, server)

	result, err := c.ApplyCoreUpdate(context.Background())
	if err != nil {
		t.Fatalf("ApplyCoreUpdate: %v", err)
	}
	_ = result

	got, err := os.ReadFile(mihomoPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "new fake mihomo content" {
		t.Fatalf("mihomoPath content = %q, want %q", got, "new fake mihomo content")
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(mihomoPath)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm()&0o100 == 0 {
			t.Fatalf("mode = %v, want executable bit set", info.Mode())
		}
	}
	bak, err := os.ReadFile(mihomoPath + ".bak")
	if err != nil {
		t.Fatalf("read .bak: %v", err)
	}
	if string(bak) != "old fake mihomo" {
		t.Fatalf(".bak content = %q, want the previous binary preserved", bak)
	}

	status := c.CoreUpdateStatus(context.Background())
	if !status.ChecksumAvailable {
		t.Fatal("ChecksumAvailable = false, want true when the asset carries a populated digest")
	}
}

// TestApplyCoreUpdateAbortsOnDigestMismatch covers the required "mismatched
// digest -> abort, target untouched" case for the asset.digest primary
// path specifically (TestApplyCoreUpdateAbortsOnSidecarChecksumMismatch
// below covers the sidecar fallback path).
func TestApplyCoreUpdateAbortsOnDigestMismatch(t *testing.T) {
	c, mihomoPath := newCoreTestController(t, []byte("old fake mihomo"))
	server, _ := fakeReleaseServer(t, "v9.9.9", []byte("new fake mihomo content"), checksumDigest, true)
	setupCoreUpdateServer(t, c, server)

	if _, err := c.ApplyCoreUpdate(context.Background()); err == nil {
		t.Fatal("want an error on digest mismatch")
	}

	got, err := os.ReadFile(mihomoPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "old fake mihomo" {
		t.Fatalf("mihomoPath content = %q, want untouched %q", got, "old fake mihomo")
	}
	if _, err := os.Stat(mihomoPath + ".bak"); !os.IsNotExist(err) {
		t.Fatal("want no .bak written -- the abort must happen before any swap")
	}
}

// TestApplyCoreUpdateFallsBackToSidecarWhenDigestAbsent covers the
// secondary path: an asset with no .Digest (the "older asset" edge case
// core_update.go's header comment describes) still resolves and installs
// via the sidecar checksum file, proving the fallback wiring works and is
// not just dead code now that digest is primary.
func TestApplyCoreUpdateFallsBackToSidecarWhenDigestAbsent(t *testing.T) {
	c, mihomoPath := newCoreTestController(t, []byte("old fake mihomo"))
	server, _ := fakeReleaseServer(t, "v9.9.9", []byte("new fake mihomo content"), checksumSidecar, false)
	setupCoreUpdateServer(t, c, server)

	if _, err := c.ApplyCoreUpdate(context.Background()); err != nil {
		t.Fatalf("ApplyCoreUpdate: %v", err)
	}
	got, err := os.ReadFile(mihomoPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "new fake mihomo content" {
		t.Fatalf("mihomoPath content = %q, want %q", got, "new fake mihomo content")
	}
}

// TestApplyCoreUpdateAbortsOnSidecarChecksumMismatch is the sidecar-fallback
// counterpart to TestApplyCoreUpdateAbortsOnDigestMismatch.
func TestApplyCoreUpdateAbortsOnSidecarChecksumMismatch(t *testing.T) {
	c, mihomoPath := newCoreTestController(t, []byte("old fake mihomo"))
	server, _ := fakeReleaseServer(t, "v9.9.9", []byte("new fake mihomo content"), checksumSidecar, true)
	setupCoreUpdateServer(t, c, server)

	if _, err := c.ApplyCoreUpdate(context.Background()); err == nil {
		t.Fatal("want an error on checksum mismatch")
	}
	got, err := os.ReadFile(mihomoPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "old fake mihomo" {
		t.Fatalf("mihomoPath content = %q, want untouched %q", got, "old fake mihomo")
	}
}

// TestApplyCoreUpdateAbortsWhenChecksumUnavailable covers the genuine edge
// case core_update.go's header comment describes: an asset with neither a
// populated digest NOR a sidecar/bundle fallback. This should be rare
// against MetaCubeX/mihomo's current stable releases (every asset checked
// live carries a digest) -- it is no longer "the common case" a first pass
// here mistakenly assumed, just the one genuinely-unverifiable edge the
// mandatory "no checksum, no update" rule must still cover correctly.
func TestApplyCoreUpdateAbortsWhenChecksumUnavailable(t *testing.T) {
	c, mihomoPath := newCoreTestController(t, []byte("old fake mihomo"))
	server, _ := fakeReleaseServer(t, "v9.9.9", []byte("new fake mihomo content"), checksumNone, false)
	setupCoreUpdateServer(t, c, server)

	_, err := c.ApplyCoreUpdate(context.Background())
	if err == nil {
		t.Fatal("want an error when the release publishes no checksum")
	}
	if !strings.Contains(err.Error(), errChecksumUnavailable.Error()) {
		t.Fatalf("error = %v, want it to mention %q", err, errChecksumUnavailable)
	}

	got, err := os.ReadFile(mihomoPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "old fake mihomo" {
		t.Fatalf("mihomoPath content = %q, want untouched", got)
	}

	status := c.CoreUpdateStatus(context.Background())
	if status.ChecksumAvailable {
		t.Fatal("ChecksumAvailable = true, want false when no checksum source is published")
	}
}

// TestApplyCoreUpdateRefusedWhileInstanceRunning covers the required "core
// swap refused while an instance is 'running'" case.
func TestApplyCoreUpdateRefusedWhileInstanceRunning(t *testing.T) {
	c, mihomoPath := newCoreTestController(t, []byte("old fake mihomo"))
	// Same-package test seam: inject a fake running process directly into
	// the Manager's private map, exactly like
	// TestManagerRestoreSelectionExitsWhenProcessDies (manager_test.go)
	// already does for its own processState -- there is no real mihomo
	// binary in this sandbox to actually launch. Removed before
	// newCoreTestController's own t.Cleanup calls c.Shutdown (registered
	// first, so this later Cleanup runs first, LIFO): Shutdown drives every
	// registered proc through the real Stop path, which dereferences
	// fields (logs, cmd, done) this zero-value fake never populated.
	c.manager.procs["fake-running-instance"] = &processState{}
	t.Cleanup(func() { delete(c.manager.procs, "fake-running-instance") })

	_, err := c.ApplyCoreUpdate(context.Background())
	if err == nil {
		t.Fatal("want an error while an instance is running")
	}
	if !strings.Contains(err.Error(), "running") {
		t.Fatalf("error = %v, want it to mention the running instance", err)
	}
	if status := updateErrorStatus(err); status != http.StatusConflict {
		t.Fatalf("updateErrorStatus = %d, want %d (Conflict)", status, http.StatusConflict)
	}

	got, err := os.ReadFile(mihomoPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "old fake mihomo" {
		t.Fatal("mihomoPath touched despite the running-instance guard")
	}
}

func TestApplyCoreUpdateRefusedWhenMihomoNotFound(t *testing.T) {
	c, err := NewController(Options{
		DataDir:    t.TempDir(),
		MihomoPath: filepath.Join(t.TempDir(), "does-not-exist"),
		AppVersion: "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Shutdown(context.Background()) })
	if c.mihomoFound {
		t.Fatal("mihomoFound = true, want false for a nonexistent -mihomo path")
	}

	_, err = c.ApplyCoreUpdate(context.Background())
	if err == nil {
		t.Fatal("want an error when no mihomo binary is resolved")
	}
	if status := updateErrorStatus(err); status != http.StatusBadRequest {
		t.Fatalf("updateErrorStatus = %d, want %d (BadRequest)", status, http.StatusBadRequest)
	}

	status := c.CoreUpdateStatus(context.Background())
	if status.Installed {
		t.Fatal("Installed = true, want false")
	}
}

// TestBeginCoreUpdateRefusedWhileInstanceRunning covers Manager.
// BeginCoreUpdate's own precondition, independent of ApplyCoreUpdate's
// wiring (already covered by TestApplyCoreUpdateRefusedWhileInstanceRunning
// above).
func TestBeginCoreUpdateRefusedWhileInstanceRunning(t *testing.T) {
	c, _ := newCoreTestController(t, []byte("old fake mihomo"))
	c.manager.procs["fake-running-instance"] = &processState{}
	t.Cleanup(func() { delete(c.manager.procs, "fake-running-instance") })

	if err := c.manager.BeginCoreUpdate(); err == nil {
		t.Fatal("want BeginCoreUpdate to refuse while an instance is running")
	}
}

// TestStartContextRefusedDuringCoreUpdate covers Fix 2's core requirement:
// once BeginCoreUpdate has armed the gate, a Start racing the in-flight
// core update (which can legitimately run for minutes -- the download and
// verification, not just atomicSwap's instant) must be refused for the
// WHOLE window, not just at the two instants an AnyRunning() check happens
// to run. Uses a real store-backed instance (not just an injected fake
// processState) so startContext actually reaches the coreUpdating check
// rather than failing earlier on "instance/profile not found".
func TestStartContextRefusedDuringCoreUpdate(t *testing.T) {
	c, _ := newCoreTestController(t, []byte("old fake mihomo"))
	item, err := c.store.Create("Gate", "", defaultUserConfig, 28210, 29210)
	if err != nil {
		t.Fatal(err)
	}

	if err := c.manager.BeginCoreUpdate(); err != nil {
		t.Fatalf("BeginCoreUpdate: %v", err)
	}

	startErr := c.manager.Start(item.ID)
	if startErr == nil {
		t.Fatal("want Start to be refused while a core update is in progress")
	}
	if !strings.Contains(startErr.Error(), "core") {
		t.Fatalf("error = %v, want it to mention the core update", startErr)
	}

	c.manager.EndCoreUpdate()

	// After the gate releases, Start must at least get PAST the
	// coreUpdating check -- proving EndCoreUpdate actually released it,
	// not just that some other guard happened to also refuse. This
	// sandbox's fake mihomo content is not a real executable, so the
	// start itself is expected to fail too, just for a different (exec)
	// reason, not the gate.
	startErr = c.manager.Start(item.ID)
	if startErr != nil && strings.Contains(startErr.Error(), "core") {
		t.Fatalf("Start still refused by the core-update gate after EndCoreUpdate: %v", startErr)
	}
	_ = c.manager.Stop(item.ID)
}

// TestHandleCoreUpdatePOSTSingleFlight covers Fix 3: two concurrent POSTs
// must not both enter ApplyCoreUpdate. A slow release-fetch handler holds
// the first request inside ApplyCoreUpdate (past coreUpdateMu.TryLock())
// long enough to deterministically observe the second request's immediate
// 409, without any sleep-based flakiness.
func TestHandleCoreUpdatePOSTSingleFlight(t *testing.T) {
	withUpdateTargetAllowed(t)
	c, _ := newCoreTestController(t, []byte("old fake mihomo"))

	started := make(chan struct{})
	var startedOnce sync.Once
	proceed := make(chan struct{})
	mux := http.NewServeMux()
	mux.HandleFunc("/latest", func(w http.ResponseWriter, r *http.Request) {
		startedOnce.Do(func() { close(started) })
		<-proceed
		w.WriteHeader(http.StatusInternalServerError)
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	original := mihomoReleaseAPI
	mihomoReleaseAPI = server.URL + "/latest"
	t.Cleanup(func() { mihomoReleaseAPI = original })
	c.updateClient = server.Client()

	firstDone := make(chan struct{})
	first := httptest.NewRecorder()
	go func() {
		c.handleCoreUpdate(first, httptest.NewRequest(http.MethodPost, "/api/system/core-update", nil))
		close(firstDone)
	}()

	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("first POST never reached the release fetch (coreUpdateMu.TryLock() may not have been acquired)")
	}

	second := httptest.NewRecorder()
	c.handleCoreUpdate(second, httptest.NewRequest(http.MethodPost, "/api/system/core-update", nil))
	if second.Code != http.StatusConflict {
		t.Fatalf("second concurrent POST status = %d, want %d", second.Code, http.StatusConflict)
	}

	close(proceed)
	select {
	case <-firstDone:
	case <-time.After(2 * time.Second):
		t.Fatal("first POST never finished after being unblocked")
	}
}
