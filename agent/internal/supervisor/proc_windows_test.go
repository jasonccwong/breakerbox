//go:build windows

package supervisor

// Windows counterparts of the unix spike tests: prove the Job Object kills
// the whole tree (including grandchildren) and that lifecycle observation
// works. Runs on the windows CI runner.

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	gops "github.com/shirou/gopsutil/v4/process"

	"github.com/breakerbox/breakerbox/pkg/protocol"
)

var testappBin string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "bb-supervisor-test")
	if err != nil {
		panic(err)
	}
	testappBin = filepath.Join(dir, "testapp.exe")
	build := exec.Command("go", "build", "-o", testappBin, "github.com/breakerbox/breakerbox/cmd/testapp")
	build.Dir = "../../.." // workspace root so go.work resolves the module
	if out, err := build.CombinedOutput(); err != nil {
		panic("build testapp: " + err.Error() + "\n" + string(out))
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

// pidAlive probes one PID.
func pidAlive(pid int) bool {
	exists, err := gops.PidExists(int32(pid))
	return err == nil && exists
}

// childPIDs lists direct+indirect children of the root.
func childPIDs(pid int) []int {
	p, err := gops.NewProcess(int32(pid))
	if err != nil {
		return nil
	}
	kids, err := p.Children()
	if err != nil {
		return nil
	}
	out := make([]int, 0, len(kids))
	for _, k := range kids {
		out = append(out, int(k.Pid))
	}
	return out
}

func waitForGrandchild(t *testing.T, pid int) []int {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if kids := childPIDs(pid); len(kids) >= 1 {
			return kids
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("grandchild never appeared")
	return nil
}

func TestJobObjectKillsWholeTree(t *testing.T) {
	def := protocol.AppDefinition{
		SchemaVersion: 1, Name: "tree", Kind: protocol.KindProcess,
		Cmd: testappBin, Args: []string{"-spawn-child"},
	}
	p, err := StartProc(def, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	root := p.PID()
	if _, ok := jobFor(root); !ok {
		t.Fatal("no job object registered for supervised process")
	}
	kids := waitForGrandchild(t, root)

	if err := p.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if !pidAlive(root) {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if pidAlive(root) {
		t.Errorf("root pid %d survived Stop", root)
	}
	for _, kid := range kids {
		if pidAlive(kid) {
			t.Errorf("grandchild pid %d escaped the job object", kid)
		}
	}
	if _, ok := jobFor(root); ok {
		t.Error("job handle leaked after tree death")
	}
}

func TestCleanExitObservedWindows(t *testing.T) {
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
	case <-time.After(10 * time.Second):
		t.Fatal("process did not exit")
	}
}

func TestLogSinkWindows(t *testing.T) {
	logFile, err := os.CreateTemp("", "bb-log")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(logFile.Name())
	def := protocol.AppDefinition{
		SchemaVersion: 1, Name: "logtest", Kind: protocol.KindProcess,
		Cmd: testappBin, Args: []string{"-exit-after", "100ms"},
	}
	p, err := StartProc(def, logFile, nil)
	if err != nil {
		t.Fatal(err)
	}
	<-p.Done()
	out, _ := os.ReadFile(logFile.Name())
	if !strings.Contains(string(out), "testapp started") {
		t.Errorf("log sink did not capture output; got: %q", out)
	}
}
