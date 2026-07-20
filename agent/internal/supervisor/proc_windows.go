//go:build windows

package supervisor

// Windows support lands in Phase 4 using Job Objects with
// JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE (Windows has no process groups in the
// unix sense; the Job Object is the correct tree-kill primitive).
// These stubs keep the module compiling on the CI matrix until then.

import (
	"errors"
	"os/exec"
	"syscall"
	"time"
)

var errWindowsNotImplemented = errors.New("windows process supervision lands in Phase 4 (Job Objects)")

func configureSysProc(cmd *exec.Cmd) {
	// CREATE_NEW_PROCESS_GROUP at minimum; full Job Object wiring in Phase 4.
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP}
}

func signalTree(pid int, _ syscall.Signal) error {
	return errWindowsNotImplemented
}

func killTree(pid int) error {
	return errWindowsNotImplemented
}

func stopSignal(name string) syscall.Signal {
	return syscall.SIGTERM
}

func processGroupAlive(pid int) bool {
	return false
}

func waitTreeGone(pid int, timeout time.Duration) bool {
	return true
}
