// Package protocol defines bounded helper metadata shared with the orchestrator.
// Both the helper and this package use only the standard library.
package protocol

// MaxTimeoutSeconds is the hard exec deadline supported by the v0.1 helper.
const MaxTimeoutSeconds = 3600

// TransportExit distinguishes completed commands from helper faults and signals.
// The public exec result always uses Status.ExitCode, never this transport code.
func TransportExit(commandExit int) int {
	if commandExit == 0 {
		return 0
	}
	return 124
}

// Status is emitted only after the controller reaps its complete process tree.
type Status struct {
	ID              string `json:"id"`
	ExitCode        int    `json:"exit_code"`
	TimedOut        bool   `json:"timed_out"`
	Signal          string `json:"signal"`
	Cancelled       bool   `json:"cancelled"`
	CleanupComplete bool   `json:"cleanup_complete"`
}

// Controller identifies one live helper without treating a mutable PID as authority.
type Controller struct {
	ID         string `json:"id"`
	PID        int    `json:"pid"`
	StartTicks uint64 `json:"start_ticks"`
}

// Cancellation is interpreted by the matching controller, never used to signal an arbitrary PID.
type Cancellation struct {
	ID         string `json:"id"`
	PID        int    `json:"pid"`
	StartTicks uint64 `json:"start_ticks"`
	GraceNanos int64  `json:"grace_nanos"`
}
