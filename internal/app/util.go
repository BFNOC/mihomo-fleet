package app

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var nonSlug = regexp.MustCompile(`[^a-z0-9]+`)

func uniqueID(name string, existing map[string]*Instance) string {
	base := strings.ToLower(strings.TrimSpace(name))
	base = nonSlug.ReplaceAllString(base, "-")
	base = strings.Trim(base, "-")
	if base == "" {
		base = "instance"
	}
	id := base
	for i := 2; ; i++ {
		if _, ok := existing[id]; !ok {
			return id
		}
		id = fmt.Sprintf("%s-%d", base, i)
	}
}

func uniqueProfileID(name string, existing map[string]*Profile) string {
	base := strings.ToLower(strings.TrimSpace(name))
	base = nonSlug.ReplaceAllString(base, "-")
	base = strings.Trim(base, "-")
	if base == "" {
		base = "profile"
	}
	id := base
	for i := 2; ; i++ {
		if _, ok := existing[id]; !ok {
			return id
		}
		id = fmt.Sprintf("%s-%d", base, i)
	}
}

func randomToken() (string, error) {
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw[:]), nil
}

var isPortFree = func(port int) bool {
	if port <= 0 {
		return false
	}
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return false
	}
	_ = ln.Close()
	return true
}

func allocatePort(start int, used map[int]bool) int {
	if start < 1 {
		start = 1
	}
	for port := start; port <= 65535 && port < start+5000; port++ {
		if used[port] {
			continue
		}
		if isPortFree(port) {
			used[port] = true
			return port
		}
	}
	return 0
}

func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmpPath)
		}
	}()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Chmod(perm); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return err
	}
	cleanup = false
	return nil
}

func cloneStringMap(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}
