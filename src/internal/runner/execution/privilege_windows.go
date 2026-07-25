//go:build windows

package execution

// checkPrivilege is a no-op on Windows; privilege ceiling on Windows is
// enforced via token integrity levels and ACLs, not Unix uid 0.
func checkPrivilege(_ bool) error { return nil }
