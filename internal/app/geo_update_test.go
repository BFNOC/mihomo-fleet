package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// fakeGeoReleaseServer builds an httptest.Server serving a meta-rules-dat-
// shaped "latest" release: one asset per files (upstream name -> raw
// content), each with BOTH a populated asset.digest field (the real
// meta-rules-dat release carries one for every asset, confirmed live
// during this feature's development) and a "<name>.sha256sum" sidecar in
// the real "<hex64>␠␠<name>" format -- matching upstream's actual shape,
// where both sources happen to agree. useDigest controls whether the
// digest field is actually populated in THIS fake response, so a test can
// exercise the sidecar-only fallback path deliberately (as if the digest
// field were absent, e.g. an older asset). Omitting an entry from files
// (vs an empty byte slice) means that upstream file simply is not
// published this release -- used by TestGeoUpdateStatusHandlesUnpublishedFile.
func fakeGeoReleaseServer(t *testing.T, files map[string][]byte, useDigest bool) *httptest.Server {
	t.Helper()
	var server *httptest.Server
	mux := http.NewServeMux()
	mux.HandleFunc("/latest", func(w http.ResponseWriter, r *http.Request) {
		var assets []map[string]any
		for name, content := range files {
			asset := map[string]any{
				"name": name, "browser_download_url": server.URL + "/assets/" + name,
			}
			if useDigest {
				digest := sha256.Sum256(content)
				asset["digest"] = "sha256:" + hex.EncodeToString(digest[:])
			}
			assets = append(assets, asset)
			assets = append(assets, map[string]any{
				"name": name + ".sha256sum", "browser_download_url": server.URL + "/assets/" + name + ".sha256sum",
			})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"tag_name": "latest", "assets": assets})
	})
	for name, content := range files {
		name, content := name, content
		digest := sha256.Sum256(content)
		mux.HandleFunc("/assets/"+name, func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write(content)
		})
		mux.HandleFunc("/assets/"+name+".sha256sum", func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(hex.EncodeToString(digest[:]) + "  " + name + "\n"))
		})
	}
	server = httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server
}

// setupGeoUpdateServer wires geoReleaseAPI/c.updateClient/withUpdateTargetAllowed
// for a fakeGeoReleaseServer, saving the boilerplate every ApplyGeoUpdate
// test below repeats.
func setupGeoUpdateServer(t *testing.T, c *Controller, server *httptest.Server) {
	t.Helper()
	withUpdateTargetAllowed(t)
	original := geoReleaseAPI
	geoReleaseAPI = server.URL + "/latest"
	t.Cleanup(func() { geoReleaseAPI = original })
	c.updateClient = server.Client()
}

func newGeoUpdateTestController(t *testing.T) *Controller {
	t.Helper()
	c, err := NewController(Options{
		DataDir:    t.TempDir(),
		MihomoPath: filepath.Join(t.TempDir(), "missing-mihomo"),
		AppVersion: "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Shutdown(context.Background()) })
	return c
}

// TestApplyGeoUpdateHappyPath covers the digest-primary path (see
// fakeGeoReleaseServer's doc comment: the real meta-rules-dat release
// publishes a populated asset.digest for every file, confirmed live).
func TestApplyGeoUpdateHappyPath(t *testing.T) {
	c := newGeoUpdateTestController(t)
	server := fakeGeoReleaseServer(t, map[string][]byte{
		"geoip.dat":         []byte("new geoip content"),
		"geosite.dat":       []byte("new geosite content"),
		"country.mmdb":      []byte("new country mmdb content"),
		"GeoLite2-ASN.mmdb": []byte("new asn mmdb content"),
	}, true)
	setupGeoUpdateServer(t, c, server)

	result, err := c.ApplyGeoUpdate(context.Background())
	if err != nil {
		t.Fatalf("ApplyGeoUpdate: %v", err)
	}
	if len(result.Errors) != 0 {
		t.Fatalf("Errors = %v, want none", result.Errors)
	}
	wantUpdated := map[string]bool{"GeoIP.dat": true, "GeoSite.dat": true, "Country.mmdb": true, "ASN.mmdb": true}
	if len(result.Updated) != len(wantUpdated) {
		t.Fatalf("Updated = %v, want exactly %v", result.Updated, wantUpdated)
	}
	for _, name := range result.Updated {
		if !wantUpdated[name] {
			t.Fatalf("unexpected updated file %q", name)
		}
	}

	geoDir := c.geoDataDir()
	got, err := os.ReadFile(filepath.Join(geoDir, "GeoIP.dat"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "new geoip content" {
		t.Fatalf("GeoIP.dat content = %q, want %q", got, "new geoip content")
	}

	// A second status check must now report everything up to date.
	status := c.GeoUpdateStatus(context.Background())
	for _, file := range status.Files {
		if !file.Present {
			t.Errorf("%s: Present = false after update", file.Name)
		}
		if file.UpdateAvailable {
			t.Errorf("%s: UpdateAvailable = true right after installing the latest content", file.Name)
		}
	}
}

// TestApplyGeoUpdateAbortsOnDigestMismatch covers the required "checksum
// verify: ... mismatched digest ABORTS and leaves the target untouched"
// case for geodata's PRIMARY (asset.digest) checksum source.
func TestApplyGeoUpdateAbortsOnDigestMismatch(t *testing.T) {
	c := newGeoUpdateTestController(t)
	geoDir := c.geoDataDir()
	if err := os.MkdirAll(geoDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(geoDir, "GeoIP.dat"), []byte("old geoip content"), 0o644); err != nil {
		t.Fatal(err)
	}

	var server *httptest.Server
	mux := http.NewServeMux()
	const assetContent = "new geoip content"
	mux.HandleFunc("/latest", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"tag_name": "latest",
			"assets": []map[string]any{
				{
					"name": "geoip.dat", "browser_download_url": server.URL + "/assets/geoip.dat",
					"digest": "sha256:0000000000000000000000000000000000000000000000000000000000000000",
				},
			},
		})
	})
	mux.HandleFunc("/assets/geoip.dat", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(assetContent))
	})
	server = httptest.NewServer(mux)
	t.Cleanup(server.Close)
	setupGeoUpdateServer(t, c, server)

	result, err := c.ApplyGeoUpdate(context.Background())
	if err != nil {
		t.Fatalf("ApplyGeoUpdate top-level error: %v, want nil (per-file failures go in Errors)", err)
	}
	if len(result.Updated) != 0 {
		t.Fatalf("Updated = %v, want none", result.Updated)
	}
	if len(result.Errors) != 1 {
		t.Fatalf("Errors = %v, want exactly one", result.Errors)
	}

	got, err := os.ReadFile(filepath.Join(geoDir, "GeoIP.dat"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "old geoip content" {
		t.Fatalf("GeoIP.dat content = %q, want untouched %q", got, "old geoip content")
	}
	if _, err := os.Stat(filepath.Join(geoDir, "GeoIP.dat.bak")); !os.IsNotExist(err) {
		t.Fatal("want no .bak written -- the abort must happen before any swap")
	}
}

// TestApplyGeoUpdateAbortsOnSidecarChecksumMismatch is the sidecar-fallback
// counterpart (digest field absent, matching an older asset).
func TestApplyGeoUpdateAbortsOnSidecarChecksumMismatch(t *testing.T) {
	c := newGeoUpdateTestController(t)
	geoDir := c.geoDataDir()
	if err := os.MkdirAll(geoDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(geoDir, "GeoIP.dat"), []byte("old geoip content"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Build the server manually (not via fakeGeoReleaseServer) so the
	// sidecar can carry a digest that does not match the served content,
	// and the asset object itself carries no digest field at all.
	var server *httptest.Server
	mux := http.NewServeMux()
	const assetContent = "new geoip content"
	mux.HandleFunc("/latest", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"tag_name": "latest",
			"assets": []map[string]any{
				{"name": "geoip.dat", "browser_download_url": server.URL + "/assets/geoip.dat"},
				{"name": "geoip.dat.sha256sum", "browser_download_url": server.URL + "/assets/geoip.dat.sha256sum"},
			},
		})
	})
	mux.HandleFunc("/assets/geoip.dat", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(assetContent))
	})
	mux.HandleFunc("/assets/geoip.dat.sha256sum", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("0000000000000000000000000000000000000000000000000000000000000000  geoip.dat\n"))
	})
	server = httptest.NewServer(mux)
	t.Cleanup(server.Close)
	setupGeoUpdateServer(t, c, server)

	result, err := c.ApplyGeoUpdate(context.Background())
	if err != nil {
		t.Fatalf("ApplyGeoUpdate top-level error: %v, want nil (per-file failures go in Errors)", err)
	}
	if len(result.Updated) != 0 {
		t.Fatalf("Updated = %v, want none", result.Updated)
	}
	if len(result.Errors) != 1 {
		t.Fatalf("Errors = %v, want exactly one", result.Errors)
	}

	got, err := os.ReadFile(filepath.Join(geoDir, "GeoIP.dat"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "old geoip content" {
		t.Fatalf("GeoIP.dat content = %q, want untouched %q", got, "old geoip content")
	}
	if _, err := os.Stat(filepath.Join(geoDir, "GeoIP.dat.bak")); !os.IsNotExist(err) {
		t.Fatal("want no .bak written -- the abort must happen before any swap")
	}
}

// TestGeoUpdateStatusHandlesUnpublishedFile covers a release that does not
// publish one of the four files at all (e.g. a future meta-rules-dat
// release dropping ASN.mmdb) -- must report it as simply unavailable, not
// error the whole status call.
func TestGeoUpdateStatusHandlesUnpublishedFile(t *testing.T) {
	c := newGeoUpdateTestController(t)
	server := fakeGeoReleaseServer(t, map[string][]byte{
		"geoip.dat": []byte("geoip content"),
	}, true)
	setupGeoUpdateServer(t, c, server)

	status := c.GeoUpdateStatus(context.Background())
	if status.CheckError != "" {
		t.Fatalf("CheckError = %q, want empty", status.CheckError)
	}
	byName := map[string]GeoFileStatus{}
	for _, f := range status.Files {
		byName[f.Name] = f
	}
	if !byName["GeoIP.dat"].ChecksumAvailable {
		t.Fatal("GeoIP.dat: ChecksumAvailable = false, want true (published in this fake release)")
	}
	if byName["ASN.mmdb"].ChecksumAvailable {
		t.Fatal("ASN.mmdb: ChecksumAvailable = true, want false (not published in this fake release)")
	}
}
