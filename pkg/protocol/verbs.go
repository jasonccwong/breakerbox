package protocol

// Verb is a control action the hub may request against a registered app.
// This is the complete set: there is deliberately no shell, no PTY, and no
// free-form file operation anywhere in the protocol. See SECURITY.md.
type Verb string

const (
	VerbStart   Verb = "start"
	VerbStop    Verb = "stop"
	VerbRestart Verb = "restart"
)

// ValidVerb reports whether v is one of the fixed control verbs.
func ValidVerb(v Verb) bool {
	switch v {
	case VerbStart, VerbStop, VerbRestart:
		return true
	}
	return false
}

// AppStatus is the agent-reported runtime state of an app. The agent is
// authoritative for actual state; the hub is authoritative for desired state.
type AppStatus string

const (
	StatusRunning  AppStatus = "running"
	StatusStopped  AppStatus = "stopped"
	StatusStarting AppStatus = "starting"
	StatusBackoff  AppStatus = "backoff"
	StatusErrored  AppStatus = "errored"
	StatusUnknown  AppStatus = "unknown"
)

// DesiredState is what the user wants the app to be doing.
type DesiredState string

const (
	DesiredRunning DesiredState = "running"
	DesiredStopped DesiredState = "stopped"
)

// Approval is the host-side approval state of an app definition. The agent
// never executes a definition whose hash it has not locally approved.
type Approval string

const (
	ApprovalPending  Approval = "pending"
	ApprovalApproved Approval = "approved"
	ApprovalRejected Approval = "rejected"
)
