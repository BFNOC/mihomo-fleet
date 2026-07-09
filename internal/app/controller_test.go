package app

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
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
func TestSecureHandlerHostAndCSRFChecks(t *testing.T) {
	c := newBatchTestController(t)
	mux := http.NewServeMux()
	mux.HandleFunc("/api/probe", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	handler := c.SecureHandler(mux)

	tests := []struct {
		name       string
		method     string
		host       string
		headers    map[string]string
		body       string
		wantStatus int
	}{
		{
			name:       "GET with loopback Host succeeds",
			method:     http.MethodGet,
			host:       "127.0.0.1",
			wantStatus: http.StatusOK,
		},
		{
			name:       "GET with attacker Host is blocked",
			method:     http.MethodGet,
			host:       "attacker.test",
			wantStatus: http.StatusForbidden,
		},
		{
			name:       "POST without X-Mihomo-Fleet header is blocked",
			method:     http.MethodPost,
			host:       "127.0.0.1",
			wantStatus: http.StatusForbidden,
		},
		{
			name:   "POST with header but wrong Content-Type and a body is rejected",
			method: http.MethodPost,
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
			host:       "127.0.0.1",
			headers:    map[string]string{"X-Mihomo-Fleet": "1", "Content-Type": "application/json"},
			body:       "{}",
			wantStatus: http.StatusOK,
		},
		{
			name:       "OPTIONS is exempt from the custom-header requirement",
			method:     http.MethodOptions,
			host:       "127.0.0.1",
			wantStatus: http.StatusOK,
		},
		{
			name:       "HEAD is exempt from the custom-header requirement",
			method:     http.MethodHead,
			host:       "127.0.0.1",
			wantStatus: http.StatusOK,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(tt.method, "/api/probe", strings.NewReader(tt.body))
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
