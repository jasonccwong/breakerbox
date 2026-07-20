//go:build !windows

package supervisor

import (
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// configureSysProc places the child in its own process group so the whole
// tree (including grandchildren) can be signaled as one unit.
func configureSysProc(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// signalTree sends sig to the child's entire process group.
func signalTree(pid int, sig syscall.Signal) error {
	// Negative pid targets the process group.
	return syscall.Kill(-pid, sig)
}

// killTree force-kills the child's entire process group.
func killTree(pid int) error {
	return signalTree(pid, syscall.SIGKILL)
}

// stopSignal maps a StopSpec signal name to a syscall signal.
func stopSignal(name string) syscall.Signal {
	switch name {
	case "SIGINT":
		return syscall.SIGINT
	case "SIGQUIT":
		return syscall.SIGQUIT
	case "SIGHUP":
		return syscall.SIGHUP
	case "", "SIGTERM":
		return syscall.SIGTERM
	}
	return syscall.SIGTERM
}

// processGroupAlive reports whether any non-zombie process remains in the
// group. Zombies must not count: when the agent's environment has no reaping
// init (e.g. a container), killed grandchildren linger as zombies in the
// process table and would otherwise make the group look alive forever.
func processGroupAlive(pid int) bool {
	// Fast path: signal 0 probes the group without delivering. If even that
	// fails, nothing (not even a zombie) is left.
	if err := syscall.Kill(-pid, 0); err != nil {
		return false
	}
	return liveGroupMembers(pid) > 0
}

// liveGroupMembers counts non-zombie processes in the group via ps, which is
// portable across macOS and Linux.
func liveGroupMembers(pgid int) int {
	out, err := exec.Command("ps", "-eo", "pgid=,stat=").Output()
	if err != nil {
		// Can't tell; assume alive so callers keep waiting rather than
		// declaring victory early.
		return 1
	}
	count := 0
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		g, err := strconv.Atoi(fields[0])
		if err != nil || g != pgid {
			continue
		}
		if strings.HasPrefix(fields[1], "Z") {
			continue // zombie: dead, just unreaped
		}
		count++
	}
	return count
}

// waitTreeGone polls until the process group is empty or the timeout elapses.
func waitTreeGone(pid int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if !processGroupAlive(pid) {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return !processGroupAlive(pid)
}
