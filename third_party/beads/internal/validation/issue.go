package validation

import (
	"fmt"
	"slices"
	"strings"

	"github.com/steveyegge/beads/internal/types"
)

// IssueValidator validates an issue and returns an error if validation fails.
// Validators can be composed using Chain() for complex validation logic.
type IssueValidator func(id string, issue *types.Issue) error

// Chain composes multiple validators into a single validator.
// Validators are executed in order and the first error stops the chain.
func Chain(validators ...IssueValidator) IssueValidator {
	return func(id string, issue *types.Issue) error {
		for _, v := range validators {
			if err := v(id, issue); err != nil {
				return err
			}
		}
		return nil
	}
}

// Exists validates that an issue is not nil.
func Exists() IssueValidator {
	return func(id string, issue *types.Issue) error {
		if issue == nil {
			return fmt.Errorf("issue %s not found", id)
		}
		return nil
	}
}

// NotTemplate validates that an issue is not a template.
// Templates are read-only and cannot be modified.
func NotTemplate() IssueValidator {
	return func(id string, issue *types.Issue) error {
		if issue == nil {
			return nil // Let Exists() handle nil check if needed
		}
		if issue.IsTemplate {
			return fmt.Errorf("cannot modify template %s: templates are read-only; use 'bd mol pour' to create a work item", id)
		}
		return nil
	}
}

// NotPinned validates that an issue is not pinned, by either of the two ways
// bd expresses a pin: the Pinned boolean column or the pinned status. Returns
// an error if either trigger fires, unless force is true.
//
// Both triggers are load-bearing because consumers disagree on which one they
// write and read (ga-z3vht). Gas Town pins by status — it enumerates pins with
// List(ListOptions{Status: StatusPinned}) across 21 sites (hook_check.go,
// prime_output.go, molecule_step.go, up.go) — while Gas City reads the boolean
// (compute_awake_set.go). Checking only one strips the other consumer's
// protection outright, so do not "simplify" either clause away.
func NotPinned(force bool) IssueValidator {
	return func(id string, issue *types.Issue) error {
		if issue == nil {
			return nil // Let Exists() handle nil check if needed
		}
		if !force && (issue.Pinned || issue.Status == types.StatusPinned) {
			return fmt.Errorf("cannot modify pinned issue %s (use --force to override)", id)
		}
		return nil
	}
}

// CanonicalActor normalizes an identity string so two spellings of the same
// Gas Town identity compare equal. The same identity arrives at bd in more
// than one spelling depending on which layer produced the string it was
// handed: a dotted alias like "gastown.mayor" gets its dot replaced wherever
// a dot is unsafe for that context — "__" in a session name, "_" in a Dolt
// table/database name, "-" elsewhere — and bd only ever sees the resulting
// string, never the substitution itself (ga-wzl83). None of ".", "_", "-"
// carries meaning in an identity string: each is always a positional
// separator between a rig and a role/agent name, never part of either name.
// Collapsing any run of them to one canonical separator lets two spellings
// of the same identity compare equal without weakening comparisons between
// genuinely different identities, whose non-separator characters still
// differ (e.g. "gastown.mayor" vs "gastown.dog-3" stay distinct).
//
// Empty stays empty: an actual absence of an actor must never canonicalize
// to the same value as a non-empty one, or AssigneeMatches would start
// accepting an empty actor against an assigned issue.
func CanonicalActor(s string) string {
	if s == "" {
		return ""
	}
	var b strings.Builder
	b.Grow(len(s))
	inSeparator := false
	for _, r := range s {
		switch r {
		case '.', '_', '-':
			if !inSeparator {
				b.WriteByte('_')
				inSeparator = true
			}
		default:
			b.WriteRune(r)
			inSeparator = false
		}
	}
	return b.String()
}

// canonicalSessionSuffix reports whether long is a session incarnation of
// short: long, once canonicalized, consists of short's canonical form
// followed by the canonical separator and (optionally) more. Gas Town
// assigns an issue to an agent's bare name ("batista-wa") but performs the
// write under that agent's per-session actor string
// ("batista-wa-awisplqy3swl") — the session is an incarnation of the agent,
// not a different identity (wa-msxg5), but no amount of delimiter-folding
// alone (CanonicalActor) makes a shorter assignee string equal a longer,
// session-qualified actor string naming the same agent. Both arguments must
// already be canonicalized; short == "" always returns false, or an
// unassigned issue (CanonicalActor("") == "") would match every actor whose
// canonical form happens to start with a separator.
//
// SCOPE LIMIT, read before extending this: this is a structural check, not
// an identity check. It cannot confirm the suffix names a session bd has
// actually seen — bd has no session registry, only the two strings a caller
// hands it (confirmed by reading every ActorMatches/actorMatches call site
// in internal/storage: assignee and actor arrive as bare strings, nothing
// more). The fix originally proposed for wa-msxg5 was to resolve actor to
// its owning agent via a registry (the same agent_name -> template
// relationship `gc session list` exposes) and compare agent-to-agent,
// specifically to close the hole this scope limit leaves open: a genuinely
// distinct agent whose own name happens to be "<other-agent>-<word>" (e.g. a
// future "batista-wa-backup") would have ITS sessions match against
// "batista-wa" too, incorrectly. That registry lookup is not reachable from
// here — it lives in Gas Town's gc/gascity layer, a dependency this package
// cannot take without inverting bd's layering (and bd is used outside Gas
// Town, where no such registry would exist at all).
//
// Accepted as the pragmatic fix instead because: (a) it strictly narrows the
// originally-proposed bare-prefix design, which matched on any shared
// prefix — this requires an exact canonical-separator boundary, so
// "batista-wa2" no longer matches "batista-wa"; (b) no agent name in the
// current Gas Town roster is a separator-extension of another agent's name
// (verified 2026-08-23, at the time this landed); (c) the bug this fixes
// (P1, reproduced independently by three agents, blocks every agent from
// closing its own beads without --force) is real and active today, while
// the collision this leaves open is hypothetical and, per (b), not
// currently live. Whoever names a new Gas Town agent should avoid
// "<existing-agent>-<word>" shapes; whoever later plumbs session-registry
// data down to bd should replace this with the registry check rather than
// extend it further.
func canonicalSessionSuffix(short, long string) bool {
	if short == "" {
		return false
	}
	return strings.HasPrefix(long, short+"_")
}

// ActorMatches reports whether actor has the same authority as assignee:
// they're byte-identical, they canonicalize to the same identity (see
// CanonicalActor), or one canonicalizes to a session incarnation of the
// other (see canonicalSessionSuffix, wa-msxg5) — an agent's bare name and
// that agent's own session-qualified actor string are the same identity at
// two granularities, and either may legitimately appear in either argument
// position (some callers compare two assignee-shaped values against each
// other, not just assignee-then-actor, so the session-suffix check runs
// both directions). The byte-identical check is not redundant — it avoids
// paying canonicalization on the overwhelmingly common exact-match path
// (ga-5ksp5 gate review, #5438: fixes a garbled doc comment, no behavior
// change).
func ActorMatches(assignee, actor string) bool {
	if assignee == actor {
		return true
	}
	ca, cb := CanonicalActor(assignee), CanonicalActor(actor)
	return ca == cb || canonicalSessionSuffix(ca, cb) || canonicalSessionSuffix(cb, ca)
}

// AssigneeMatches validates that the actor has authority to close the issue.
// Authority means the issue is unassigned or the actor matches the current
// assignee. Returns an error on mismatch unless force is true.
//
// Authority is identity-by-string: actor is compared to assignee under
// CanonicalActor, so two principals sharing one canonical actor name both
// pass — including the same principal spelled two different ways by two
// different layers of Gas Town (ga-wzl83). bd has no identity layer, so this
// matches the existing semantics of the actor field — the guard removes
// silent cross-actor closes without adding new identity guarantees.
//
// This guards against the silent-success bug where actor A closes a bead that
// was concurrently re-claimed by actor B: storage accepts the close (id-only
// WHERE clause), so without this check bd reports "✓ Closed" even though A had
// no authority over the bead. (be-035)
func AssigneeMatches(actor string, force bool) IssueValidator {
	return func(id string, issue *types.Issue) error {
		if issue == nil || force {
			return nil
		}
		if issue.Assignee == "" || ActorMatches(issue.Assignee, actor) {
			return nil
		}
		return fmt.Errorf("cannot close %s: assignee is %q, actor is %q; reclaim or use --force to override", id, issue.Assignee, actor)
	}
}

// AssigneeNotStolen validates that a plain assignee update does not silently
// overwrite another actor's live claim (bd-98s5c). newAssignee is the value
// the write would set (may be "" for an unassign — fenced the same way, since
// stripping a live claim is what bd unclaim refuses without --force).
//
// The refusal fires only when every clause holds; each one is load-bearing:
//   - Assignee != ""                    — unassigned issues are freely
//     assignable.
//   - Assignee doesn't canonicalize to actor       — an actor editing its
//     own claim is untouched, under any spelling of its own identity.
//   - Assignee doesn't canonicalize to newAssignee — an idempotent re-assert
//     of the current holder stays a success (retry/replay safety on the
//     proxied path), even when the re-assert names the holder under a
//     different spelling.
//   - Status == in_progress   — reassigning an OPEN bead stays frictionless:
//     dispatchers hand-dole open beads and pool-queue takes happen constantly,
//     which is why this cannot reuse the unclaim/close ownership rule verbatim.
//   - holder is not a claim.pools alias — --claim deliberately treats
//     pool-assigned issues as takeable by any actor; without the same
//     carve-out here, --claim and -a would give opposite answers on the same
//     bead. poolAliases is a thunk so call sites don't pay the config read on
//     the overwhelmingly common non-conflicting paths.
//
// Like AssigneeMatches, authority is identity-by-string — compared under
// CanonicalActor rather than verbatim (ga-wzl83), so this removes silent
// cross-actor takeovers without adding new identity guarantees, and without
// falsely treating the current holder as a stranger when actor or
// newAssignee names it under a different layer's spelling of the same
// identity.
func AssigneeNotStolen(actor, newAssignee string, poolAliases func() []string, force bool) IssueValidator {
	return func(id string, issue *types.Issue) error {
		if issue == nil || force {
			return nil
		}
		if issue.Assignee == "" || ActorMatches(issue.Assignee, actor) || ActorMatches(issue.Assignee, newAssignee) {
			return nil
		}
		if issue.Status != types.StatusInProgress {
			return nil
		}
		if poolAliases != nil && slices.Contains(poolAliases(), issue.Assignee) {
			return nil
		}
		return fmt.Errorf("cannot reassign %s: held by %q (in_progress); coordinate with the holder (bd mail %s) — pass --force only if their claim is abandoned (crashed agent, expired lease), or use bd reclaim", id, issue.Assignee, issue.Assignee)
	}
}

// NotClosed validates that an issue is not already closed.
func NotClosed() IssueValidator {
	return func(id string, issue *types.Issue) error {
		if issue == nil {
			return nil
		}
		if issue.Status == types.StatusClosed {
			return fmt.Errorf("issue %s is already closed", id)
		}
		return nil
	}
}

// NotHooked validates that an issue is not in hooked status.
func NotHooked(force bool) IssueValidator {
	return func(id string, issue *types.Issue) error {
		if issue == nil {
			return nil
		}
		if !force && issue.Status == types.StatusHooked {
			return fmt.Errorf("cannot modify hooked issue %s (use --force to override)", id)
		}
		return nil
	}
}

// HasStatus validates that an issue has one of the allowed statuses.
func HasStatus(allowed ...types.Status) IssueValidator {
	return func(id string, issue *types.Issue) error {
		if issue == nil {
			return nil
		}
		for _, status := range allowed {
			if issue.Status == status {
				return nil
			}
		}
		return fmt.Errorf("issue %s has status %s, expected one of: %v", id, issue.Status, allowed)
	}
}

// HasType validates that an issue has one of the allowed types.
func HasType(allowed ...types.IssueType) IssueValidator {
	return func(id string, issue *types.Issue) error {
		if issue == nil {
			return nil
		}
		for _, t := range allowed {
			if issue.IssueType == t {
				return nil
			}
		}
		return fmt.Errorf("issue %s has type %s, expected one of: %v", id, issue.IssueType, allowed)
	}
}

// EpicHasOpenChildren validates that an epic does not have open children.
// If the issue is an epic with open children, returns an error unless force is true.
// Non-epic issues pass through without validation.
func EpicHasOpenChildren(force bool, openChildCount int) IssueValidator {
	return func(id string, issue *types.Issue) error {
		if issue == nil || force {
			return nil
		}
		if issue.IssueType != types.TypeEpic {
			return nil
		}
		if openChildCount > 0 {
			return fmt.Errorf("epic %s has %d open child issue(s); close children first or use --force to override", id, openChildCount)
		}
		return nil
	}
}

// forUpdate returns a validator chain for update operations.
// Validates: issue exists and is not a template.
func forUpdate() IssueValidator {
	return Chain(
		Exists(),
		NotTemplate(),
	)
}

// forClose returns a validator chain for close operations.
// Validates: issue exists, is not a template, and is not pinned (unless force).
func forClose(force bool) IssueValidator {
	return Chain(
		Exists(),
		NotTemplate(),
		NotPinned(force),
	)
}

// forDelete returns a validator chain for delete operations.
// Validates: issue exists and is not a template.
func forDelete() IssueValidator {
	return Chain(
		Exists(),
		NotTemplate(),
	)
}

// forReopen returns a validator chain for reopen operations.
// Validates: issue exists, is not a template, and is closed.
func forReopen() IssueValidator {
	return Chain(
		Exists(),
		NotTemplate(),
		HasStatus(types.StatusClosed),
	)
}
