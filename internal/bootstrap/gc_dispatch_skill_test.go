package bootstrap

import (
	"io/fs"
	"strings"
	"testing"
)

// gcDispatchSkillPath is the embedded path to the core pack's dispatch
// doctrine skill, materialized into every role's skill directory (Mayor,
// crew, dog, refinery, witness, ...) in every rig via .gc/system/packs.
const gcDispatchSkillPath = "packs/core/skills/gc-dispatch/SKILL.md"

// TestGCDispatchSkillWarnsAboutDedicatedPools is the regression fence for
// ga-rq9sqe (follow-up to ga-68cijh). The skill taught "<rig>/polecat" as
// the dispatch pattern in multiple examples with no mention that a rig
// running its own dedicated pool (e.g. wa-worker, ps-worker) instead of
// the generic polecat role must not be routed there: the pattern still
// resolves (gc sling accepts it) but routes to nothing spawned to claim
// it, and the bead sits routed-but-dead until a watchdog notices. Any
// role composing a sling command by reading this skill — not just the
// Mayor — could reproduce the exact dead-route bug ga-68cijh fixed in the
// Mayor's own prompt template.
func TestGCDispatchSkillWarnsAboutDedicatedPools(t *testing.T) {
	data, err := fs.ReadFile(bootstrapAssets, gcDispatchSkillPath)
	if err != nil {
		t.Fatalf("read %s: %v", gcDispatchSkillPath, err)
	}
	body := string(data)

	t.Run("documents_dedicated_pool_exception", func(t *testing.T) {
		if !strings.Contains(body, "dedicated pool") {
			t.Errorf("%s does not mention the dedicated-pool exception "+
				"(e.g. wa-worker, ps-worker) anywhere. A rig running its "+
				"own named pool instead of the generic polecat role must "+
				"not be routed via the generic <rig>/polecat pattern. "+
				"See ga-68cijh, ga-rq9sqe.", gcDispatchSkillPath)
		}
	})

	t.Run("tells_reader_to_check_actual_pool", func(t *testing.T) {
		if !strings.Contains(body, "rig's actual pool") {
			t.Errorf("%s does not tell the reader to check the rig's "+
				"actual pool (e.g. via `gc session list`) before "+
				"defaulting to the generic <rig>/polecat pattern.",
				gcDispatchSkillPath)
		}
	})

	t.Run("frontend_polecat_example_has_caveat", func(t *testing.T) {
		idx := strings.Index(body, "frontend/polecat fe-xxx")
		if idx == -1 {
			t.Fatalf("%s no longer contains the `frontend/polecat fe-xxx` "+
				"example this test targets; update the test to match the "+
				"current example.", gcDispatchSkillPath)
		}
		if !strings.Contains(body[idx:], "generic polecat role") {
			t.Errorf("%s's frontend/polecat example carries no caveat "+
				"about the generic-polecat-role assumption anywhere after "+
				"it in the file.", gcDispatchSkillPath)
		}
	})

	t.Run("review_target_example_has_caveat", func(t *testing.T) {
		idx := strings.Index(body, "review_target=<rig>/polecat")
		if idx == -1 {
			t.Fatalf("%s no longer contains the `review_target=<rig>/polecat` "+
				"example this test targets; update the test to match the "+
				"current example.", gcDispatchSkillPath)
		}
		if !strings.Contains(body[idx:], "real work-review target") {
			t.Errorf("%s's review_target example carries no caveat "+
				"clarifying it must be the rig's real work-review target, "+
				"not a blind <rig>/polecat guess.", gcDispatchSkillPath)
		}
	})
}
