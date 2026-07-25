//go:build windows

package cli

// checkDotEnvPerms is a no-op on Windows, which uses ACLs rather than
// Unix-style permission bits.
func checkDotEnvPerms(_ string) error { return nil }
