package issueops

import "strings"

// canonicalActor normalizes an identity string so two spellings of the same
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
// to the same value as a non-empty one, or a caller comparing against an
// unassigned issue would start matching everyone.
//
// This is a package-local duplicate of internal/validation/issue.go's
// canonicalActor (ga-wzl83): issueops sits below validation in the layering
// (storage depending on a higher-level validation package would invert it),
// so the two copies stay independent rather than sharing an import. Keep
// them in sync if the separator set or normalization rule ever changes
// (ga-5ksp5).
func canonicalActor(s string) string {
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
// followed by the canonical separator and (optionally) more — an agent's
// bare name ("batista-wa") and that agent's own session-qualified actor
// string ("batista-wa-awisplqy3swl") are the same identity at two
// granularities (wa-msxg5). Both arguments must already be canonicalized;
// short == "" always returns false. This is a package-local duplicate of
// internal/validation/issue.go's canonicalSessionSuffix — see that copy for
// the full rationale, including the documented scope limit (structural
// check, not a registry lookup) and why the two copies stay independent
// (same layering constraint as canonicalActor above). Keep them in sync.
func canonicalSessionSuffix(short, long string) bool {
	if short == "" {
		return false
	}
	return strings.HasPrefix(long, short+"_")
}

// actorMatches reports whether a and b denote the same identity: they're
// byte-identical, they canonicalize to the same string (see canonicalActor),
// or one canonicalizes to a session incarnation of the other (see
// canonicalSessionSuffix, wa-msxg5), checked both directions since callers
// don't always pass assignee-then-actor. The byte-identical check is not
// redundant — it avoids paying canonicalization on the overwhelmingly common
// exact-match path.
func actorMatches(a, b string) bool {
	if a == b {
		return true
	}
	ca, cb := canonicalActor(a), canonicalActor(b)
	return ca == cb || canonicalSessionSuffix(ca, cb) || canonicalSessionSuffix(cb, ca)
}

// ActorMatches is the exported form of actorMatches. Exported for the
// proxied-server (uow) claim path in internal/storage/domain/db (ga-v2k49),
// which builds its own claim UPDATE rather than calling ClaimIssueInTx —
// same reason lease.go's RowLockClause and FreshRowLock are exported, and
// same package-local-duplicate-over-cross-layer-import tradeoff documented on
// canonicalActor above: domain/db already depends on issueops for the
// row_lock and pool/status helpers, so reusing this one too adds no new
// layering edge, unlike importing validation would.
func ActorMatches(a, b string) bool {
	return actorMatches(a, b)
}
