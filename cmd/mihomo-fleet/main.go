package main

import (
	"context"
	"flag"
	"fmt"
	"log"
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
		bind        = flag.String("bind", "127.0.0.1", "HTTP bind address")
		port        = flag.Int("port", 47890, "HTTP bind port")
		dataDir     = flag.String("data", ".mihomo-fleet", "runtime data directory")
		mihomoPath  = flag.String("mihomo", "", "path to mihomo binary")
		openBrowser = flag.Bool("open", false, "print browser URL with emphasis")
		showVersion = flag.Bool("version", false, "print version and exit")
	)
	flag.Parse()

	metadata := resolveBuildMetadata(readBuildInfo(), versionFileCandidates())
	if *showVersion {
		fmt.Printf("mihomo-fleet %s\n", metadata.Version)
		fmt.Printf("commit: %s\n", metadata.Commit)
		fmt.Printf("build date: %s\n", metadata.BuildDate)
		return
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
