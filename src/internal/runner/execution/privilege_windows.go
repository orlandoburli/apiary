//go:build windows

package execution

// checkPrivilege is a no-op on Windows: the Unix uid-0 model does not apply
// and Windows SYSTEM/Administrator privilege detection requires a separate
// token-query path not implemented here.
func checkPrivilege(_ bool) error { return nil }
