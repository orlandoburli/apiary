//go:build !linux

package plugin

// IsSandboxLauncher always returns false on non-Linux platforms;
// Landlock filesystem sandboxing is Linux-specific.
func IsSandboxLauncher() bool { return false }

// RunSandboxLauncher is a no-op on non-Linux platforms.
func RunSandboxLauncher() {}
