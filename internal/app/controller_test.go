package app

import (
	"errors"
	"path/filepath"
	"testing"
)

func TestAllowedHostStrictLoopback(t *testing.T) {
	c := &Controller{}
	allowed := []string{"127.0.0.1:47890", "localhost:47890", "[::1]:47890", "[::1]"}
	for _, host := range allowed {
		if !c.allowedHost(host) {
			t.Fatalf("expected host %q to be allowed", host)
		}
	}

	blocked := []string{"attacker.test:47890", "[::1].attacker.test:47890", "127.0.0.1.attacker:47890", ""}
	for _, host := range blocked {
		if c.allowedHost(host) {
			t.Fatalf("expected host %q to be blocked", host)
		}
	}
}

func TestResolveMihomoPathPriority(t *testing.T) {
	exePath := filepath.Join(t.TempDir(), "mihomo-fleet")
	sameDirPath := filepath.Join(filepath.Dir(exePath), mihomoBinaryNames()[0])

	tests := []struct {
		name       string
		flagPath   string
		exeErr     error
		foundPaths map[string]string
		wantPath   string
		wantSource string
	}{
		{
			name:     "flag path wins",
			flagPath: "/custom/mihomo",
			foundPaths: map[string]string{
				"/custom/mihomo": "/custom/mihomo",
				sameDirPath:      sameDirPath,
				"mihomo":         "/usr/local/bin/mihomo",
			},
			wantPath:   "/custom/mihomo",
			wantSource: "flag",
		},
		{
			name: "same dir wins before PATH",
			foundPaths: map[string]string{
				sameDirPath: sameDirPath,
				"mihomo":    "/usr/local/bin/mihomo",
			},
			wantPath:   sameDirPath,
			wantSource: "same-dir",
		},
		{
			name: "PATH fallback",
			foundPaths: map[string]string{
				"mihomo": "/usr/local/bin/mihomo",
			},
			wantPath:   "/usr/local/bin/mihomo",
			wantSource: "PATH",
		},
		{
			name:   "executable path error falls back to PATH",
			exeErr: errors.New("executable path unavailable"),
			foundPaths: map[string]string{
				"mihomo": "/usr/local/bin/mihomo",
			},
			wantPath:   "/usr/local/bin/mihomo",
			wantSource: "PATH",
		},
		{
			name:       "missing",
			foundPaths: map[string]string{},
			wantSource: "missing",
		},
		{
			name:     "missing flag does not fall back",
			flagPath: "/missing/mihomo",
			foundPaths: map[string]string{
				sameDirPath: sameDirPath,
				"mihomo":    "/usr/local/bin/mihomo",
			},
			wantSource: "missing",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotPath, gotSource := resolveMihomoPath(
				tt.flagPath,
				func() (string, error) { return exePath, tt.exeErr },
				func(path string) (string, error) { return path, nil },
				func(path string) (string, error) {
					if found, ok := tt.foundPaths[path]; ok {
						return found, nil
					}
					return "", errors.New("not found")
				},
			)
			if gotPath != tt.wantPath || gotSource != tt.wantSource {
				t.Fatalf("resolveMihomoPath() = (%q, %q), want (%q, %q)", gotPath, gotSource, tt.wantPath, tt.wantSource)
			}
		})
	}
}

func TestResolveMihomoSameDirEvaluatesSymlink(t *testing.T) {
	dir := t.TempDir()
	linkPath := filepath.Join(dir, "link", "mihomo-fleet")
	realPath := filepath.Join(dir, "real", "mihomo-fleet")
	sameDirPath := filepath.Join(filepath.Dir(realPath), "mihomo")

	got := resolveMihomoSameDir(
		func() (string, error) { return linkPath, nil },
		func(path string) (string, error) {
			if path == linkPath {
				return realPath, nil
			}
			return path, nil
		},
		func(path string) (string, error) {
			if path == sameDirPath {
				return sameDirPath, nil
			}
			return "", errors.New("not found")
		},
		[]string{"mihomo"},
	)
	if got != sameDirPath {
		t.Fatalf("resolveMihomoSameDir() = %q, want %q", got, sameDirPath)
	}
}

func TestResolveMihomoSameDirFallsBackToSecondCandidate(t *testing.T) {
	exePath := filepath.Join(t.TempDir(), "mihomo-fleet")
	secondCandidate := filepath.Join(filepath.Dir(exePath), "mihomo")

	got := resolveMihomoSameDir(
		func() (string, error) { return exePath, nil },
		func(path string) (string, error) { return path, nil },
		func(path string) (string, error) {
			if path == secondCandidate {
				return secondCandidate, nil
			}
			return "", errors.New("not found")
		},
		[]string{"mihomo.exe", "mihomo"},
	)
	if got != secondCandidate {
		t.Fatalf("resolveMihomoSameDir() = %q, want %q", got, secondCandidate)
	}
}
