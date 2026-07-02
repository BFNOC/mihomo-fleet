package app

import (
	"bytes"
	"context"
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
				writeError(w, http.StatusNotFound, err)
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
			cfg, err := c.store.ReadProfileConfig(id)
			if err != nil {
				writeError(w, http.StatusNotFound, err)
				return
			}
			var selections map[string]string
			if instanceID := r.URL.Query().Get("instanceId"); instanceID != "" {
				item, ok := c.store.Get(instanceID)
				if !ok {
					writeError(w, http.StatusNotFound, fmt.Errorf("instance %q not found", instanceID))
					return
				}
				groups, err := profileProxyGroupsForInstance(cfg, item)
				if err != nil {
					writeError(w, http.StatusBadRequest, err)
					return
				}
				writeJSON(w, map[string]any{"groups": groups})
				return
			}
			groups, err := parseProfileProxyGroups(cfg, selections)
			if err != nil {
				writeError(w, http.StatusBadRequest, err)
				return
			}
			writeJSON(w, map[string]any{"groups": groups})
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
		var fetched *subscriptionFetchResult
		if req.SubscriptionURL != nil && strings.TrimSpace(*req.SubscriptionURL) != "" {
			nextURL := strings.TrimSpace(*req.SubscriptionURL)
			parsed, err := url.Parse(nextURL)
			if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
				writeError(w, http.StatusBadRequest, errors.New("subscription URL must start with http:// or https://"))
				return
			}
			if current.SubscriptionURL != nextURL {
				ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
				defer cancel()
				var err error
				fetched, err = fetchSubscription(ctx, c.subscriptionClient, nextURL, c.subscriptionUserAgent())
				if err != nil {
					writeError(w, http.StatusBadRequest, err)
					return
				}
			}
		}
		profile, err := c.store.PatchProfile(id, ProfilePatch{
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
		if fetched != nil {
			profile, err = c.store.ApplySubscriptionFetchForURL(id, strings.TrimSpace(*req.SubscriptionURL), fetched)
			if err != nil {
				writeError(w, http.StatusBadRequest, err)
				return
			}
		}
		writeJSON(w, profile)
	default:
		methodNotAllowed(w)
	}
}

func (c *Controller) SecureHandler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !c.allowedHost(r.Host) {
			writeError(w, http.StatusForbidden, errors.New("invalid host header"))
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead && r.Method != http.MethodOptions {
			if r.Header.Get("X-Mihomo-Fleet") != "1" {
				writeError(w, http.StatusForbidden, errors.New("missing X-Mihomo-Fleet header"))
				return
			}
			contentType := r.Header.Get("Content-Type")
			if r.ContentLength != 0 && !strings.HasPrefix(contentType, "application/json") {
				writeError(w, http.StatusUnsupportedMediaType, errors.New("Content-Type must be application/json"))
				return
			}
		}
		next.ServeHTTP(w, r)
	})
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
			if isPortUnavailableError(err) {
				status = http.StatusConflict
			} else if isInstanceValidationError(err) {
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
	var result InstanceBatchResult
	switch action {
	case "start-all":
		result = c.manager.StartAll(r.Context())
	case "stop-all":
		result = c.manager.StopAll(r.Context())
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
		if c.manager.state(id) != nil && (req.MixedPort > 0 || req.ControllerPort > 0) {
			writeError(w, http.StatusConflict, errors.New("stop the instance before changing ports"))
			return
		}
		if c.manager.state(id) != nil && req.ProxyBind != nil {
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
		if c.manager.state(id) != nil && req.ProfileID != "" && current.ProfileID != req.ProfileID {
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
			if isPortUnavailableError(err) {
				status = http.StatusConflict
			}
			writeError(w, status, err)
			return
		}
		view, _ := c.manager.View(item.ID)
		writeJSON(w, view)
	case http.MethodDelete:
		_ = c.manager.Stop(id)
		if err := c.store.Delete(id); err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
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
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	item, err := c.store.Clone(id, req.Name, req.MixedPort, req.ControllerPort)
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, errInstanceNotFound) {
			status = http.StatusNotFound
		} else if isPortUnavailableError(err) {
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
	if req.Apply && c.manager.state(id) != nil {
		if err := putMihomoProxy(item, req.Group, req.Proxy); err != nil {
			writeError(w, http.StatusBadGateway, err)
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
	targetPath := "/"
	if len(parts) == 2 {
		targetPath = "/" + parts[1]
	}
	target, _ := url.Parse("http://127.0.0.1:" + strconv.Itoa(item.ControllerPort))
	proxy := httputil.NewSingleHostReverseProxy(target)
	proxy.Transport = c.proxyTransport
	originalDirector := proxy.Director
	proxy.Director = func(req *http.Request) {
		originalDirector(req)
		req.URL.Path = targetPath
		req.URL.RawQuery = r.URL.RawQuery
		req.Host = target.Host
		req.Header.Set("Authorization", "Bearer "+item.Secret)
	}
	proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		writeError(w, http.StatusBadGateway, fmt.Errorf("mihomo controller unreachable: %w", err))
	}
	proxy.ServeHTTP(w, r)
}

func (c *Controller) handleStatic(w http.ResponseWriter, r *http.Request) {
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

func (c *Controller) refreshDueSubscriptions(ctx context.Context) {
	now := time.Now().UTC()
	for _, profile := range c.store.ListProfiles() {
		if !profileSubscriptionDue(profile, now) {
			continue
		}
		if c.subscriptionUpdateRunning(profile.ID) {
			continue
		}
		profileID := profile.ID
		go func() {
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
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, "-v")
	out, err := cmd.CombinedOutput()
	if err != nil {
		cmd = exec.CommandContext(ctx, binary, "version")
		out, err = cmd.CombinedOutput()
		if err != nil {
			return ""
		}
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

func isPortUnavailableError(err error) bool {
	if err == nil {
		return false
	}
	message := err.Error()
	return (strings.Contains(message, "proxy port") && strings.Contains(message, "is unavailable")) ||
		(strings.Contains(message, "controller port") && strings.Contains(message, "is unavailable")) ||
		message == "unable to allocate local ports"
}

func isInstanceValidationError(err error) bool {
	if err == nil {
		return false
	}
	message := err.Error()
	return strings.Contains(message, "proxy bind address") ||
		strings.Contains(message, "instance mode") ||
		strings.Contains(message, "parse local proxies") ||
		strings.Contains(message, "mixed and controller ports must differ")
}
