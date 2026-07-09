package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime/debug"
	"strings"
	"syscall"
	"time"

	"mihomo-fleet/internal/app"
)

var (
	version   = "dev"
	commit    = "unknown"
	buildDate = "unknown"
)

type buildMetadata struct {
	Version   string
	Commit    string
	BuildDate string
}

func main() {
	var (
		bind = flag.String("bind", "127.0.0.1", "HTTP bind address. Keep this at 127.0.0.1/localhost for "+
			"local-only access. Binding to any other address (e.g. 0.0.0.0 or a LAN IP) exposes an "+
			"UNAUTHENTICATED control plane to the network unless -api-secret is also set")
		port        = flag.Int("port", 47890, "HTTP bind port")
		dataDir     = flag.String("data", ".mihomo-fleet", "runtime data directory")
		mihomoPath  = flag.String("mihomo", "", "path to mihomo binary")
		openBrowser = flag.Bool("open", false, "print browser URL with emphasis")
		showVersion = flag.Bool("version", false, "print version and exit")
		apiSecret   = flag.String("api-secret", "", "bearer token required on the Authorization header for all "+
			"/api/ requests. Required (non-empty) when -bind is not a loopback address; optional but recommended "+
			"otherwise")
	)
	flag.Parse()

	metadata := resolveBuildMetadata(readBuildInfo(), versionFileCandidates())
	if *showVersion {
		fmt.Printf("mihomo-fleet %s\n", metadata.Version)
		fmt.Printf("commit: %s\n", metadata.Commit)
		fmt.Printf("build date: %s\n", metadata.BuildDate)
		return
	}

	if !isLoopbackBind(*bind) {
		if strings.TrimSpace(*apiSecret) == "" {
			fmt.Fprintf(os.Stderr, "refusing to start: -bind=%q is not a loopback address, which would expose "+
				"Mihomo Fleet's control plane (instance/profile config, proxy credentials, start/stop/config "+
				"endpoints) to the network with NO authentication. Set -api-secret to a long random token to "+
				"allow this bind, e.g.:\n  ./mihomo-fleet -bind %s -api-secret \"$(openssl rand -hex 32)\"\n",
				*bind, *bind)
			os.Exit(1)
		}
		log.Printf("WARNING: binding to non-loopback address %s:%d exposes the control API to the network; "+
			"keep -api-secret private and restrict network access (firewall/VPN) to trusted clients", *bind, *port)
	}

	controller, err := app.NewController(app.Options{
		Bind:       *bind,
		Port:       *port,
		DataDir:    *dataDir,
		MihomoPath: *mihomoPath,
		AppVersion: metadata.Version,
	})
	if err != nil {
		log.Fatalf("init controller: %v", err)
	}
	controller.SetAPISecret(*apiSecret)
	defer controller.Shutdown(context.Background())

	mux := http.NewServeMux()
	controller.RegisterRoutes(mux)
	handler := controller.SecureHandler(mux)

	addr := fmt.Sprintf("%s:%d", *bind, *port)
	server := &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
	}

	errs := make(chan error, 1)
	go func() {
		errs <- server.ListenAndServe()
	}()

	url := fmt.Sprintf("http://%s:%d", *bind, *port)
	if *openBrowser {
		log.Printf("Mihomo Fleet %s ready: %s", metadata.Version, url)
	} else {
		log.Printf("Mihomo Fleet %s listening on %s", metadata.Version, url)
	}

	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)

	select {
	case sig := <-signals:
		log.Printf("received %s, shutting down", sig)
	case err := <-errs:
		if err != nil && err != http.ErrServerClosed {
			log.Fatalf("http server: %v", err)
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := server.Shutdown(ctx); err != nil {
		log.Printf("http shutdown: %v", err)
	}
	controller.Shutdown(ctx)
}

// isLoopbackBind reports whether bind refers to a loopback-only address:
// 127.0.0.0/8, ::1, or the hostname "localhost" (case-insensitive). An empty
// bind address behaves like Go's http.Server (listens on all interfaces,
// same as 0.0.0.0) and is therefore NOT loopback; neither is 0.0.0.0, a LAN
// IP, or any other hostname.
func isLoopbackBind(bind string) bool {
	host := strings.TrimSpace(bind)
	if host == "" {
		return false
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	host = strings.Trim(host, "[]")
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	return ip.IsLoopback()
}

func resolveBuildMetadata(info *debug.BuildInfo, versionFiles []string) buildMetadata {
	return buildMetadata{
		Version:   resolveAppVersion(info, versionFiles),
		Commit:    resolveCommit(info),
		BuildDate: resolveBuildDate(info),
	}
}

func resolveAppVersion(info *debug.BuildInfo, versionFiles []string) string {
	if isConcreteBuildValue(version) {
		return normalizeReleaseVersion(version)
	}
	if fileVersion := readVersionFile(versionFiles); fileVersion != "" {
		return normalizeReleaseVersion(fileVersion)
	}
	if info != nil && isConcreteBuildValue(info.Main.Version) {
		return normalizeReleaseVersion(info.Main.Version)
	}
	return "dev"
}

func resolveCommit(info *debug.BuildInfo) string {
	if isConcreteBuildValue(commit) {
		return commit
	}
	revision := buildSetting(info, "vcs.revision")
	if revision == "" {
		return "unknown"
	}
	if len(revision) > 7 {
		revision = revision[:7]
	}
	if buildSetting(info, "vcs.modified") == "true" {
		return revision + "-dirty"
	}
	return revision
}

func resolveBuildDate(info *debug.BuildInfo) string {
	if isConcreteBuildValue(buildDate) {
		return buildDate
	}
	if value := buildSetting(info, "vcs.time"); value != "" {
		return value
	}
	return "unknown"
}

func isConcreteBuildValue(value string) bool {
	value = strings.TrimSpace(value)
	return value != "" && value != "dev" && value != "unknown" && value != "(devel)"
}

func normalizeReleaseVersion(value string) string {
	return strings.TrimPrefix(strings.TrimSpace(value), "v")
}

func readBuildInfo() *debug.BuildInfo {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return nil
	}
	return info
}

func buildSetting(info *debug.BuildInfo, key string) string {
	if info == nil {
		return ""
	}
	for _, setting := range info.Settings {
		if setting.Key == key {
			return strings.TrimSpace(setting.Value)
		}
	}
	return ""
}

func readVersionFile(paths []string) string {
	for _, path := range paths {
		if path == "" {
			continue
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		if value := strings.TrimSpace(string(raw)); value != "" {
			return value
		}
	}
	return ""
}

func versionFileCandidates() []string {
	var paths []string
	if wd, err := os.Getwd(); err == nil {
		paths = append(paths, filepath.Join(wd, "VERSION"))
	}
	if exe, err := os.Executable(); err == nil {
		paths = append(paths, filepath.Join(filepath.Dir(exe), "VERSION"))
	}
	return paths
}
