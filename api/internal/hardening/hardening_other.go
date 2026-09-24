//go:build !linux

package hardening

// PrivateEnvironment does nothing outside Linux: the site runs on Linux, development may not.
func PrivateEnvironment() error { return nil }
