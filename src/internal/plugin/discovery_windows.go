//go:build windows

package plugin

// checkDirOwnerOnly is a no-op on Windows; ACL-based access control is outside
// the scope of this stopgap check.
func checkDirOwnerOnly(_ string) error {
	return nil
}
