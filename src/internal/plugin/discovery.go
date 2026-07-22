package plugin

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type Registry struct {
	plugins map[string]*Installed
}

func DiscoverConfigured(paths []string, configFile, apiaryVersion string) (*Registry, []error) {
	absoluteConfig, err := filepath.Abs(configFile)
	if err != nil {
		absoluteConfig = configFile
	}
	baseDir := filepath.Dir(absoluteConfig)
	if len(paths) == 0 {
		paths = []string{filepath.Join(".apiary", "plugins")}
	}
	return Discover(paths, baseDir, apiaryVersion)
}

func Discover(paths []string, baseDir, apiaryVersion string) (*Registry, []error) {
	registry := &Registry{plugins: map[string]*Installed{}}
	var errs []error
	for _, configuredPath := range paths {
		path := expandPath(configuredPath, baseDir)
		entries, err := os.ReadDir(path)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			errs = append(errs, fmt.Errorf("discover plugins in %q: %w", path, err))
			continue
		}
		roots := []string{}
		if _, err := os.Stat(filepath.Join(path, ManifestFilename)); err == nil {
			roots = append(roots, path)
		} else {
			for _, entry := range entries {
				if entry.IsDir() {
					roots = append(roots, filepath.Join(path, entry.Name()))
				}
			}
		}
		sort.Strings(roots)
		for _, root := range roots {
			if _, err := os.Stat(filepath.Join(root, ManifestFilename)); err != nil {
				continue
			}
			installed, err := Load(root, apiaryVersion)
			if err != nil {
				errs = append(errs, err)
				continue
			}
			if previous, exists := registry.plugins[installed.Manifest.ID]; exists {
				errs = append(errs, fmt.Errorf("duplicate plugin id %q in %q and %q; remove one installation", installed.Manifest.ID, previous.Root, installed.Root))
				continue
			}
			registry.plugins[installed.Manifest.ID] = installed
		}
	}
	return registry, errs
}

func (r *Registry) Get(id string) (*Installed, bool) {
	if r == nil {
		return nil, false
	}
	p, ok := r.plugins[id]
	return p, ok
}

func (r *Registry) List() []*Installed {
	if r == nil {
		return nil
	}
	result := make([]*Installed, 0, len(r.plugins))
	for _, installed := range r.plugins {
		result = append(result, installed)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Manifest.ID < result[j].Manifest.ID })
	return result
}

func expandPath(path, baseDir string) string {
	path = strings.TrimSpace(path)
	if path == "~" || strings.HasPrefix(path, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			path = filepath.Join(home, strings.TrimPrefix(path, "~/"))
		}
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(baseDir, path)
	}
	path, _ = filepath.Abs(path)
	return filepath.Clean(path)
}
