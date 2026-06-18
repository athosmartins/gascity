package issueops

import (
	"context"
	"regexp"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/steveyegge/beads/internal/types"
)

// ga-ftmci: the supervisor cache reconcile drove Dolt :52756 to sustained high
// CPU by hydrating the three large LONGTEXT body columns (design,
// acceptance_criteria, notes) for every row on every full-table scan, even
// though the reconcile diff never reads them. SkipBody narrows the projection.
// These tests lock the projection-selection invariants so the optimization
// can't silently regress (full projection re-introduced, or column drift
// between the full and lite constants breaking ScanIssueFrom).

func countCols(colList string) int {
	// IssueSelectColumns uses ", " separators across multiple lines; collapse
	// whitespace then split on commas. Each "x AS y" alias is still one column.
	flat := strings.Join(strings.Fields(strings.ReplaceAll(colList, "\n", " ")), " ")
	parts := strings.Split(flat, ",")
	return len(parts)
}

func TestIssueSelectColumnsLiteMatchesFullArity(t *testing.T) {
	full := countCols(IssueSelectColumns)
	lite := countCols(IssueSelectColumnsLite)
	if full != lite {
		t.Fatalf("column count drift: full=%d lite=%d — ScanIssueFrom requires identical arity/order", full, lite)
	}
}

func TestIssueSelectColumnsLiteDropsBodyColumns(t *testing.T) {
	// The lite projection must NOT select the raw body columns; it replaces
	// them with empty-string literals so the scan slots stay aligned. A raw
	// column is "design," but NOT "AS design," — strip the literal aliases
	// first so we only flag a genuine raw hydration.
	stripped := IssueSelectColumnsLite
	for _, lit := range []string{"'' AS design", "'' AS acceptance_criteria", "'' AS notes"} {
		stripped = strings.ReplaceAll(stripped, lit, "")
	}
	for _, col := range []string{"design", "acceptance_criteria", "notes"} {
		if strings.Contains(stripped, col) {
			t.Fatalf("lite projection still hydrates raw body column %q", col)
		}
	}
	for _, lit := range []string{"'' AS design", "'' AS acceptance_criteria", "'' AS notes"} {
		if !strings.Contains(IssueSelectColumnsLite, lit) {
			t.Fatalf("lite projection missing literal %q", lit)
		}
	}
	// content_hash and description must remain — change detection needs them.
	for _, keep := range []string{"content_hash", "description"} {
		if !strings.Contains(IssueSelectColumnsLite, keep) {
			t.Fatalf("lite projection dropped required column %q", keep)
		}
	}
}

func TestIssueSelectColumnsSelectorPicksProjection(t *testing.T) {
	if got := issueSelectColumns(false); got != IssueSelectColumns {
		t.Fatalf("skipBody=false should return full projection")
	}
	if got := issueSelectColumns(true); got != IssueSelectColumnsLite {
		t.Fatalf("skipBody=true should return lite projection")
	}
}

// liteScanColumns is the column set ScanIssueFrom expects, in order, used to
// build mock rows. It mirrors IssueSelectColumns (alias names for the lite
// body slots are irrelevant to the scanner — only count/order matter).
func liteScanColumns() []string {
	return []string{
		"id", "content_hash", "title", "description", "design", "acceptance_criteria", "notes",
		"status", "priority", "issue_type", "assignee", "estimated_minutes",
		"created_at", "created_by", "owner", "updated_at", "started_at", "closed_at", "external_ref", "spec_id",
		"compaction_level", "compacted_at", "compacted_at_commit", "original_size", "source_repo", "close_reason",
		"sender", "ephemeral", "no_history", "wisp_type", "pinned", "is_template",
		"await_type", "await_id", "timeout_ns", "waiters",
		"mol_type",
		"event_kind", "actor", "target", "payload",
		"due_at", "defer_until",
		"work_type", "source_system", "metadata",
	}
}

// TestSearchTablePatternAUsesLiteProjectionWhenSkipBody asserts that the
// unlimited Pattern A scan (the reconcile codepath: Limit=0, AllowScan) emits
// the lite projection when filter.SkipBody is set, and the full projection
// otherwise. This is the load-bearing wiring: if it regresses, the CPU win is
// silently lost.
func TestSearchTablePatternAUsesLiteProjectionWhenSkipBody(t *testing.T) {
	cols := liteScanColumns()

	cases := []struct {
		name     string
		skipBody bool
		wantCols string
	}{
		{"full projection when SkipBody off", false, IssueSelectColumns},
		{"lite projection when SkipBody on", true, IssueSelectColumnsLite},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
			if err != nil {
				t.Fatalf("sqlmock.New: %v", err)
			}
			defer db.Close()

			mock.ExpectBegin()
			// Main scan: assert the exact projection.
			scanPat := "SELECT " + regexp.QuoteMeta(tc.wantCols) + " FROM issues"
			mock.ExpectQuery(scanPat).
				WillReturnRows(sqlmock.NewRows(cols).AddRow(
					"x-1", "h1", "Title", "desc", "", "", "",
					"open", 2, "task", "", nil,
					"2026-06-12T00:00:00Z", "", "", "2026-06-12T00:00:00Z", nil, nil, nil, nil,
					0, nil, nil, 0, nil, nil,
					"", 0, 0, nil, 0, 0,
					nil, nil, nil, nil,
					nil,
					nil, nil, nil, nil,
					nil, nil,
					nil, nil, nil,
				))
			// Label hydration follows (not skipped in this test).
			mock.ExpectQuery(regexp.QuoteMeta("issue_id, label")).
				WillReturnRows(sqlmock.NewRows([]string{"issue_id", "label"}))
			// Dependency hydration only when IncludeDependencies is set; we leave it off.

			tx, err := db.Begin()
			if err != nil {
				t.Fatalf("begin: %v", err)
			}
			filter := types.IssueFilter{SkipBody: tc.skipBody}
			issues, err := searchTableInTx(context.Background(), tx, "", filter, IssuesFilterTables)
			if err != nil {
				t.Fatalf("searchTableInTx: %v", err)
			}
			if len(issues) != 1 || issues[0].ID != "x-1" {
				t.Fatalf("unexpected issues: %+v", issues)
			}
			if tc.skipBody {
				if issues[0].Design != "" || issues[0].AcceptanceCriteria != "" || issues[0].Notes != "" {
					t.Fatalf("SkipBody should yield empty body fields, got design=%q ac=%q notes=%q",
						issues[0].Design, issues[0].AcceptanceCriteria, issues[0].Notes)
				}
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatalf("sql expectations: %v", err)
			}
		})
	}
}

// TestReadyWorkColumnsForHonorsSkipBody guards the DOMINANT bd list path:
// SearchIssuesWithCountsInTx (the JSON/with-counts query the gc reconcile
// subprocess actually executes) builds its projection from readyWorkColumnsFor,
// NOT from issueSelectColumns. The original ga-ftmci patch only narrowed the
// search.go (searchTableInTx) path; the counts path kept emitting the full
// "i.design, i.acceptance_criteria, i.notes" projection, so the CPU win never
// reached production. This asserts the counts projection now drops the three
// body columns when SkipBody is set, and that the column-prefix helper never
// produces the invalid "i.'' AS design" form for the lite literals.
func TestReadyWorkColumnsForHonorsSkipBody(t *testing.T) {
	full := readyWorkColumnsFor(false)
	lite := readyWorkColumnsFor(true)

	// Full projection qualifies every body column with the table alias.
	for _, want := range []string{"i.design", "i.acceptance_criteria", "i.notes"} {
		if !strings.Contains(full, want) {
			t.Fatalf("full counts projection missing %q:\n%s", want, full)
		}
	}

	// Lite projection emits empty-string literals for the three body columns
	// and must NOT carry the qualified body columns.
	for _, want := range []string{"'' AS design", "'' AS acceptance_criteria", "'' AS notes"} {
		if !strings.Contains(lite, want) {
			t.Fatalf("lite counts projection missing %q:\n%s", want, lite)
		}
	}
	for _, bad := range []string{"i.design", "i.acceptance_criteria", "i.notes"} {
		if strings.Contains(lite, bad) {
			t.Fatalf("lite counts projection must not contain %q:\n%s", bad, lite)
		}
	}

	// The "i." prefixer must leave literal/aliased expressions untouched —
	// "i.'' AS design" would be invalid SQL.
	for _, bad := range []string{"i.''", "i.'' AS"} {
		if strings.Contains(lite, bad) {
			t.Fatalf("lite counts projection has malformed alias %q:\n%s", bad, lite)
		}
	}

	// Both projections must select the same number of columns so the shared
	// ScanIssueFrom scan target stays aligned.
	if gotFull, gotLite := strings.Count(full, ","), strings.Count(lite, ","); gotFull != gotLite {
		t.Fatalf("column count drift: full has %d commas, lite has %d", gotFull, gotLite)
	}
}
