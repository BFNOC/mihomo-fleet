package app

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type Controller struct {
	opts         Options
	store        *Store
	manager      *Manager
	mihomoPath   string
	mihomoFound  bool
	mihomoSource string
	appVersion   string
	// version is read by handleSystem (GET /api/system) and rewritten by
	// ApplyCoreUpdate (core_update.go) once a swap succeeds -- both can run
	// concurrently on different request goroutines, unlike every other
	// field above, which is set once in NewController and never mutated
	// again. versionMu is exactly this field's guard; use
	// currentMihomoVersion()/setMihomoVersion() rather than touching
	// version directly.
	version             string
	versionMu           sync.RWMutex
	proxyTransport      http.RoundTripper
	subscriptionClient  *http.Client
	subscriptionCancel  context.CancelFunc
	subscriptionMu      sync.Mutex
	subscriptionRunning map[string]bool
	// updateClient is the SSRF-hardened client core_update.go/geo_update.go
	// share for GitHub API calls and asset/checksum downloads (feature #3).
	// Deliberately separate from subscriptionClient: that client's 25s
	// overall Timeout is sized for one small subscription YAML fetch and
	// would truncate a legitimate multi-megabyte binary/geodata download on
	// a slow connection -- see newUpdateHTTPClient's doc comment.
	updateClient *http.Client
	// coreUpdateMu/geoUpdateMu single-flight handleCoreUpdate/
	// handleGeoUpdate's POST case: TryLock rejects a second concurrent
	// update-of-the-same-kind request outright (409) rather than letting
	// two ApplyCoreUpdate/ApplyGeoUpdate calls run at once. Without this, A
	// renaming target->.bak followed by B's atomicSwap rename-then-remove
	// of that SAME .bak (B believes it is removing ITS OWN stale .bak, per
	// atomicSwap's own doc comment) destroys A's rollback copy, and two
	// full-size downloads running concurrently doubles the resource
	// exhaustion the size cap alone does not prevent. One mutex per kind
	// (not one shared) because a core update and a geo update touch
	// disjoint files and have no reason to block each other.
	coreUpdateMu sync.Mutex
	geoUpdateMu  sync.Mutex
	// importMu single-flights POST /api/import: a second concurrent import
	// gets 409 instead of racing the first's create/rollback (feature #7).
	importMu  sync.Mutex
	apiSecret string
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
	// geo is defined by the geoLookup type in geoip_handler.go, along with
	// handleGeoIP/geoDatabase, the geo-related consts, and geoDatabaseNames.
	geo geoLookup
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
		updateClient:        newUpdateHTTPClient(),
		mihomoProxies:       make(map[int]*httputil.ReverseProxy),
	}
	c.version = detectVersion(mihomoPath)
	c.manager = NewManager(store, mihomoPath)
	c.startSubscriptionScheduler()
	return c, nil
}

// currentMihomoVersion/setMihomoVersion guard Controller.version (see its
// field comment): every read after startup must go through
// currentMihomoVersion, and the only post-startup write is
// ApplyCoreUpdate's, through setMihomoVersion.
func (c *Controller) currentMihomoVersion() string {
	c.versionMu.RLock()
	defer c.versionMu.RUnlock()
	return c.version
}

func (c *Controller) setMihomoVersion(v string) {
	c.versionMu.Lock()
	c.version = v
	c.versionMu.Unlock()
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
	mux.HandleFunc("/api/system/bind-addresses", c.handleBindAddresses)
	mux.HandleFunc("/api/profiles", c.handleProfiles)
	mux.HandleFunc("/api/profiles/", c.handleProfile)
	mux.HandleFunc("/api/ports/suggest", c.handlePortSuggest)
	mux.HandleFunc("/api/instances", c.handleInstances)
	// This literal pattern is more specific than "/api/instances/" below, so
	// net/http.ServeMux prefers it -- POST /api/instances/chain-candidates
	// never reaches handleInstance's id/action routing. See
	// TestRouteChainCandidatesPrecedesInstanceRoute.
	mux.HandleFunc("/api/instances/chain-candidates", c.handleChainCandidates)
	mux.HandleFunc("/api/instances/", c.handleInstance)
	mux.HandleFunc("/api/mihomo/", c.handleMihomoProxy)
	mux.HandleFunc("/api/geoip", c.handleGeoIP)
	mux.HandleFunc("/api/system/core-update", c.handleCoreUpdate)
	mux.HandleFunc("/api/system/geo-update", c.handleGeoUpdate)
	mux.HandleFunc("/api/system/proxy-instances", c.handleProxyInstances)
	mux.HandleFunc("/api/export", c.handleExport)
	mux.HandleFunc("/api/import", c.handleImport)
	mux.HandleFunc("/", c.handleStatic)
}

// handleCoreUpdate serves feature #3's mihomo core binary check/update
// (docs/feature-roadmap-post-1.3.md #3). GET reports current vs latest
// version and whether the release publishes a verifiable checksum; POST
// downloads, verifies, and installs it -- see core_update.go's
// ApplyCoreUpdate for the mandatory checksum-before-download-content-used
// ordering.
func (c *Controller) handleCoreUpdate(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
		defer cancel()
		writeJSON(w, c.CoreUpdateStatus(ctx))
	case http.MethodPost:
		// Single-flight: a second concurrent POST while one is already
		// running is rejected outright rather than letting two
		// ApplyCoreUpdate calls race (see coreUpdateMu's field comment).
		if !c.coreUpdateMu.TryLock() {
			writeError(w, http.StatusConflict, errors.New("a mihomo core update is already in progress"))
			return
		}
		defer c.coreUpdateMu.Unlock()
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Minute)
		defer cancel()
		result, err := c.ApplyCoreUpdate(ctx)
		if err != nil {
			writeError(w, updateErrorStatus(err), err)
			return
		}
		writeJSON(w, result)
	default:
		methodNotAllowed(w)
	}
}

// updateErrorStatus classifies an ApplyCoreUpdate/ApplyGeoUpdate error into
// an HTTP status: errMihomoNotFound is a client-fixable precondition (400),
// errCoreUpdateBusy is the same "stop the instance first" conflict every
// other write-guard in this file already reports as 409 (see e.g. the
// "stop instance before changing ports" checks in handleInstance), and
// anything else (network/checksum/extract/install failure) is treated as
// an upstream/verification problem (502).
func updateErrorStatus(err error) int {
	switch {
	case errors.Is(err, errMihomoNotFound):
		return http.StatusBadRequest
	case errors.Is(err, errCoreUpdateBusy):
		return http.StatusConflict
	default:
		return http.StatusBadGateway
	}
}

// maxImportBundleBytes bounds POST /api/import's request body (feature #7,
// docs/feature-roadmap-post-1.3.md #7), mirroring readJSON's 2MiB cap on
// every other POST body in this package but sized for what an import bundle
// actually needs to hold: possibly several profiles, each with a full
// subscription YAML up to maxSubscriptionBytes (16MiB, subscription.go)
// inlined as a string, plus every instance's metadata.
const maxImportBundleBytes = 64 << 20

// handleExport serves feature #7's fleet backup: GET /api/export returns the
// whole fleet (every profile's config.yaml content inlined, every instance
// minus its runtime secret) as one downloadable JSON document. See
// ExportBundle's doc comment (types.go) for the envelope shape and export.go's
// ExportBundle for what does/doesn't get carried over.
func (c *Controller) handleExport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	bundle, err := ExportBundle(c.store)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	filename := fmt.Sprintf("mihomo-fleet-backup-%s.json", time.Now().UTC().Format("20060102-150405"))
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filename))
	// The bundle carries every profile's config.yaml (proxy credentials) and
	// subscription URLs -- keep it out of the browser's HTTP disk cache, the
	// same way /api/ports/suggest guards its dynamic response.
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, bundle)
}

// handleImport serves feature #7's fleet restore: POST /api/import takes a
// bundle GET /api/export produced (or a hand-built one following the same
// schema) and, once ImportBundle's validate-then-mutate pass accepts it in
// full, creates every profile and instance it describes. The response is an
// ImportResult reporting exactly what was created, renamed, and/or had its
// ports re-allocated -- see ImportBundle's doc comment (export.go).
func (c *Controller) handleImport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	// Single-flight: an import is a multi-record create+rollback sequence, so
	// two concurrent imports could interleave their creates. Reject the second
	// with 409 rather than racing (mirrors coreUpdateMu/geoUpdateMu).
	if !c.importMu.TryLock() {
		writeError(w, http.StatusConflict, errors.New("an import is already in progress"))
		return
	}
	defer c.importMu.Unlock()
	w.Header().Set("Cache-Control", "no-store")
	defer r.Body.Close()
	data, err := io.ReadAll(io.LimitReader(r.Body, maxImportBundleBytes+1))
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if len(data) > maxImportBundleBytes {
		writeError(w, http.StatusRequestEntityTooLarge, fmt.Errorf("import bundle is larger than %d bytes", maxImportBundleBytes))
		return
	}
	result, err := ImportBundle(c.store, data)
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, errValidation) {
			status = http.StatusBadRequest
		}
		writeError(w, status, err)
		return
	}
	writeJSONStatus(w, http.StatusCreated, result)
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
		Version:      c.currentMihomoVersion(),
	})
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
