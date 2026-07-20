// Package collector samples host and per-app metrics via gopsutil. Listening
// ports are refreshed on a slower cadence than CPU/memory because the
// per-process socket enumeration is comparatively expensive.
package collector

import (
	"time"

	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/disk"
	"github.com/shirou/gopsutil/v4/mem"
	gnet "github.com/shirou/gopsutil/v4/net"
	"github.com/shirou/gopsutil/v4/process"

	"github.com/breakerbox/breakerbox/pkg/protocol"
)

// portCacheTTL bounds how stale the LISTEN-port map may be.
const portCacheTTL = 60 * time.Second

// Collector samples metrics. Not safe for concurrent use; the daemon calls
// it from a single ticker goroutine.
type Collector struct {
	ports    map[int][]protocol.Port // pid -> listening ports
	portsAt  time.Time
	procs    map[int]*process.Process // cache so CPUPercent deltas work
}

func New() *Collector {
	return &Collector{ports: map[int][]protocol.Port{}, procs: map[int]*process.Process{}}
}

// HostSample collects one host-level sample.
func (c *Collector) HostSample() protocol.HostSample {
	s := protocol.HostSample{TS: time.Now().UnixMilli()}
	// Non-blocking cpu.Percent: delta since previous call (first call = 0).
	if pcts, err := cpu.Percent(0, false); err == nil && len(pcts) > 0 {
		s.CPUPct = pcts[0]
	}
	if vm, err := mem.VirtualMemory(); err == nil {
		s.MemPct = vm.UsedPercent
		s.MemUsed = vm.Used
	}
	if du, err := disk.Usage("/"); err == nil {
		s.DiskPct = du.UsedPercent
	}
	if counters, err := gnet.IOCounters(false); err == nil && len(counters) > 0 {
		s.NetSent = counters[0].BytesSent
		s.NetRecv = counters[0].BytesRecv
	}
	return s
}

// AppSample collects one sample for a supervised app's process tree rooted
// at pid: CPU and RSS are summed over the root and all descendants.
func (c *Collector) AppSample(appID string, pid int) protocol.AppSample {
	s := protocol.AppSample{TS: time.Now().UnixMilli(), AppID: appID}
	for _, p := range c.tree(pid) {
		if pct, err := p.CPUPercent(); err == nil {
			s.CPUPct += pct
		}
		if mi, err := p.MemoryInfo(); err == nil && mi != nil {
			s.MemRSS += mi.RSS
		}
	}
	s.Ports = c.listenPorts(pid)
	return s
}

// tree returns cached process handles for pid and its descendants. Handles
// are cached because gopsutil computes CPUPercent as a delta since the
// previous call on the same handle.
func (c *Collector) tree(pid int) []*process.Process {
	root := c.proc(pid)
	if root == nil {
		return nil
	}
	out := []*process.Process{root}
	if children, err := root.Children(); err == nil {
		for _, ch := range children {
			out = append(out, c.tree(int(ch.Pid))...)
		}
	}
	return out
}

func (c *Collector) proc(pid int) *process.Process {
	if p, ok := c.procs[pid]; ok {
		if running, err := p.IsRunning(); err == nil && running {
			return p
		}
		delete(c.procs, pid)
	}
	p, err := process.NewProcess(int32(pid))
	if err != nil {
		return nil
	}
	c.procs[pid] = p
	// Prime the CPU delta so the next call returns a meaningful value.
	_, _ = p.CPUPercent()
	return p
}

// listenPorts returns the LISTEN ports owned by pid's process tree, from a
// cache refreshed at most once per portCacheTTL.
func (c *Collector) listenPorts(pid int) []protocol.Port {
	if time.Since(c.portsAt) > portCacheTTL {
		c.refreshPorts()
	}
	var out []protocol.Port
	for _, p := range c.tree(pid) {
		out = append(out, c.ports[int(p.Pid)]...)
	}
	return out
}

func (c *Collector) refreshPorts() {
	c.portsAt = time.Now()
	conns, err := gnet.Connections("inet")
	if err != nil {
		return
	}
	fresh := map[int][]protocol.Port{}
	for _, conn := range conns {
		if conn.Status != "LISTEN" || conn.Pid == 0 {
			continue
		}
		proto := "tcp"
		if conn.Type == 2 { // SOCK_DGRAM
			proto = "udp"
		}
		fresh[int(conn.Pid)] = append(fresh[int(conn.Pid)], protocol.Port{Proto: proto, Port: int(conn.Laddr.Port)})
	}
	c.ports = fresh
}
