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

// uniqueSlug slugifies name (lowercase, non-alphanumeric runs collapsed to a
// single "-") and disambiguates it against existing by appending "-2", "-3",
// etc. until it no longer collides. fallback is used verbatim when name
// slugifies to the empty string. This is the shared implementation behind
// uniqueID and uniqueProfileID (testing L1): the two were previously
// identical line-for-line except for the map's value type and the fallback
// string, which Go 1.22 generics let us collapse into one function.
func uniqueSlug[T any](name, fallback string, existing map[string]T) string {
	base := strings.ToLower(strings.TrimSpace(name))
	base = nonSlug.ReplaceAllString(base, "-")
	base = strings.Trim(base, "-")
	if base == "" {
		base = fallback
	}
	id := base
	for i := 2; ; i++ {
		if _, ok := existing[id]; !ok {
			return id
		}
		id = fmt.Sprintf("%s-%d", base, i)
	}
}

func uniqueID(name string, existing map[string]*Instance) string {
	return uniqueSlug(name, "instance", existing)
}

func uniqueProfileID(name string, existing map[string]*Profile) string {
	return uniqueSlug(name, "profile", existing)
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
	// L8 (docs/review-2026-07-11-go-architecture.md): without an explicit
	// fsync here, the rename below is only guaranteed atomic with respect to
	// the directory entry -- the data itself may still be sitting in the
	// page cache. A crash/power loss between rename and the OS's own
	// eventual flush can then leave the destination pointing at a
	// zero-length or partially-written file. instances.json and profile
	// config.yaml are the two files this matters for; the ~1ms fsync cost
	// per write is worth it for data that would otherwise need manual
	// recovery (see Store.load's corrupt-file handling below).
	if err := tmp.Sync(); err != nil {
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
