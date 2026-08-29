package dolt

import (
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// TestDeleteWispRecordsAWispEvent guards ga-7olk0's audit-trail half: wisps
// carry no Dolt version-control history (dolt_ignored — see
// 0021_create_wisp_auxiliary.up.sql), so a "deleted" wisp_events row is the
// only trace that survives an id being removed. Fails on the prior
// deleteWisp, which deleted the wisps row and left wisp_events untouched —
// the same signature the original bug report captured live against a real
// server (wisp_events showed only created/label_added after an archive,
// never a terminal event, and the row was unrecoverable).
func TestDeleteWispRecordsAWispEvent(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	id := "test-wispdel-ev"
	if err := store.CreateIssue(ctx, &types.Issue{
		ID: id, Title: id, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask, Ephemeral: true,
	}, "seed"); err != nil {
		t.Fatalf("CreateIssue(wisp): %v", err)
	}

	if err := store.DeleteIssue(ctx, id); err != nil {
		t.Fatalf("DeleteIssue: %v", err)
	}

	var stillPresent int
	if err := store.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM wisps WHERE id = ?", id).Scan(&stillPresent); err != nil {
		t.Fatalf("query wisps: %v", err)
	}
	if stillPresent != 0 {
		t.Fatalf("wisps row for %s still present after DeleteIssue", id)
	}

	var eventCount int
	if err := store.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM wisp_events WHERE issue_id = ? AND event_type = ?",
		id, string(types.EventDeleted),
	).Scan(&eventCount); err != nil {
		t.Fatalf("query wisp_events: %v", err)
	}
	if eventCount != 1 {
		t.Fatalf("wisp_events rows with event_type=%q for %s = %d, want 1 — the delete left no audit trail",
			types.EventDeleted, id, eventCount)
	}
}
