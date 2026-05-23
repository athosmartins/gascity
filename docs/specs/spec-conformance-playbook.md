---
title: Spec Conformance Playbook
description: How public source-of-truth specs stay synchronized with implementation.
---

# Spec Conformance Playbook

Public specifications are product contracts. When an area has a public spec,
that spec is the source of truth for user-facing documentation, examples,
schema generation, and implementation behavior.

This playbook defines the repeatable pattern for keeping a spec and its
implementation in sync. PackV2 is the first adopter. Formulas should use the
same pattern next.

## Goals

- Make public specs reviewable as product contracts, not prose summaries.
- Make implementation drift fail locally and in CI.
- Let a spec honestly document known implementation gaps without letting those
  gaps disappear into prose.
- Give future workstreams a standard way to add, defer, and retire conformance
  rows.

## Source Order

Use this source order whenever a product area has a public spec:

1. Public spec in `docs/specs/`.
2. Generated schemas and generated reference docs.
3. Implementation tests and conformance fixtures.
4. Guides, tutorials, and architecture docs.
5. Historical design notes and proposal docs.

Design notes may explain why the spec is shaped a certain way. They do not
override the public spec after the spec is marked authoritative.

## Requirement Anchors

Each normative requirement that needs implementation tracking should have a
stable requirement anchor.

Use HTML comments near the relevant paragraph or table row:

```markdown
<!-- gc-spec:req id="pack.identity.schema" status="conformant" tests="internal/config/pack_test.go:TestLoadRejectsUnsupportedSchema" -->
```

Required fields:

| Field | Meaning |
|---|---|
| `id` | Stable dotted identifier. Never reuse an ID for a different requirement. |
| `status` | One of `conformant`, `divergent`, `deferred`, `non_gating`, or `spec_only`. |

Optional fields:

| Field | Meaning |
|---|---|
| `tests` | Space-separated test names, package paths, or fixture names that enforce the requirement. |
| `issue` | GitHub issue number that tracks a known gap. |
| `pr` | GitHub PR number expected to close a known gap. |
| `since` | Date or release marker when the requirement became authoritative. |
| `notes` | Short machine-readable note. Prefer a linked issue for long context. |

The prose should remain readable without these comments. The comments are for
automation and review bookkeeping.

## Visible Sidebars

If the current implementation does not match the spec, the spec must say so in
visible text immediately beside the requirement. Use a short blockquote:

```markdown
> **Implementation status:** Divergent. The current loader accepts this legacy
> field with a warning. Conformance is tracked by #1234 and PR #5678.
```

Use visible sidebars only for real reader-impacting drift. Do not add visible
sidebars for routine tests that already pass.

Every visible divergence sidebar must have a nearby `gc-spec:req` marker with
`status="divergent"` or `status="deferred"` and an `issue` or `pr` field.

## Status Meanings

| Status | Meaning | CI behavior |
|---|---|---|
| `conformant` | The implementation is expected to match the spec now. | Failing or missing tests fail CI. |
| `divergent` | The spec is authoritative, but implementation currently differs. | Requires `issue` or `pr`; CI verifies the gap is explicitly tracked. |
| `deferred` | The spec describes accepted future behavior that is intentionally not implemented yet. | Requires `issue`; CI verifies the deferral is explicit. |
| `non_gating` | The requirement is useful context but not a release gate. | CI verifies the status is explicit; no behavior test required. |
| `spec_only` | The requirement is about documentation structure or product contract text, not runtime behavior. | CI verifies the anchor exists and is well formed. |

Prefer `divergent` when the product currently behaves differently from an
authoritative requirement. Prefer `deferred` when the product has not promised
the behavior yet but the accepted spec includes it as future work.

## Conformance Ledger

Each spec should have a generated or hand-maintained conformance ledger under
`engdocs/conformance/`:

```text
engdocs/conformance/pack-spec.md
engdocs/conformance/formula-spec.md
```

The ledger is reviewer-friendly. It lists each requirement ID, status, tests,
tracking issue or PR, and a one-line summary. It is not the source of truth.
The source of truth is the requirement marker embedded in the spec.

The first PackV2 ledger can be generated from `docs/specs/pack-spec.md`. If a
generator is too much for the first slice, start with a hand-maintained ledger
and add the marker parser before more domains adopt the pattern.

## Test Tiers

Use the lightest tier that can prove the requirement:

| Tier | Use it for |
|---|---|
| Unit tests | Loader rules, parser behavior, merge order, field validation, schema enforcement. |
| CLI tests | User-visible command behavior, doctor output, migration commands, warnings, and exit codes. |
| Testscript/txtar | End-to-end examples that should stay stable and readable. |
| Generated artifact tests | Schema, reference docs, and spec ledger drift. |
| Integration tests | Only when real external infrastructure is essential. |

Do not move a conformance requirement to integration just because the
implementation path is complicated. Prefer a smaller package-level seam.

## Test Placement

Put conformance tests next to the implementation seam they protect. A spec
requirement may have more than one test, but each test should live at the
lowest layer that can prove the behavior.

| Requirement kind | Preferred location | Examples |
|---|---|---|
| Parser or loader rules | Same package as the parser/loader | `internal/config/*_test.go` |
| Merge/composition semantics | Same package as composition code | `internal/config/compose_test.go` |
| Public schema generation | Schema/doc generator package or generated-artifact test | `internal/docgen/*_test.go`, `cmd/genschema` checks |
| CLI behavior and user-visible errors | CLI package or testscript fixture | `cmd/gc/*_test.go`, `cmd/gc/testdata/*.txtar` |
| Migration/doctor behavior | Doctor package for checks, CLI tests for command UX | `internal/doctor/*_test.go`, `cmd/gc/doctor_*_test.go` |
| Cross-implementation contracts | Shared conformance helper package plus per-implementation wiring | `internal/<area>/<areatest>/conformance.go` |
| Public documentation drift | Docsync or generated-reference tests | `test/docsync`, `internal/docgen` |

Avoid one giant `spec_conformance_test.go` file that imports the world. That
kind of suite becomes slow, vague, and hard to debug. Prefer many small tests
whose package and name make the protected seam obvious.

## Test Characteristics

Good spec conformance tests have these properties:

- **Hermetic:** they do not require network access, user-specific files,
  external services, or a running city unless the requirement is explicitly
  about those things.
- **Deterministic:** they avoid wall-clock timing, random data without fixed
  seeds, process tables, and environment-dependent ordering.
- **Narrow:** each test proves one requirement or a small related cluster.
- **Bidirectional:** where practical, test both accepted and rejected forms.
- **Requirement-linked:** the test name, comment, or table row cites the
  requirement ID it protects.
- **Readable failure:** failures should say which spec rule drifted and what
  changed.
- **Generated-artifact aware:** if a requirement affects generated schema,
  reference docs, or ledgers, the test should fail when regeneration is needed.
- **No inference:** tests should assert deterministic contract behavior, not
  product judgment or model-authored interpretation.

Use table-driven tests when the spec has a field matrix. Use golden files only
when the full rendered output is the contract. If a golden file is necessary,
keep it small and make regeneration explicit.

## Naming

Name tests after the contract, not the bug that led to them:

```go
func TestPackSpecRejectsPackLevelRigImports(t *testing.T) {}
func TestPackSpecAllowsCityDefaultRigImports(t *testing.T) {}
func TestPackSpecRejectsPersistedRegistryHandles(t *testing.T) {}
```

For shared conformance helpers, use the established pattern:

```text
internal/<area>/<areatest>/conformance.go
internal/<area>/<implementation>_conformance_test.go
```

The helper package should define the contract once. Each implementation test
should only adapt an implementation to that contract.

## Required Checks

Every authoritative spec should eventually have these checks:

1. Marker syntax check: every `gc-spec:req` marker parses and has a unique ID.
2. Status policy check: every `divergent` or `deferred` marker has an issue or PR.
3. Sidebar check: visible divergence sidebars have matching divergent markers.
4. Test reference check: every `conformant` runtime requirement names at least
   one test or fixture.
5. Schema/reference check: generated docs link back to the public spec and do
   not restate conflicting rules.
6. Ledger check: `engdocs/conformance/<spec>.md` is regenerated and committed.

The first two checks should land early because they prevent the tracker from
rotting. The behavior checks can arrive row by row.

## PackV2 First Slice

For `docs/specs/pack-spec.md`, start with these requirement families:

| Family | Example requirement IDs | Primary seams |
|---|---|---|
| Pack identity | `pack.identity.name`, `pack.identity.schema`, `pack.identity.requires_gc` | `internal/config/pack.go`, schema generation |
| Import shape | `pack.imports.source`, `pack.imports.version`, `pack.imports.reject_path_ref_commit_hash` | `internal/config/pack.go`, `cmd/gc/cmd_import.go` |
| City-owned policy | `pack.defaults.rig_imports.forbidden`, `city.defaults.rig_imports.allowed` | `internal/config/config.go`, `internal/config/compose.go` |
| Definition discovery | `pack.agent.inline`, `pack.named_session.inline`, `pack.formulas.well_known_dir` | `internal/config/pack.go`, formula discovery |
| Patch surface | `pack.patches.agent.allowed`, `pack.patches.rigs.forbidden`, `pack.patches.providers.forbidden` | `internal/config/compose.go`, `internal/config/undecoded.go` |
| Legacy compatibility | `pack.legacy.includes.warn`, `pack.legacy.schema1.reject_or_migrate` | doctor checks, strict warnings |
| Generated surfaces | `pack.schema.generated_matches_spec`, `pack.reference.links_to_spec` | `cmd/genschema`, `internal/docgen`, docsync |

Do not try to annotate every paragraph in the first PR. Pick the rows that
protect the current PackV2 landing path and expand from there.

## Handling Known Divergence

When the spec and implementation disagree:

1. Decide which side is authoritative.
2. If the spec is right, add a visible implementation-status sidebar.
3. Mark the requirement `divergent` with an issue or PR.
4. Add or update a failing test only if the owning PR is expected to fix it now.
5. When the implementation catches up, remove the visible sidebar, change the
   marker to `conformant`, and add the enforcing test reference.

Known divergence must be uncomfortable but allowed. Silent divergence is not
allowed.

## Formula Adoption

The formulas crew should follow the same sequence:

1. Create `docs/specs/formula-spec.md` as the public source of truth.
2. Move or link existing reference material so guides point at the public spec.
3. Add requirement markers for the stable formula model first: formula identity,
   steps, dependencies, variables, expansion, parent/child behavior, and
   conflict handling.
4. Add visible sidebars for any known implementation drift.
5. Add `engdocs/conformance/formula-spec.md`.
6. Add marker syntax and status-policy tests before broad behavior tests.
7. Convert existing formula tests to cite requirement IDs as they are touched.

The first formula conformance PR should prove the process, not complete the
entire domain.

## Reviewer Checklist

Use this checklist for PRs that modify an authoritative spec or implementation
covered by one:

- Does every new normative rule have a requirement marker?
- If implementation differs, is there a visible sidebar plus issue or PR?
- If implementation conforms, is there a named test or fixture?
- Did generated schemas/reference docs stay consistent with the spec?
- Did the conformance ledger update?
- Did the PR avoid putting new source-of-truth prose in guides or design notes?

## Long-Term Shape

The mature version of this system should have one small parser package that can:

- read `docs/specs/*.md`
- extract `gc-spec:req` markers
- render conformance ledgers
- fail on malformed markers, duplicate IDs, stale issue/PR syntax, missing
  tests for `conformant` rows, and visible sidebars without matching markers

That parser should be domain-neutral. Pack, Formulas, Sessions, Worker,
Events, and future public contracts should all use the same mechanism.
