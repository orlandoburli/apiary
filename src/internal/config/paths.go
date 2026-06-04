package config

import (
	"path/filepath"
)

// DataDir returns the project-scoped data directory for a given config file:
// a `.apiary` folder alongside the config. Apiary keeps each project's state
// (database, logs, IPC socket) here so projects stay isolated from one another.
//
// If the config file is already inside a `.apiary` directory (e.g. when the
// user placed apiary.yaml inside .apiary/apiary.yaml), that directory is used
// directly — no nesting happens.
//
// It is the single source of truth for where project state lives — the CLI and
// the daemon both derive their paths from it.
func DataDir(configFile string) string {
	abs, err := filepath.Abs(configFile)
	if err != nil {
		abs = configFile
	}
	dir := filepath.Dir(abs)
	if filepath.Base(dir) == ".apiary" {
		return dir
	}
	return filepath.Join(dir, ".apiary")
}
