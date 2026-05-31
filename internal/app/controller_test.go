package app

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
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

func TestHandleInstancesStartAllReportsPartialFailure(t *testing.T) {
	c := newBatchTestController(t)
	starting := createTestInstance(t, c, "Already starting")
	failing := createTestInstance(t, c, "Needs binary")
	c.manager.mu.Lock()
	c.manager.starting[starting.ID] = true
	c.manager.mu.Unlock()
	t.Cleanup(func() {
		c.manager.mu.Lock()
		delete(c.manager.starting, starting.ID)
		c.manager.mu.Unlock()
	})

	payload := postInstancesBatch(t, c, "start-all", http.StatusOK)
	if payload.Total != 2 || payload.Success != 1 || payload.Failed != 1 {
		t.Fatalf("batch result = total:%d success:%d failed:%d, want 2/1/1", payload.Total, payload.Success, payload.Failed)
	}
	if len(payload.Errors) != 1 || payload.Errors[0].ID != failing.ID {
		t.Fatalf("errors = %#v, want one error for %q", payload.Errors, failing.ID)
	}
	if payload.Errors[0].Error != "mihomo binary not found. Install mihomo or start with -mihomo /path/to/mihomo" {
		t.Fatalf("unexpected error: %q", payload.Errors[0].Error)
	}

	statuses := instanceStatuses(payload.Instances)
	if statuses[starting.ID] != "starting" {
		t.Fatalf("starting instance status = %q, want starting", statuses[starting.ID])
	}
	if statuses[failing.ID] != "error" {
		t.Fatalf("failing instance status = %q, want error", statuses[failing.ID])
	}
}

func TestHandleInstancesStopAllTreatsStoppedInstancesAsSuccess(t *testing.T) {
	c := newBatchTestController(t)
	first := createTestInstance(t, c, "First")
	second := createTestInstance(t, c, "Second")

	payload := postInstancesBatch(t, c, "stop-all", http.StatusOK)
	if payload.Total != 2 || payload.Success != 2 || payload.Failed != 0 {
		t.Fatalf("batch result = total:%d success:%d failed:%d, want 2/2/0", payload.Total, payload.Success, payload.Failed)
	}

	statuses := instanceStatuses(payload.Instances)
	if statuses[first.ID] != "stopped" || statuses[second.ID] != "stopped" {
		t.Fatalf("statuses = %#v, want both stopped", statuses)
	}
}

func TestHandleInstancesRejectsUnknownBatchAction(t *testing.T) {
	c := newBatchTestController(t)
	postInstancesBatch(t, c, "restart-all", http.StatusBadRequest)
}

func TestHandleInstancesStartAllAllowsEmptyFleet(t *testing.T) {
	c := newBatchTestController(t)

	payload := postInstancesBatch(t, c, "start-all", http.StatusOK)
	if payload.Total != 0 || payload.Success != 0 || payload.Failed != 0 {
		t.Fatalf("batch result = total:%d success:%d failed:%d, want 0/0/0", payload.Total, payload.Success, payload.Failed)
	}
	if len(payload.Instances) != 0 {
		t.Fatalf("instances = %d, want 0", len(payload.Instances))
	}
}

type batchResponse struct {
	InstanceBatchResult
	Instances []InstanceView `json:"instances"`
}

func newBatchTestController(t *testing.T) *Controller {
	t.Helper()
	c, err := NewController(Options{
		DataDir:    t.TempDir(),
		MihomoPath: filepath.Join(t.TempDir(), "missing-mihomo"),
		AppVersion: "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		c.Shutdown(context.Background())
	})
	return c
}

func createTestInstance(t *testing.T, c *Controller, name string) *Instance {
	t.Helper()
	item, err := c.store.Create(name, "", defaultUserConfig, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	return item
}

func postInstancesBatch(t *testing.T, c *Controller, action string, wantStatus int) batchResponse {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/instances?action="+action, nil)
	rec := httptest.NewRecorder()
	c.handleInstances(rec, req)
	if rec.Code != wantStatus {
		t.Fatalf("status = %d, want %d, body: %s", rec.Code, wantStatus, rec.Body.String())
	}
	var payload batchResponse
	if rec.Code == http.StatusOK {
		if err := json.NewDecoder(rec.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
	}
	return payload
}

func instanceStatuses(views []InstanceView) map[string]string {
	out := make(map[string]string, len(views))
	for _, view := range views {
		out[view.ID] = view.Status
	}
	return out
}
