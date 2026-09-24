//go:build linux

package hardening

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"testing"
)

func TestPrivateEnvironment(t *testing.T) {
	if err := PrivateEnvironment(); err != nil {
		t.Fatal(err)
	}
	dumpable, _, errno := syscall.RawSyscall(syscall.SYS_PRCTL, syscall.PR_GET_DUMPABLE, 0, 0)
	if errno != 0 || dumpable != 0 {
		t.Errorf("dumpable: %d (%v)", dumpable, errno)
	}
	// What the process and the Go runtime read of their own still works (its own environ does not:
	// that file is root's now, and nothing of the site reads it).
	if _, err := os.Executable(); err != nil {
		t.Errorf("os.Executable: %v", err)
	}
	for _, file := range []string{"/proc/self/stat", "/proc/self/status", "/proc/self/cgroup", "/proc/self/mountinfo"} {
		if _, err := os.ReadFile(file); err != nil {
			t.Errorf("%s: %v", file, err)
		}
	}
	// Another process of the same user does not. Root reads everything, so it proves nothing there.
	if os.Geteuid() == 0 {
		t.Skip("running as root")
	}
	if out, err := exec.Command("cat", fmt.Sprintf("/proc/%d/environ", os.Getpid())).CombinedOutput(); err == nil {
		t.Errorf("another process of the same user read the environment: %q", out)
	}
}
