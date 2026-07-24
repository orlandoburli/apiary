//go:build windows

package config

// warnDotEnvPerms is a no-op on Windows, which does not use Unix-style
// permission bits for file access control.
func warnDotEnvPerms(_ string) {}
