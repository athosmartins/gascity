package main

import "testing"

// TestFairPoolSessionCreateShares_CountsWakeKnownIdentityDemand is a
// regression test for ga-815mi: a template whose ONLY create-needing demand
// is a "wake-known-identity" request (Pilot rig-native dispatch assigning a
// work bead to the bare template name before any session bead exists for it)
// must still be counted as a fair-share contender. Before the fix, only
// Tier == "new" counted toward demand, so a template like this never
// appeared in `demands`, fair share never engaged (len(demands) <= 1), and a
// purely self-serve "new"-tier template (e.g. a high-churn dog pool) could
// claim the entire per-tick create budget every single tick regardless of
// processing order — strict starvation, not just an uneven share.
func TestFairPoolSessionCreateShares_CountsWakeKnownIdentityDemand(t *testing.T) {
	states := []PoolDesiredState{
		{
			Template: "gastown.dog",
			Requests: []SessionRequest{
				{Template: "gastown.dog", Tier: "new"},
				{Template: "gastown.dog", Tier: "new"},
				{Template: "gastown.dog", Tier: "new"},
				{Template: "gastown.dog", Tier: "new"},
				{Template: "gastown.dog", Tier: "new"},
			},
		},
		{
			Template: "wa-worker",
			Requests: []SessionRequest{
				{Template: "wa-worker", Tier: "wake-known-identity", WorkBeadID: "wa-1zrn0"},
			},
		},
	}

	shares, spare := fairPoolSessionCreateShares(states, 2, 0)

	if shares == nil {
		t.Fatalf("fairPoolSessionCreateShares returned nil (fair share did not engage): wa-worker's wake-known-identity demand was not counted, so gastown.dog's 5 self-serve requests can claim the entire budget uncontested")
	}
	if shares["wa-worker"] != 1 {
		t.Errorf("wa-worker share = %d, want 1 (its one wake-known-identity request must win a fair-share slot instead of losing every tick to gastown.dog's self-serve churn)", shares["wa-worker"])
	}
	if shares["gastown.dog"] != 1 {
		t.Errorf("gastown.dog share = %d, want 1 (budget=2 split across 2 contending templates)", shares["gastown.dog"])
	}
	if spare != 0 {
		t.Errorf("spare = %d, want 0", spare)
	}
}

// TestFairPoolSessionCreateShares_ResumeStillExcluded guards the other half
// of the fix: a "resume" request always carries a SessionBeadID (it reuses
// an existing live/known session bead), so it must NOT count as fresh-create
// demand. The fix keys on SessionBeadID == "" rather than Tier == "new";
// this test makes sure that widening didn't overshoot into counting
// requests that will never call tryClaimPoolSessionCreate.
func TestFairPoolSessionCreateShares_ResumeStillExcluded(t *testing.T) {
	states := []PoolDesiredState{
		{
			Template: "gastown.dog",
			Requests: []SessionRequest{
				{Template: "gastown.dog", Tier: "new"},
			},
		},
		{
			Template: "wa-worker",
			Requests: []SessionRequest{
				{Template: "wa-worker", Tier: "resume", SessionBeadID: "sess-123"},
			},
		},
	}

	shares, _ := fairPoolSessionCreateShares(states, 2, 0)

	if shares != nil {
		t.Errorf("shares = %v, want nil (only gastown.dog has fresh-create demand; wa-worker's resume request must not register as a contender)", shares)
	}
}
