package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type geoIPResponse struct {
	Available    bool                 `json:"available"`
	Countries    map[string]string    `json:"countries"`
	ASNAvailable bool                 `json:"asnAvailable"`
	ASNs         map[string]ASNRecord `json:"asns"`
}

func newGeoTestController(t *testing.T) (*Controller, string) {
	t.Helper()
	dataDir := t.TempDir()
	c, err := NewController(Options{
		DataDir:    dataDir,
		MihomoPath: filepath.Join(t.TempDir(), "missing-mihomo"),
		AppVersion: "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		c.Shutdown(context.Background())
	})
	return c, dataDir
}

func postGeoIP(t *testing.T, c *Controller, body string) (*httptest.ResponseRecorder, geoIPResponse) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/geoip", strings.NewReader(body))
	rec := httptest.NewRecorder()
	c.handleGeoIP(rec, req)
	var payload geoIPResponse
	if rec.Code == http.StatusOK {
		if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
			t.Fatalf("decode response: %v (body %s)", err, rec.Body.String())
		}
	}
	return rec, payload
}

// writeGeoDatabase drops a database naming 203.0.113.0/24 into the same
// directory prepareGeodata stages instance copies from.
func writeGeoDatabase(t *testing.T, dataDir, iso string) string {
	t.Helper()
	builder := newMmdbTestBuilder(28, 6)
	builder.insertV4(203, 0, 113, 0, 24, builder.addData(mmdbTestCountry(iso)))
	dir := filepath.Join(dataDir, "geo")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "Country.mmdb")
	if err := os.WriteFile(path, builder.build(), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestGeoIPWithoutDatabase(t *testing.T) {
	c, _ := newGeoTestController(t)
	rec, payload := postGeoIP(t, c, `{"ips":["203.0.113.9"]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	// A missing database is a normal state, not an error: the UI just shows no
	// country and stops asking.
	if payload.Available {
		t.Fatal("available = true without a Country.mmdb on disk")
	}
	if len(payload.Countries) != 0 {
		t.Fatalf("countries = %v, want empty", payload.Countries)
	}
}

func TestGeoIPResolvesAndIgnoresUnusableInput(t *testing.T) {
	c, dataDir := newGeoTestController(t)
	writeGeoDatabase(t, dataDir, "SE")

	_, payload := postGeoIP(t, c, `{"ips":["203.0.113.9","198.51.100.4","not-an-ip",""]}`)
	if !payload.Available {
		t.Fatal("available = false with a Country.mmdb staged")
	}
	if got := payload.Countries["203.0.113.9"]; got != "SE" {
		t.Fatalf("203.0.113.9 = %q, want SE", got)
	}
	// Addresses the database does not carry, and inputs that are not addresses
	// at all, are simply absent rather than mapped to an empty string.
	for _, key := range []string{"198.51.100.4", "not-an-ip", ""} {
		if _, ok := payload.Countries[key]; ok {
			t.Fatalf("%q present in %v, want absent", key, payload.Countries)
		}
	}
}

func TestGeoIPPicksUpAReplacedDatabase(t *testing.T) {
	c, dataDir := newGeoTestController(t)
	writeGeoDatabase(t, dataDir, "SE")
	if _, payload := postGeoIP(t, c, `{"ips":["203.0.113.9"]}`); payload.Countries["203.0.113.9"] != "SE" {
		t.Fatalf("first lookup = %v, want SE", payload.Countries)
	}

	path := writeGeoDatabase(t, dataDir, "JP")
	// The stat is throttled, so a replacement inside the interval is only seen
	// once the cached check is expired -- simulate the interval having passed
	// rather than sleeping for it.
	c.geo.mu.Lock()
	c.geo.checked = c.geo.checked.Add(-geoStatInterval - 1)
	c.geo.mu.Unlock()
	// Some filesystems have coarse mtimes; force a distinct one so the reopen
	// is driven by the change, not by luck.
	stat, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, stat.ModTime().Add(2), stat.ModTime().Add(2)); err != nil {
		t.Fatal(err)
	}

	if _, payload := postGeoIP(t, c, `{"ips":["203.0.113.9"]}`); payload.Countries["203.0.113.9"] != "JP" {
		t.Fatalf("after replacement = %v, want JP", payload.Countries)
	}
}

// A stock GeoLite2 file sitting beside mihomo's Country.mmdb must win, since
// the latter answers with vendor tags instead of ISO codes for a lot of space.
func TestGeoIPPrefersGeoLite2OverCountryMmdb(t *testing.T) {
	c, dataDir := newGeoTestController(t)
	writeGeoDatabase(t, dataDir, "SE")

	stock := newMmdbTestBuilder(28, 6)
	stock.insertV4(203, 0, 113, 0, 24, stock.addData(mmdbTestCountry("JP")))
	if err := os.WriteFile(filepath.Join(dataDir, "geo", "GeoLite2-Country.mmdb"), stock.build(), 0o644); err != nil {
		t.Fatal(err)
	}

	_, payload := postGeoIP(t, c, `{"ips":["203.0.113.9"]}`)
	if got := payload.Countries["203.0.113.9"]; got != "JP" {
		t.Fatalf("203.0.113.9 = %q, want JP from GeoLite2-Country.mmdb", got)
	}
}

func TestGeoIPRejectsWrongMethodAndBadBody(t *testing.T) {
	c, _ := newGeoTestController(t)

	rec := httptest.NewRecorder()
	c.handleGeoIP(rec, httptest.NewRequest(http.MethodGet, "/api/geoip", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET status = %d, want 405", rec.Code)
	}

	rec, _ = postGeoIP(t, c, `{"ips":`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("truncated body status = %d, want 400", rec.Code)
	}
}

func TestGeoIPCapsTheBatch(t *testing.T) {
	c, dataDir := newGeoTestController(t)
	writeGeoDatabase(t, dataDir, "SE")

	ips := make([]string, 0, geoBatchLimit+1)
	for i := 0; i < geoBatchLimit; i++ {
		ips = append(ips, "198.51.100.1")
	}
	// The one address that would resolve sits past the cap, so its absence is
	// what proves the truncation happened.
	ips = append(ips, "203.0.113.9")
	body, err := json.Marshal(map[string]any{"ips": ips})
	if err != nil {
		t.Fatal(err)
	}
	_, payload := postGeoIP(t, c, string(body))
	if _, ok := payload.Countries["203.0.113.9"]; ok {
		t.Fatal("address past the batch limit was resolved, want it dropped")
	}
}

// writeASNDatabase drops an ASN database naming 203.0.113.0/24 beside the
// country database, in the same directory prepareGeodata stages from.
func writeASNDatabase(t *testing.T, dataDir string, number uint32, org string) string {
	t.Helper()
	builder := newMmdbTestBuilder(28, 6)
	builder.insertV4(203, 0, 113, 0, 24, builder.addData(mmdbTestMap(
		mmdbTestString("autonomous_system_number"), mmdbTestUint(mmdbUint32, uint64(number)),
		mmdbTestString("autonomous_system_organization"), mmdbTestString(org),
	)))
	dir := filepath.Join(dataDir, "geo")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "ASN.mmdb")
	if err := os.WriteFile(path, builder.build(), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestGeoIPResolvesASNAlongsideCountry(t *testing.T) {
	c, dataDir := newGeoTestController(t)
	writeGeoDatabase(t, dataDir, "NL")
	writeASNDatabase(t, dataDir, 64500, "Example Net")

	_, payload := postGeoIP(t, c, `{"ips":["203.0.113.9","198.51.100.7"]}`)
	if !payload.Available || !payload.ASNAvailable {
		t.Fatalf("available = %v, asnAvailable = %v; want both true", payload.Available, payload.ASNAvailable)
	}
	if payload.Countries["203.0.113.9"] != "NL" {
		t.Fatalf("countries = %v", payload.Countries)
	}
	got := payload.ASNs["203.0.113.9"]
	if got.Number != 64500 || got.Org != "Example Net" {
		t.Fatalf("asns[203.0.113.9] = %+v, want 64500/Example Net", got)
	}
	// An address neither database knows is simply absent from both maps.
	if _, ok := payload.ASNs["198.51.100.7"]; ok {
		t.Fatalf("asns = %v, want no entry for an unknown address", payload.ASNs)
	}
}

// The two databases are independent files. An ASN database with no country
// database beside it must still answer -- the frontend only stops asking when
// both flags are false, and that contract starts here.
func TestGeoIPAnswersASNWithoutCountryDatabase(t *testing.T) {
	c, dataDir := newGeoTestController(t)
	writeASNDatabase(t, dataDir, 15169, "Google LLC")

	_, payload := postGeoIP(t, c, `{"ips":["203.0.113.9"]}`)
	if payload.Available {
		t.Fatal("available = true with no country database staged")
	}
	if !payload.ASNAvailable {
		t.Fatal("asnAvailable = false with an ASN.mmdb staged")
	}
	if got := payload.ASNs["203.0.113.9"]; got.Number != 15169 || got.Org != "Google LLC" {
		t.Fatalf("asns[203.0.113.9] = %+v, want 15169/Google LLC", got)
	}
}

func TestGeoIPWithoutASNDatabase(t *testing.T) {
	c, dataDir := newGeoTestController(t)
	writeGeoDatabase(t, dataDir, "NL")

	_, payload := postGeoIP(t, c, `{"ips":["203.0.113.9"]}`)
	if payload.ASNAvailable {
		t.Fatal("asnAvailable = true without an ASN database on disk")
	}
	if len(payload.ASNs) != 0 {
		t.Fatalf("asns = %v, want empty", payload.ASNs)
	}
	if payload.Countries["203.0.113.9"] != "NL" {
		t.Fatalf("countries = %v; the country lookup must be unaffected", payload.Countries)
	}
}
