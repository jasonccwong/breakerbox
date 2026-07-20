// Package dockerapp manages existing Docker containers and Compose projects
// behind the same lifecycle the supervisor provides for native processes.
// It speaks the Docker Engine REST API directly over the local socket — no
// SDK dependency — because the agent must stay a small static binary. Only
// lifecycle verbs for containers the user already created are exposed;
// BreakerBox never builds images or creates containers (non-goal).
package dockerapp

import (
	"bufio"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

// apiVersion is a floor every engine since 2019 supports.
const apiVersion = "v1.40"

// Client talks to the local Docker engine.
type Client struct {
	http *http.Client
	// httpStream shares the socket transport but has no global timeout;
	// streaming endpoints (log follow) are bounded by request context instead.
	httpStream *http.Client
}

// socketPath returns the engine socket, honoring DOCKER_HOST for unix:// only.
func socketPath() string {
	if h := os.Getenv("DOCKER_HOST"); strings.HasPrefix(h, "unix://") {
		return strings.TrimPrefix(h, "unix://")
	}
	return "/var/run/docker.sock"
}

// New returns a client, or an error when no engine socket is reachable.
// Callers treat the error as "docker capability absent", not fatal.
func New() (*Client, error) {
	sock := socketPath()
	if _, err := os.Stat(sock); err != nil {
		return nil, fmt.Errorf("docker socket not found at %s", sock)
	}
	transport := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", sock)
		},
	}
	c := &Client{
		http: &http.Client{
			Transport: transport,
			Timeout:   65 * time.Second, // above the longest stop timeout we send
		},
		httpStream: &http.Client{Transport: transport},
	}
	if err := c.Ping(); err != nil {
		return nil, err
	}
	return c, nil
}

func (c *Client) get(path string, out any) error {
	resp, err := c.http.Get("http://docker/" + apiVersion + path)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("docker API %s: %s: %s", path, resp.Status, strings.TrimSpace(string(b)))
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func (c *Client) post(path string) error {
	resp, err := c.http.Post("http://docker/"+apiVersion+path, "application/json", nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	// 204 = done, 304 = already in desired state (start on running, etc.)
	if resp.StatusCode >= 300 && resp.StatusCode != 304 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("docker API %s: %s: %s", path, resp.Status, strings.TrimSpace(string(b)))
	}
	return nil
}

// Ping verifies the engine is reachable.
func (c *Client) Ping() error { return c.get("/_ping", nil) }

// newRequestWithContext builds a GET whose lifetime follows ctx (used by the
// streaming log follower; the client's global timeout would kill it).
func newRequestWithContext(ctx context.Context, rawURL string) (*http.Request, error) {
	return http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
}

// ContainerState is the subset of inspect output the daemon needs.
type ContainerState struct {
	ID       string
	Name     string
	Running  bool
	PID      int
	ExitCode int
}

// Inspect fetches current state for a container (ID or name).
func (c *Client) Inspect(id string) (ContainerState, error) {
	var raw struct {
		ID    string `json:"Id"`
		Name  string `json:"Name"`
		State struct {
			Running  bool `json:"Running"`
			Pid      int  `json:"Pid"`
			ExitCode int  `json:"ExitCode"`
		} `json:"State"`
	}
	if err := c.get("/containers/"+url.PathEscape(id)+"/json", &raw); err != nil {
		return ContainerState{}, err
	}
	return ContainerState{
		ID: raw.ID, Name: strings.TrimPrefix(raw.Name, "/"),
		Running: raw.State.Running, PID: raw.State.Pid, ExitCode: raw.State.ExitCode,
	}, nil
}

// Start starts an existing container. Idempotent.
func (c *Client) Start(id string) error {
	return c.post("/containers/" + url.PathEscape(id) + "/start")
}

// Stop gracefully stops a container (engine sends the image's StopSignal,
// then SIGKILL after timeoutS). Idempotent.
func (c *Client) Stop(id string, timeoutS int) error {
	if timeoutS <= 0 {
		timeoutS = 10
	}
	return c.post("/containers/" + url.PathEscape(id) + "/stop?t=" + strconv.Itoa(timeoutS))
}

// Restart bounces a container with the same grace semantics as Stop.
func (c *Client) Restart(id string, timeoutS int) error {
	if timeoutS <= 0 {
		timeoutS = 10
	}
	return c.post("/containers/" + url.PathEscape(id) + "/restart?t=" + strconv.Itoa(timeoutS))
}

// StatsSample is one CPU/memory reading for a container.
type StatsSample struct {
	CPUPct float64
	MemRSS uint64
}

// Stats takes a one-shot stats sample. The engine fills precpu_stats on
// one-shot reads, so a CPU delta is available without streaming.
func (c *Client) Stats(id string) (StatsSample, error) {
	var raw struct {
		CPUStats struct {
			CPUUsage struct {
				Total uint64 `json:"total_usage"`
			} `json:"cpu_usage"`
			SystemUsage uint64 `json:"system_cpu_usage"`
			OnlineCPUs  int    `json:"online_cpus"`
		} `json:"cpu_stats"`
		PreCPUStats struct {
			CPUUsage struct {
				Total uint64 `json:"total_usage"`
			} `json:"cpu_usage"`
			SystemUsage uint64 `json:"system_cpu_usage"`
		} `json:"precpu_stats"`
		MemoryStats struct {
			Usage uint64            `json:"usage"`
			Stats map[string]uint64 `json:"stats"`
		} `json:"memory_stats"`
	}
	if err := c.get("/containers/"+url.PathEscape(id)+"/stats?stream=false&one-shot=false", &raw); err != nil {
		return StatsSample{}, err
	}
	s := StatsSample{}
	cpuDelta := float64(raw.CPUStats.CPUUsage.Total) - float64(raw.PreCPUStats.CPUUsage.Total)
	sysDelta := float64(raw.CPUStats.SystemUsage) - float64(raw.PreCPUStats.SystemUsage)
	if cpuDelta > 0 && sysDelta > 0 {
		n := raw.CPUStats.OnlineCPUs
		if n == 0 {
			n = 1
		}
		s.CPUPct = cpuDelta / sysDelta * float64(n) * 100
	}
	// cgroup v2 reports inactive file cache under "inactive_file"; subtracting
	// it approximates the RSS figure docker stats shows.
	s.MemRSS = raw.MemoryStats.Usage - raw.MemoryStats.Stats["inactive_file"]
	return s, nil
}

// Logs returns up to tail lines of a container's output. Non-TTY containers
// multiplex stdout/stderr with an 8-byte frame header, which is stripped.
func (c *Client) Logs(id string, tail int) ([]string, error) {
	if tail <= 0 {
		tail = 200
	}
	path := "/containers/" + url.PathEscape(id) + "/logs?stdout=1&stderr=1&tail=" + strconv.Itoa(tail)
	resp, err := c.http.Get("http://docker/" + apiVersion + path)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("docker logs: %s: %s", resp.Status, strings.TrimSpace(string(b)))
	}
	// TTY containers emit a raw stream; multiplexed streams start each frame
	// with {STREAM_TYPE, 0, 0, 0, SIZE_BE32}. Sniff the first byte.
	br := bufio.NewReader(resp.Body)
	first, err := br.Peek(1)
	if err != nil {
		return nil, nil // empty log
	}
	var lines []string
	if first[0] > 2 { // printable char: raw TTY stream
		sc := bufio.NewScanner(br)
		sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for sc.Scan() {
			lines = append(lines, sc.Text())
		}
		return lines, nil
	}
	var pending strings.Builder
	hdr := make([]byte, 8)
	for {
		if _, err := io.ReadFull(br, hdr); err != nil {
			break
		}
		size := binary.BigEndian.Uint32(hdr[4:])
		frame := make([]byte, size)
		if _, err := io.ReadFull(br, frame); err != nil {
			break
		}
		pending.Write(frame)
		for {
			s := pending.String()
			i := strings.IndexByte(s, '\n')
			if i < 0 {
				break
			}
			lines = append(lines, strings.TrimSuffix(s[:i], "\r"))
			pending.Reset()
			pending.WriteString(s[i+1:])
		}
	}
	if rest := pending.String(); rest != "" {
		lines = append(lines, rest)
	}
	return lines, nil
}
