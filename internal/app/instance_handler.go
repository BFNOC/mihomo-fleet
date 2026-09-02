package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"
)

func (c *Controller) handleInstances(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		views := c.manager.Views()
		sort.Slice(views, func(i, j int) bool {
			return views[i].CreatedAt.Before(views[j].CreatedAt)
		})
		writeJSON(w, map[string]any{"instances": views})
	case http.MethodPost:
		if action := r.URL.Query().Get("action"); action != "" {
			c.handleInstancesBatch(w, r, action)
			return
		}
		var req struct {
			Name                  string   `json:"name"`
			ProfileID             string   `json:"profileId"`
			ProfileName           string   `json:"profileName"`
			Config                string   `json:"config"`
			SubscriptionURL       string   `json:"subscriptionUrl"`
			AutoUpdate            *bool    `json:"autoUpdate"`
			UpdateIntervalMinutes int      `json:"updateIntervalMinutes"`
			MixedPort             int      `json:"mixedPort"`
			ProxyBind             string   `json:"proxyBind"`
			ControllerPort        int      `json:"controllerPort"`
			Mode                  string   `json:"mode"`
			LocalProxies          string   `json:"localProxies"`
			ConfigOverride        string   `json:"configOverride"`
			Chain                 []string `json:"chain"`
			AutoRestart           bool     `json:"autoRestart"`
		}
		if err := readJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		if strings.TrimSpace(req.Name) == "" {
			req.Name = "New instance"
		}
		subscriptionURL := strings.TrimSpace(req.SubscriptionURL)
		if subscriptionURL != "" && strings.TrimSpace(req.Config) != "" {
			writeError(w, http.StatusBadRequest, errors.New("subscriptionUrl and config cannot both be set"))
			return
		}
		if req.ProfileID != "" && subscriptionURL != "" {
			writeError(w, http.StatusBadRequest, errors.New("subscriptionUrl requires a new profile"))
			return
		}
		var subscriptionFetched *subscriptionFetchResult
		autoUpdate := false
		if subscriptionURL != "" {
			autoUpdate = true
			if req.AutoUpdate != nil {
				autoUpdate = *req.AutoUpdate
			}
			ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
			defer cancel()
			var err error
			subscriptionFetched, err = fetchSubscription(ctx, c.subscriptionClient, subscriptionURL, c.subscriptionUserAgent())
			if err != nil {
				writeError(w, http.StatusBadRequest, err)
				return
			}
		}
		item, err := c.store.CreateWithOptions(createInstanceOptions{
			Name:                   req.Name,
			ProfileID:              req.ProfileID,
			ProfileName:            req.ProfileName,
			Config:                 req.Config,
			SubscriptionURL:        subscriptionURL,
			SubscriptionAutoUpdate: autoUpdate,
			SubscriptionInterval:   req.UpdateIntervalMinutes,
			SubscriptionFetch:      subscriptionFetched,
			MixedPort:              req.MixedPort,
			ProxyBind:              req.ProxyBind,
			ControllerPort:         req.ControllerPort,
			Mode:                   req.Mode,
			LocalProxies:           req.LocalProxies,
			ConfigOverride:         req.ConfigOverride,
			Chain:                  req.Chain,
			AutoRestart:            req.AutoRestart,
		})
		if err != nil {
			status := http.StatusInternalServerError
			switch {
			case errors.Is(err, errProfileNotFound):
				status = http.StatusNotFound
			case errors.Is(err, errPortUnavailable):
				status = http.StatusConflict
			case errors.Is(err, errValidation):
				status = http.StatusBadRequest
			}
			writeError(w, status, err)
			return
		}
		view, _ := c.manager.View(item.ID)
		writeJSONStatus(w, http.StatusCreated, view)
	default:
		methodNotAllowed(w)
	}
}

func (c *Controller) handlePortSuggest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	mixed, controller := c.store.SuggestPorts()
	if mixed == 0 || controller == 0 {
		writeError(w, http.StatusServiceUnavailable, errors.New("unable to allocate local ports"))
		return
	}
	writeJSON(w, map[string]int{
		"mixedPort":      mixed,
		"controllerPort": controller,
	})
}

func (c *Controller) handleInstancesBatch(w http.ResponseWriter, r *http.Request, action string) {
	// conc L-8 (docs/review-2026-07-11-go-concurrency-performance.md): a
	// server-side budget instead of r.Context() -- StartContext's first line
	// returns immediately once its ctx is cancelled, so binding this to the
	// request context meant a client disconnect (page refresh, browser tab
	// closed, request timeout) partway through a "start all"/"stop all"
	// click silently aborted every instance the batch hadn't reached yet,
	// with no way for the (now gone) client to see which ones were.
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	var result InstanceBatchResult
	switch action {
	case "start-all":
		result = c.manager.StartAll(ctx)
	case "stop-all":
		result = c.manager.StopAll(ctx)
	default:
		writeError(w, http.StatusBadRequest, fmt.Errorf("unknown instance action %q", action))
		return
	}

	views := c.manager.Views()
	sort.Slice(views, func(i, j int) bool {
		return views[i].CreatedAt.Before(views[j].CreatedAt)
	})
	writeJSON(w, struct {
		InstanceBatchResult
		Instances []InstanceView `json:"instances"`
	}{
		InstanceBatchResult: result,
		Instances:           views,
	})
}

func (c *Controller) handleInstance(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/api/instances/")
	parts := strings.Split(strings.Trim(rest, "/"), "/")
	if len(parts) == 0 || parts[0] == "" {
		http.NotFound(w, r)
		return
	}
	id := parts[0]
	action := ""
	if len(parts) > 1 {
		action = parts[1]
	}

	switch action {
	case "":
		c.handleInstanceRoot(w, r, id)
	case "config":
		c.handleConfig(w, r, id)
	case "start":
		c.handleAction(w, r, id, "start")
	case "stop":
		c.handleAction(w, r, id, "stop")
	case "restart":
		c.handleAction(w, r, id, "restart")
	case "reload":
		c.handleReload(w, r, id)
	case "clone":
		c.handleClone(w, r, id)
	case "logs":
		c.handleLogs(w, r, id)
	case "selection":
		c.handleSelection(w, r, id)
	case "latency":
		c.handleLatency(w, r, id)
	default:
		http.NotFound(w, r)
	}
}

func (c *Controller) handleInstanceRoot(w http.ResponseWriter, r *http.Request, id string) {
	switch r.Method {
	case http.MethodGet:
		view, ok := c.manager.View(id)
		if !ok {
			http.NotFound(w, r)
			return
		}
		writeJSON(w, view)
	case http.MethodPut:
		var req struct {
			Name              string    `json:"name"`
			ProfileID         string    `json:"profileId"`
			ExpectedProfileID string    `json:"expectedProfileId"`
			Config            string    `json:"config"`
			MixedPort         int       `json:"mixedPort"`
			ProxyBind         *string   `json:"proxyBind"`
			ControllerPort    int       `json:"controllerPort"`
			Mode              string    `json:"mode"`
			LocalProxies      *string   `json:"localProxies"`
			ConfigOverride    *string   `json:"configOverride"`
			Chain             *[]string `json:"chain"`
			AutoRestart       *bool     `json:"autoRestart"`
		}
		if err := readJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		current, ok := c.store.Get(id)
		if !ok {
			http.NotFound(w, r)
			return
		}
		if req.ExpectedProfileID != "" && current.ProfileID != req.ExpectedProfileID {
			writeError(w, http.StatusConflict, errors.New("profile changed while configuration was being edited"))
			return
		}
		if c.manager.Busy(id) {
			if (req.MixedPort > 0 && req.MixedPort != current.MixedPort) ||
				(req.ControllerPort > 0 && req.ControllerPort != current.ControllerPort) {
				writeError(w, http.StatusConflict, errors.New("stop the instance before changing ports"))
				return
			}
		}
		if c.manager.Busy(id) && req.ProxyBind != nil {
			nextProxyBind, err := normalizeProxyBind(*req.ProxyBind)
			if err != nil {
				writeError(w, http.StatusBadRequest, err)
				return
			}
			if nextProxyBind != instanceProxyBind(current.ProxyBind) {
				writeError(w, http.StatusConflict, errors.New("stop the instance before changing proxy bind"))
				return
			}
		}
		if c.manager.Busy(id) && req.ProfileID != "" && current.ProfileID != req.ProfileID {
			writeError(w, http.StatusConflict, errors.New("stop the instance before changing profile"))
			return
		}
		if req.Config != "" && req.ProfileID != "" && current.ProfileID != req.ProfileID {
			writeError(w, http.StatusBadRequest, errors.New("profileId and config cannot be changed in the same request"))
			return
		}
		if req.Config != "" {
			profile, ok := c.store.GetProfile(current.ProfileID)
			if !ok {
				writeError(w, http.StatusBadRequest, fmt.Errorf("profile %q not found", current.ProfileID))
				return
			}
			if profile.SubscriptionURL != "" {
				writeError(w, http.StatusBadRequest, errors.New("subscription profile config is refreshed from its URL"))
				return
			}
		}
		item, err := c.store.UpdateWithOptions(id, updateInstanceOptions{
			Name:              req.Name,
			ProfileID:         req.ProfileID,
			ExpectedProfileID: req.ExpectedProfileID,
			Config:            req.Config,
			MixedPort:         req.MixedPort,
			ProxyBind:         req.ProxyBind,
			ControllerPort:    req.ControllerPort,
			Mode:              req.Mode,
			LocalProxies:      req.LocalProxies,
			ConfigOverride:    req.ConfigOverride,
			Chain:             req.Chain,
			AutoRestart:       req.AutoRestart,
		})
		if err != nil {
			status := http.StatusBadRequest
			switch {
			case errors.Is(err, errProfileNotFound):
				status = http.StatusNotFound
			case errors.Is(err, errPortUnavailable):
				status = http.StatusConflict
			case errors.Is(err, errConflict):
				status = http.StatusConflict
			}
			writeError(w, status, err)
			return
		}
		view, _ := c.manager.View(item.ID)
		writeJSON(w, view)
	case http.MethodDelete:
		// BeginDelete/EndDelete close the narrow window where an external
		// concurrent POST .../start could win a race against this handler:
		// launch after Stop below but before store.Delete removes the
		// record, orphaning the process. EndDelete runs via defer so it
		// fires on every return path (including the two error returns
		// below), never wedging the instance in a permanently
		// "being deleted" state.
		c.manager.BeginDelete(id)
		defer c.manager.EndDelete(id)
		// Stop runs against a background context (not r.Context()) so a client
		// disconnect during a slow SIGTERM/SIGKILL grace period or an in-flight
		// start's cancellation window can never leave an orphaned mihomo process
		// or resurrect the instance directory (geodata.go's MkdirAll) after the
		// store record below is removed. If the instance cannot be confirmed
		// stopped, deletion is aborted rather than silently dropping the error.
		if err := c.manager.Stop(id); err != nil {
			writeError(w, http.StatusConflict, fmt.Errorf("stop instance before delete: %w", err))
			return
		}
		if err := c.store.Delete(id); err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		// arch L7 / conc L-1: release id's log buffer now that the instance
		// record (and its on-disk directory) are gone -- otherwise it stays in
		// Manager.logs forever, since nothing else ever removes that entry.
		c.manager.dropLogs(id)
		// Same reasoning for the crash watchdog's bookkeeping (#2): without
		// this, m.watchdogs[id] would leak forever too, and (defense in
		// depth) any in-flight backoff for id is cancelled immediately
		// instead of waking up later only to find isDeleting still true.
		c.manager.dropWatchdog(id)
		w.WriteHeader(http.StatusNoContent)
	default:
		methodNotAllowed(w)
	}
}

func (c *Controller) handleClone(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	var req struct {
		Name           string `json:"name"`
		MixedPort      int    `json:"mixedPort"`
		ControllerPort int    `json:"controllerPort"`
	}
	// arch L10: every field of a clone request is optional (Store.Clone
	// already falls back to "<source name> copy" and auto-allocated ports),
	// so an empty request body -- readJSON's json.Decoder.Decode returns a
	// bare io.EOF for one -- is a legitimate all-defaults clone, not a
	// malformed request. Previously this surfaced as the distinctly
	// unhelpful {"error":"EOF"} response.
	if err := readJSON(r, &req); err != nil && !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	item, err := c.store.Clone(id, req.Name, req.MixedPort, req.ControllerPort)
	if err != nil {
		status := http.StatusBadRequest
		switch {
		case errors.Is(err, errInstanceNotFound), errors.Is(err, errProfileNotFound):
			status = http.StatusNotFound
		case errors.Is(err, errPortUnavailable):
			status = http.StatusConflict
		}
		writeError(w, status, err)
		return
	}
	view, ok := c.manager.View(item.ID)
	if !ok {
		writeError(w, http.StatusInternalServerError, fmt.Errorf("instance %q not available after clone", item.ID))
		return
	}
	writeJSONStatus(w, http.StatusCreated, view)
}

func (c *Controller) handleSelection(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	var req struct {
		Group string `json:"group"`
		Proxy string `json:"proxy"`
		Apply bool   `json:"apply"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if req.Group == "" || req.Proxy == "" {
		writeError(w, http.StatusBadRequest, errors.New("group and proxy are required"))
		return
	}
	item, ok := c.store.Get(id)
	if !ok {
		http.NotFound(w, r)
		return
	}
	// N1 (docs/review-2026-07-11-fix-verification-round4.md), superseding
	// arch L4's original static-validation-for-everyone shape: a running
	// instance's node panel is populated from mihomo's *live* API
	// (GET /api/mihomo/{id}/proxies), which includes proxy-provider nodes
	// that fetch at runtime and never appear in the static YAML parse --
	// parseProfileProxyGroupsBase (subscription.go) skips any group with no
	// static `proxies:` list entirely, so a provider-only group vanishes from
	// Store.ProfileProxyGroupsForInstance. Validating a running instance's
	// selection against that static parse rejected every provider node with
	// 400 before the request ever reached mihomo, breaking selection for any
	// subscription that is proxy-providers-only. mihomo already validates
	// group/node itself (PUT /proxies/{group} 404s on an unknown group or
	// node), so for a running instance we trust that instead: apply first,
	// and only persist via SetSelection once the apply actually succeeds --
	// this also completes the "apply before persist" ordering L4 left
	// unfinished (a failed persist no longer diverges from a live mihomo
	// state that already moved).
	if ps := c.manager.state(id); ps != nil {
		if req.Apply {
			if err := putMihomoProxy(r.Context(), item, req.Group, req.Proxy); err != nil {
				// mihomo's own rejection (unknown group/node, or any other
				// PUT /proxies/{group} failure) is surfaced as a 400 the same
				// way the stopped-instance static-validation path below
				// reports an invalid selection -- from the client's
				// perspective both are "this group/node doesn't exist".
				writeError(w, http.StatusBadRequest, err)
				return
			}
		}
	} else if c.manager.isStarting(id) {
		// The instance is mid-launch: there is no controller port to push to
		// yet, and pushing the selection now (instead of rejecting) would let
		// it silently persist without ever reaching the process that is about
		// to start with an older snapshot. Ask the caller to retry once the
		// instance has finished starting.
		if req.Apply {
			writeError(w, http.StatusConflict, errors.New("instance is starting; retry once it finishes starting"))
			return
		}
	} else {
		// Stopped instance: keep arch L4's original best-effort static
		// validation (its actual pain point -- a stopped instance storing a
		// typo'd selection and only discovering it when restoreSelection,
		// manager.go, spins for 5s on the next start). But only reject when
		// the *group* itself is known from the static parse and the proxy is
		// not among its members -- if the group is absent from the static
		// parse (a provider-backed group, or an unparseable/empty profile),
		// there is nothing to validate against, so accept: restoreSelection
		// already tolerates unknown selections at start.
		groups, err := c.store.ProfileProxyGroupsForInstance(item.ProfileID, item)
		if err != nil {
			writeError(w, profileProxyGroupsStatus(err), err)
			return
		}
		if proxyGroupKnown(groups, req.Group) && !proxyGroupHasNode(groups, req.Group, req.Proxy) {
			writeError(w, http.StatusBadRequest, errors.New("unknown proxy group or node"))
			return
		}
	}
	if _, err := c.store.SetSelection(id, req.Group, req.Proxy); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	view, _ := c.manager.View(id)
	writeJSON(w, view)
}

func (c *Controller) handleLatency(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	var req struct {
		Group     string `json:"group"`
		Proxy     string `json:"proxy"`
		Kind      string `json:"kind"`
		URL       string `json:"url"`
		TimeoutMS int    `json:"timeoutMs"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if req.Kind == "" {
		req.Kind = "url"
	}
	if req.Kind != "url" && req.Kind != "real" {
		writeError(w, http.StatusBadRequest, errors.New("latency kind must be url or real"))
		return
	}
	testURL, err := normalizeLatencyRequestURL(strings.TrimSpace(req.URL))
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	timeoutMS := clampLatencyTimeoutMS(req.TimeoutMS)
	item, ok := c.store.Get(id)
	if !ok {
		http.NotFound(w, r)
		return
	}
	if c.manager.state(id) == nil {
		writeError(w, http.StatusConflict, errors.New("instance must be running to test latency"))
		return
	}
	if req.Group != "" && req.Proxy == "" && req.Kind == "real" {
		writeError(w, http.StatusBadRequest, errors.New("proxy is required for real latency"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), time.Duration(latencyRequestBudgetMS(req.Kind, timeoutMS))*time.Millisecond)
	defer cancel()
	if req.Group != "" && req.Proxy == "" && req.Kind == "url" {
		delays, err := mihomoGroupDelay(ctx, item, strings.TrimSpace(req.Group), testURL, timeoutMS)
		if err != nil {
			writeError(w, http.StatusBadGateway, err)
			return
		}
		writeJSON(w, map[string]any{"delays": delays, "url": testURL, "timeoutMs": timeoutMS})
		return
	}
	if strings.TrimSpace(req.Proxy) == "" {
		writeError(w, http.StatusBadRequest, errors.New("proxy is required"))
		return
	}
	var delay int
	if req.Kind == "real" {
		delay, err = mihomoRealProxyDelay(ctx, item, strings.TrimSpace(req.Proxy), testURL, timeoutMS)
	} else {
		delay, err = mihomoProxyDelay(ctx, item, strings.TrimSpace(req.Proxy), testURL, timeoutMS)
	}
	if err != nil {
		writeError(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, map[string]any{"delay": delay, "url": testURL, "timeoutMs": timeoutMS})
}

func (c *Controller) handleConfig(w http.ResponseWriter, r *http.Request, id string) {
	switch r.Method {
	case http.MethodGet:
		// security L-3 (docs/review-2026-07-11-security.md): unlike the
		// GET .../profiles/{id}/config site (see
		// sanitizedProfileConfigReadError), Store.ReadUserConfig's
		// "not found" case is an untyped fmt.Errorf built from `id`
		// (store.go), not one of the typed sentinel errors this package
		// classifies with errors.Is elsewhere. Distinguishing it from a raw
		// *os.PathError here would mean either reconstructing and
		// string-comparing its exact text -- the very substring-matching
		// anti-pattern errValidation/errProfileNotFound/errPortUnavailable
		// were introduced to eliminate (see their doc comment in store.go)
		// -- or changing ReadUserConfig's error type, which is out of this
		// pass's scope. Left as-is rather than risk silently changing the
		// "instance not found" message text app.js's errorPatterns match.
		cfg, err := c.store.ReadUserConfig(id)
		if err != nil {
			writeError(w, http.StatusNotFound, err)
			return
		}
		writeJSON(w, map[string]string{"config": cfg})
	default:
		methodNotAllowed(w)
	}
}

func (c *Controller) handleAction(w http.ResponseWriter, r *http.Request, id, action string) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	var err error
	switch action {
	case "start":
		err = c.manager.Start(id)
	case "stop":
		err = c.manager.Stop(id)
	case "restart":
		err = c.manager.Restart(id)
	}
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	view, ok := c.manager.View(id)
	if !ok {
		http.NotFound(w, r)
		return
	}
	writeJSON(w, view)
}

// handleReload hot-reloads a running instance's config without restarting its
// process (feature #4, docs/feature-roadmap-post-1.3.md): it regenerates
// item.RuntimeConfigPath from the instance's current stored fields and
// profile (Manager.ReloadContext reuses config.go's writeRuntimeConfig --
// the exact generator StartContext itself calls, so this never diverges from
// what a fresh start would produce) and pushes it into the already-running
// mihomo process via PUT /configs (reloadMihomoConfig, mihomo_api.go).
//
// Only a running instance can be reloaded -- mirrors handleLatency's
// same-shaped guard below, and reuses handleMihomoProxy's exact wording so
// app.js's/constants.ts's errorPatterns entry for `instance "(.+)" is not
// running` covers this response too instead of needing a second one for the
// same condition.
func (c *Controller) handleReload(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	if _, ok := c.store.Get(id); !ok {
		http.NotFound(w, r)
		return
	}
	if c.manager.state(id) == nil {
		writeError(w, http.StatusConflict, fmt.Errorf("instance %q is not running", id))
		return
	}
	if err := c.manager.ReloadContext(r.Context(), id); err != nil {
		// Default (502) is reserved for reloadMihomoConfig's own failures --
		// an actual downstream mihomo rejection or network error -- since
		// that is the only case ReloadContext returns an error un-classified
		// below.
		status := http.StatusBadGateway
		var genErr reloadGenerationError
		switch {
		case errors.Is(err, errReloadNetworkChanged):
			// Port/controller-port/proxy-bind changed since the process
			// launched: reload refuses to touch listeners live (see
			// errReloadNetworkChanged's doc comment in manager.go). This is a
			// conflict between the pending edit and what's safe to hot-apply,
			// not a downstream mihomo failure -- 409, like the other
			// "instance state disagrees with the request" cases above
			// (handleLatency's stopped-instance check, handleSelection's
			// starting-instance retry-later case).
			status = http.StatusConflict
		case errors.Is(err, errProfileNotFound):
			status = http.StatusNotFound
		case errors.As(err, &genErr):
			// writeRuntimeConfig/prepareGeodata failed before ReloadContext
			// ever contacted mihomo -- a broken profile/local-proxy/
			// global-chain config, not a downstream failure, so this must
			// not fall into the 502 default (see reloadGenerationError's doc
			// comment, manager.go).
			status = http.StatusUnprocessableEntity
		}
		writeError(w, status, err)
		return
	}
	view, ok := c.manager.View(id)
	if !ok {
		http.NotFound(w, r)
		return
	}
	writeJSON(w, view)
}

func (c *Controller) handleLogs(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	if _, ok := c.store.Get(id); !ok {
		http.NotFound(w, r)
		return
	}
	writeJSON(w, map[string]any{"lines": c.manager.Logs(id)})
}
