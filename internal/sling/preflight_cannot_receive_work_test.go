package sling

import (
	"context"
	"errors"
	"testing"

	"github.com/gastownhall/gascity/internal/agent"
	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/runtime"
)

func boolPtr(v bool) *bool { return &v }

// attachedProviderCity returns a minimal city config with two providers:
// "attached" (requires_attached_session = true, e.g. an interactive claude
// worker) and "headless" (requires_attached_session = false, e.g.
// claude-headless). Neither provider's Command needs to exist on PATH —
// preflight's capability check resolves providers with a permissive
// lookPath (see alwaysFoundLookPath) since it only cares what the config
// declares.
func attachedProviderCity() *config.City {
	return &config.City{
		Workspace: config.Workspace{Name: "test-city"},
		Providers: map[string]config.ProviderSpec{
			"attached": {
				Command:                 "attached-cmd",
				RequiresAttachedSession: boolPtr(true),
			},
			"headless": {
				Command:                 "headless-cmd",
				RequiresAttachedSession: boolPtr(false),
			},
		},
	}
}

// singleSessionCapabilityTestAgent builds a single-session (non-pool) agent targeting the
// named provider, matching the MaxActiveSessions=1/no-Min/no-ScaleCheck
// shape the existing suspended/pool-empty/implicit-target tests already use
// for IsMultiSessionAgent() == false.
func singleSessionCapabilityTestAgent(name, provider string) config.Agent {
	return config.Agent{
		Name:              name,
		Provider:          provider,
		MaxActiveSessions: intPtr(1),
	}
}

func TestPreflightCannotReceiveWork_HardFailsWhenNoLiveSessionAndRequiresAttached(t *testing.T) {
	cfg := attachedProviderCity()
	a := singleSessionCapabilityTestAgent("solo-claude", "attached")
	deps := testDeps(cfg, runtime.NewFake(), newFakeRunner().run)
	deps.Store = seededStore("BL-1")

	result, err := DoSling(testOpts(a, "BL-1"), deps, nil)

	var wantErr *CannotReceiveWorkError
	if !errors.As(err, &wantErr) {
		t.Fatalf("DoSling error = %v, want *CannotReceiveWorkError", err)
	}
	if !result.CannotReceiveWork {
		t.Error("expected result.CannotReceiveWork = true")
	}
	if wantErr.Target != a.QualifiedName() {
		t.Errorf("CannotReceiveWorkError.Target = %q, want %q", wantErr.Target, a.QualifiedName())
	}
}

func TestPreflightCannotReceiveWork_SkipsWhenLiveSessionExists(t *testing.T) {
	cfg := attachedProviderCity()
	a := singleSessionCapabilityTestAgent("solo-claude", "attached")
	sp := runtime.NewFake()
	deps := testDeps(cfg, sp, newFakeRunner().run)
	deps.Store = seededStore("BL-1")

	sessionName := agent.SessionNameFor(deps.CityName, a.QualifiedName(), cfg.Workspace.SessionTemplate)
	if err := sp.Start(context.Background(), sessionName, runtime.Config{}); err != nil {
		t.Fatalf("sp.Start: %v", err)
	}

	result, err := DoSling(testOpts(a, "BL-1"), deps, nil)
	if err != nil {
		t.Fatalf("DoSling error = %v, want nil (live session should skip the hard-fail check)", err)
	}
	if result.CannotReceiveWork {
		t.Error("expected result.CannotReceiveWork = false when a live session exists")
	}
}

func TestPreflightCannotReceiveWork_SkipsWhenProviderCanSpawnUnattended(t *testing.T) {
	cfg := attachedProviderCity()
	a := singleSessionCapabilityTestAgent("headless-worker", "headless")
	deps := testDeps(cfg, runtime.NewFake(), newFakeRunner().run)
	deps.Store = seededStore("BL-1")

	result, err := DoSling(testOpts(a, "BL-1"), deps, nil)
	if err != nil {
		t.Fatalf("DoSling error = %v, want nil (headless provider can spawn unattended)", err)
	}
	if result.CannotReceiveWork {
		t.Error("expected result.CannotReceiveWork = false for a provider that can spawn unattended")
	}
}

func TestPreflightCannotReceiveWork_SkipsForMultiSessionAgent(t *testing.T) {
	cfg := attachedProviderCity()
	a := config.Agent{
		Name:              "claude-pool",
		Dir:               "whatsapp_automation",
		Provider:          "attached",
		MaxActiveSessions: intPtr(3), // pool: SupportsInstanceExpansion() == true
	}
	deps := testDeps(cfg, runtime.NewFake(), newFakeRunner().run)
	deps.Store = seededStore("BL-1")

	result, err := DoSling(testOpts(a, "BL-1"), deps, nil)
	if err != nil {
		t.Fatalf("DoSling error = %v, want nil (multi-session/pool targets are out of scope for this check)", err)
	}
	if result.CannotReceiveWork {
		t.Error("expected result.CannotReceiveWork = false for a multi-session/pool agent")
	}
}

func TestPreflightCannotReceiveWork_SkipsWhenProviderUnresolved(t *testing.T) {
	// Mirrors the shape of the pre-existing TestDoSlingSuspendedAgentWarns /
	// TestDoSlingPoolEmptyWarnsOnFailure fixtures: a bare cfg with no
	// [providers.*] entries at all. Provider resolution fails, so the new
	// check must stay silent (conservative-by-default) rather than error.
	cfg := &config.City{Workspace: config.Workspace{Name: "test-city"}}
	a := singleSessionCapabilityTestAgent("mayor", "")
	deps := testDeps(cfg, runtime.NewFake(), newFakeRunner().run)
	deps.Store = seededStore("BL-1")

	result, err := DoSling(testOpts(a, "BL-1"), deps, nil)
	if err != nil {
		t.Fatalf("DoSling error = %v, want nil (unresolvable provider must not hard-fail)", err)
	}
	if result.CannotReceiveWork {
		t.Error("expected result.CannotReceiveWork = false when the provider cannot be resolved")
	}
}

func TestPreflightCannotReceiveWork_ForceBypasses(t *testing.T) {
	cfg := attachedProviderCity()
	a := singleSessionCapabilityTestAgent("solo-claude", "attached")
	deps := testDeps(cfg, runtime.NewFake(), newFakeRunner().run)
	deps.Store = seededStore("BL-1")

	opts := testOpts(a, "BL-1")
	opts.Force = true
	result, err := DoSling(opts, deps, nil)
	if err != nil {
		t.Fatalf("DoSling error = %v, want nil (--force bypasses the hard-fail check)", err)
	}
	if result.CannotReceiveWork {
		t.Error("expected result.CannotReceiveWork = false under --force")
	}
}

// TestPreflightCannotReceiveWork_IdempotentReSlingDoesNotHardFail verifies
// the check is placed AFTER the idempotency short-circuit in preflight: a
// bead already routed to this exact target must not start hard-failing
// merely because the target's session happens to be down right now — that
// would make routine defensive re-slinging brittle in exactly the case
// it's meant to be a safe no-op.
func TestPreflightCannotReceiveWork_IdempotentReSlingDoesNotHardFail(t *testing.T) {
	cfg := attachedProviderCity()
	a := singleSessionCapabilityTestAgent("solo-claude", "attached")
	deps := testDeps(cfg, runtime.NewFake(), newFakeRunner().run)
	deps.Store = seededStore("BL-1")
	if err := deps.Store.SetMetadata("BL-1", "gc.routed_to", a.QualifiedName()); err != nil {
		t.Fatalf("seeding gc.routed_to: %v", err)
	}

	// NoConvoy: true — without it, CheckBeadStateWithOptions treats an
	// already-routed bead with no live tracking convoy as needing recovery
	// (needsConvoyRecovery), not as idempotent, and legitimately re-attempts
	// routing. That's correct existing behavior, not what this test is
	// about; NoConvoy isolates the idempotency path itself.
	opts := testOpts(a, "BL-1")
	opts.NoConvoy = true
	// The idempotency check needs a non-nil querier to see the seeded
	// gc.routed_to metadata (DoSling's querier param is separate from
	// deps.Store — nil short-circuits CheckBeadStateWithOptions to "not
	// idempotent" regardless of store state).
	result, err := DoSling(opts, deps, deps.Store)
	if err != nil {
		t.Fatalf("DoSling error = %v, want nil (idempotent re-sling must not hard-fail)", err)
	}
	if !result.Idempotent {
		t.Fatal("expected result.Idempotent = true (test setup didn't reach the idempotent path)")
	}
	if result.CannotReceiveWork {
		t.Error("expected result.CannotReceiveWork = false on the idempotent short-circuit path")
	}
}
