package app

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// chainCandidatesProfileConfig mirrors subscriptionConfig's shape but adds a
// proxy-provider so tests can verify provider names are reported separately
// from Candidates rather than being offered as chain members.
const chainCandidatesProfileConfig = `mixed-port: 7890
proxies:
  - name: US-01
    type: ss
    server: example.com
    port: 443
  - name: JP-01
    type: ss
    server: example.net
    port: 443
proxy-groups:
  - name: Proxy
    type: select
    proxies:
      - US-01
      - JP-01
      - DIRECT
proxy-providers:
  ProviderB:
    type: http
    url: https://example.com/providerB.yaml
    path: ./providerB.yaml
    interval: 3600
  ProviderA:
    type: http
    url: https://example.com/providerA.yaml
    path: ./providerA.yaml
    interval: 3600
rules:
  - MATCH,Proxy
`

const localProxiesYAML = `- name: local-hop-1
  type: socks5
  server: 127.0.0.1
  port: 1080
- name: local-hop-2
  type: socks5
  server: 127.0.0.1
  port: 1081
`

func TestChainCandidatesOrderingAndProviderNamesExcluded(t *testing.T) {
	result, err := chainCandidates(chainCandidatesProfileConfig, localProxiesYAML)
	if err != nil {
		t.Fatal(err)
	}

	var names []string
	var kinds []string
	for _, c := range result.Candidates {
		names = append(names, c.Name)
		kinds = append(kinds, c.Kind)
	}
	wantNames := []string{globalChainSelectGroupName, "local-hop-1", "local-hop-2", "US-01", "JP-01"}
	wantKinds := []string{"group", "local", "local", "profile", "profile"}
	if strings.Join(names, ",") != strings.Join(wantNames, ",") {
		t.Fatalf("candidate names = %v, want %v", names, wantNames)
	}
	if strings.Join(kinds, ",") != strings.Join(wantKinds, ",") {
		t.Fatalf("candidate kinds = %v, want %v", kinds, wantKinds)
	}

	// proxyProviderNames sorts alphabetically (config.go).
	if strings.Join(result.ProviderNames, ",") != "ProviderA,ProviderB" {
		t.Fatalf("providerNames = %v, want [ProviderA ProviderB]", result.ProviderNames)
	}
	for _, c := range result.Candidates {
		if c.Name == "ProviderA" || c.Name == "ProviderB" {
			t.Fatalf("provider name %q leaked into Candidates: %+v", c.Name, result.Candidates)
		}
	}
	if result.LocalError != "" {
		t.Fatalf("LocalError = %q, want empty for a valid draft", result.LocalError)
	}
	if result.Truncated {
		t.Fatal("Truncated = true, want false for a small candidate set")
	}
}

// TestChainCandidatesDedupesRepeatedName covers a profile inline proxy that
// happens to be literally named 节点选择: it must not be emitted twice (once
// as the synthetic "group" entry, once as a "profile" entry) -- the first
// occurrence (the group) wins.
func TestChainCandidatesDedupesRepeatedName(t *testing.T) {
	cfg := fmt.Sprintf(`proxies:
  - name: %s
    type: ss
    server: example.com
    port: 443
  - name: US-01
    type: ss
    server: example.com
    port: 443
rules:
  - MATCH,DIRECT
`, globalChainSelectGroupName)
	result, err := chainCandidates(cfg, "")
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	var kind string
	for _, c := range result.Candidates {
		if c.Name == globalChainSelectGroupName {
			count++
			kind = c.Kind
		}
	}
	if count != 1 {
		t.Fatalf("%q appeared %d times in Candidates, want 1: %+v", globalChainSelectGroupName, count, result.Candidates)
	}
	if kind != "group" {
		t.Fatalf("kind of %q = %q, want group (first occurrence wins)", globalChainSelectGroupName, kind)
	}
}

// TestChainCandidatesBrokenLocalYAMLYieldsLocalErrorNotFailure covers the
// core UX requirement: a user mid-typing an invalid local-proxies draft must
// still get profile candidates back, with the exact Go error text
// parseLocalProxyItems produces (constants.ts:143 matches it verbatim).
func TestChainCandidatesBrokenLocalYAMLYieldsLocalErrorNotFailure(t *testing.T) {
	badYAML := "not: [valid"
	_, _, parseErr := parseLocalProxyItems(badYAML)
	if parseErr == nil {
		t.Fatal("expected parseLocalProxyItems to fail on malformed YAML for this test to be meaningful")
	}

	result, err := chainCandidates(chainCandidatesProfileConfig, badYAML)
	if err != nil {
		t.Fatalf("chainCandidates must not fail the whole request on a bad local draft, got err: %v", err)
	}
	if result.LocalError != parseErr.Error() {
		t.Fatalf("LocalError = %q, want exact parseLocalProxyItems message %q", result.LocalError, parseErr.Error())
	}
	if !strings.HasPrefix(result.LocalError, "parse local proxies: ") {
		t.Fatalf("LocalError = %q, want it to start with %q", result.LocalError, "parse local proxies: ")
	}
	for _, c := range result.Candidates {
		if c.Kind == "local" {
			t.Fatalf("expected zero local candidates after a parse failure, got %+v", result.Candidates)
		}
	}
	// Profile candidates must still be offered.
	found := false
	for _, c := range result.Candidates {
		if c.Name == "US-01" && c.Kind == "profile" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected profile candidates to survive a broken local draft: %+v", result.Candidates)
	}
}

// TestChainCandidatesLocalProfileNameConflict reproduces config.go:196-200's
// cross-check: a local proxy name colliding with a profile inline name is
// not a YAML parse error, so it needs its own message -- and must drop the
// local names (all of them, matching how a real conflict aborts the whole
// local set) rather than only the offending one.
func TestChainCandidatesLocalProfileNameConflict(t *testing.T) {
	conflicting := `- name: US-01
  type: socks5
  server: 127.0.0.1
  port: 1080
- name: local-hop-2
  type: socks5
  server: 127.0.0.1
  port: 1081
`
	result, err := chainCandidates(chainCandidatesProfileConfig, conflicting)
	if err != nil {
		t.Fatal(err)
	}
	want := `local proxy name "US-01" conflicts with profile proxy`
	if result.LocalError != want {
		t.Fatalf("LocalError = %q, want %q", result.LocalError, want)
	}
	for _, c := range result.Candidates {
		if c.Kind == "local" {
			t.Fatalf("expected zero local candidates after a name conflict, got %+v", result.Candidates)
		}
	}
}

// TestChainCandidatesTruncationKeepsLocalNames covers the cap: local names
// (and the synthetic group entry) are always kept in full, only profile
// names are trimmed, and Truncated is set when that trimming happens.
func TestChainCandidatesTruncationKeepsLocalNames(t *testing.T) {
	var sb strings.Builder
	sb.WriteString("proxies:\n")
	const profileCount = chainCandidatesMax + 10
	for i := 0; i < profileCount; i++ {
		fmt.Fprintf(&sb, "  - name: P%d\n    type: ss\n    server: example.com\n    port: 443\n", i)
	}
	sb.WriteString("rules:\n  - MATCH,DIRECT\n")

	result, err := chainCandidates(sb.String(), localProxiesYAML)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Truncated {
		t.Fatal("Truncated = false, want true when profile names exceed the cap")
	}
	localCount, profileCount2, groupCount := 0, 0, 0
	for _, c := range result.Candidates {
		switch c.Kind {
		case "local":
			localCount++
		case "profile":
			profileCount2++
		case "group":
			groupCount++
		}
	}
	if localCount != 2 {
		t.Fatalf("local candidates = %d, want 2 (all kept despite truncation)", localCount)
	}
	if groupCount != 1 {
		t.Fatalf("group candidates = %d, want 1", groupCount)
	}
	total := localCount + profileCount2 + groupCount
	if total > chainCandidatesMax {
		t.Fatalf("total candidates = %d, want <= %d", total, chainCandidatesMax)
	}
	if profileCount2 != chainCandidatesMax-localCount-groupCount {
		t.Fatalf("profile candidates = %d, want exactly %d (cap - local - group)", profileCount2, chainCandidatesMax-localCount-groupCount)
	}
}

func TestChainCandidatesBadProfileConfigReturnsError(t *testing.T) {
	if _, err := chainCandidates("not: [valid", ""); err == nil {
		t.Fatal("expected an error for malformed profile config YAML")
	}
}

// --- HTTP handler tests ---

func postChainCandidates(t *testing.T, c *Controller, body string) (*httptest.ResponseRecorder, ChainCandidatesResult) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/instances/chain-candidates", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c.handleChainCandidates(rec, req)
	var payload ChainCandidatesResult
	if rec.Code == http.StatusOK {
		if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
			t.Fatalf("decode response: %v (body %s)", err, rec.Body.String())
		}
	}
	return rec, payload
}

func TestHandleChainCandidatesHappyPath(t *testing.T) {
	c := newBatchTestController(t)
	profile, err := c.store.CreateProfile("Main", chainCandidatesProfileConfig)
	if err != nil {
		t.Fatal(err)
	}
	rec, payload := postChainCandidates(t, c, `{"profileId":"`+profile.ID+`","localProxies":""}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body: %s", rec.Code, rec.Body.String())
	}
	if len(payload.Candidates) == 0 {
		t.Fatal("expected at least the group candidate")
	}
}

func TestHandleChainCandidatesRejectsWrongMethod(t *testing.T) {
	c := newBatchTestController(t)
	req := httptest.NewRequest(http.MethodGet, "/api/instances/chain-candidates", nil)
	rec := httptest.NewRecorder()
	c.handleChainCandidates(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405, body: %s", rec.Code, rec.Body.String())
	}
}

func TestHandleChainCandidatesRejectsEmptyProfileID(t *testing.T) {
	c := newBatchTestController(t)
	rec, _ := postChainCandidates(t, c, `{"profileId":"","localProxies":""}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400, body: %s", rec.Code, rec.Body.String())
	}
}

func TestHandleChainCandidatesUnknownProfileReturns404(t *testing.T) {
	c := newBatchTestController(t)
	rec, _ := postChainCandidates(t, c, `{"profileId":"missing"}`)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404, body: %s", rec.Code, rec.Body.String())
	}
}

func TestHandleChainCandidatesBadProfileConfigReturns400(t *testing.T) {
	c := newBatchTestController(t)
	profile, err := c.store.CreateProfile("Broken", "not: [valid")
	if err != nil {
		t.Fatal(err)
	}
	rec, _ := postChainCandidates(t, c, `{"profileId":"`+profile.ID+`"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400, body: %s", rec.Code, rec.Body.String())
	}
}

// TestRouteChainCandidatesPrecedesInstanceRoute proves the more specific
// literal pattern "/api/instances/chain-candidates" wins over the
// "/api/instances/" prefix pattern: handleInstance must never see
// "chain-candidates" as an instance ID. If routing regressed, handleInstance
// would treat this as a POST to instance id "chain-candidates" with no
// action, which its method switch (handleInstanceRoot) 405s -- so a non-405
// response with a real "candidates" payload demonstrates the correct route
// was taken.
func TestRouteChainCandidatesPrecedesInstanceRoute(t *testing.T) {
	c := newBatchTestController(t)
	profile, err := c.store.CreateProfile("Main", chainCandidatesProfileConfig)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	c.RegisterRoutes(mux)

	req := httptest.NewRequest(http.MethodPost, "/api/instances/chain-candidates", strings.NewReader(`{"profileId":"`+profile.ID+`"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (must reach handleChainCandidates, not handleInstance), body: %s", rec.Code, rec.Body.String())
	}
	var payload ChainCandidatesResult
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode: %v (body %s)", err, rec.Body.String())
	}
	if len(payload.Candidates) == 0 {
		t.Fatal("expected candidates in the response")
	}

	// And an actual instance ID under /api/instances/ must still route to
	// handleInstance as before.
	item := createTestInstance(t, c, "Real instance")
	getReq := httptest.NewRequest(http.MethodGet, "/api/instances/"+item.ID, nil)
	getRec := httptest.NewRecorder()
	mux.ServeHTTP(getRec, getReq)
	if getRec.Code != http.StatusOK {
		t.Fatalf("GET /api/instances/{id} status = %d, want 200, body: %s", getRec.Code, getRec.Body.String())
	}
}
