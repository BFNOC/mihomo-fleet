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
	"time"
)

type Controller struct {
	opts           Options
	store          *Store
	manager        *Manager
	mihomoPath     string
	mihomoFound    bool
	mihomoSource   string
	appVersion     string
	version        string
	proxyTransport http.RoundTripper
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
	c := &Controller{
		opts:         opts,
		store:        store,
		mihomoPath:   mihomoPath,
		mihomoFound:  mihomoPath != "",
		mihomoSource: mihomoSource,
		appVersion:   opts.AppVersion,
		proxyTransport: &http.Transport{
			Proxy:                 http.ProxyFromEnvironment,
			DialContext:           dialer.DialContext,
			ResponseHeaderTimeout: 5 * time.Second,
			IdleConnTimeout:       30 * time.Second,
		},
	}
	c.version = detectVersion(mihomoPath)
	c.manager = NewManager(store, mihomoPath)
	return c, nil
}

func (c *Controller) Shutdown(ctx context.Context) {
	if c.manager != nil {
		c.manager.Shutdown(ctx)
	}
}

func (c *Controller) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/system", c.handleSystem)
	mux.HandleFunc("/api/profiles", c.handleProfiles)
	mux.HandleFunc("/api/profiles/", c.handleProfile)
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
			Name   string `json:"name"`
			Config string `json:"config"`
		}
		if err := readJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err)
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
	if len(parts) > 1 && parts[1] == "config" {
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
			Name   string `json:"name"`
			Config string `json:"config"`
		}
		if err := readJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		profile, err := c.store.UpdateProfile(id, req.Name, req.Config)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
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
		var req struct {
			Name           string `json:"name"`
			ProfileID      string `json:"profileId"`
			Config         string `json:"config"`
			MixedPort      int    `json:"mixedPort"`
			ControllerPort int    `json:"controllerPort"`
		}
		if err := readJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		if strings.TrimSpace(req.Name) == "" {
			req.Name = "New instance"
		}
		item, err := c.store.Create(req.Name, req.ProfileID, req.Config, req.MixedPort, req.ControllerPort)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		view, _ := c.manager.View(item.ID)
		writeJSONStatus(w, http.StatusCreated, view)
	default:
		methodNotAllowed(w)
	}
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
	case "logs":
		c.handleLogs(w, r, id)
	case "selection":
		c.handleSelection(w, r, id)
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
			Name           string `json:"name"`
			ProfileID      string `json:"profileId"`
			Config         string `json:"config"`
			MixedPort      int    `json:"mixedPort"`
			ControllerPort int    `json:"controllerPort"`
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
		if c.manager.state(id) != nil && req.ProfileID != "" && current.ProfileID != req.ProfileID {
			writeError(w, http.StatusConflict, errors.New("stop the instance before changing profile"))
			return
		}
		if req.Config != "" && req.ProfileID != "" && current.ProfileID != req.ProfileID {
			writeError(w, http.StatusBadRequest, errors.New("profileId and config cannot be changed in the same request"))
			return
		}
		item, err := c.store.Update(id, req.Name, req.ProfileID, req.Config, req.MixedPort, req.ControllerPort)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
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
