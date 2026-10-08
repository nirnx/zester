package update

import (
	"testing"
	"time"
)

// TestRolloutController_AdoptionTimers pins the configurable orphan-adoption
// timers (master rollout_stale_after / rollout_resume_interval): zero keeps
// the defaults, and the staleness window is floored at two driver heartbeats
// so a live driver that missed one tick is never adopted.
func TestRolloutController_AdoptionTimers(t *testing.T) {
	r := &RolloutController{}
	if got := r.staleAfter(); got != DefaultRolloutStaleAfter {
		t.Errorf("zero StaleAfter = %v, want default %v", got, DefaultRolloutStaleAfter)
	}
	if got := r.resumeInterval(); got != DefaultRolloutResumeInterval {
		t.Errorf("zero ResumeInterval = %v, want default %v", got, DefaultRolloutResumeInterval)
	}

	r.StaleAfter = 5 * time.Second
	if got, floor := r.staleAfter(), 2*rolloutHeartbeatInterval; got != floor {
		t.Errorf("StaleAfter below the heartbeat floor = %v, want floored %v", got, floor)
	}
	r.StaleAfter = 45 * time.Second
	if got := r.staleAfter(); got != 45*time.Second {
		t.Errorf("StaleAfter above the floor = %v, want 45s", got)
	}

	r.ResumeInterval = 5 * time.Second
	if got := r.resumeInterval(); got != 5*time.Second {
		t.Errorf("ResumeInterval = %v, want 5s", got)
	}
}
