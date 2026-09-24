//go:build linux

package hardening

import "syscall"

// PrivateEnvironment makes the process non-dumpable. Its /proc/<pid>/environ — every secret of
// /etc/krokosha/env — and its memory then belong to root: other processes of the same user cannot
// read them. The site build runs third-party code (npm) as that user (deploy/bin/build-release.sh).
func PrivateEnvironment() error {
	if _, _, errno := syscall.RawSyscall(syscall.SYS_PRCTL, syscall.PR_SET_DUMPABLE, 0, 0); errno != 0 {
		return errno
	}
	return nil
}
