//go:build linux

package plugin

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/unix"
)

// Sandbox launcher environment variable names used to pass configuration
// from the parent process to the re-exec'd launcher child.
const (
	envSandboxExec  = "_APIARY_SANDBOX_EXEC"
	envSandboxBin   = "_APIARY_SANDBOX_BIN"
	envSandboxRoot  = "_APIARY_SANDBOX_ROOT"
	envSandboxRead  = "_APIARY_SANDBOX_READ"
	envSandboxWrite = "_APIARY_SANDBOX_WRITE"

	// pathSep is the delimiter used to encode multiple paths in a single env var.
	// Newline is safe because validateSecurity rejects empty path entries and
	// paths cannot contain newlines.
	pathSep = "\n"
)

// IsSandboxLauncher reports whether the current process was re-exec'd by
// applySandbox to apply Landlock filesystem restrictions before running a plugin.
func IsSandboxLauncher() bool {
	return os.Getenv(envSandboxExec) == "1"
}

// RunSandboxLauncher applies Landlock filesystem restrictions declared in the
// plugin manifest and then exec()s the actual plugin binary. It never returns
// on success. Must be called before any other initialization in main().
func RunSandboxLauncher() {
	bin := os.Getenv(envSandboxBin)
	root := os.Getenv(envSandboxRoot)
	readPaths := decodePaths(os.Getenv(envSandboxRead))
	writePaths := decodePaths(os.Getenv(envSandboxWrite))

	// Build env for the plugin without the sandbox control variables.
	env := filterSandboxEnv(os.Environ())

	// Apply Landlock (best-effort; no-op on kernels < 5.13 or without support).
	applyLandlock(root, readPaths, writePaths)

	if err := syscall.Exec(bin, []string{bin}, env); err != nil {
		os.Stderr.WriteString("apiary sandbox: exec " + bin + ": " + err.Error() + "\n")
		os.Exit(125)
	}
}

// probeLandlockSupport returns true if the kernel supports Landlock ABI >= 1.
// Used by tests to skip when the feature is unavailable.
func probeLandlockSupport() bool {
	v, _, errno := syscall.RawSyscall(unix.SYS_LANDLOCK_CREATE_RULESET, 0, 0, uintptr(unix.LANDLOCK_CREATE_RULESET_VERSION))
	return errno == 0 && int(v) >= 1
}

// applyLandlock restricts the calling process's filesystem access to a minimal
// set of system paths, the plugin root, and the declared read/write paths.
// It is a no-op on kernels that do not support Landlock (< 5.13).
func applyLandlock(pluginRoot string, readPaths, writePaths []string) {
	// Probe: LANDLOCK_CREATE_RULESET_VERSION returns the highest supported ABI.
	abi, _, errno := syscall.RawSyscall(unix.SYS_LANDLOCK_CREATE_RULESET, 0, 0, uintptr(unix.LANDLOCK_CREATE_RULESET_VERSION))
	if errno != 0 || int(abi) < 1 {
		return
	}

	// ABI 1 access rights (kernel 5.13+).
	// REFER (ABI 2) and TRUNCATE (ABI 3) are intentionally excluded so the
	// 8-byte ABI-1 attr size is correct on kernels that predate those ABIs.
	const (
		abiOneReadAccess = unix.LANDLOCK_ACCESS_FS_READ_FILE |
			unix.LANDLOCK_ACCESS_FS_READ_DIR |
			unix.LANDLOCK_ACCESS_FS_EXECUTE
		abiOneWriteAccess = unix.LANDLOCK_ACCESS_FS_WRITE_FILE |
			unix.LANDLOCK_ACCESS_FS_MAKE_REG |
			unix.LANDLOCK_ACCESS_FS_MAKE_DIR |
			unix.LANDLOCK_ACCESS_FS_REMOVE_FILE |
			unix.LANDLOCK_ACCESS_FS_REMOVE_DIR |
			unix.LANDLOCK_ACCESS_FS_MAKE_CHAR |
			unix.LANDLOCK_ACCESS_FS_MAKE_SOCK |
			unix.LANDLOCK_ACCESS_FS_MAKE_FIFO |
			unix.LANDLOCK_ACCESS_FS_MAKE_BLOCK |
			unix.LANDLOCK_ACCESS_FS_MAKE_SYM
	)

	// Use a 1-field struct so the size argument is 8 (ABI 1 only).
	// Passing the full unix.LandlockRulesetAttr (24 bytes) on an ABI-1 kernel
	// would cause EINVAL because the kernel does not recognise the extra fields.
	type rulesetAttr1 struct{ accessFs uint64 }
	attr := rulesetAttr1{accessFs: abiOneReadAccess | abiOneWriteAccess}

	fd, _, errno := syscall.RawSyscall(
		unix.SYS_LANDLOCK_CREATE_RULESET,
		uintptr(unsafe.Pointer(&attr)),
		unsafe.Sizeof(attr),
		0,
	)
	if errno != 0 {
		return
	}
	rulesetFd := int(fd)
	defer syscall.Close(rulesetFd)

	addReadable := func(path string) {
		addLandlockPath(rulesetFd, path, abiOneReadAccess)
	}
	addWritable := func(path string) {
		addLandlockPath(rulesetFd, path, abiOneReadAccess|abiOneWriteAccess)
	}

	// System paths required for any ELF or shell-script plugin to start.
	for _, p := range landlockSystemReadPaths() {
		addReadable(p)
	}

	// Plugin root: always readable + executable (the plugin binary lives here).
	addReadable(pluginRoot)

	// Declared read paths (relative → joined with plugin root; absolute as-is).
	for _, p := range readPaths {
		addReadable(resolvePath(p, pluginRoot))
	}

	// Declared write paths get full read+write access.
	for _, p := range writePaths {
		addWritable(resolvePath(p, pluginRoot))
	}

	// PR_SET_NO_NEW_PRIVS is required before landlock_restrict_self.
	if err := unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0); err != nil {
		return
	}

	// Apply restrictions to this process; they are inherited across exec.
	syscall.RawSyscall(unix.SYS_LANDLOCK_RESTRICT_SELF, uintptr(rulesetFd), 0, 0)
}

// addLandlockPath opens path with O_PATH and registers it as a Landlock rule.
// Paths that do not exist or cannot be opened are silently skipped — the
// caller should declare only paths that will exist at invocation time.
func addLandlockPath(rulesetFd int, path string, access uint64) {
	fd, err := syscall.Open(path, syscall.O_RDONLY|unix.O_PATH, 0)
	if err != nil {
		return
	}
	defer syscall.Close(fd)

	// LandlockPathBeneathAttr is __attribute__((packed)) in the kernel header:
	// { __u64 allowed_access; __s32 parent_fd; } → 12 bytes, no padding.
	// unix.LandlockPathBeneathAttr has the same layout (uint64 + int32 = 12 B).
	attr := unix.LandlockPathBeneathAttr{
		Allowed_access: access,
		Parent_fd:      int32(fd),
	}
	syscall.RawSyscall(
		unix.SYS_LANDLOCK_ADD_RULE,
		uintptr(rulesetFd),
		uintptr(unix.LANDLOCK_RULE_PATH_BENEATH),
		uintptr(unsafe.Pointer(&attr)),
	)
}

// landlockSystemReadPaths returns the minimal set of read-only FHS paths that
// every sandboxed process needs (shell interpreter, shared libraries, linker).
// /proc, /dev, /sys, and /run are excluded: Landlock ABI 1 does not restrict
// access to pseudo-filesystems (tmpfs/proc/devtmpfs/sysfs), so those remain
// unrestricted regardless.
func landlockSystemReadPaths() []string {
	return []string{
		"/bin", "/usr/bin", "/usr/local/bin",
		"/lib", "/lib64",
		"/usr/lib", "/usr/lib64",
		"/usr/local/lib",
		"/etc/ld.so.cache", "/etc/ld.so.conf", "/etc/ld.so.conf.d",
		"/usr/share/zoneinfo",
	}
}

func resolvePath(p, root string) string {
	if filepath.IsAbs(p) {
		return filepath.Clean(p)
	}
	return filepath.Join(root, p)
}

func decodePaths(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(s, pathSep)
}

func filterSandboxEnv(environ []string) []string {
	drop := map[string]bool{
		envSandboxExec:  true,
		envSandboxBin:   true,
		envSandboxRoot:  true,
		envSandboxRead:  true,
		envSandboxWrite: true,
	}
	out := make([]string, 0, len(environ))
	for _, e := range environ {
		k, _, _ := strings.Cut(e, "=")
		if !drop[k] {
			out = append(out, e)
		}
	}
	return out
}
