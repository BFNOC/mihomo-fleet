package app

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"strings"
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

// TestSecureHandlerHostAndCSRFChecks drives requests through
// c.SecureHandler(mux) (rather than calling the wrapped handler directly)
// so a future regression that drops the Host allowlist, the
// X-Mihomo-Fleet/Content-Type CSRF checks, or the GET/HEAD/OPTIONS
// exemption is caught here instead of passing silently, per review finding
// H2 (docs/review-2026-07-11-testing-quality.md): SecureHandler previously
// had 0% test coverage.
//
// security L-4 (docs/review-2026-07-11-security.md) extended the custom
// X-Mihomo-Fleet header requirement from mutating methods only to GET
// requests under /api/ as well; the cases below cover both that GET/api/
// path (blocked without the header, allowed with it) and that GETs outside
// /api/ (the static UI shell) stay exempt either way.
func TestSecureHandlerHostAndCSRFChecks(t *testing.T) {
	c := newBatchTestController(t)
	mux := http.NewServeMux()
	mux.HandleFunc("/api/probe", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	handler := c.SecureHandler(mux)

	tests := []struct {
		name       string
		method     string
		path       string
		host       string
		headers    map[string]string
		body       string
		wantStatus int
	}{
		{
			name:       "GET /api/ with loopback Host and custom header succeeds",
			method:     http.MethodGet,
			path:       "/api/probe",
			host:       "127.0.0.1",
			headers:    map[string]string{"X-Mihomo-Fleet": "1"},
			wantStatus: http.StatusOK,
		},
		{
			name:       "GET /api/ without X-Mihomo-Fleet header is blocked",
			method:     http.MethodGet,
			path:       "/api/probe",
			host:       "127.0.0.1",
			wantStatus: http.StatusForbidden,
		},
		{
			name:       "GET static path without X-Mihomo-Fleet header succeeds",
			method:     http.MethodGet,
			path:       "/",
			host:       "127.0.0.1",
			wantStatus: http.StatusOK,
		},
		{
			name:       "GET /api/ with attacker Host is blocked",
			method:     http.MethodGet,
			path:       "/api/probe",
			host:       "attacker.test",
			headers:    map[string]string{"X-Mihomo-Fleet": "1"},
			wantStatus: http.StatusForbidden,
		},
		{
			name:       "POST without X-Mihomo-Fleet header is blocked",
			method:     http.MethodPost,
			path:       "/api/probe",
			host:       "127.0.0.1",
			wantStatus: http.StatusForbidden,
		},
		{
			name:   "POST with header but wrong Content-Type and a body is rejected",
			method: http.MethodPost,
			path:   "/api/probe",
			host:   "127.0.0.1",
			headers: map[string]string{
				"X-Mihomo-Fleet": "1",
				"Content-Type":   "text/plain",
			},
			body:       "hello",
			wantStatus: http.StatusUnsupportedMediaType,
		},
		{
			name:       "POST with header and application/json succeeds",
			method:     http.MethodPost,
			path:       "/api/probe",
			host:       "127.0.0.1",
			headers:    map[string]string{"X-Mihomo-Fleet": "1", "Content-Type": "application/json"},
			body:       "{}",
			wantStatus: http.StatusOK,
		},
		{
			name:       "OPTIONS is exempt from the custom-header requirement",
			method:     http.MethodOptions,
			path:       "/api/probe",
			host:       "127.0.0.1",
			wantStatus: http.StatusOK,
		},
		{
			name:       "HEAD is exempt from the custom-header requirement",
			method:     http.MethodHead,
			path:       "/api/probe",
			host:       "127.0.0.1",
			wantStatus: http.StatusOK,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(tt.method, tt.path, strings.NewReader(tt.body))
			req.Host = tt.host
			for k, v := range tt.headers {
				req.Header.Set(k, v)
			}
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d, body: %s", rec.Code, tt.wantStatus, rec.Body.String())
			}
		})
	}
}

// TestSecureHandlerAPISecretGatesAPIPathsOnly covers H-1's fix: once an API
// secret is configured (mandatory for non-loopback binds), every /api/
// request must carry a matching "Authorization: Bearer <secret>" header,
// while static/UI paths stay reachable without one so the page can load far
// enough to prompt for the token. This must be strictly additive to the
// Host/CSRF checks verified above.
func TestSecureHandlerAPISecretGatesAPIPathsOnly(t *testing.T) {
	c := newBatchTestController(t)
	c.SetAPISecret("s3cr3t-token")
	mux := http.NewServeMux()
	mux.HandleFunc("/api/probe", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	handler := c.SecureHandler(mux)

	get := func(path, authHeader string) int {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Host = "127.0.0.1"
		// app.js's api() helper sends this on every request, GET included
		// (security L-4); set it unconditionally here since this test is
		// about api-secret gating specifically, not the header requirement
		// itself (covered by TestSecureHandlerHostAndCSRFChecks).
		req.Header.Set("X-Mihomo-Fleet", "1")
		if authHeader != "" {
			req.Header.Set("Authorization", authHeader)
		}
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec.Code
	}

	if code := get("/api/probe", ""); code != http.StatusUnauthorized {
		t.Fatalf("GET /api/probe without Authorization: status = %d, want 401", code)
	}
	if code := get("/api/probe", "Bearer wrong-token"); code != http.StatusUnauthorized {
		t.Fatalf("GET /api/probe with wrong bearer: status = %d, want 401", code)
	}
	if code := get("/api/probe", "Bearer s3cr3t-token"); code != http.StatusOK {
		t.Fatalf("GET /api/probe with correct bearer: status = %d, want 200", code)
	}
	if code := get("/", ""); code != http.StatusOK {
		t.Fatalf("GET / (static) without Authorization: status = %d, want 200", code)
	}
}

// TestSecureHandlerAPISecretSkipsHostAllowlist covers N1: once an API secret
// is configured, the Host allowlist must not block the documented LAN
// scenario (-bind 0.0.0.0 -api-secret ...), where a remote browser sends the
// LAN Host header rather than "localhost". The Host check is DNS-rebinding
// protection for the unauthenticated loopback setup; with a secret
// configured /api/ is already token-gated, so it is skipped entirely (for
// both static and /api/ paths) rather than selectively -- the token
// check below is still fully enforced.
func TestSecureHandlerAPISecretSkipsHostAllowlist(t *testing.T) {
	c := newBatchTestController(t)
	c.SetAPISecret("s3cr3t-token")
	mux := http.NewServeMux()
	mux.HandleFunc("/api/probe", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	handler := c.SecureHandler(mux)

	get := func(path, host, authHeader string) int {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Host = host
		// See the matching comment in TestSecureHandlerAPISecretGatesAPIPathsOnly.
		req.Header.Set("X-Mihomo-Fleet", "1")
		if authHeader != "" {
			req.Header.Set("Authorization", authHeader)
		}
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec.Code
	}

	const lanHost = "192.168.1.5:47890"

	if code := get("/api/probe", lanHost, "Bearer s3cr3t-token"); code != http.StatusOK {
		t.Fatalf("GET /api/probe with LAN Host and correct bearer: status = %d, want 200", code)
	}
	if code := get("/api/probe", lanHost, ""); code != http.StatusUnauthorized {
		t.Fatalf("GET /api/probe with LAN Host and no token: status = %d, want 401 (not 403)", code)
	}
	if code := get("/", lanHost, ""); code != http.StatusOK {
		t.Fatalf("GET / (static) with LAN Host: status = %d, want 200 so the UI shell can load", code)
	}
}

// TestSecureHandlerNoAPISecretSkipsAuth documents that leaving -api-secret
// unset (the default for loopback binds) preserves the pre-H-1 behavior
// exactly: no Authorization header is required anywhere.
func TestSecureHandlerNoAPISecretSkipsAuth(t *testing.T) {
	c := newBatchTestController(t)
	mux := http.NewServeMux()
	mux.HandleFunc("/api/probe", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	handler := c.SecureHandler(mux)

	req := httptest.NewRequest(http.MethodGet, "/api/probe", nil)
	req.Host = "127.0.0.1"
	req.Header.Set("X-Mihomo-Fleet", "1")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (no secret configured means no auth required)", rec.Code)
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

func TestHandlePortSuggest(t *testing.T) {
	withPortFree(t, func(port int) bool { return port != 28001 && port != 29001 })
	c := newBatchTestController(t)
	if _, err := c.store.Create("Seed", "", defaultUserConfig, 28000, 29000); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/ports/suggest", nil)
	rec := httptest.NewRecorder()
	c.handlePortSuggest(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body: %s", rec.Code, rec.Body.String())
	}
	var payload struct {
		MixedPort      int `json:"mixedPort"`
		ControllerPort int `json:"controllerPort"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if payload.MixedPort != 28002 || payload.ControllerPort != 29002 {
		t.Fatalf("payload = %#v, want mixed 28002 controller 29002", payload)
	}
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", rec.Header().Get("Cache-Control"))
	}
}

func TestHandleInstancesCreateAllocatesPortsInStore(t *testing.T) {
	withPortFree(t, func(int) bool { return true })
	c := newBatchTestController(t)

	view := postInstanceJSON(t, c, `{"name":"Auto ports","proxyBind":"127.0.0.1,192.168.64.1"}`, http.StatusCreated)
	if view.MixedPort != 28000 || view.ControllerPort != 29000 {
		t.Fatalf("ports = (%d, %d), want (28000, 29000)", view.MixedPort, view.ControllerPort)
	}
	if view.ProxyBind != "127.0.0.1,192.168.64.1" {
		t.Fatalf("proxyBind = %q, want normalized multi bind", view.ProxyBind)
	}
}

func TestHandleInstancesCreateRejectsInvalidProxyBind(t *testing.T) {
	withPortFree(t, func(int) bool { return true })
	c := newBatchTestController(t)

	postInstanceJSON(t, c, `{"name":"Bad bind","proxyBind":"127.0.0.1:28000"}`, http.StatusBadRequest)
}

func TestHandleInstancesCreateAcceptsGlobalChainFields(t *testing.T) {
	withPortFree(t, func(int) bool { return true })
	c := newBatchTestController(t)
	profile, err := c.store.CreateProfile("Main", subscriptionConfig)
	if err != nil {
		t.Fatal(err)
	}

	body := `{"name":"Chain","profileId":"` + profile.ID + `","mode":"global-chain","localProxies":"- name: local-hop\n  type: socks5\n  server: 127.0.0.1\n  port: 1080\n","chain":["local-hop","` + globalChainSelectGroupName + `"]}`
	view := postInstanceJSON(t, c, body, http.StatusCreated)
	if view.Mode != InstanceModeGlobalChain || view.LocalProxies == "" || strings.Join(view.Chain, ",") != "local-hop,"+globalChainSelectGroupName {
		t.Fatalf("view lost global-chain fields: %+v", view)
	}
}

func TestHandleInstancesCreateReportsPortConflict(t *testing.T) {
	withPortFree(t, func(int) bool { return true })
	c := newBatchTestController(t)

	postInstanceJSON(t, c, `{"name":"First","mixedPort":28000,"controllerPort":29000}`, http.StatusCreated)
	postInstanceJSON(t, c, `{"name":"Conflict","mixedPort":28000,"controllerPort":29001}`, http.StatusConflict)
}

func TestHandleInstanceUpdateReportsPortConflict(t *testing.T) {
	withPortFree(t, func(int) bool { return true })
	c := newBatchTestController(t)
	first := postInstanceJSON(t, c, `{"name":"First","mixedPort":28000,"controllerPort":29000}`, http.StatusCreated)
	postInstanceJSON(t, c, `{"name":"Second","mixedPort":28001,"controllerPort":29001}`, http.StatusCreated)

	req := httptest.NewRequest(http.MethodPut, "/api/instances/"+first.ID, strings.NewReader(`{"mixedPort":28001}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c.handleInstance(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409, body: %s", rec.Code, rec.Body.String())
	}
}

// TestHandleInstanceUpdateSamePortsReturnsBadRequest covers H1
// (REVIEW-2026-07-04.md): setting mixedPort == controllerPort on update is a
// validation failure (400), not a port-availability conflict (409) -- unlike
// TestHandleInstanceUpdateReportsPortConflict above, which conflicts with
// another instance's already-used port.
func TestHandleInstanceUpdateSamePortsReturnsBadRequest(t *testing.T) {
	withPortFree(t, func(int) bool { return true })
	c := newBatchTestController(t)
	first := postInstanceJSON(t, c, `{"name":"First","mixedPort":28000,"controllerPort":29000}`, http.StatusCreated)

	req := httptest.NewRequest(http.MethodPut, "/api/instances/"+first.ID, strings.NewReader(`{"mixedPort":29000,"controllerPort":29000}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c.handleInstance(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400, body: %s", rec.Code, rec.Body.String())
	}
}

func TestHandleInstanceCloneCreatesStoppedCopy(t *testing.T) {
	withPortFree(t, func(int) bool { return true })
	c := newBatchTestController(t)
	source := postInstanceJSON(t, c, `{"name":"Source","mixedPort":28000,"controllerPort":29000}`, http.StatusCreated)
	if _, err := c.store.SetSelection(source.ID, "Proxy", "UK-01"); err != nil {
		t.Fatal(err)
	}

	clone := postCloneJSON(t, c, source.ID, `{}`, http.StatusCreated)
	if clone.ID == source.ID {
		t.Fatal("clone should have a distinct id")
	}
	if clone.Status != "stopped" {
		t.Fatalf("clone status = %q, want stopped", clone.Status)
	}
	if clone.ProfileID != source.ProfileID || clone.UserConfigPath != source.UserConfigPath {
		t.Fatalf("clone profile = %q/%q, want shared %q/%q", clone.ProfileID, clone.UserConfigPath, source.ProfileID, source.UserConfigPath)
	}
	if clone.MixedPort != 28001 || clone.ControllerPort != 29001 {
		t.Fatalf("clone ports = (%d, %d), want (28001, 29001)", clone.MixedPort, clone.ControllerPort)
	}
	if clone.SelectedProxies["Proxy"] != "UK-01" || clone.SelectedGroup != "Proxy" || clone.SelectedProxy != "UK-01" {
		t.Fatalf("clone selection = %#v/%q/%q, want copied selection", clone.SelectedProxies, clone.SelectedGroup, clone.SelectedProxy)
	}
}

func TestHandleInstanceCloneReportsMissingSource(t *testing.T) {
	c := newBatchTestController(t)
	postCloneJSON(t, c, "missing", `{}`, http.StatusNotFound)
}

func TestHandleInstanceCloneReportsPortConflict(t *testing.T) {
	withPortFree(t, func(int) bool { return true })
	c := newBatchTestController(t)
	source := postInstanceJSON(t, c, `{"name":"Source","mixedPort":28000,"controllerPort":29000}`, http.StatusCreated)

	postCloneJSON(t, c, source.ID, `{"mixedPort":28000,"controllerPort":29001}`, http.StatusConflict)
}

func TestHandleInstanceCloneRejectsSameMixedAndControllerPort(t *testing.T) {
	withPortFree(t, func(int) bool { return true })
	c := newBatchTestController(t)
	source := postInstanceJSON(t, c, `{"name":"Source","mixedPort":28000,"controllerPort":29000}`, http.StatusCreated)

	postCloneJSON(t, c, source.ID, `{"mixedPort":28001,"controllerPort":28001}`, http.StatusBadRequest)
}

// TestHandleInstanceCloneAcceptsEmptyBody covers arch L10
// (docs/review-2026-07-11-go-architecture.md): every clone field is
// optional, so POST .../clone with no body at all (readJSON's decoder
// returns a bare io.EOF for it) must be treated the same as an explicit
// "{}" -- previously it surfaced as the distinctly unhelpful
// {"error":"EOF"} response.
func TestHandleInstanceCloneAcceptsEmptyBody(t *testing.T) {
	withPortFree(t, func(int) bool { return true })
	c := newBatchTestController(t)
	source := postInstanceJSON(t, c, `{"name":"Source","mixedPort":28000,"controllerPort":29000}`, http.StatusCreated)

	clone := postCloneJSON(t, c, source.ID, ``, http.StatusCreated)
	if clone.Name != "Source copy" {
		t.Fatalf("clone name = %q, want %q (default from an all-empty request)", clone.Name, "Source copy")
	}
	if clone.MixedPort != 28001 || clone.ControllerPort != 29001 {
		t.Fatalf("clone ports = (%d, %d), want auto-allocated (28001, 29001)", clone.MixedPort, clone.ControllerPort)
	}
}

// TestHandleInstanceCloneRejectsMalformedBody documents that the io.EOF
// carve-out above is specific to a genuinely empty body -- a syntactically
// invalid non-empty body must still be rejected as a bad request.
func TestHandleInstanceCloneRejectsMalformedBody(t *testing.T) {
	withPortFree(t, func(int) bool { return true })
	c := newBatchTestController(t)
	source := postInstanceJSON(t, c, `{"name":"Source","mixedPort":28000,"controllerPort":29000}`, http.StatusCreated)

	postCloneJSON(t, c, source.ID, `{not json`, http.StatusBadRequest)
}

// TestHandleMihomoProxyRejectsNotRunningInstance covers arch L9
// (docs/review-2026-07-11-go-architecture.md): forwarding to a stopped
// instance's ControllerPort would risk handing that instance's controller
// secret to whatever unrelated local process (if any) now owns the port.
func TestHandleMihomoProxyRejectsNotRunningInstance(t *testing.T) {
	c := newBatchTestController(t)
	item := createTestInstance(t, c, "Stopped")

	req := httptest.NewRequest(http.MethodGet, "/api/mihomo/"+item.ID+"/version", nil)
	rec := httptest.NewRecorder()
	c.handleMihomoProxy(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409, body: %s", rec.Code, rec.Body.String())
	}
}

func TestHandleMihomoProxyRejectsUnknownInstance(t *testing.T) {
	c := newBatchTestController(t)
	req := httptest.NewRequest(http.MethodGet, "/api/mihomo/missing/version", nil)
	rec := httptest.NewRecorder()
	c.handleMihomoProxy(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404, body: %s", rec.Code, rec.Body.String())
	}
}

// TestHandleMihomoProxyForwardsWhileRunningAndCachesProxy covers arch L9's
// other half: a running instance's request is forwarded to its
// ControllerPort with the instance's secret attached, and the underlying
// *httputil.ReverseProxy is cached (reused across the two requests below)
// rather than rebuilt per-request.
func TestHandleMihomoProxyForwardsWhileRunningAndCachesProxy(t *testing.T) {
	c := newBatchTestController(t)
	item := createTestInstance(t, c, "Running")

	var gotPaths []string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer "+item.Secret {
			t.Errorf("Authorization = %q, want Bearer %s", got, item.Secret)
		}
		gotPaths = append(gotPaths, r.URL.Path)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer upstream.Close()

	target := testMihomoItem(t, upstream.URL)
	c.store.mu.Lock()
	c.store.items[item.ID].ControllerPort = target.ControllerPort
	c.store.mu.Unlock()
	c.manager.mu.Lock()
	c.manager.procs[item.ID] = &processState{cmd: &exec.Cmd{}, logs: newLogBuffer(1000), done: make(chan struct{})}
	c.manager.mu.Unlock()
	// This fake processState has no real child process behind it (cmd was
	// never Started), so it must be removed before newBatchTestController's
	// own t.Cleanup calls c.Shutdown -- otherwise Manager.Shutdown would try
	// to gracefully stop it and burn several seconds waiting on a ps.done
	// that nothing will ever close. t.Cleanup runs LIFO, so registering this
	// after newBatchTestController's own means it runs first.
	t.Cleanup(func() {
		c.manager.mu.Lock()
		delete(c.manager.procs, item.ID)
		c.manager.mu.Unlock()
	})

	for _, path := range []string{"/version", "/configs"} {
		req := httptest.NewRequest(http.MethodGet, "/api/mihomo/"+item.ID+path, nil)
		rec := httptest.NewRecorder()
		c.handleMihomoProxy(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200, body: %s", rec.Code, rec.Body.String())
		}
	}
	if strings.Join(gotPaths, ",") != "/version,/configs" {
		t.Fatalf("upstream paths = %v, want [/version /configs]", gotPaths)
	}

	c.mihomoProxiesMu.Lock()
	cached, ok := c.mihomoProxies[target.ControllerPort]
	c.mihomoProxiesMu.Unlock()
	if !ok || cached == nil {
		t.Fatal("expected a cached ReverseProxy for the instance's controller port")
	}
}

// TestHandleSelectionRejectsUnknownGroupOrProxyForStoppedInstance covers
// arch L4 (docs/review-2026-07-11-go-architecture.md): a stopped instance's
// selection request must be validated against the profile's actual proxy
// groups before being persisted -- previously any group/proxy string was
// accepted and silently saved, only surfacing as broken once
// restoreSelection (manager.go) spun for 5s on the instance's next start.
func TestHandleSelectionRejectsUnknownGroupOrProxyForStoppedInstance(t *testing.T) {
	withPortFree(t, func(int) bool { return true })
	c := newBatchTestController(t)
	profile, err := c.store.CreateProfile("Main", subscriptionConfig)
	if err != nil {
		t.Fatal(err)
	}
	item, err := c.store.Create("Selector", profile.ID, "", 0, 0)
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/instances/"+item.ID+"/selection", strings.NewReader(`{"group":"Proxy","proxy":"Nope"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c.handleSelection(rec, req, item.ID)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400, body: %s", rec.Code, rec.Body.String())
	}
	var payload struct {
		Error string `json:"error"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if payload.Error != "unknown proxy group or node" {
		t.Fatalf("error = %q, want the exact validation message", payload.Error)
	}

	updated, ok := c.store.Get(item.ID)
	if !ok {
		t.Fatal("expected the instance to still exist")
	}
	if updated.SelectedGroup != "" || updated.SelectedProxy != "" {
		t.Fatalf("expected the invalid selection to not be persisted, got %#v", updated)
	}
}

// TestHandleSelectionAcceptsKnownGroupAndProxyForStoppedInstance is the
// positive counterpart: a group/proxy pair that actually exists in the
// profile's proxy groups is validated and persisted as before.
func TestHandleSelectionAcceptsKnownGroupAndProxyForStoppedInstance(t *testing.T) {
	withPortFree(t, func(int) bool { return true })
	c := newBatchTestController(t)
	profile, err := c.store.CreateProfile("Main", subscriptionConfig)
	if err != nil {
		t.Fatal(err)
	}
	item, err := c.store.Create("Selector", profile.ID, "", 0, 0)
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/instances/"+item.ID+"/selection", strings.NewReader(`{"group":"Proxy","proxy":"US-01"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c.handleSelection(rec, req, item.ID)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body: %s", rec.Code, rec.Body.String())
	}

	updated, ok := c.store.Get(item.ID)
	if !ok {
		t.Fatal("expected the instance to still exist")
	}
	if updated.SelectedGroup != "Proxy" || updated.SelectedProxy != "US-01" {
		t.Fatalf("selection = %q/%q, want Proxy/US-01", updated.SelectedGroup, updated.SelectedProxy)
	}
}

// TestHandleSelectionAcceptsUnknownGroupForStoppedInstanceWithProviderProfile
// covers N1's stopped-instance half (docs/review-2026-07-11-fix-verification-
// round4.md): a group absent from the static parse entirely -- as any
// proxy-provider-backed group is, since parseProfileProxyGroupsBase
// (subscription.go) only ever sees the static `proxies:` list -- has nothing
// to validate against, so it must be accepted and persisted rather than
// rejected as "unknown". restoreSelection (manager.go) already tolerates an
// unresolvable saved selection at start.
func TestHandleSelectionAcceptsUnknownGroupForStoppedInstanceWithProviderProfile(t *testing.T) {
	withPortFree(t, func(int) bool { return true })
	c := newBatchTestController(t)
	profile, err := c.store.CreateProfile("Main", subscriptionConfig)
	if err != nil {
		t.Fatal(err)
	}
	item, err := c.store.Create("Selector", profile.ID, "", 0, 0)
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/instances/"+item.ID+"/selection", strings.NewReader(`{"group":"ProviderGroup","proxy":"provider-node-1"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c.handleSelection(rec, req, item.ID)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body: %s", rec.Code, rec.Body.String())
	}

	updated, ok := c.store.Get(item.ID)
	if !ok {
		t.Fatal("expected the instance to still exist")
	}
	if updated.SelectedGroup != "ProviderGroup" || updated.SelectedProxy != "provider-node-1" {
		t.Fatalf("selection = %q/%q, want ProviderGroup/provider-node-1", updated.SelectedGroup, updated.SelectedProxy)
	}
}

// selectionTestRunningInstance registers item as running against a fake
// mihomo upstream, mirroring TestHandleMihomoProxyForwardsWhileRunningAndCachesProxy
// above: item keeps its real store ID/Secret (so putMihomoProxy's
// Authorization header check and the store lookup by id both still line up),
// only its ControllerPort is repointed at the upstream test server.
func selectionTestRunningInstance(t *testing.T, c *Controller, item *Instance, upstream *httptest.Server) {
	t.Helper()
	target := testMihomoItem(t, upstream.URL)
	c.store.mu.Lock()
	c.store.items[item.ID].ControllerPort = target.ControllerPort
	c.store.mu.Unlock()
	c.manager.mu.Lock()
	c.manager.procs[item.ID] = &processState{cmd: &exec.Cmd{}, logs: newLogBuffer(1000), done: make(chan struct{})}
	c.manager.mu.Unlock()
	t.Cleanup(func() {
		c.manager.mu.Lock()
		delete(c.manager.procs, item.ID)
		c.manager.mu.Unlock()
	})
}

// TestHandleSelectionRunningInstanceAppliesBeforePersisting covers N1's
// running-instance half (docs/review-2026-07-11-fix-verification-round4.md):
// a running instance's node panel is populated from mihomo's live API and
// can include proxy-provider nodes that never appear in the static YAML
// parse, so handleSelection must skip Store.ProfileProxyGroupsForInstance
// entirely for a running instance and let mihomo itself be the authority --
// applying via putMihomoProxy before persisting (completing arch L4's
// "apply before persist" ordering). The instance's own profile only has a
// static "Proxy" group with no "provider-node" member, which would have
// 400'd under the old static-validation-for-everyone behavior.
func TestHandleSelectionRunningInstanceAppliesBeforePersisting(t *testing.T) {
	withPortFree(t, func(int) bool { return true })
	c := newBatchTestController(t)
	item := createTestInstance(t, c, "Running")

	var gotBody string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			t.Errorf("upstream method = %s, want PUT", r.Method)
		}
		if r.URL.Path != "/proxies/Proxy" {
			t.Errorf("upstream path = %s, want /proxies/Proxy", r.URL.Path)
		}
		body, _ := io.ReadAll(r.Body)
		gotBody = string(body)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer upstream.Close()
	selectionTestRunningInstance(t, c, item, upstream)

	req := httptest.NewRequest(http.MethodPost, "/api/instances/"+item.ID+"/selection", strings.NewReader(`{"group":"Proxy","proxy":"provider-node","apply":true}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c.handleSelection(rec, req, item.ID)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(gotBody, "provider-node") {
		t.Fatalf("upstream PUT body = %q, want it to contain the requested proxy name", gotBody)
	}

	updated, ok := c.store.Get(item.ID)
	if !ok {
		t.Fatal("expected the instance to still exist")
	}
	if updated.SelectedGroup != "Proxy" || updated.SelectedProxy != "provider-node" {
		t.Fatalf("selection = %q/%q, want Proxy/provider-node to be persisted after a successful apply", updated.SelectedGroup, updated.SelectedProxy)
	}
}

// TestHandleSelectionRunningInstanceRejectsWhenMihomoRejects is the negative
// counterpart: mihomo's own rejection of an unknown group/node maps to 400,
// and -- since apply now runs before persist -- the invalid selection must
// not be saved.
func TestHandleSelectionRunningInstanceRejectsWhenMihomoRejects(t *testing.T) {
	withPortFree(t, func(int) bool { return true })
	c := newBatchTestController(t)
	item := createTestInstance(t, c, "Running")
	if _, err := c.store.SetSelection(item.ID, "Proxy", "DIRECT"); err != nil {
		t.Fatal(err)
	}

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "proxy group not found", http.StatusNotFound)
	}))
	defer upstream.Close()
	selectionTestRunningInstance(t, c, item, upstream)

	req := httptest.NewRequest(http.MethodPost, "/api/instances/"+item.ID+"/selection", strings.NewReader(`{"group":"Nope","proxy":"nope","apply":true}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c.handleSelection(rec, req, item.ID)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400, body: %s", rec.Code, rec.Body.String())
	}

	updated, ok := c.store.Get(item.ID)
	if !ok {
		t.Fatal("expected the instance to still exist")
	}
	if updated.SelectedGroup != "Proxy" || updated.SelectedProxy != "DIRECT" {
		t.Fatalf("selection = %q/%q, want the prior Proxy/DIRECT selection left untouched after a rejected apply", updated.SelectedGroup, updated.SelectedProxy)
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

func postInstanceJSON(t *testing.T, c *Controller, body string, wantStatus int) InstanceView {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/instances", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c.handleInstances(rec, req)
	if rec.Code != wantStatus {
		t.Fatalf("status = %d, want %d, body: %s", rec.Code, wantStatus, rec.Body.String())
	}
	var view InstanceView
	if rec.Code == http.StatusCreated {
		if err := json.NewDecoder(rec.Body).Decode(&view); err != nil {
			t.Fatal(err)
		}
	}
	return view
}

func postCloneJSON(t *testing.T, c *Controller, id, body string, wantStatus int) InstanceView {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/instances/"+id+"/clone", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c.handleInstance(rec, req)
	if rec.Code != wantStatus {
		t.Fatalf("status = %d, want %d, body: %s", rec.Code, wantStatus, rec.Body.String())
	}
	var view InstanceView
	if rec.Code == http.StatusCreated {
		if err := json.NewDecoder(rec.Body).Decode(&view); err != nil {
			t.Fatal(err)
		}
	}
	return view
}

func instanceStatuses(views []InstanceView) map[string]string {
	out := make(map[string]string, len(views))
	for _, view := range views {
		out[view.ID] = view.Status
	}
	return out
}
