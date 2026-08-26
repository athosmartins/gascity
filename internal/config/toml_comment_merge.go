package config

import (
	"bytes"
	"strings"

	"github.com/pmezard/go-difflib/difflib"
)

// MarshalForWritePreservingComments behaves like MarshalForWrite, but when
// original holds a previously-written city.toml for this same config, it
// splices the semantic changes into original's own text instead of emitting
// a fresh serialization — preserving hand-written comments and blank-line
// grouping that a plain re-encode has no concept of (ga-twzji; same defect
// class as ga-gdjav's agent.toml fix, but city.toml's nested/array-of-table
// shape rules out that fix's flat-file line-patcher).
//
// This never blocks or corrupts a write: the merge is verified before it's
// returned (see spliceTOMLPreservingComments), and falls back to a plain
// MarshalForWrite() — today's behavior, comments lost but always correct —
// on the rare input that verification can't clear.
func (c *City) MarshalForWritePreservingComments(original []byte) ([]byte, error) {
	fresh, err := c.MarshalForWrite()
	if err != nil {
		return nil, err
	}
	if len(bytes.TrimSpace(original)) == 0 {
		return fresh, nil
	}
	baseCfg, err := Parse(original)
	if err != nil {
		return fresh, nil
	}
	base, err := baseCfg.MarshalForWrite()
	if err != nil {
		return fresh, nil
	}
	if patched := spliceTOMLPreservingComments(original, base, fresh); patched != nil {
		return patched, nil
	}
	return fresh, nil
}

// spliceTOMLPreservingComments reconstructs a document that is semantically
// identical to fresh but keeps original's comments and blank-line grouping
// wherever possible. It works in two diff passes:
//
//  1. Line-diff original against base (a clean re-encode of the config AS
//     PARSED FROM original, i.e. its state before this mutation). Lines
//     original has that base doesn't are kept as "decoration" ONLY when
//     they're a comment or blank line; anything else original has that
//     base doesn't (e.g. a hand-edited value the encoder wouldn't
//     reproduce byte for byte, or a field original omits at its zero
//     value that the encoder always writes explicitly) is dropped rather
//     than risk smuggling stale content into the result. This pass is
//     local — a drifted line in one section never affects decoration
//     elsewhere in the file, unlike an all-or-nothing whole-file check.
//  2. Line-diff base against fresh to get the actual semantic edit script,
//     then replay it: unchanged base lines carry over the decoration
//     collected in front of them, changed/added lines come from fresh
//     verbatim, removed lines (and any decoration directly above them)
//     are dropped.
//
// Returns nil when the result doesn't verify — every fresh line emitted
// exactly once, in order, with only decoration interleaved — signaling the
// caller to fall back to fresh as-is. Given how construction works, that
// should only fire on a bug in this function, not on any particular input;
// it exists as a mechanical safety net for the most consequential file in
// the city, not as the primary defense.
func spliceTOMLPreservingComments(original, base, fresh []byte) []byte {
	originalLines := splitLinesKeepEnding(string(original))
	baseLines := splitLinesKeepEnding(string(base))
	freshLines := splitLinesKeepEnding(string(fresh))

	extrasBefore, extrasAfter := alignOriginalToBase(originalLines, baseLines)

	matcher := difflib.NewMatcherWithJunk(baseLines, freshLines, false, nil)
	var out []string
	var freshEmitted []string
	for _, op := range matcher.GetOpCodes() {
		switch op.Tag {
		case 'e':
			for k := 0; k < op.J2-op.J1; k++ {
				out = append(out, extrasBefore[op.I1+k]...)
				out = append(out, freshLines[op.J1+k])
				freshEmitted = append(freshEmitted, freshLines[op.J1+k])
			}
		case 'r':
			out = append(out, extrasBefore[op.I1]...)
			out = append(out, freshLines[op.J1:op.J2]...)
			freshEmitted = append(freshEmitted, freshLines[op.J1:op.J2]...)
		case 'i':
			out = append(out, freshLines[op.J1:op.J2]...)
			freshEmitted = append(freshEmitted, freshLines[op.J1:op.J2]...)
		case 'd':
			// Base lines removed with no replacement — drop them, and
			// drop the decoration attached above them: a comment
			// describing a field/block that no longer exists shouldn't
			// linger.
		}
	}
	out = append(out, extrasAfter...)

	if strings.Join(freshEmitted, "") != string(fresh) {
		return nil
	}
	return []byte(strings.Join(out, ""))
}

// alignOriginalToBase line-diffs originalLines against baseLines and
// attributes decoration (comments/blank lines) from original to the base
// line immediately after them. extrasBefore[j] holds the decoration lines
// that preceded baseLines[j] in original; extrasAfter holds any trailing
// decoration after the last base line (e.g. a comment at EOF).
//
// A line original has that base doesn't is kept as decoration only when
// it's blank or a comment; any other such line (drift — a hand-formatted
// value the encoder wouldn't reproduce, or a zero-value field original
// omits that the encoder always writes explicitly) is silently dropped.
// This keeps drift LOCAL: it costs that one line's own decoration
// attribution, not the whole file's.
func alignOriginalToBase(originalLines, baseLines []string) (extrasBefore [][]string, extrasAfter []string) {
	extrasBefore = make([][]string, len(baseLines))
	matcher := difflib.NewMatcherWithJunk(originalLines, baseLines, false, nil)
	var pending []string
	collectDecoration := func(i1, i2 int) {
		for i := i1; i < i2; i++ {
			line := originalLines[i]
			t := strings.TrimSpace(line)
			if t == "" || strings.HasPrefix(t, "#") {
				pending = append(pending, line)
			}
		}
	}
	for _, op := range matcher.GetOpCodes() {
		switch op.Tag {
		case 'e':
			for k := 0; k < op.I2-op.I1; k++ {
				extrasBefore[op.J1+k] = pending
				pending = nil
			}
		case 'd', 'r':
			// Lines only original has at this point (or, for 'r', has in
			// a different form than base). Comment/blank lines become
			// pending decoration for the next matched base line; anything
			// else is dropped, never carried into extrasBefore/extrasAfter.
			collectDecoration(op.I1, op.I2)
		case 'i':
			// Lines only base has (e.g. a field the encoder always
			// writes explicitly that original omitted at its zero
			// value). No original-side decoration applies to these.
		}
	}
	extrasAfter = pending
	return extrasBefore, extrasAfter
}

// splitLinesKeepEnding splits s into lines, keeping each line's trailing
// "\n" attached (so the pieces can be rejoined with plain concatenation,
// with no ambiguity about a missing final newline).
func splitLinesKeepEnding(s string) []string {
	if s == "" {
		return nil
	}
	var lines []string
	for {
		idx := strings.IndexByte(s, '\n')
		if idx < 0 {
			lines = append(lines, s)
			return lines
		}
		lines = append(lines, s[:idx+1])
		s = s[idx+1:]
	}
}
