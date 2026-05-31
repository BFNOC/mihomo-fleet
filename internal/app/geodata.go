package app

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

var geodataFiles = []struct {
	canonical string
	aliases   []string
}{
	{canonical: "GeoSite.dat", aliases: []string{"GeoSite.dat", "geosite.dat"}},
	{canonical: "GeoIP.dat", aliases: []string{"GeoIP.dat", "geoip.dat"}},
	{canonical: "Country.mmdb", aliases: []string{"Country.mmdb", "country.mmdb"}},
	{canonical: "ASN.mmdb", aliases: []string{"ASN.mmdb", "asn.mmdb"}},
}

func (m *Manager) prepareGeodata(item *Instance) ([]string, error) {
	return ensureGeodataFiles(filepath.Dir(item.RuntimeConfigPath), m.geodataSourceDirs())
}

func (m *Manager) geodataSourceDirs() []string {
	var dirs []string
	if m.store != nil && m.store.dataDir != "" {
		dirs = append(dirs, filepath.Join(m.store.dataDir, "geo"), m.store.dataDir)
	}
	if exe, err := os.Executable(); err == nil {
		if resolved, err := filepath.EvalSymlinks(exe); err == nil {
			exe = resolved
		}
		dirs = append(dirs, filepath.Dir(exe))
	}
	if cwd, err := os.Getwd(); err == nil {
		dirs = append(dirs, cwd)
	}
	if m.mihomoPath != "" {
		path := m.mihomoPath
		if abs, err := filepath.Abs(path); err == nil {
			path = abs
		}
		if resolved, err := filepath.EvalSymlinks(path); err == nil {
			path = resolved
		}
		dirs = append(dirs, filepath.Dir(path))
	}
	return uniqueExistingDirs(dirs)
}

func ensureGeodataFiles(instanceDir string, sourceDirs []string) ([]string, error) {
	if instanceDir == "" || instanceDir == "." {
		return nil, errors.New("runtime config path has no instance directory")
	}
	if err := os.MkdirAll(instanceDir, 0o700); err != nil {
		return nil, err
	}
	sourceDirs = uniqueExistingDirs(sourceDirs)
	instanceSourceDirs := uniqueExistingDirs([]string{instanceDir})
	prepared := make([]string, 0, len(geodataFiles))
	for _, group := range geodataFiles {
		src := findGeodataSource(sourceDirs, group.aliases)
		if src == "" {
			src = findGeodataSource(instanceSourceDirs, group.aliases)
		}
		if src == "" {
			continue
		}
		for _, name := range group.aliases {
			dst := filepath.Join(instanceDir, name)
			if err := linkOrCopyGeodata(src, dst); err != nil {
				return prepared, fmt.Errorf("prepare %s: %w", group.canonical, err)
			}
		}
		prepared = append(prepared, group.canonical)
	}
	return prepared, nil
}

func findGeodataSource(sourceDirs, names []string) string {
	for _, dir := range sourceDirs {
		for _, name := range names {
			path := filepath.Join(dir, name)
			info, err := os.Stat(path)
			if err == nil && !info.IsDir() {
				return path
			}
		}
	}
	return ""
}

func linkOrCopyGeodata(src, dst string) error {
	srcAbs, err := filepath.Abs(src)
	if err != nil {
		srcAbs = src
	}
	dstAbs, err := filepath.Abs(dst)
	if err != nil {
		dstAbs = dst
	}
	if filepath.Clean(srcAbs) == filepath.Clean(dstAbs) {
		return nil
	}
	if ok, err := existingGeodataTargetOK(srcAbs, dst); err != nil {
		return err
	} else if ok {
		return nil
	}
	symlinkErr := os.Symlink(srcAbs, dst)
	if symlinkErr == nil {
		return nil
	} else if errors.Is(symlinkErr, os.ErrExist) {
		return nil
	}
	linkErr := os.Link(srcAbs, dst)
	if linkErr == nil {
		return nil
	} else if errors.Is(linkErr, os.ErrExist) {
		return nil
	}
	if err := copyGeodataFile(srcAbs, dst); err != nil {
		return fmt.Errorf("symlink failed: %v; hardlink failed: %v; copy failed: %w", symlinkErr, linkErr, err)
	}
	return nil
}

func existingGeodataTargetOK(src, dst string) (bool, error) {
	info, err := os.Lstat(dst)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if info.IsDir() {
		return false, fmt.Errorf("%s is a directory", dst)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		return existingRegularGeodataOK(src, dst, info)
	}
	if symlinkPointsToSource(src, dst) {
		return true, nil
	}
	if err := os.Remove(dst); err != nil && !errors.Is(err, os.ErrNotExist) {
		return false, err
	}
	return false, nil
}

func symlinkPointsToSource(src, dst string) bool {
	srcInfo, err := os.Stat(src)
	if err != nil {
		return false
	}
	dstInfo, err := os.Stat(dst)
	if err != nil {
		return false
	}
	return os.SameFile(srcInfo, dstInfo)
}

func existingRegularGeodataOK(src, dst string, dstInfo os.FileInfo) (bool, error) {
	srcInfo, err := os.Stat(src)
	if err != nil {
		return false, err
	}
	if os.SameFile(srcInfo, dstInfo) {
		return true, nil
	}
	if srcInfo.Size() == dstInfo.Size() && srcInfo.ModTime().Equal(dstInfo.ModTime()) {
		return true, nil
	}
	if err := os.Remove(dst); err != nil && !errors.Is(err, os.ErrNotExist) {
		return false, err
	}
	return false, nil
}

func copyGeodataFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	info, err := in.Stat()
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(dst), ".geodata-*")
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
	if _, err := io.Copy(tmp, in); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Chmod(info.Mode().Perm()); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chtimes(tmpPath, info.ModTime(), info.ModTime()); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, dst); err != nil {
		if errors.Is(err, os.ErrExist) {
			return nil
		}
		return err
	}
	cleanup = false
	return nil
}

func uniqueExistingDirs(in []string) []string {
	seen := make(map[string]bool, len(in))
	out := make([]string, 0, len(in))
	for _, dir := range in {
		if dir == "" {
			continue
		}
		abs, err := filepath.Abs(dir)
		if err != nil {
			abs = dir
		}
		clean := filepath.Clean(abs)
		if seen[clean] {
			continue
		}
		info, err := os.Stat(clean)
		if err != nil || !info.IsDir() {
			continue
		}
		seen[clean] = true
		out = append(out, clean)
	}
	return out
}

type geodataNeeds struct {
	site bool
	ip   bool
}

func configGeodataNeeds(profile *Profile) geodataNeeds {
	if profile == nil || profile.ConfigPath == "" {
		return geodataNeeds{}
	}
	raw, err := os.ReadFile(profile.ConfigPath)
	if err != nil {
		return geodataNeeds{}
	}
	var cfg map[string]any
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		return geodataNeeds{}
	}
	rules, ok := cfg["rules"].([]any)
	if !ok {
		return geodataNeeds{}
	}
	var needs geodataNeeds
	for _, rule := range rules {
		text, ok := rule.(string)
		if !ok {
			continue
		}
		kind := strings.ToUpper(strings.TrimSpace(strings.SplitN(text, ",", 2)[0]))
		switch kind {
		case "GEOSITE":
			needs.site = true
		case "GEOIP":
			needs.ip = true
		}
	}
	return needs
}

func hasPreparedGeodata(prepared []string, name string) bool {
	for _, item := range prepared {
		if item == name {
			return true
		}
	}
	return false
}
