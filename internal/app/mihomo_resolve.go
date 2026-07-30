package app

import (
	"bytes"
	"context"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

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
