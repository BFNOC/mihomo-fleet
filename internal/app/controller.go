package app

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

type Controller struct {
	opts                Options
	store               *Store
	manager             *Manager
	mihomoPath          string
	mihomoFound         bool
	mihomoSource        string
	appVersion          string
	version             string
	proxyTransport      http.RoundTripper
	subscriptionClient  *http.Client
	subscriptionCancel  context.CancelFunc
	subscriptionMu      sync.Mutex
	subscriptionRunning map[string]bool
	apiSecret           string
	// mihomoProxies caches one *httputil.ReverseProxy per controller port
	// (arch L9, docs/review-2026-07-11-go-architecture.md): handleMihomoProxy
	// previously built a brand new ReverseProxy (and Director closure) on
	// every single request. Keyed by port rather than instance id so a port
	// change naturally "invalidates" the old entry by simply never being
	// looked up again, without needing explicit invalidation bookkeeping;
	// the stale entry for an abandoned port is harmless, just an unused map
	// entry.
	mihomoProxiesMu sync.Mutex
	mihomoProxies   map[int]*httputil.ReverseProxy
}

// SetAPISecret configures the bearer token that SecureHandler requires on
// every /api/ request's Authorization header. Pass an empty string (the
// zero value, and the default) to disable the check entirely -- this
// preserves the historical no-auth-on-loopback behavior for callers that
// never set a secret. Follows the same "construct then configure" pattern
// main.go already uses for wiring CLI flags into the controller.
func (c *Controller) SetAPISecret(secret string) {
	c.apiSecret = secret
}

func NewController(opts Options) (*Controller, error) {
	if opts.Bind == "" {
		opts.Bind = "127.0.0.1"
	}
	if opts.Port == 0 {
		opts.Port = 47890
	}
	if opts.DataDir == "" {
		opts.DataDir = ".mihomo-fleet"
	}
	if opts.AppVersion == "" {
		opts.AppVersion = "dev"
	}

	mihomoPath, mihomoSource := resolveMihomoPath(opts.MihomoPath, os.Executable, filepath.EvalSymlinks, exec.LookPath)
	if opts.MihomoPath != "" && mihomoPath == "" {
		log.Printf("mihomo binary %q from -mihomo was not found or is not executable", opts.MihomoPath)
	}

	store, err := NewStore(opts.DataDir)
	if err != nil {
		return nil, err
	}
	dialer := &net.Dialer{
		Timeout:   2 * time.Second,
		KeepAlive: 30 * time.Second,
	}
	transport := &http.Transport{
		DialContext:           dialer.DialContext,
		ResponseHeaderTimeout: 5 * time.Second,
		IdleConnTimeout:       30 * time.Second,
	}
	c := &Controller{
		opts:                opts,
		store:               store,
		mihomoPath:          mihomoPath,
		mihomoFound:         mihomoPath != "",
		mihomoSource:        mihomoSource,
		appVersion:          opts.AppVersion,
		proxyTransport:      transport,
		subscriptionClient:  newSubscriptionHTTPClient(),
		subscriptionRunning: make(map[string]bool),
		mihomoProxies:       make(map[int]*httputil.ReverseProxy),
	}
	c.version = detectVersion(mihomoPath)
	c.manager = NewManager(store, mihomoPath)
	c.startSubscriptionScheduler()
	return c, nil
}

func (c *Controller) Shutdown(ctx context.Context) {
	if c.manager != nil {
		c.manager.Shutdown(ctx)
	}
	if c.subscriptionCancel != nil {
		c.subscriptionCancel()
	}
}

func (c *Controller) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/system", c.handleSystem)
	mux.HandleFunc("/api/profiles", c.handleProfiles)
	mux.HandleFunc("/api/profiles/", c.handleProfile)
	mux.HandleFunc("/api/ports/suggest", c.handlePortSuggest)
	mux.HandleFunc("/api/instances", c.handleInstances)
	mux.HandleFunc("/api/instances/", c.handleInstance)
	mux.HandleFunc("/api/mihomo/", c.handleMihomoProxy)
	mux.HandleFunc("/", c.handleStatic)
}

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
			profile, err := c.refreshProfileSubscription(r.Context(), id)
			if err != nil {
				writeError(w, http.StatusBadRequest, err)
				return
			}
			writeJSON(w, profile)
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
			profile, err = c.store.ReplaceProfileSubscription(id, nextURL, fetched)
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
		writeJSON(w, profile)
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

func (c *Controller) SecureHandler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The Host allowlist exists to stop DNS-rebinding attacks against the
		// unauthenticated loopback-only setup (an attacker-controlled page
		// pointing some hostname at 127.0.0.1). Once an API secret is
		// configured, every /api/ request is already token-gated below and a
		// rebound origin cannot read the real origin's localStorage token, so
		// the Host check is no longer load-bearing -- and enforcing it would
		// otherwise permanently block the documented LAN scenario, since a
		// remote browser sends the LAN Host header (e.g. "192.168.1.5:47890"),
		// never "localhost". Skip the check entirely in that case, for both
		// static and /api/ paths (the remote browser must be able to load the
		// UI shell too). When no secret is configured this is unreachable and
		// behavior is unchanged.
		if c.apiSecret == "" && !c.allowedHost(r.Host) {
			writeError(w, http.StatusForbidden, errors.New("invalid host header"))
			return
		}
		// Additive on top of the Host/CSRF checks below: when an API secret
		// is configured (mandatory for non-loopback binds, see main.go),
		// every /api/ request must also present it. Static assets are left
		// unauthenticated so the UI shell can load before the user has
		// entered a token; app.js prompts for one on the first 401.
		if c.apiSecret != "" && strings.HasPrefix(r.URL.Path, "/api/") && !c.authorizedRequest(r) {
			writeError(w, http.StatusUnauthorized, errors.New("missing or invalid API token"))
			return
		}
		// The custom X-Mihomo-Fleet header defeats cross-site requests: a
		// remote page cannot set a custom header on a cross-origin request
		// without triggering a CORS preflight, which this server never
		// answers with an Access-Control-Allow-* response. It was previously
		// only required for mutating methods; security L-4
		// (docs/review-2026-07-11-security.md) extends it to GET requests
		// under /api/ too, since several GET endpoints have server-side side
		// effects (e.g. .../delay makes mihomo issue an outbound network
		// probe) that a cross-site <img>/fetch could otherwise trigger blind
		// (the response itself is unreadable cross-origin, but the side
		// effect still happens). app.js's api() helper already sends this
		// header on every request, GET included, so this is not a behavior
		// change for the shipped UI. HEAD/OPTIONS stay exempt (no side
		// effects, and OPTIONS is the CORS preflight method itself); GETs
		// outside /api/ (the static UI shell) stay exempt so a plain browser
		// navigation can still load the page.
		needsCustomHeader := r.Method != http.MethodHead && r.Method != http.MethodOptions &&
			(r.Method != http.MethodGet || strings.HasPrefix(r.URL.Path, "/api/"))
		if needsCustomHeader && r.Header.Get("X-Mihomo-Fleet") != "1" {
			writeError(w, http.StatusForbidden, errors.New("missing X-Mihomo-Fleet header"))
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead && r.Method != http.MethodOptions {
			contentType := r.Header.Get("Content-Type")
			if r.ContentLength != 0 && !strings.HasPrefix(contentType, "application/json") {
				writeError(w, http.StatusUnsupportedMediaType, errors.New("Content-Type must be application/json"))
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// authorizedRequest reports whether r carries the configured API secret as
// an "Authorization: Bearer <secret>" header. Uses a constant-time compare
// so response timing cannot be used to brute-force the token byte by byte.
func (c *Controller) authorizedRequest(r *http.Request) bool {
	const prefix = "Bearer "
	auth := r.Header.Get("Authorization")
	if !strings.HasPrefix(auth, prefix) {
		return false
	}
	token := strings.TrimPrefix(auth, prefix)
	return subtle.ConstantTimeCompare([]byte(token), []byte(c.apiSecret)) == 1
}

func (c *Controller) allowedHost(host string) bool {
	if host == "" {
		return false
	}
	name := host
	if parsed, _, err := net.SplitHostPort(host); err == nil {
		name = parsed
	} else if strings.HasPrefix(host, "[") && strings.HasSuffix(host, "]") {
		name = strings.Trim(host, "[]")
	} else if parsed, _, ok := strings.Cut(host, ":"); ok {
		name = parsed
	}
	return name == "127.0.0.1" || name == "localhost" || name == "::1"
}

func (c *Controller) handleSystem(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	writeJSON(w, SystemStatus{
		Bind:         c.opts.Bind,
		Port:         c.opts.Port,
		DataDir:      c.opts.DataDir,
		AppVersion:   c.appVersion,
		MihomoPath:   c.mihomoPath,
		MihomoFound:  c.mihomoFound,
		MihomoSource: c.mihomoSource,
		Version:      c.version,
	})
}

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
			Name           string   `json:"name"`
			ProfileID      string   `json:"profileId"`
			Config         string   `json:"config"`
			MixedPort      int      `json:"mixedPort"`
			ProxyBind      string   `json:"proxyBind"`
			ControllerPort int      `json:"controllerPort"`
			Mode           string   `json:"mode"`
			LocalProxies   string   `json:"localProxies"`
			Chain          []string `json:"chain"`
		}
		if err := readJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		if strings.TrimSpace(req.Name) == "" {
			req.Name = "New instance"
		}
		item, err := c.store.CreateWithOptions(createInstanceOptions{
			Name:           req.Name,
			ProfileID:      req.ProfileID,
			Config:         req.Config,
			MixedPort:      req.MixedPort,
			ProxyBind:      req.ProxyBind,
			ControllerPort: req.ControllerPort,
			Mode:           req.Mode,
			LocalProxies:   req.LocalProxies,
			Chain:          req.Chain,
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
			Name           string    `json:"name"`
			ProfileID      string    `json:"profileId"`
			Config         string    `json:"config"`
			MixedPort      int       `json:"mixedPort"`
			ProxyBind      *string   `json:"proxyBind"`
			ControllerPort int       `json:"controllerPort"`
			Mode           string    `json:"mode"`
			LocalProxies   *string   `json:"localProxies"`
			Chain          *[]string `json:"chain"`
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
		if c.manager.Busy(id) && (req.MixedPort > 0 || req.ControllerPort > 0) {
			writeError(w, http.StatusConflict, errors.New("stop the instance before changing ports"))
			return
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
			Name:           req.Name,
			ProfileID:      req.ProfileID,
			Config:         req.Config,
			MixedPort:      req.MixedPort,
			ProxyBind:      req.ProxyBind,
			ControllerPort: req.ControllerPort,
			Mode:           req.Mode,
			LocalProxies:   req.LocalProxies,
			Chain:          req.Chain,
		})
		if err != nil {
			status := http.StatusBadRequest
			switch {
			case errors.Is(err, errProfileNotFound):
				status = http.StatusNotFound
			case errors.Is(err, errPortUnavailable):
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

// mihomoProxyRequestInfo carries the per-request values (arch L9) that a
// cached, port-keyed *httputil.ReverseProxy cannot close over directly: the
// same *ReverseProxy instance is reused across many different instances/
// requests over its lifetime, so its Director reads these from the request's
// context (stashed by handleMihomoProxy below) instead.
type mihomoProxyRequestInfo struct {
	targetPath string
	rawQuery   string
	secret     string
}

type mihomoProxyContextKey struct{}

// mihomoProxyFor returns a *httputil.ReverseProxy targeting
// 127.0.0.1:<port>, creating and caching one on first use (arch L9): the
// previous implementation constructed a brand new ReverseProxy (and Director
// closure) on every single request.
func (c *Controller) mihomoProxyFor(port int) *httputil.ReverseProxy {
	c.mihomoProxiesMu.Lock()
	defer c.mihomoProxiesMu.Unlock()
	if proxy, ok := c.mihomoProxies[port]; ok {
		return proxy
	}
	target, _ := url.Parse("http://127.0.0.1:" + strconv.Itoa(port))
	proxy := httputil.NewSingleHostReverseProxy(target)
	proxy.Transport = c.proxyTransport
	baseDirector := proxy.Director
	proxy.Director = func(req *http.Request) {
		baseDirector(req)
		info, _ := req.Context().Value(mihomoProxyContextKey{}).(mihomoProxyRequestInfo)
		req.URL.Path = info.targetPath
		req.URL.RawQuery = info.rawQuery
		req.Host = target.Host
		req.Header.Set("Authorization", "Bearer "+info.secret)
	}
	proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		writeError(w, http.StatusBadGateway, fmt.Errorf("mihomo controller unreachable: %w", err))
	}
	c.mihomoProxies[port] = proxy
	return proxy
}

func (c *Controller) handleMihomoProxy(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/api/mihomo/")
	parts := strings.SplitN(strings.Trim(rest, "/"), "/", 2)
	if len(parts) == 0 || parts[0] == "" {
		http.NotFound(w, r)
		return
	}
	id := parts[0]
	item, ok := c.store.Get(id)
	if !ok {
		http.NotFound(w, r)
		return
	}
	// arch L9: forwarding unconditionally here meant a stopped instance
	// whose ControllerPort had since been reused by an unrelated local
	// process would receive that process, not mihomo, complete with the
	// instance's controller secret in the Authorization header. Busy
	// (running or still starting) is the same guard the PUT handlers above
	// use to decide whether an instance's runtime state can be trusted.
	if !c.manager.Busy(id) {
		writeError(w, http.StatusConflict, fmt.Errorf("instance %q is not running", id))
		return
	}
	targetPath := "/"
	if len(parts) == 2 {
		targetPath = "/" + parts[1]
	}
	proxy := c.mihomoProxyFor(item.ControllerPort)
	info := mihomoProxyRequestInfo{targetPath: targetPath, rawQuery: r.URL.RawQuery, secret: item.Secret}
	r = r.WithContext(context.WithValue(r.Context(), mihomoProxyContextKey{}, info))
	proxy.ServeHTTP(w, r)
}

func (c *Controller) handleStatic(w http.ResponseWriter, r *http.Request) {
	// arch L10: "/" is registered as the catch-all pattern (RegisterRoutes),
	// so any /api/ path that doesn't match one of the specific handlers
	// above -- e.g. a typo'd endpoint -- previously fell through to here and
	// got served index.html with a 200, making a misspelled/unregistered API
	// call silently "succeed" while actually returning the UI shell's HTML.
	// Anything under /api/ reaching this handler is by definition unmatched
	// by every registered API route, so it 404s instead of falling through
	// to the static file server.
	if strings.HasPrefix(r.URL.Path, "/api/") {
		http.NotFound(w, r)
		return
	}
	serveStatic(w, r)
}

func (c *Controller) subscriptionUserAgent() string {
	return "clash-verge/v" + c.appVersion
}

func (c *Controller) refreshProfileSubscription(ctx context.Context, id string) (*Profile, error) {
	profile, ok := c.store.GetProfile(id)
	if !ok {
		return nil, fmt.Errorf("profile %q not found", id)
	}
	if profile.SubscriptionURL == "" {
		return nil, fmt.Errorf("profile %q is not a subscription profile", id)
	}
	if !c.beginSubscriptionUpdate(id) {
		return nil, fmt.Errorf("profile %q subscription update is already running", id)
	}
	defer c.endSubscriptionUpdate(id)

	ctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	fetched, err := fetchSubscription(ctx, c.subscriptionClient, profile.SubscriptionURL, c.subscriptionUserAgent())
	if err != nil {
		c.store.SetProfileUpdateError(id, err.Error())
		return nil, err
	}
	return c.store.ApplySubscriptionFetchForURL(id, profile.SubscriptionURL, fetched)
}

func (c *Controller) beginSubscriptionUpdate(id string) bool {
	c.subscriptionMu.Lock()
	defer c.subscriptionMu.Unlock()
	if c.subscriptionRunning[id] {
		return false
	}
	c.subscriptionRunning[id] = true
	return true
}

func (c *Controller) endSubscriptionUpdate(id string) {
	c.subscriptionMu.Lock()
	defer c.subscriptionMu.Unlock()
	delete(c.subscriptionRunning, id)
}

func (c *Controller) startSubscriptionScheduler() {
	ctx, cancel := context.WithCancel(context.Background())
	c.subscriptionCancel = cancel
	go func() {
		timer := time.NewTimer(3 * time.Second)
		defer timer.Stop()
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-timer.C:
				c.refreshDueSubscriptions(ctx)
			case <-ticker.C:
				c.refreshDueSubscriptions(ctx)
			}
		}
	}()
}

// maxConcurrentSubscriptionRefreshes caps how many refreshDueSubscriptions
// goroutines may be fetching a subscription at once (testing H5 /
// concurrency L-4 area): previously one goroutine was spawned per due
// profile with no limit, so a fleet with many subscription profiles that
// all came due in the same scheduler tick (e.g. after being offline) would
// fire that many concurrent outbound HTTP fetches.
const maxConcurrentSubscriptionRefreshes = 4

func (c *Controller) refreshDueSubscriptions(ctx context.Context) {
	now := time.Now().UTC()
	sem := make(chan struct{}, maxConcurrentSubscriptionRefreshes)
	for _, profile := range c.store.ListProfiles() {
		if !profileSubscriptionDue(profile, now) {
			continue
		}
		if c.subscriptionUpdateRunning(profile.ID) {
			continue
		}
		profileID := profile.ID
		sem <- struct{}{}
		go func() {
			defer func() { <-sem }()
			if _, err := c.refreshProfileSubscription(ctx, profileID); err != nil {
				log.Printf("subscription update failed for profile %s: %v", profileID, err)
			}
		}()
	}
}

func (c *Controller) subscriptionUpdateRunning(id string) bool {
	c.subscriptionMu.Lock()
	defer c.subscriptionMu.Unlock()
	return c.subscriptionRunning[id]
}

func detectVersion(binary string) string {
	if binary == "" {
		return ""
	}
	// H4 (REVIEW-2026-07-04.md): each attempt gets its own independent 2s
	// budget. Previously both attempts shared a single 2s context, so a slow
	// "-v" left "version" with little or no time to run, and c.version would
	// silently end up empty under load.
	if out := runVersionProbe(binary, "-v"); out != "" {
		return out
	}
	return runVersionProbe(binary, "version")
}

func runVersionProbe(binary, arg string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, binary, arg).CombinedOutput()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(bytes.TrimSpace(out)))
}

func resolveMihomoPath(flagPath string, executablePath func() (string, error), evalSymlinks func(string) (string, error), lookPath func(string) (string, error)) (string, string) {
	if flagPath != "" {
		if found, err := lookPath(flagPath); err == nil {
			return found, "flag"
		}
		return "", "missing"
	}
	if found := resolveMihomoSameDir(executablePath, evalSymlinks, lookPath, mihomoBinaryNames()); found != "" {
		return found, "same-dir"
	}
	if found, err := lookPath("mihomo"); err == nil {
		return found, "PATH"
	}
	return "", "missing"
}

func resolveMihomoSameDir(executablePath func() (string, error), evalSymlinks func(string) (string, error), lookPath func(string) (string, error), names []string) string {
	exePath, err := executablePath()
	if err != nil {
		return ""
	}
	if evaluated, err := evalSymlinks(exePath); err == nil {
		exePath = evaluated
	}
	exeDir := filepath.Dir(exePath)
	for _, name := range names {
		if found, err := lookPath(filepath.Join(exeDir, name)); err == nil {
			return found
		}
	}
	return ""
}

func mihomoBinaryNames() []string {
	if runtime.GOOS == "windows" {
		return []string{"mihomo.exe", "mihomo"}
	}
	return []string{"mihomo"}
}

func readJSON(r *http.Request, out any) error {
	defer r.Body.Close()
	dec := json.NewDecoder(io.LimitReader(r.Body, 2<<20))
	dec.DisallowUnknownFields()
	return dec.Decode(out)
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
