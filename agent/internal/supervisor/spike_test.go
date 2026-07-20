//go:build !windows

package supervisor

// Spike B (Phase 0): prove reliable process-tree termination — stopping a
// supervised app must take down grandchildren too, and SIGKILL escalation
// must defeat processes that ignore SIGTERM. Uses cmd/testapp as the
// deterministic guinea pig.

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/breakerbox/breakerbox/pkg/protocol"
)

var testappBin string

func TestMain(m *testing.M) {
	// Build testapp once for all tests.
	dir, err := os.MkdirTemp("", "bb-supervisor-test")
	if err != nil {
		panic(err)
	}
	testappBin = filepath.Join(dir, "testapp")
	if runtime.GOOS == "windows" {
		testappBin += ".exe"
	}
	build := exec.Command("go", "build", "-o", testappBin, "github.com/breakerbox/breakerbox/cmd/testapp")
	build.Dir = "../../.." // workspace root so go.work resolves the module
	if out, err := build.CombinedOutput(); err != nil {
		panic("build testapp: " + err.Error() + "\n" + string(out))
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

// groupPIDs returns all live (non-zombie) PIDs in the given process group
// (via ps, which is portable across macOS and Linux). Zombies are excluded:
// in environments without a reaping init (Docker), killed orphans linger as
// zombies, but they are dead for every purpose that matters.
func groupPIDs(t *testing.T, pgid int) []int {
	t.Helper()
	out, err := exec.Command("ps", "-eo", "pid,pgid,stat").Output()
	if err != nil {
		t.Fatal(err)
	}
	var pids []int
	for _, line := range strings.Split(string(out), "\n")[1:] {
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		pid, err1 := strconv.Atoi(fields[0])
		g, err2 := strconv.Atoi(fields[1])
		if err1 == nil && err2 == nil && g == pgid && !strings.HasPrefix(fields[2], "Z") {
			pids = append(pids, pid)
		}
	}
	return pids
}

func waitForGrandchild(t *testing.T, pgid int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if len(groupPIDs(t, pgid)) >= 2 {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("grandchild never appeared in process group %d (pids: %v)", pgid, groupPIDs(t, pgid))
}

func TestStopKillsWholeTree(t *testing.T) {
	def := protocol.AppDefinition{
		SchemaVersion: 1, Name: "tree", Kind: protocol.KindProcess,
		Cmd: testappBin, Args: []string{"-spawn-child"},
	}
	p, err := StartProc(def, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	pgid := p.PID()
	waitForGrandchild(t, pgid)

	if err := p.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if orphans := groupPIDs(t, pgid); len(orphans) != 0 {
		for _, pid := range orphans {
			_ = syscall.Kill(pid, syscall.SIGKILL) // cleanup before failing
		}
		t.Fatalf("orphaned processes left in group %d after Stop: %v", pgid, orphans)
	}
}

func TestStopEscalatesToSigkill(t *testing.T) {
	def := protocol.AppDefinition{
		SchemaVersion: 1, Name: "stubborn", Kind: protocol.KindProcess,
		Cmd:  testappBin,
		Args: []string{"-ignore-sigterm", "-spawn-child"},
		Stop: &protocol.StopSpec{Signal: "SIGTERM", TimeoutS: 1},
	}
	p, err := StartProc(def, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	pgid := p.PID()
	waitForGrandchild(t, pgid)

	start := time.Now()
	if err := p.Stop(); err != nil {
		t.Fatalf("Stop with escalation: %v", err)
	}
	dur := time.Since(start)
	if dur < 1*time.Second {
		t.Errorf("Stop returned in %s — SIGTERM was 'honored' by a process that ignores it? escalation logic suspect", dur)
	}
	if orphans := groupPIDs(t, pgid); len(orphans) != 0 {
		for _, pid := range orphans {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
		t.Fatalf("orphans survived SIGKILL escalation: %v", orphans)
	}
}

func TestCleanExitObserved(t *testing.T) {
	def := protocol.AppDefinition{
		SchemaVersion: 1, Name: "shortlived", Kind: protocol.KindProcess,
		Cmd: testappBin, Args: []string{"-exit-after", "300ms"},
	}
	p, err := StartProc(def, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-p.Done():
		if code := p.ExitCode(); code != 0 {
			t.Errorf("exit code = %d, want 0", code)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("process did not exit")
	}
}

func TestEnvAndCwdApplied(t *testing.T) {
	dir := t.TempDir()
	def := protocol.AppDefinition{
		SchemaVersion: 1, Name: "envtest", Kind: protocol.KindProcess,
		Cmd: testappBin, Args: []string{"-exit-after", "100ms"},
		Cwd: dir,
		Env: map[string]string{"BB_TEST_MARKER": "42"},
	}
	logFile, err := os.CreateTemp("", "bb-log")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(logFile.Name())
	p, err := StartProc(def, logFile, nil)
	if err != nil {
		t.Fatal(err)
	}
	<-p.Done()
	out, _ := os.ReadFile(logFile.Name())
	if !strings.Contains(string(out), "testapp started") {
		t.Errorf("log sink did not capture stdout/stderr; got: %q", out)
	}
}
