package app

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

func (c *Controller) handleProfiles(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		profiles := c.store.ListProfiles()
		sort.Slice(profiles, func(i, j int) bool {
			return profiles[i].CreatedAt.Before(profiles[j].CreatedAt)
		})
		writeJSON(w, map[string]any{"profiles": profiles})
	case http.MethodPost:
		var req struct {
			Name                  string `json:"name"`
			Config                string `json:"config"`
			SubscriptionURL       string `json:"subscriptionUrl"`
			AutoUpdate            *bool  `json:"autoUpdate"`
			UpdateIntervalMinutes int    `json:"updateIntervalMinutes"`
		}
		if err := readJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		if strings.TrimSpace(req.SubscriptionURL) != "" && strings.TrimSpace(req.Config) != "" {
			writeError(w, http.StatusBadRequest, errors.New("subscriptionUrl and config cannot both be set"))
			return
		}
		if strings.TrimSpace(req.SubscriptionURL) != "" {
			autoUpdate := true
			if req.AutoUpdate != nil {
				autoUpdate = *req.AutoUpdate
			}
			ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
			defer cancel()
			fetched, err := fetchSubscription(ctx, c.subscriptionClient, req.SubscriptionURL, c.subscriptionUserAgent())
			if err != nil {
				writeError(w, http.StatusBadRequest, err)
				return
			}
			profile, err := c.store.CreateSubscriptionProfile(req.Name, strings.TrimSpace(req.SubscriptionURL), autoUpdate, req.UpdateIntervalMinutes, fetched)
			if err != nil {
				writeError(w, http.StatusBadRequest, err)
				return
			}
			writeJSONStatus(w, http.StatusCreated, profile)
			return
		}
		profile, err := c.store.CreateProfile(req.Name, req.Config)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSONStatus(w, http.StatusCreated, profile)
	default:
		methodNotAllowed(w)
	}
}

func (c *Controller) handleProfile(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/api/profiles/")
	parts := strings.Split(strings.Trim(rest, "/"), "/")
	if len(parts) == 0 || parts[0] == "" {
		http.NotFound(w, r)
		return
	}
	id := parts[0]
	if len(parts) > 1 {
		switch parts[1] {
		case "config":
			if r.Method != http.MethodGet {
				methodNotAllowed(w)
				return
			}
			cfg, err := c.store.ReadProfileConfig(id)
			if err != nil {
				writeError(w, http.StatusNotFound, sanitizedProfileConfigReadError(id, err))
				return
			}
			writeJSON(w, map[string]string{"config": cfg})
			return
		case "refresh":
			if r.Method != http.MethodPost {
				methodNotAllowed(w)
				return
			}
			profile, changes, err := c.refreshProfileSubscription(r.Context(), id)
			if err != nil {
				writeError(w, http.StatusBadRequest, err)
				return
			}
			// selectionChanges is optional and only present when the refresh
			// actually reassigned an instance's active selection (BUG 2) --
			// a client that doesn't read this field sees the same response
			// as before.
			writeJSON(w, struct {
				*Profile
				SelectionChanges []SelectionReconciliation `json:"selectionChanges,omitempty"`
			}{Profile: profile, SelectionChanges: changes})
			return
		case "proxies":
			if r.Method != http.MethodGet {
				methodNotAllowed(w)
				return
			}
			// The common (non-global-chain) case goes through
			// Store.ProfileProxyGroups/ProfileProxyGroupsForInstance, which
			// cache the parsed proxy-groups keyed by the profile config's
			// (path, modTime, size). This endpoint is polled by the UI
			// roughly every 1.8s while the proxies tab of an open instance
			// is active, so avoiding a full re-read + re-parse of a
			// potentially multi-MB subscription YAML on every poll matters.
			if instanceID := r.URL.Query().Get("instanceId"); instanceID != "" {
				item, ok := c.store.Get(instanceID)
				if !ok {
					writeError(w, http.StatusNotFound, fmt.Errorf("instance %q not found", instanceID))
					return
				}
				groups, err := c.store.ProfileProxyGroupsForInstance(id, item)
				if err != nil {
					writeError(w, profileProxyGroupsStatus(err), err)
					return
				}
				writeJSON(w, map[string]any{"groups": groups})
				return
			}
			groups, err := c.store.ProfileProxyGroups(id)
			if err != nil {
				writeError(w, profileProxyGroupsStatus(err), err)
				return
			}
			writeJSON(w, map[string]any{"groups": groups})
			return
		default:
			// L3 (docs/review-2026-07-11-go-architecture.md): an unrecognized
			// sub-resource previously fell through the switch and was handled
			// by the profile-root method switch below -- so
			// GET /api/profiles/{id}/bogus silently returned the profile
			// itself (and PUT could even modify it) instead of 404, unlike
			// handleInstance's equivalent routing (controller.go's default:
			// http.NotFound).
			http.NotFound(w, r)
			return
		}
	}

	switch r.Method {
	case http.MethodGet:
		profile, ok := c.store.GetProfile(id)
		if !ok {
			http.NotFound(w, r)
			return
		}
		writeJSON(w, profile)
	case http.MethodPut:
		var req struct {
			Name                  string  `json:"name"`
			Config                *string `json:"config"`
			SubscriptionURL       *string `json:"subscriptionUrl"`
			AutoUpdate            *bool   `json:"autoUpdate"`
			UpdateIntervalMinutes *int    `json:"updateIntervalMinutes"`
		}
		if err := readJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		current, ok := c.store.GetProfile(id)
		if !ok {
			writeError(w, http.StatusNotFound, fmt.Errorf("profile %q not found", id))
			return
		}
		if req.Config != nil && current.SubscriptionURL != "" {
			writeError(w, http.StatusBadRequest, errors.New("subscription profile config is refreshed from its URL"))
			return
		}
		var nextURL string
		var urlChanged bool
		if req.SubscriptionURL != nil && strings.TrimSpace(*req.SubscriptionURL) != "" {
			nextURL = strings.TrimSpace(*req.SubscriptionURL)
			parsed, err := url.Parse(nextURL)
			if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
				writeError(w, http.StatusBadRequest, errors.New("subscription URL must start with http:// or https://"))
				return
			}
			urlChanged = current.SubscriptionURL != nextURL
		}

		var profile *Profile
		var err error
		var selectionChanges []SelectionReconciliation
		if urlChanged {
			ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
			fetched, ferr := fetchSubscription(ctx, c.subscriptionClient, nextURL, c.subscriptionUserAgent())
			cancel()
			if ferr != nil {
				writeError(w, http.StatusBadRequest, ferr)
				return
			}
			// arch M4: ReplaceProfileSubscription persists the new URL and
			// applies the fetched config under a single store lock+save, so a
			// failure here can never leave the URL pointing at a config the
			// profile's config.yaml doesn't actually contain (the previous
			// two-call PatchProfile-then-ApplySubscriptionFetchForURL
			// sequence could partially apply and diverge on the second
			// call's failure).
			profile, selectionChanges, err = c.store.ReplaceProfileSubscription(id, nextURL, fetched)
			if err != nil {
				writeError(w, http.StatusBadRequest, err)
				return
			}
			// Name/AutoUpdate/UpdateIntervalMinutes are independent of the
			// URL/config transaction above; apply them as a second,
			// best-effort step so a failure here only leaves those fields
			// stale (not the URL/config pair M4 is about).
			if req.Name != "" || req.AutoUpdate != nil || req.UpdateIntervalMinutes != nil {
				profile, err = c.store.PatchProfile(id, ProfilePatch{
					Name:                  req.Name,
					AutoUpdate:            req.AutoUpdate,
					UpdateIntervalMinutes: req.UpdateIntervalMinutes,
				})
				if err != nil {
					writeError(w, http.StatusBadRequest, err)
					return
				}
			}
		} else {
			profile, err = c.store.PatchProfile(id, ProfilePatch{
				Name:                  req.Name,
				Config:                req.Config,
				SubscriptionURL:       req.SubscriptionURL,
				AutoUpdate:            req.AutoUpdate,
				UpdateIntervalMinutes: req.UpdateIntervalMinutes,
			})
			if err != nil {
				writeError(w, http.StatusBadRequest, err)
				return
			}
		}
		// selectionChanges is only non-empty on the urlChanged branch above
		// (the non-subscription PatchProfile path never reassigns a
		// selection), and only present in the JSON at all when non-empty --
		// see the "refresh" case above for the same optional-field contract.
		writeJSON(w, struct {
			*Profile
			SelectionChanges []SelectionReconciliation `json:"selectionChanges,omitempty"`
		}{Profile: profile, SelectionChanges: selectionChanges})
	case http.MethodDelete:
		// arch M2: refuse to delete a profile still referenced by an
		// instance (409); unknown profile is 404. There is deliberately no
		// cascade-delete from the instance side -- see DeleteProfile's doc
		// comment in store.go for why.
		if err := c.store.DeleteProfile(id); err != nil {
			status := http.StatusBadRequest
			switch {
			case errors.Is(err, errProfileNotFound):
				status = http.StatusNotFound
			case errors.Is(err, errValidation):
				status = http.StatusConflict
			}
			writeError(w, status, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		methodNotAllowed(w)
	}
}
