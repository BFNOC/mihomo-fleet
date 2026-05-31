package app

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestEnsureGeodataFilesLinksLowercaseSourcesWithCanonicalNames(t *testing.T) {
	dir := t.TempDir()
	sourceDir := filepath.Join(dir, "source")
	instanceDir := filepath.Join(dir, "instance")
	if err := os.Mkdir(sourceDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(instanceDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sourceDir, "geosite.dat"), []byte("site-data"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sourceDir, "geoip.dat"), []byte("ip-data"), 0o644); err != nil {
		t.Fatal(err)
	}

	prepared, err := ensureGeodataFiles(instanceDir, []string{sourceDir})
	if err != nil {
		t.Fatal(err)
	}
	if len(prepared) != 2 {
		t.Fatalf("expected two prepared geodata groups, got %v", prepared)
	}

	assertFileContent(t, filepath.Join(instanceDir, "GeoSite.dat"), "site-data")
	assertFileContent(t, filepath.Join(instanceDir, "GeoIP.dat"), "ip-data")
	if filesystemIsCaseSensitive(t, instanceDir) {
		assertFileContent(t, filepath.Join(instanceDir, "geosite.dat"), "site-data")
		assertFileContent(t, filepath.Join(instanceDir, "geoip.dat"), "ip-data")
	}
}

func TestEnsureGeodataFilesKeepsExistingInstanceFileWithoutSharedSource(t *testing.T) {
	dir := t.TempDir()
	instanceDir := filepath.Join(dir, "instance")
	if err := os.Mkdir(instanceDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(instanceDir, "GeoSite.dat"), []byte("existing"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := ensureGeodataFiles(instanceDir, nil); err != nil {
		t.Fatal(err)
	}

	assertFileContent(t, filepath.Join(instanceDir, "GeoSite.dat"), "existing")
}

func TestEnsureGeodataFilesRefreshesStaleRegularFileFromSharedSource(t *testing.T) {
	dir := t.TempDir()
	sourceDir := filepath.Join(dir, "source")
	instanceDir := filepath.Join(dir, "instance")
	if err := os.Mkdir(sourceDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(instanceDir, 0o700); err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(sourceDir, "GeoSite.dat")
	dst := filepath.Join(instanceDir, "GeoSite.dat")
	if err := os.WriteFile(src, []byte("new-source"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst, []byte("old-copy"), 0o644); err != nil {
		t.Fatal(err)
	}
	oldTime := time.Now().Add(-time.Hour)
	if err := os.Chtimes(dst, oldTime, oldTime); err != nil {
		t.Fatal(err)
	}

	if _, err := ensureGeodataFiles(instanceDir, []string{sourceDir}); err != nil {
		t.Fatal(err)
	}

	assertFileContent(t, dst, "new-source")
}

func TestEnsureGeodataFilesReplacesBrokenSymlink(t *testing.T) {
	dir := t.TempDir()
	sourceDir := filepath.Join(dir, "source")
	instanceDir := filepath.Join(dir, "instance")
	if err := os.Mkdir(sourceDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(instanceDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sourceDir, "GeoSite.dat"), []byte("source"), 0o644); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(instanceDir, "GeoSite.dat")
	if err := os.Symlink(filepath.Join(dir, "missing.dat"), dst); err != nil {
		t.Skipf("symlink not available: %v", err)
	}

	if _, err := ensureGeodataFiles(instanceDir, []string{sourceDir}); err != nil {
		t.Fatal(err)
	}

	assertFileContent(t, dst, "source")
}

func TestEnsureGeodataFilesReplacesSymlinkToStaleSource(t *testing.T) {
	dir := t.TempDir()
	oldSourceDir := filepath.Join(dir, "old")
	newSourceDir := filepath.Join(dir, "new")
	instanceDir := filepath.Join(dir, "instance")
	for _, path := range []string{oldSourceDir, newSourceDir, instanceDir} {
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	oldSrc := filepath.Join(oldSourceDir, "GeoSite.dat")
	newSrc := filepath.Join(newSourceDir, "GeoSite.dat")
	if err := os.WriteFile(oldSrc, []byte("old-source"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(newSrc, []byte("new-source"), 0o644); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(instanceDir, "GeoSite.dat")
	if err := os.Symlink(oldSrc, dst); err != nil {
		t.Skipf("symlink not available: %v", err)
	}

	if _, err := ensureGeodataFiles(instanceDir, []string{newSourceDir, oldSourceDir}); err != nil {
		t.Fatal(err)
	}

	assertFileContent(t, dst, "new-source")
}

func TestGeodataSourceDirsIncludesDataGeoBeforeDataDir(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "geo"), 0o700); err != nil {
		t.Fatal(err)
	}
	manager := NewManager(store, "")
	dirs := manager.geodataSourceDirs()
	if len(dirs) < 2 {
		t.Fatalf("expected data source directories, got %v", dirs)
	}
	if dirs[0] != filepath.Join(dir, "geo") {
		t.Fatalf("expected first source dir to be data geo dir, got %v", dirs)
	}
	if dirs[1] != dir {
		t.Fatalf("expected second source dir to be data dir, got %v", dirs)
	}
}

func TestConfigGeodataNeeds(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(configPath, []byte("rules:\n  - GEOSITE,google,Proxy\n  - GEOIP,private,DIRECT\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	needs := configGeodataNeeds(&Profile{ConfigPath: configPath})
	if !needs.site || !needs.ip {
		t.Fatalf("expected config to need both geodata files, got %+v", needs)
	}
}

func TestConfigGeodataNeedsIgnoresNamesAndComments(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	config := "proxies:\n  - name: GeoSite-CN\n    type: direct\n# GEOIP,private,DIRECT\nrules:\n  - MATCH,DIRECT\n"
	if err := os.WriteFile(configPath, []byte(config), 0o644); err != nil {
		t.Fatal(err)
	}
	needs := configGeodataNeeds(&Profile{ConfigPath: configPath})
	if needs.site || needs.ip {
		t.Fatalf("expected config to not need geodata, got %+v", needs)
	}
}

func assertFileContent(t *testing.T, path, want string) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if string(got) != want {
		t.Fatalf("content mismatch for %s: got %q want %q", path, string(got), want)
	}
}

func filesystemIsCaseSensitive(t *testing.T, dir string) bool {
	t.Helper()
	probe := filepath.Join(dir, "CaseProbe")
	if err := os.WriteFile(probe, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := os.Stat(filepath.Join(dir, "caseprobe"))
	if errors.Is(err, os.ErrNotExist) {
		return true
	}
	if err != nil {
		t.Fatal(err)
	}
	return false
}
