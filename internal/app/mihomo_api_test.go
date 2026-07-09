package app

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os/exec"
	"strconv"
	"sync/atomic"
	"testing"
	"time"
)

func TestMihomoProxyDelay(t *testing.T) {
	const testURL = "https://www.gstatic.com/generate_204"
	proxyName := "HK/JP Plus"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got, want := r.URL.EscapedPath(), "/proxies/"+url.PathEscape(proxyName)+"/delay"; got != want {
			t.Fatalf("path = %q, want %q", got, want)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer secret" {
			t.Fatalf("Authorization = %q", got)
		}
		if got := r.URL.Query().Get("timeout"); got != "5000" {
			t.Fatalf("timeout = %q", got)
		}
		if got := r.URL.Query().Get("url"); got != testURL {
			t.Fatalf("url = %q", got)
		}
		_, _ = w.Write([]byte(`{"delay":123}`))
	}))
	defer server.Close()

	delay, err := mihomoProxyDelay(context.Background(), testMihomoItem(t, server.URL), proxyName, testURL, 5000)
	if err != nil {
		t.Fatalf("mihomoProxyDelay() error = %v", err)
	}
	if delay != 123 {
		t.Fatalf("delay = %d, want 123", delay)
	}
}

func TestMihomoRealProxyDelayTakesMinimumPositive(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch calls.Add(1) {
		case 1:
			_, _ = w.Write([]byte(`{"delay":240}`))
		default:
			_, _ = w.Write([]byte(`{"delay":180}`))
		}
	}))
	defer server.Close()

	delay, err := mihomoRealProxyDelay(context.Background(), testMihomoItem(t, server.URL), "JP AWS", defaultLatencyURL, 5000)
	if err != nil {
		t.Fatalf("mihomoRealProxyDelay() error = %v", err)
	}
	if delay != 180 {
		t.Fatalf("delay = %d, want 180", delay)
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("calls = %d, want 2", got)
	}
}

func TestMihomoRealProxyDelayRequiresTwoPositiveResults(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			_, _ = w.Write([]byte(`{"delay":120}`))
			return
		}
		_, _ = w.Write([]byte(`{"delay":0}`))
	}))
	defer server.Close()

	delay, err := mihomoRealProxyDelay(context.Background(), testMihomoItem(t, server.URL), "JP AWS", defaultLatencyURL, 5000)
	if err != nil {
		t.Fatalf("mihomoRealProxyDelay() error = %v", err)
	}
	if delay != 0 {
		t.Fatalf("delay = %d, want 0", delay)
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("calls = %d, want 2", got)
	}
}

func TestMihomoRealProxyDelayStopsAfterFirstZero(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		_, _ = w.Write([]byte(`{"delay":0}`))
	}))
	defer server.Close()

	delay, err := mihomoRealProxyDelay(context.Background(), testMihomoItem(t, server.URL), "JP AWS", defaultLatencyURL, 5000)
	if err != nil {
		t.Fatalf("mihomoRealProxyDelay() error = %v", err)
	}
	if delay != 0 {
		t.Fatalf("delay = %d, want 0", delay)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("calls = %d, want 1", got)
	}
}

func TestMihomoRealProxyDelayReturnsProbeError(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			_, _ = w.Write([]byte(`{"delay":120}`))
			return
		}
		http.Error(w, "bad node", http.StatusServiceUnavailable)
	}))
	defer server.Close()

	if _, err := mihomoRealProxyDelay(context.Background(), testMihomoItem(t, server.URL), "JP AWS", defaultLatencyURL, 5000); err == nil {
		t.Fatal("expected error")
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("calls = %d, want 2", got)
	}
}

func TestMihomoRealProxyDelayTreatsFirstDelayTestFailureAsUnavailable(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"message":"An error occurred in delay test"}`))
	}))
	defer server.Close()

	delay, err := mihomoRealProxyDelay(context.Background(), testMihomoItem(t, server.URL), "JP AWS", defaultLatencyURL, 5000)
	if err != nil {
		t.Fatalf("mihomoRealProxyDelay() error = %v", err)
	}
	if delay != 0 {
		t.Fatalf("delay = %d, want 0", delay)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("calls = %d, want 1", got)
	}
}

func TestMihomoGroupDelay(t *testing.T) {
	groupName := "Proxy Group"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got, want := r.URL.EscapedPath(), "/group/"+url.PathEscape(groupName)+"/delay"; got != want {
			t.Fatalf("path = %q, want %q", got, want)
		}
		_, _ = w.Write([]byte(`{"JP AWS":88,"US AWS":160}`))
	}))
	defer server.Close()

	delays, err := mihomoGroupDelay(context.Background(), testMihomoItem(t, server.URL), groupName, defaultLatencyURL, 5000)
	if err != nil {
		t.Fatalf("mihomoGroupDelay() error = %v", err)
	}
	if delays["JP AWS"] != 88 || delays["US AWS"] != 160 {
		t.Fatalf("delays = %#v", delays)
	}
}

func TestMihomoGroupDelayTreatsDelayTestFailureAsEmptyResult(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"message":"An error occurred in the delay test"}`))
	}))
	defer server.Close()

	delays, err := mihomoGroupDelay(context.Background(), testMihomoItem(t, server.URL), "Proxy", defaultLatencyURL, 5000)
	if err != nil {
		t.Fatalf("mihomoGroupDelay() error = %v", err)
	}
	if len(delays) != 0 {
		t.Fatalf("delays = %#v, want empty", delays)
	}
}

func TestMihomoGroupDelayTreatsTimeoutAsEmptyResult(t *testing.T) {
	tests := []struct {
		name   string
		body   string
		status int
	}{
		{name: "gateway timeout json", status: http.StatusGatewayTimeout, body: `{"message":"Timeout"}`},
		{name: "gateway timeout empty", status: http.StatusGatewayTimeout},
		{name: "service unavailable empty", status: http.StatusServiceUnavailable},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tt.status)
				_, _ = w.Write([]byte(tt.body))
			}))
			defer server.Close()

			delays, err := mihomoGroupDelay(context.Background(), testMihomoItem(t, server.URL), "Proxy", defaultLatencyURL, 5000)
			if err != nil {
				t.Fatalf("mihomoGroupDelay() error = %v", err)
			}
			if len(delays) != 0 {
				t.Fatalf("delays = %#v, want empty", delays)
			}
		})
	}
}

func TestMihomoDelayReturnsUpstreamError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "bad node", http.StatusServiceUnavailable)
	}))
	defer server.Close()

	_, err := mihomoProxyDelay(context.Background(), testMihomoItem(t, server.URL), "bad", defaultLatencyURL, 5000)
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestMihomoProxyDelayTreatsDelayTestFailureAsUnavailable(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"message":"An error occurred in the delay test"}`))
	}))
	defer server.Close()

	delay, err := mihomoProxyDelay(context.Background(), testMihomoItem(t, server.URL), "bad", defaultLatencyURL, 5000)
	if err != nil {
		t.Fatalf("mihomoProxyDelay() error = %v", err)
	}
	if delay != 0 {
		t.Fatalf("delay = %d, want 0", delay)
	}
}

func TestMihomoProxyDelayTreatsTimeoutAsUnavailable(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusGatewayTimeout)
		_, _ = w.Write([]byte(`{"message":"Timeout"}`))
	}))
	defer server.Close()

	delay, err := mihomoProxyDelay(context.Background(), testMihomoItem(t, server.URL), "timeout", defaultLatencyURL, 5000)
	if err != nil {
		t.Fatalf("mihomoProxyDelay() error = %v", err)
	}
	if delay != 0 {
		t.Fatalf("delay = %d, want 0", delay)
	}
}

func TestMihomoProxyDelayTreatsEmptyTimeoutAsUnavailable(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusGatewayTimeout)
	}))
	defer server.Close()

	delay, err := mihomoProxyDelay(context.Background(), testMihomoItem(t, server.URL), "timeout", defaultLatencyURL, 5000)
	if err != nil {
		t.Fatalf("mihomoProxyDelay() error = %v", err)
	}
	if delay != 0 {
		t.Fatalf("delay = %d, want 0", delay)
	}
}

func TestMihomoProxyDelayTreatsEmptyServiceUnavailableAsUnavailable(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()

	delay, err := mihomoProxyDelay(context.Background(), testMihomoItem(t, server.URL), "bad", defaultLatencyURL, 5000)
	if err != nil {
		t.Fatalf("mihomoProxyDelay() error = %v", err)
	}
	if delay != 0 {
		t.Fatalf("delay = %d, want 0", delay)
	}
}

func TestMihomoProxyDelayReturnsUnmatchedTimeoutError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusGatewayTimeout)
		_, _ = w.Write([]byte(`{"message":"server busy timeout"}`))
	}))
	defer server.Close()

	if _, err := mihomoProxyDelay(context.Background(), testMihomoItem(t, server.URL), "timeout", defaultLatencyURL, 5000); err == nil {
		t.Fatal("expected error")
	}
}

// TestClampLatencyTimeoutMS, TestNormalizeLatencyRequestURL and
// TestLatencyRequestBudgetMS split what used to be a single
// TestNormalizeLatencyRequest test covering three unrelated functions
// (testing L7, docs/review-2026-07-11-testing-quality.md): clamping,
// URL validation and budget computation don't share behavior, so a failure
// in one previously masked whether the other two still passed, and the name
// didn't point at which one broke.
func TestClampLatencyTimeoutMS(t *testing.T) {
	tests := []struct {
		name  string
		input int
		want  int
	}{
		{name: "zero uses the default", input: 0, want: defaultLatencyTimeoutMS},
		{name: "below minimum clamps up", input: 1, want: minLatencyTimeoutMS},
		{name: "above maximum clamps down", input: 99999, want: maxLatencyTimeoutMS},
		{name: "within range passes through unchanged", input: 3000, want: 3000},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := clampLatencyTimeoutMS(tt.input); got != tt.want {
				t.Fatalf("clampLatencyTimeoutMS(%d) = %d, want %d", tt.input, got, tt.want)
			}
		})
	}
}

func TestNormalizeLatencyRequestURL(t *testing.T) {
	if _, err := normalizeLatencyRequestURL("file:///etc/passwd"); err == nil {
		t.Fatal("expected an error for a non-http(s) scheme")
	}
	if got, err := normalizeLatencyRequestURL(""); err != nil || got != defaultLatencyURL {
		t.Fatalf("default URL = %q, %v, want %q, nil", got, err, defaultLatencyURL)
	}
	const explicit = "https://example.com/generate_204"
	if got, err := normalizeLatencyRequestURL(explicit); err != nil || got != explicit {
		t.Fatalf("explicit URL = %q, %v, want %q, nil", got, err, explicit)
	}
}

func TestLatencyRequestBudgetMS(t *testing.T) {
	if got := latencyRequestBudgetMS("url", 3000); got != 8000 {
		t.Fatalf("url budget = %d, want 8000", got)
	}
	if got := latencyRequestBudgetMS("real", 3000); got != 11000 {
		t.Fatalf("real budget = %d, want 11000", got)
	}
}

func TestHandleLatencyProxyRequest(t *testing.T) {
	const testURL = "https://www.gstatic.com/generate_204"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got, want := r.URL.EscapedPath(), "/proxies/US%20AWS/delay"; got != want {
			t.Fatalf("path = %q, want %q", got, want)
		}
		_, _ = w.Write([]byte(`{"delay":91}`))
	}))
	defer server.Close()

	controller, instanceID := testLatencyController(t, server.URL, true)
	body := bytes.NewBufferString(`{"proxy":"US AWS","kind":"url","url":"` + testURL + `","timeoutMs":3000}`)
	req := httptest.NewRequest(http.MethodPost, "/api/instances/"+instanceID+"/latency", body)
	rec := httptest.NewRecorder()

	controller.handleLatency(rec, req, instanceID)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var payload struct {
		Delay     int    `json:"delay"`
		URL       string `json:"url"`
		TimeoutMS int    `json:"timeoutMs"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("response json = %v", err)
	}
	if payload.Delay != 91 || payload.TimeoutMS != 3000 || payload.URL != testURL {
		t.Fatalf("payload = %#v", payload)
	}
}

func TestHandleLatencyTreatsDelayTestFailureAsUnavailable(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"message":"An error occurred in the delay test"}`))
	}))
	defer server.Close()

	controller, instanceID := testLatencyController(t, server.URL, true)
	body := bytes.NewBufferString(`{"proxy":"US AWS","kind":"url","timeoutMs":3000}`)
	req := httptest.NewRequest(http.MethodPost, "/api/instances/"+instanceID+"/latency", body)
	rec := httptest.NewRecorder()

	controller.handleLatency(rec, req, instanceID)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var payload struct {
		Delay int `json:"delay"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("response json = %v", err)
	}
	if payload.Delay != 0 {
		t.Fatalf("delay = %d, want 0", payload.Delay)
	}
}

func TestHandleLatencyTreatsTimeoutAsUnavailable(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusGatewayTimeout)
		_, _ = w.Write([]byte(`{"message":"Timeout"}`))
	}))
	defer server.Close()

	controller, instanceID := testLatencyController(t, server.URL, true)
	body := bytes.NewBufferString(`{"proxy":"US AWS","kind":"url","timeoutMs":3000}`)
	req := httptest.NewRequest(http.MethodPost, "/api/instances/"+instanceID+"/latency", body)
	rec := httptest.NewRecorder()

	controller.handleLatency(rec, req, instanceID)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var payload struct {
		Delay int `json:"delay"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("response json = %v", err)
	}
	if payload.Delay != 0 {
		t.Fatalf("delay = %d, want 0", payload.Delay)
	}
}

func TestHandleLatencyGroupRequest(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got, want := r.URL.EscapedPath(), "/group/Proxy/delay"; got != want {
			t.Fatalf("path = %q, want %q", got, want)
		}
		_, _ = w.Write([]byte(`{"US AWS":91}`))
	}))
	defer server.Close()

	controller, instanceID := testLatencyController(t, server.URL, true)
	body := bytes.NewBufferString(`{"group":"Proxy","kind":"url","url":"https://www.gstatic.com/generate_204","timeoutMs":3000}`)
	req := httptest.NewRequest(http.MethodPost, "/api/instances/"+instanceID+"/latency", body)
	rec := httptest.NewRecorder()

	controller.handleLatency(rec, req, instanceID)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var payload struct {
		Delays map[string]int `json:"delays"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("response json = %v", err)
	}
	if payload.Delays["US AWS"] != 91 {
		t.Fatalf("payload = %#v", payload)
	}
}

func TestHandleLatencyRejectsRealGroupWithoutProxy(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("mihomo API should not be called when proxy is missing")
	}))
	defer server.Close()

	controller, instanceID := testLatencyController(t, server.URL, true)
	req := httptest.NewRequest(http.MethodPost, "/api/instances/"+instanceID+"/latency", bytes.NewBufferString(`{"group":"Proxy","kind":"real"}`))
	rec := httptest.NewRecorder()

	controller.handleLatency(rec, req, instanceID)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

func TestHandleLatencyRejectsInvalidKind(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("mihomo API should not be called for invalid kind")
	}))
	defer server.Close()

	controller, instanceID := testLatencyController(t, server.URL, true)
	req := httptest.NewRequest(http.MethodPost, "/api/instances/"+instanceID+"/latency", bytes.NewBufferString(`{"proxy":"US AWS","kind":"icmp"}`))
	rec := httptest.NewRecorder()

	controller.handleLatency(rec, req, instanceID)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

func TestHandleLatencyRejectsStoppedInstance(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("mihomo API should not be called for stopped instances")
	}))
	defer server.Close()

	controller, instanceID := testLatencyController(t, server.URL, false)
	req := httptest.NewRequest(http.MethodPost, "/api/instances/"+instanceID+"/latency", bytes.NewBufferString(`{"proxy":"US AWS"}`))
	rec := httptest.NewRecorder()

	controller.handleLatency(rec, req, instanceID)

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusConflict)
	}
}

func testMihomoItem(t *testing.T, rawURL string) *Instance {
	t.Helper()
	parsed, err := url.Parse(rawURL)
	if err != nil {
		t.Fatal(err)
	}
	_, portText, err := net.SplitHostPort(parsed.Host)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatal(err)
	}
	return &Instance{
		ID:             "test",
		ControllerPort: port,
		Secret:         "secret",
	}
}

// testLatencyController builds a real Store (backed by a temp dir, via
// NewStore) and Manager for the handleLatency tests above, rather than a
// literal &Store{} (no dataDir/storePath) and an empty processState{} with a
// nil cmd (testing L6, docs/review-2026-07-11-testing-quality.md). The
// previous fixture would panic on any code path that calls
// store.saveLocked (empty storePath) or dereferences ps.cmd.Process (nil
// *exec.Cmd) -- handleLatency happens not to touch either today, but the
// fixture itself no longer models an impossible state, so a future handler
// that does exercise those paths fails with a normal test assertion instead
// of a confusing panic.
func testLatencyController(t *testing.T, rawURL string, running bool) (*Controller, string) {
	t.Helper()
	withPortFree(t, func(int) bool { return true })

	target := testMihomoItem(t, rawURL)
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	item, err := store.Create("latency-test", "", defaultUserConfig, 28500, target.ControllerPort)
	if err != nil {
		t.Fatal(err)
	}
	manager := NewManager(store, "")
	if running {
		manager.mu.Lock()
		manager.procs[item.ID] = &processState{cmd: &exec.Cmd{}, started: time.Now().UTC(), done: make(chan struct{})}
		manager.mu.Unlock()
	}
	return &Controller{store: store, manager: manager}, item.ID
}
