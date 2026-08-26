package main

import (
	"io"
	"testing"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/config"
)

// TestSetTemplateEnvIdentityIncludesBeadsActor guards against wa-xwa7l: a
// session whose env is stamped through this helper (pool desired-state
// materialization at line ~649, the dependency-floor path at ~1975, pool
// alias restamping at ~2292) must carry an explicit BEADS_ACTOR alongside
// GC_AGENT/GC_ALIAS. Without it, `bd` (run interactively inside that
// session) falls back through its own actor-resolution chain — BEADS_ACTOR
// env, then git config user.name, then $USER — none of which know this
// session's identity, and if the tmux server's own ambient environment
// happens to carry BEADS_ACTOR=controller (set deliberately by `gc
// supervisor run` on itself via defaultSupervisorBeadsActor in
// cmd_supervisor_lifecycle.go, for its own hook-subprocess attribution),
// every bd write from the affected session is silently misattributed to
// actor "controller" instead of the real session identity.
func TestSetTemplateEnvIdentityIncludesBeadsActor(t *testing.T) {
	tp := &TemplateParams{}
	setTemplateEnvIdentity(tp, "batista-wa-gawispekvmub")

	if got, want := tp.Env["GC_AGENT"], "batista-wa-gawispekvmub"; got != want {
		t.Fatalf("GC_AGENT = %q, want %q", got, want)
	}
	if got, want := tp.Env["GC_ALIAS"], "batista-wa-gawispekvmub"; got != want {
		t.Fatalf("GC_ALIAS = %q, want %q", got, want)
	}
	if got, want := tp.Env["BEADS_ACTOR"], "batista-wa-gawispekvmub"; got != want {
		t.Fatalf("BEADS_ACTOR = %q, want %q (setTemplateEnvIdentity must stamp BEADS_ACTOR alongside GC_AGENT/GC_ALIAS, or bd's actor-resolution chain has no way to learn the session identity)", got, want)
	}
}

func TestSetTemplateEnvIdentityNoopOnEmptyIdentity(t *testing.T) {
	tp := &TemplateParams{}
	setTemplateEnvIdentity(tp, "")

	if tp.Env != nil {
		t.Fatalf("Env = %#v, want nil (empty identity must be a no-op, matching the existing GC_AGENT/GC_ALIAS guard)", tp.Env)
	}
}

// TestResolvePreservedConfiguredNamedSessionTemplateIncludesBeadsActor covers
// the sibling half of wa-xwa7l: a PRESERVED (already-running, reconciled in
// place) configured named session — e.g. a persistent crew identity like
// batista-wa — must also get BEADS_ACTOR stamped on its resolved template,
// or a session that has been reconciled through this path (rather than
// freshly created) never has it set at all.
func TestResolvePreservedConfiguredNamedSessionTemplateIncludesBeadsActor(t *testing.T) {
	env := newReconcilerTestEnv()
	env.cfg = &config.City{
		Workspace:     config.Workspace{Name: "test-city"},
		Agents:        []config.Agent{{Name: "worker", StartCommand: "true", MaxActiveSessions: intPtr(2)}},
		NamedSessions: []config.NamedSession{{Template: "worker", Mode: "on_demand"}},
	}
	sessionName := config.NamedSessionRuntimeName(env.cfg.Workspace.Name, env.cfg.Workspace, "worker")
	session := env.createSessionBead(sessionName, "worker")
	env.markSessionActive(&session)
	env.setSessionMetadata(&session, map[string]string{
		namedSessionMetadataKey:      "true",
		namedSessionIdentityMetadata: "worker",
		namedSessionModeMetadata:     "on_demand",
	})

	tp, err := resolvePreservedConfiguredNamedSessionTemplate(".", env.cfg.Workspace.Name, env.cfg, env.sp, env.store, []beads.Bead{session}, session, env.clk, io.Discard)
	if err != nil {
		t.Fatalf("resolve preserved named session: %v", err)
	}
	if got, want := tp.Env["BEADS_ACTOR"], "worker"; got != want {
		t.Errorf("Env[BEADS_ACTOR] = %q, want %q (bd's actor-resolution chain has no other source for this named session's identity)", got, want)
	}
}
