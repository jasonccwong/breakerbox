// Package protocol defines the wire contract between the BreakerBox hub and
// agents. Both binaries compile against this module; the envelope and message
// types here are the compatibility surface, pinned by golden-file tests.
//
// Versioning rules (v1): additive optional fields only. Unknown message types
// are logged and ignored by both sides. The hub advertises its minimum
// supported protocol version in HelloAck and closes with a typed error if the
// agent is too old.
package protocol

import "encoding/json"

// Version is the current protocol version, sent in every envelope.
const Version = 1

// Envelope wraps every frame on the agent<->hub WebSocket.
type Envelope struct {
	V  int             `json:"v"`            // protocol version
	T  string          `json:"t"`            // message type
	ID string          `json:"id,omitempty"` // ULID, set on messages that expect a response
	TS int64           `json:"ts"`           // unix millis at send time
	D  json.RawMessage `json:"d,omitempty"`  // payload, type determined by T
}

// Message types: agent -> hub.
const (
	TypeHello           = "hello"
	TypeMetrics         = "metrics"
	TypeAppEvent        = "app_event"
	TypeCmdAck          = "cmd_ack"
	TypeCmdResult       = "cmd_result"
	TypeLogChunk        = "log_chunk"
	TypeTokenUsageBatch = "token_usage_batch"
	TypeApprovalEvent   = "approval_event"
	TypeAppRegister     = "app_register"
)

// Message types: hub -> agent.
const (
	TypeHelloAck  = "hello_ack"
	TypeAppSync   = "app_sync"
	TypeCmd       = "cmd"
	TypeLogFollow = "log_follow"
	TypeLogCancel = "log_cancel"
	TypePing      = "ping"
)

// Hello is sent by the agent immediately after the WebSocket opens.
type Hello struct {
	AgentVersion    string               `json:"agent_version"`
	ProtocolVersion int                  `json:"protocol_version"`
	OS              string               `json:"os"`   // runtime.GOOS
	Arch            string               `json:"arch"` // runtime.GOARCH
	Hostname        string               `json:"hostname"`
	Capabilities    []string             `json:"capabilities"` // e.g. "docker", "tokenwatch"
	Apps            map[string]AppDigest `json:"apps"`         // app ID -> local state digest
}

// AppDigest summarizes the agent's local view of one app, used by the hub to
// reconcile state on every (re)connect.
type AppDigest struct {
	Status         AppStatus `json:"status"`
	DefinitionHash string    `json:"definition_hash"`
	Approval       Approval  `json:"approval"`
	PID            int       `json:"pid,omitempty"`
}

// HelloAck is the hub's reply to Hello. A full AppSync follows immediately.
type HelloAck struct {
	ServerTimeMS         int64 `json:"server_time_ms"`
	MinSupportedProtocol int   `json:"min_supported_protocol"`
}

// AppSync carries the hub's authoritative app definitions and desired states.
// It is always full-state and idempotent; there is no diff protocol in v1.
type AppSync struct {
	Apps []AppSpec `json:"apps"`
}

// AppSpec is one app as the hub knows it.
type AppSpec struct {
	ID             string       `json:"id"`
	Definition     AppDefinition `json:"definition"`
	DefinitionHash string       `json:"definition_hash"`
	DesiredState   DesiredState `json:"desired_state"`
}

// Cmd instructs the agent to run one verb against one app. The agent refuses
// if the hash does not match its approved definition or the app is unapproved.
type Cmd struct {
	CmdID          string `json:"cmd_id"`
	AppID          string `json:"app_id"`
	Verb           Verb   `json:"verb"`
	DefinitionHash string `json:"definition_hash"`
}

// CmdAck acknowledges receipt of a Cmd before execution starts.
type CmdAck struct {
	CmdID string `json:"cmd_id"`
}

// CmdResult reports the outcome of a Cmd.
type CmdResult struct {
	CmdID  string `json:"cmd_id"`
	OK     bool   `json:"ok"`
	Error  string `json:"error,omitempty"`
	Detail string `json:"detail,omitempty"`
}

// AppEvent reports an app status transition, sent immediately on change.
type AppEvent struct {
	AppID        string    `json:"app_id"`
	Status       AppStatus `json:"status"`
	PID          int       `json:"pid,omitempty"`
	ExitCode     *int      `json:"exit_code,omitempty"`
	RestartCount int       `json:"restart_count,omitempty"`
	Ports        []Port    `json:"ports,omitempty"`
}

// Port is one listening socket owned by a supervised app.
type Port struct {
	Proto string `json:"proto"` // "tcp" | "udp"
	Port  int    `json:"port"`
}

// MetricsBatch carries one collection cycle (or several coalesced cycles when
// the agent was buffering offline).
type MetricsBatch struct {
	Host []HostSample `json:"host,omitempty"`
	Apps []AppSample  `json:"apps,omitempty"`
}

// HostSample is one host-level sample.
type HostSample struct {
	TS      int64   `json:"ts"` // unix millis
	CPUPct  float64 `json:"cpu_pct"`
	MemPct  float64 `json:"mem_pct"`
	MemUsed uint64  `json:"mem_used"` // bytes
	DiskPct float64 `json:"disk_pct"`
	NetSent uint64  `json:"net_sent"` // cumulative bytes
	NetRecv uint64  `json:"net_recv"` // cumulative bytes
}

// AppSample is one per-app sample (process tree rollup).
type AppSample struct {
	TS     int64   `json:"ts"`
	AppID  string  `json:"app_id"`
	CPUPct float64 `json:"cpu_pct"`
	MemRSS uint64  `json:"mem_rss"` // bytes
	Ports  []Port  `json:"ports,omitempty"`
}

// LogFollow asks the agent to start streaming an app's log.
type LogFollow struct {
	StreamID string `json:"stream_id"`
	AppID    string `json:"app_id"`
	TailN    int    `json:"tail_n"`
}

// LogCancel stops a log stream.
type LogCancel struct {
	StreamID string `json:"stream_id"`
}

// LogChunk is one batch of log lines for an active stream.
type LogChunk struct {
	StreamID string   `json:"stream_id"`
	Lines    []string `json:"lines"`
	EOF      bool     `json:"eof,omitempty"`
}

// TokenUsageBatch carries token usage rows harvested by the agent.
type TokenUsageBatch struct {
	Rows []TokenUsageRow `json:"rows"`
}

// TokenUsageRow is one deduplicatable unit of LLM usage.
type TokenUsageRow struct {
	DedupKey            string `json:"dedup_key"` // e.g. "<message.id>:<requestId>"
	AppID               string `json:"app_id,omitempty"` // empty = unmatched, attribute to system
	Source              string `json:"source"`           // "claude_code" | "codex" | "runtime_proxy"
	Model               string `json:"model"`
	InputTokens         int64  `json:"input_tokens"`
	OutputTokens        int64  `json:"output_tokens"`
	CacheCreationTokens int64  `json:"cache_creation_tokens"`
	CacheReadTokens     int64  `json:"cache_read_tokens"`
	SessionID           string `json:"session_id,omitempty"`
	OccurredAtMS        int64  `json:"occurred_at_ms"`
}

// ApprovalEvent reports a host-side approval state change for an app.
type ApprovalEvent struct {
	AppID          string   `json:"app_id"`
	Approval       Approval `json:"approval"`
	DefinitionHash string   `json:"definition_hash"`
}

// AppRegister asks the hub to create an app record for a definition imported
// on the host (`breakerbox-agent apps import`). Because the import happened
// on the machine itself, the definition arrives pre-approved. The hub replies
// with a fresh AppSync carrying the assigned app ID.
type AppRegister struct {
	Definition AppDefinition `json:"definition"`
	// LocalRef lets the agent correlate the AppSync entry with its spooled
	// import (matched by definition hash).
	LocalRef string `json:"local_ref,omitempty"`
}
