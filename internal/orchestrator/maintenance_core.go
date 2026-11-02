package orchestrator

import "time"

type expirySnapshot struct {
	phase             Phase
	expires, activity time.Time
	idleSeconds       int64
}

// expiryReason gives absolute TTL precedence over idle expiry.
func expiryReason(now time.Time, value expirySnapshot) string {
	if value.phase != Running {
		return ""
	}
	if !now.Before(value.expires) {
		return "ttl"
	}
	if value.idleSeconds > 0 && !now.Before(value.activity.Add(time.Duration(value.idleSeconds)*time.Second)) {
		return "idle"
	}
	return ""
}

type reconcileAction uint8

const (
	keepInstance reconcileAction = iota
	removeOrphan
	markLost
	resumeDestroy
	removeInterrupted
)

func reconcileDecision(phase Phase, present, interrupted bool) reconcileAction {
	if terminal(phase) {
		return removeOrphan
	}
	if phase == Destroying {
		return resumeDestroy
	}
	if phase == Creating || !present {
		return markLost
	}
	if interrupted {
		return removeInterrupted
	}
	return keepInstance
}
