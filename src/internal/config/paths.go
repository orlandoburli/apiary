package config

import "path/filepath"

// DataDir returns the project-scoped data directory for a given config file:
// a `.apiary` folder alongside the config. Apiary keeps each project's state
// (database, logs, IPC socket) here so projects stay isolated from one another.
//
// It is the single source of truth for where project state lives — the CLI and
// the daemon both derive their paths from it.
func DataDir(configFile string) string {
	abs, err := filepath.Abs(configFile)
	if err != nil {
		abs = configFile
	}
	return filepath.Join(filepath.Dir(abs), ".apiary")
}
