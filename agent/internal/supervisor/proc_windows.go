//go:build windows

package supervisor

// Windows process-tree supervision uses Job Objects with
// JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE — the correct tree-kill primitive on a
// platform without unix process groups.
//
// Two documented platform caveats:
//   - Graceful stop does not exist here: Windows has no cross-process POSIX
//     signals, and console ctrl events cannot be delivered safely from a
//     service. stop.signal is ignored; stop == tree kill via the job.
//   - A grandchild spawned in the microseconds between process start and job
//     assignment can escape the job. In practice assignment happens before
//     the child's main() runs.

import (
	"os/exec"
	"strconv"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// jobs maps a supervised root PID to its Job Object handle.
var jobs = struct {
	sync.Mutex
	m map[int]windows.Handle
}{m: map[int]windows.Handle{}}

func configureSysProc(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP,
	}
}

// attachTree creates a kill-on-close job and puts the new process in it.
func attachTree(pid int) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return
	}
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{
		BasicLimitInformation: windows.JOBOBJECT_BASIC_LIMIT_INFORMATION{
			LimitFlags: windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE,
		},
	}
	if _, err := windows.SetInformationJobObject(job,
		windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
		windows.CloseHandle(job)
		return
	}
	proc, err := windows.OpenProcess(
		windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(pid))
	if err != nil {
		windows.CloseHandle(job)
		return
	}
	defer windows.CloseHandle(proc)
	if err := windows.AssignProcessToJobObject(job, proc); err != nil {
		windows.CloseHandle(job)
		return
	}
	jobs.Lock()
	jobs.m[pid] = job
	jobs.Unlock()
}

func jobFor(pid int) (windows.Handle, bool) {
	jobs.Lock()
	defer jobs.Unlock()
	h, ok := jobs.m[pid]
	return h, ok
}

// signalTree: no POSIX signals on Windows — graceful and forced stop are the
// same operation (see package comment).
func signalTree(pid int, _ syscall.Signal) error {
	return killTree(pid)
}

// killTree terminates the whole job. Fallback for orphans from a previous
// agent run (whose job handle died with that process): taskkill by parent
// chain.
func killTree(pid int) error {
	if job, ok := jobFor(pid); ok {
		err := windows.TerminateJobObject(job, 137)
		// Handle stays open until the tree is confirmed gone (waitTreeGone
		// queries it); release happens there.
		return err
	}
	return exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(pid)).Run()
}

func stopSignal(name string) syscall.Signal {
	return syscall.SIGTERM // unused on windows; see signalTree
}

// processGroupAlive uses job accounting when we own the job, else probes the
// root process.
func processGroupAlive(pid int) bool {
	if job, ok := jobFor(pid); ok {
		var acct jobBasicAccounting
		err := windows.QueryInformationJobObject(job,
			windows.JobObjectBasicAccountingInformation,
			uintptr(unsafe.Pointer(&acct)), uint32(unsafe.Sizeof(acct)), nil)
		if err == nil {
			return acct.ActiveProcesses > 0
		}
	}
	proc, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return false
	}
	defer windows.CloseHandle(proc)
	var code uint32
	if err := windows.GetExitCodeProcess(proc, &code); err != nil {
		return false
	}
	return code == 259 // STILL_ACTIVE
}

// jobBasicAccounting mirrors JOBOBJECT_BASIC_ACCOUNTING_INFORMATION.
type jobBasicAccounting struct {
	TotalUserTime             int64
	TotalKernelTime           int64
	ThisPeriodTotalUserTime   int64
	ThisPeriodTotalKernelTime int64
	TotalPageFaultCount       uint32
	TotalProcesses            uint32
	ActiveProcesses           uint32
	TotalTerminatedProcesses  uint32
}

// waitTreeGone polls until the job is empty (or root gone) or timeout; on
// success it releases the job handle.
func waitTreeGone(pid int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if !processGroupAlive(pid) {
			releaseJob(pid)
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !processGroupAlive(pid) {
		releaseJob(pid)
		return true
	}
	return false
}

func releaseJob(pid int) {
	jobs.Lock()
	if h, ok := jobs.m[pid]; ok {
		// The tree is gone; closing cannot kill anything anymore.
		windows.CloseHandle(h)
		delete(jobs.m, pid)
	}
	jobs.Unlock()
}
