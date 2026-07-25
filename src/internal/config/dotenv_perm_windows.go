//go:build windows

package config

// checkDotEnvPerms is a no-op on Windows, which does not use Unix-style
// permission bits for file access control.
func checkDotEnvPerms(_ string) error { return nil }
