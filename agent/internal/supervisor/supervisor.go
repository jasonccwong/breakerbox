// Package supervisor owns app process lifecycle: spawn as an isolated
// process tree, watch, stop gracefully with escalation, and (Phase 1+)
// restart per policy. It executes only pre-registered, locally approved app
// definitions — there is no path to arbitrary command execution from the hub.
package supervisor

import (
	"fmt"
	"os"
	"os/exec"
	"sync"
	"time"

	"github.com/breakerbox/breakerbox/pkg/protocol"
)

// defaultStopTimeout is used when a definition has no stop.timeout_s.
const defaultStopTimeout = 10 * time.Second

// Proc is one supervised process tree.
type Proc struct {
	mu   sync.Mutex
	def  protocol.AppDefinition
	cmd  *exec.Cmd
	done chan struct{} // closed when Wait returns

	exitCode int
	exitErr  error
}

// StartProc spawns the definition's command in its own process group/tree.
// stdout/stderr both go to logSink (may be nil).
func StartProc(def protocol.AppDefinition, logSink *os.File) (*Proc, error) {
	if def.Cmd == "" {
		return nil, fmt.Errorf("empty cmd")
	}
	cmd := exec.Command(def.Cmd, def.Args...)
	cmd.Dir = def.Cwd
	cmd.Env = os.Environ()
	for k, v := range def.Env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	if logSink != nil {
		cmd.Stdout = logSink
		cmd.Stderr = logSink
	}
	configureSysProc(cmd)
	if err := cmd.Start(); err != nil {
		return nil, err
	}

	p := &Proc{def: def, cmd: cmd, done: make(chan struct{})}
	go func() {
		err := cmd.Wait()
		p.mu.Lock()
		p.exitErr = err
		p.exitCode = cmd.ProcessState.ExitCode()
		p.mu.Unlock()
		close(p.done)
	}()
	return p, nil
}

// PID returns the root process id.
func (p *Proc) PID() int { return p.cmd.Process.Pid }

// Done returns a channel closed when the root process exits.
func (p *Proc) Done() <-chan struct{} { return p.done }

// ExitCode returns the exit code once Done is closed.
func (p *Proc) ExitCode() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.exitCode
}

// Alive reports whether any process in the tree is still running.
func (p *Proc) Alive() bool {
	select {
	case <-p.done:
		// Root exited; grandchildren may still linger in the group.
		return processGroupAlive(p.PID())
	default:
		return true
	}
}

// Stop performs graceful shutdown of the whole tree: the definition's stop
// signal (default SIGTERM) to the process group, then SIGKILL escalation
// after the stop timeout. It returns once the entire tree is gone.
func (p *Proc) Stop() error {
	timeout := defaultStopTimeout
	sigName := ""
	if s := p.def.Stop; s != nil {
		if s.TimeoutS > 0 {
			timeout = time.Duration(s.TimeoutS) * time.Second
		}
		sigName = s.Signal
	}

	pid := p.PID()
	if err := signalTree(pid, stopSignal(sigName)); err != nil {
		// Group may already be gone.
		if !processGroupAlive(pid) {
			return nil
		}
		return err
	}
	if waitTreeGone(pid, timeout) {
		return nil
	}
	// Escalate.
	if err := killTree(pid); err != nil && processGroupAlive(pid) {
		return fmt.Errorf("SIGKILL escalation failed: %w", err)
	}
	if !waitTreeGone(pid, 5*time.Second) {
		return fmt.Errorf("process group %d survived SIGKILL", pid)
	}
	return nil
}
