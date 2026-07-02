package main

import (
	"os"
	"path/filepath"
	"runtime/debug"
	"testing"
)

func TestResolveBuildMetadataPrefersInjectedValues(t *testing.T) {
	withBuildVars(t, "1.2.3", "abcdef1", "2026-07-02T13:45:00Z")

	got := resolveBuildMetadata(nil, nil)
	if got.Version != "1.2.3" || got.Commit != "abcdef1" || got.BuildDate != "2026-07-02T13:45:00Z" {
		t.Fatalf("metadata = %+v, want injected values", got)
	}
}

func TestResolveBuildMetadataUsesGoBuildInfo(t *testing.T) {
	withBuildVars(t, "dev", "unknown", "unknown")

	info := &debug.BuildInfo{
		Main: debug.Module{Version: "v0.13.0"},
		Settings: []debug.BuildSetting{
			{Key: "vcs.revision", Value: "1234567890abcdef"},
			{Key: "vcs.time", Value: "2026-07-02T13:46:00Z"},
			{Key: "vcs.modified", Value: "true"},
		},
	}
	got := resolveBuildMetadata(info, nil)
	if got.Version != "0.13.0" || got.Commit != "1234567-dirty" || got.BuildDate != "2026-07-02T13:46:00Z" {
		t.Fatalf("metadata = %+v, want build info values", got)
	}
}

func TestResolveBuildMetadataReadsVersionFile(t *testing.T) {
	withBuildVars(t, "dev", "unknown", "unknown")

	dir := t.TempDir()
	versionPath := filepath.Join(dir, "VERSION")
	if err := os.WriteFile(versionPath, []byte("0.12.2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got := resolveBuildMetadata(nil, []string{filepath.Join(dir, "missing"), versionPath})
	if got.Version != "0.12.2" || got.Commit != "unknown" || got.BuildDate != "unknown" {
		t.Fatalf("metadata = %+v, want version file fallback", got)
	}
}

func TestResolveBuildMetadataPrefersVersionFileOverGoPseudoVersion(t *testing.T) {
	withBuildVars(t, "dev", "unknown", "unknown")

	dir := t.TempDir()
	versionPath := filepath.Join(dir, "VERSION")
	if err := os.WriteFile(versionPath, []byte("0.13.0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	info := &debug.BuildInfo{
		Main: debug.Module{Version: "v0.12.1-0.20260702135251-20ca7dedc20d+dirty"},
	}
	got := resolveBuildMetadata(info, []string{versionPath})
	if got.Version != "0.13.0" {
		t.Fatalf("version = %q, want VERSION file value", got.Version)
	}
}

func withBuildVars(t *testing.T, nextVersion, nextCommit, nextBuildDate string) {
	t.Helper()
	oldVersion, oldCommit, oldBuildDate := version, commit, buildDate
	version, commit, buildDate = nextVersion, nextCommit, nextBuildDate
	t.Cleanup(func() {
		version, commit, buildDate = oldVersion, oldCommit, oldBuildDate
	})
}
