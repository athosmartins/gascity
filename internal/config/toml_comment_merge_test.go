package config

import (
	"strings"
	"testing"
)

func TestSpliceTOMLPreservingComments_CommentSurvivesPureInsert(t *testing.T) {
	base := "a = 1\n"
	fresh := "a = 1\nb = 2\n"
	original := "# note about a\na = 1\n"
	want := "# note about a\na = 1\nb = 2\n"

	got := spliceTOMLPreservingComments([]byte(original), []byte(base), []byte(fresh))
	if got == nil {
		t.Fatalf("spliceTOMLPreservingComments returned nil, want a clean merge")
	}
	if string(got) != want {
		t.Fatalf("got:\n%q\nwant:\n%q", got, want)
	}
}

func TestSpliceTOMLPreservingComments_CommentSurvivesValueChange(t *testing.T) {
	base := "a = 1\n"
	fresh := "a = 2\n"
	original := "# note about a\na = 1\n"
	want := "# note about a\na = 2\n"

	got := spliceTOMLPreservingComments([]byte(original), []byte(base), []byte(fresh))
	if got == nil {
		t.Fatalf("spliceTOMLPreservingComments returned nil, want a clean merge")
	}
	if string(got) != want {
		t.Fatalf("got:\n%q\nwant:\n%q", got, want)
	}
}

func TestSpliceTOMLPreservingComments_TrailingCommentSurvives(t *testing.T) {
	base := "a = 1\n"
	fresh := "a = 1\nb = 2\n"
	original := "a = 1\n# trailing note\n"
	want := "a = 1\nb = 2\n# trailing note\n"

	got := spliceTOMLPreservingComments([]byte(original), []byte(base), []byte(fresh))
	if got == nil {
		t.Fatalf("spliceTOMLPreservingComments returned nil, want a clean merge")
	}
	if string(got) != want {
		t.Fatalf("got:\n%q\nwant:\n%q", got, want)
	}
}

func TestSpliceTOMLPreservingComments_DeletedFieldDropsItsOwnComment(t *testing.T) {
	base := "a = 1\nb = 2\n"
	fresh := "a = 1\n"
	original := "a = 1\n# about b\nb = 2\n"
	want := "a = 1\n"

	got := spliceTOMLPreservingComments([]byte(original), []byte(base), []byte(fresh))
	if got == nil {
		t.Fatalf("spliceTOMLPreservingComments returned nil, want a clean merge")
	}
	if string(got) != want {
		t.Fatalf("got:\n%q\nwant:\n%q", got, want)
	}
}

// TestSpliceTOMLPreservingComments_GuardCommentSurvivesSiblingLineChange
// mirrors the real-world case that motivated this fix (ga-gdjav): a
// multi-line comment warning about two value lines below it, where only ONE
// of the two lines actually changes in this mutation. The comment must stay
// attached above both lines, not just the one that changed.
func TestSpliceTOMLPreservingComments_GuardCommentSurvivesSiblingLineChange(t *testing.T) {
	base := "x = 1\ny = 2\n"
	fresh := "x = 1\ny = 3\n"
	original := "# ga-23z6a eval-window-concurrency-guard: do not hand-edit the two value lines below\nx = 1\ny = 2\n"
	want := "# ga-23z6a eval-window-concurrency-guard: do not hand-edit the two value lines below\nx = 1\ny = 3\n"

	got := spliceTOMLPreservingComments([]byte(original), []byte(base), []byte(fresh))
	if got == nil {
		t.Fatalf("spliceTOMLPreservingComments returned nil, want a clean merge")
	}
	if string(got) != want {
		t.Fatalf("got:\n%q\nwant:\n%q", got, want)
	}
}

// TestSpliceTOMLPreservingComments_DriftedLineGetsFreshValueWithoutCorruption
// covers a line original wrote in a form the encoder wouldn't reproduce byte
// for byte (e.g. hand-formatted). That line must never be preserved
// verbatim (it's not decoration — dropping it, not keeping it, is what
// keeps the result correct); the field's actual value must come from fresh.
func TestSpliceTOMLPreservingComments_DriftedLineGetsFreshValueWithoutCorruption(t *testing.T) {
	base := "a = 1\n"
	fresh := "a = 2\n"
	original := "a = 'one'\n" // formatting drift the encoder wouldn't emit

	got := spliceTOMLPreservingComments([]byte(original), []byte(base), []byte(fresh))
	if got == nil {
		t.Fatalf("spliceTOMLPreservingComments returned nil, want a clean merge (drift is handled locally, not a global bail-out)")
	}
	if string(got) != "a = 2\n" {
		t.Fatalf("got %q, want fresh's value only — the drifted original line must not survive", got)
	}
}

// TestSpliceTOMLPreservingComments_LocalizedDriftDoesNotSinkUnrelatedComments
// is the case that motivated the local (per-line) drift handling over an
// earlier all-or-nothing design: one field (b) is hand-written without a
// zero-value key the encoder always writes explicitly, which would make a
// strict original-must-be-an-exact-subsequence-of-base check fail for the
// WHOLE file. A completely unrelated comment (above a) must still survive.
func TestSpliceTOMLPreservingComments_LocalizedDriftDoesNotSinkUnrelatedComments(t *testing.T) {
	base := "a = 1\nb = 2\nb_extra = 0\n"
	fresh := "a = 9\nb = 2\nb_extra = 0\n"
	original := "# note about a\na = 1\nb = 2\n" // original omits b_extra entirely
	want := "# note about a\na = 9\nb = 2\nb_extra = 0\n"

	got := spliceTOMLPreservingComments([]byte(original), []byte(base), []byte(fresh))
	if got == nil {
		t.Fatalf("spliceTOMLPreservingComments returned nil, want a clean merge")
	}
	if string(got) != want {
		t.Fatalf("got:\n%q\nwant:\n%q", got, want)
	}
}

func TestMarshalForWritePreservingComments_EmptyOriginalUsesFreshMarshal(t *testing.T) {
	cfg := &City{Workspace: Workspace{Name: "test-city"}}
	got, err := cfg.MarshalForWritePreservingComments(nil)
	if err != nil {
		t.Fatalf("MarshalForWritePreservingComments: %v", err)
	}
	want, err := cfg.MarshalForWrite()
	if err != nil {
		t.Fatalf("MarshalForWrite: %v", err)
	}
	if string(got) != string(want) {
		t.Fatalf("got:\n%q\nwant:\n%q", got, want)
	}
}

// TestMarshalForWritePreservingComments_EndToEndThroughRealEncoder exercises
// the full Parse -> mutate -> MarshalForWritePreservingComments pipeline
// through the actual TOML encoder (not hand-built line fixtures), to catch
// any real-world formatting surprise the low-level splice tests above
// wouldn't see.
func TestMarshalForWritePreservingComments_EndToEndThroughRealEncoder(t *testing.T) {
	// Seed by parsing a minimal city.toml and re-marshaling it, so
	// Parse-time defaulting (e.g. [daemon] formula_v2 = true when unset)
	// is already baked into the canonical form. A bare struct literal's
	// MarshalForWrite would NOT carry those defaults (they're injected by
	// Parse, not Marshal), so this would otherwise test the "field the
	// encoder always writes explicitly but original omits" drift path
	// instead of the plain end-to-end path this test means to exercise.
	seedCfg, err := Parse([]byte("[workspace]\nname = \"test-city\"\n"))
	if err != nil {
		t.Fatalf("seeding: %v", err)
	}
	canonical, err := seedCfg.MarshalForWrite()
	if err != nil {
		t.Fatalf("seeding canonical form: %v", err)
	}
	original := strings.Replace(string(canonical), "[workspace]\n",
		"# workspace identity: keep this name stable, downstream tooling\n"+
			"# derives the HQ beads prefix from it (see DeriveBeadsPrefix).\n"+
			"[workspace]\n", 1)

	cfg, err := Parse([]byte(original))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	cfg.Workspace.Provider = "claude"

	got, err := cfg.MarshalForWritePreservingComments([]byte(original))
	if err != nil {
		t.Fatalf("MarshalForWritePreservingComments: %v", err)
	}

	if !strings.Contains(string(got), "# workspace identity: keep this name stable") {
		t.Fatalf("lost the leading comment block:\n%s", got)
	}
	if !strings.Contains(string(got), `name = "test-city"`) {
		t.Fatalf("lost the unrelated pre-existing field:\n%s", got)
	}
	if !strings.Contains(string(got), `provider = "claude"`) {
		t.Fatalf("missing the newly-set field:\n%s", got)
	}

	// The merge must never change the actual semantic content: re-parsing
	// the merged output has to produce the same config as a plain
	// (comment-losing) MarshalForWrite of the same mutated cfg.
	roundTripped, err := Parse(got)
	if err != nil {
		t.Fatalf("re-parsing merged output: %v", err)
	}
	wantContent, err := cfg.MarshalForWrite()
	if err != nil {
		t.Fatalf("MarshalForWrite: %v", err)
	}
	gotContent, err := roundTripped.MarshalForWrite()
	if err != nil {
		t.Fatalf("MarshalForWrite of round-tripped cfg: %v", err)
	}
	if string(gotContent) != string(wantContent) {
		t.Fatalf("merge changed semantic content:\ngot:  %s\nwant: %s", gotContent, wantContent)
	}
}
