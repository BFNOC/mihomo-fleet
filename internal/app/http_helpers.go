package app

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
)

const maxJSONBodyBytes = 2 << 20

func readJSON(r *http.Request, out any) error {
	defer r.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(r.Body, maxJSONBodyBytes+1))
	if err != nil {
		return fmt.Errorf("read request body: %w", err)
	}
	if len(raw) > maxJSONBodyBytes {
		return fmt.Errorf("request body exceeds %d byte limit", maxJSONBodyBytes)
	}

	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(out); err != nil {
		return err
	}
	// 一个请求体只能有一个 JSON 值；否则上游与 Fleet 对同一字节流的
	// 解释可能不一致。仅允许第一个值之后出现空白。
	var trailing any
	if err := dec.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("request body must contain exactly one JSON value")
		}
		return fmt.Errorf("invalid trailing data after JSON value: %w", err)
	}
	return nil
}

func writeJSON(w http.ResponseWriter, payload any) {
	writeJSONStatus(w, http.StatusOK, payload)
}

func writeJSONStatus(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func writeError(w http.ResponseWriter, status int, err error) {
	writeJSONStatus(w, status, map[string]string{"error": err.Error()})
}

func methodNotAllowed(w http.ResponseWriter) {
	writeError(w, http.StatusMethodNotAllowed, errors.New("method not allowed"))
}

// profileProxyGroupsStatus classifies errors from Store.ProfileProxyGroups /
// ProfileProxyGroupsForInstance for the GET .../proxies endpoint: a missing
// profile is a 404, anything else (bad YAML, a global-chain plan error, a
// disk read failure) is a 400, matching the status codes those call sites
// used before they were consolidated behind the two Store methods.
func profileProxyGroupsStatus(err error) int {
	if errors.Is(err, errProfileNotFound) {
		return http.StatusNotFound
	}
	return http.StatusBadRequest
}

// sanitizedProfileConfigReadError classifies an error from
// Store.ReadProfileConfig for the GET /api/profiles/{id}/config response
// (security L-3, docs/review-2026-07-11-security.md): a typed
// profileNotFoundError keeps its exact message (app.js's errorPatterns match
// it verbatim), but ReadProfileConfig's only other failure mode is
// os.ReadFile on the profile's config.yaml -- a raw *os.PathError whose
// Error() text includes the absolute data-dir path. That path is logged
// server-side instead, and the client gets the same "not found" message
// shape errorPatterns already localizes (from the client's perspective the
// two cases -- profile record missing vs. its config file unexpectedly
// missing/unreadable on disk -- are indistinguishable anyway), rather than
// inventing a new user-facing string.
func sanitizedProfileConfigReadError(id string, err error) error {
	if errors.Is(err, errProfileNotFound) {
		return err
	}
	log.Printf("read profile config for %q failed: %v", id, err)
	return fmt.Errorf("profile %q not found", id)
}

// proxyGroupHasNode reports whether groups contains a group named `group`
// that lists `proxy` among its candidates (arch L4): the validation
// handleSelection performs before persisting or applying a selection.
func proxyGroupHasNode(groups []ProfileProxyGroup, group, proxy string) bool {
	for _, g := range groups {
		if g.Name != group {
			continue
		}
		for _, name := range g.All {
			if name == proxy {
				return true
			}
		}
		return false
	}
	return false
}

// proxyGroupKnown reports whether groups contains a group named `group` at
// all (N1, docs/review-2026-07-11-fix-verification-round4.md). handleSelection
// uses this to decide whether the static parse has enough information to
// validate a stopped instance's selection -- an absent group is not
// necessarily invalid, it may just be provider-backed and therefore missing
// from the static parse entirely (see parseProfileProxyGroupsBase).
func proxyGroupKnown(groups []ProfileProxyGroup, group string) bool {
	for _, g := range groups {
		if g.Name == group {
			return true
		}
	}
	return false
}
