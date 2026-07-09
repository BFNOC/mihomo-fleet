package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestHandleProfileDeleteRemovesUnusedProfile covers arch M2's DELETE
// happy path through the HTTP handler: an unreferenced profile is removed
// and the response is 204.
func TestHandleProfileDeleteRemovesUnusedProfile(t *testing.T) {
	c := newBatchTestController(t)
	profile, err := c.store.CreateProfile("Unused", defaultUserConfig)
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodDelete, "/api/profiles/"+profile.ID, nil)
	rec := httptest.NewRecorder()
	c.handleProfile(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204, body: %s", rec.Code, rec.Body.String())
	}
	if _, ok := c.store.GetProfile(profile.ID); ok {
		t.Fatal("expected profile to be removed")
	}
}

// TestHandleProfileDeleteRejectsWhenReferencedByInstance covers arch M2:
// deleting a profile still referenced by an instance returns 409 with the
// exact validationError message.
func TestHandleProfileDeleteRejectsWhenReferencedByInstance(t *testing.T) {
	withPortFree(t, func(int) bool { return true })
	c := newBatchTestController(t)
	profile, err := c.store.CreateProfile("In use", defaultUserConfig)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.store.Create("Test", profile.ID, "", 28100, 29100); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodDelete, "/api/profiles/"+profile.ID, nil)
	rec := httptest.NewRecorder()
	c.handleProfile(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409, body: %s", rec.Code, rec.Body.String())
	}
	var payload struct {
		Error string `json:"error"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if payload.Error != "profile is in use by existing instances" {
		t.Fatalf("error = %q, want the exact in-use message", payload.Error)
	}
	if _, ok := c.store.GetProfile(profile.ID); !ok {
		t.Fatal("profile should remain after a rejected delete")
	}
}

// TestHandleProfileDeleteUnknownReturnsNotFound covers arch M2: deleting a
// nonexistent profile ID returns 404.
func TestHandleProfileDeleteUnknownReturnsNotFound(t *testing.T) {
	c := newBatchTestController(t)
	req := httptest.NewRequest(http.MethodDelete, "/api/profiles/missing", nil)
	rec := httptest.NewRecorder()
	c.handleProfile(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404, body: %s", rec.Code, rec.Body.String())
	}
}

// TestHandleProfileBogusSubresourceReturnsNotFound covers arch L3: an
// unrecognized sub-resource path under /api/profiles/{id}/ must 404
// instead of falling through to the profile-root method switch (which
// would let GET return the profile itself and PUT even modify it).
func TestHandleProfileBogusSubresourceReturnsNotFound(t *testing.T) {
	c := newBatchTestController(t)
	profile, err := c.store.CreateProfile("Main", defaultUserConfig)
	if err != nil {
		t.Fatal(err)
	}

	getReq := httptest.NewRequest(http.MethodGet, "/api/profiles/"+profile.ID+"/bogus", nil)
	getRec := httptest.NewRecorder()
	c.handleProfile(getRec, getReq)
	if getRec.Code != http.StatusNotFound {
		t.Fatalf("GET bogus subresource status = %d, want 404, body: %s", getRec.Code, getRec.Body.String())
	}

	putReq := httptest.NewRequest(http.MethodPut, "/api/profiles/"+profile.ID+"/bogus", strings.NewReader(`{"name":"Renamed"}`))
	putReq.Header.Set("Content-Type", "application/json")
	putRec := httptest.NewRecorder()
	c.handleProfile(putRec, putReq)
	if putRec.Code != http.StatusNotFound {
		t.Fatalf("PUT bogus subresource status = %d, want 404, body: %s", putRec.Code, putRec.Body.String())
	}

	unchanged, ok := c.store.GetProfile(profile.ID)
	if !ok || unchanged.Name != "Main" {
		t.Fatalf("PUT against a bogus subresource must not modify the profile: %+v", unchanged)
	}
}

// TestControllerBeginSubscriptionUpdateDedupesConcurrentCallers covers
// testing H5: concurrent refresh attempts for the same profile must dedup
// to exactly one in-flight update.
func TestControllerBeginSubscriptionUpdateDedupesConcurrentCallers(t *testing.T) {
	c := newBatchTestController(t)
	const attempts = 20
	var successCount int32
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < attempts; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			if c.beginSubscriptionUpdate("profile-1") {
				atomic.AddInt32(&successCount, 1)
			}
		}()
	}
	close(start)
	wg.Wait()
	if successCount != 1 {
		t.Fatalf("beginSubscriptionUpdate concurrent successes = %d, want exactly 1", successCount)
	}
}

// TestControllerRefreshProfileSubscriptionFailureRecordsErrorAndBacksOff
// covers testing H5's scheduler-closure gap: refreshProfileSubscription is
// exactly the closure body refreshDueSubscriptions spawns per due profile
// (controller.go), called synchronously here instead of through the
// fire-and-forget goroutine so the test doesn't need to synchronize with
// it. A failing fetch must record LastUpdateError, and the resulting
// profile must no longer look due for another immediate attempt
// (profileSubscriptionDue's backoff, subscription.go).
func TestControllerRefreshProfileSubscriptionFailureRecordsErrorAndBacksOff(t *testing.T) {
	withSubscriptionTargetAllowed(t)
	c := newBatchTestController(t)
	// A plain client (not newSubscriptionHTTPClient's) so the request
	// actually reaches the loopback httptest server below -- see
	// withSubscriptionTargetAllowed's doc comment.
	c.subscriptionClient = &http.Client{Timeout: 5 * time.Second}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer server.Close()

	profile, err := c.store.CreateSubscriptionProfile("Provider", server.URL, true, 15, &subscriptionFetchResult{Config: defaultUserConfig})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := c.refreshProfileSubscription(context.Background(), profile.ID); err == nil {
		t.Fatal("expected refreshProfileSubscription to fail against a 500-returning server")
	}

	updated, ok := c.store.GetProfile(profile.ID)
	if !ok {
		t.Fatal("expected profile to still exist after the failed refresh")
	}
	if updated.LastUpdateError == "" {
		t.Fatal("expected LastUpdateError to be recorded after a failed fetch")
	}
	if profileSubscriptionDue(updated, time.Now().UTC()) {
		t.Fatal("expected the just-failed profile to back off rather than immediately be due again")
	}
}
