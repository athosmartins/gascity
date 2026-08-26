package config

import (
	"testing"
	"time"
)

// TestSessionSpawnStaggerDuration_ResolutionOrder covers ga-ih4ma's
// resolution order for the async session-start spawn stagger: the
// GC_SESSION_SPAWN_STAGGER env var (when a valid, non-negative duration)
// wins over [daemon].session_spawn_stagger in city.toml, which wins over
// DefaultSessionSpawnStagger. Hardcodes the literal env var name (rather
// than referencing the unexported sessionSpawnStaggerEnvVar constant) so
// the test fails if the documented operator-facing name is ever changed
// without updating the doc comment on SessionSpawnStaggerDuration.
func TestSessionSpawnStaggerDuration_ResolutionOrder(t *testing.T) {
	const envVar = "GC_SESSION_SPAWN_STAGGER"

	tests := []struct {
		name       string
		envValue   string // "" means the env var is cleared for this case
		configured string // DaemonConfig.SessionSpawnStagger
		want       time.Duration
	}{
		{
			name:       "unset env and unset config field falls back to built-in default",
			envValue:   "",
			configured: "",
			want:       DefaultSessionSpawnStagger,
		},
		{
			name:       "config field alone overrides the default",
			envValue:   "",
			configured: "9s",
			want:       9 * time.Second,
		},
		{
			name:       "env var overrides a set config field",
			envValue:   "2s",
			configured: "9s",
			want:       2 * time.Second,
		},
		{
			name:       "env var overrides an unset config field",
			envValue:   "750ms",
			configured: "",
			want:       750 * time.Millisecond,
		},
		{
			name:       "explicit zero config field disables staggering",
			envValue:   "",
			configured: "0s",
			want:       0,
		},
		{
			name:       "explicit zero env var disables staggering even over a nonzero config field",
			envValue:   "0",
			configured: "9s",
			want:       0,
		},
		{
			name:       "unparseable config field falls through to the default",
			envValue:   "",
			configured: "not-a-duration",
			want:       DefaultSessionSpawnStagger,
		},
		{
			name:       "unparseable env var falls through to a set config field",
			envValue:   "garbage",
			configured: "9s",
			want:       9 * time.Second,
		},
		{
			name:       "negative config field falls through to the default",
			envValue:   "",
			configured: "-3s",
			want:       DefaultSessionSpawnStagger,
		},
		{
			name:       "negative env var falls through to a set config field",
			envValue:   "-5s",
			configured: "9s",
			want:       9 * time.Second,
		},
		{
			name:       "whitespace-only config field is treated as unset",
			envValue:   "",
			configured: "   ",
			want:       DefaultSessionSpawnStagger,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// t.Setenv clears/restores automatically and forbids
			// t.Parallel() in this subtest, which we don't use here.
			t.Setenv(envVar, tt.envValue)
			d := &DaemonConfig{SessionSpawnStagger: tt.configured}
			got := d.SessionSpawnStaggerDuration()
			if got != tt.want {
				t.Fatalf("SessionSpawnStaggerDuration() = %s, want %s", got, tt.want)
			}
		})
	}
}

// TestSessionSpawnStaggerDuration_NilDaemonConfig documents that a nil
// *DaemonConfig resolves to the env var (if set) or the built-in default
// rather than panicking. Production call sites (sessionSpawnStagger in
// cmd/gc/session_lifecycle_parallel.go) already guard cfg == nil before
// reaching cfg.Daemon, but DaemonConfig methods elsewhere in this file
// (e.g. MaxWakesPerTickOrDefault) assume a non-nil receiver, so this
// method's extra nil check is a deliberate, defensive divergence — this
// test pins that choice.
func TestSessionSpawnStaggerDuration_NilDaemonConfig(t *testing.T) {
	t.Setenv("GC_SESSION_SPAWN_STAGGER", "")
	var d *DaemonConfig
	if got := d.SessionSpawnStaggerDuration(); got != DefaultSessionSpawnStagger {
		t.Fatalf("nil *DaemonConfig: SessionSpawnStaggerDuration() = %s, want %s", got, DefaultSessionSpawnStagger)
	}
}

// TestDefaultSessionSpawnStagger_PositiveAndBelowGateDefault pins two
// properties the doc comment on DefaultSessionSpawnStagger argues for:
// staggering is on by default (nonzero — a silently-disabled safety net
// would defeat ga-ih4ma's purpose), and it deliberately differs from
// quality-gate-dispatcher.sh's 3s GATE_SPAWN_STAGGER_SECS default because
// crew boot cost is a heavier, longer profile than gate-reviewer boot.
// This is not a claim that 4s is uniquely correct, only that it was a
// reasoned choice, not an accidental copy of the gate's default.
func TestDefaultSessionSpawnStagger_PositiveAndBelowGateDefault(t *testing.T) {
	if DefaultSessionSpawnStagger <= 0 {
		t.Fatalf("DefaultSessionSpawnStagger = %s, want > 0 (0 would silently disable ga-ih4ma's fix by default)", DefaultSessionSpawnStagger)
	}
	const gateDefault = 3 * time.Second
	if DefaultSessionSpawnStagger == gateDefault {
		t.Fatalf("DefaultSessionSpawnStagger = %s equals quality-gate-dispatcher.sh's GATE_SPAWN_STAGGER_SECS default; this must be a deliberately-reasoned value for crew boot cost, not a blind copy of the gate's reviewer-boot-tuned default", DefaultSessionSpawnStagger)
	}
}
