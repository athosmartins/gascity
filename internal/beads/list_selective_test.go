package beads

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	beadslib "github.com/steveyegge/beads"
)

// selectiveUpstream stands in for the upstream beads search. Like the real SQL
// it applies the metadata equality filter first and the limit after, and it
// ignores predicates NativeDoltStore only applies client-side (UpdatedBefore).
type selectiveUpstream struct {
	issues  []*beadslib.Issue
	limits  []int                  // Limit of every SearchIssues call, in order
	filters []beadslib.IssueFilter // the whole filter of every call, in order
	err     error
}

func (u *selectiveUpstream) search(_ context.Context, _ string, filter beadslib.IssueFilter) ([]*beadslib.Issue, error) {
	u.limits = append(u.limits, filter.Limit)
	u.filters = append(u.filters, filter)
	if u.err != nil {
		return nil, u.err
	}
	var out []*beadslib.Issue
	for _, issue := range u.issues {
		var meta map[string]string
		_ = json.Unmarshal(issue.Metadata, &meta)
		match := true
		for k, v := range filter.MetadataFields {
			if meta[k] != v {
				match = false
				break
			}
		}
		if !match {
			continue
		}
		out = append(out, issue)
		if filter.Limit > 0 && len(out) >= filter.Limit {
			break
		}
	}
	return out, nil
}

func selectiveIssue(id, sessionName string, updatedAt time.Time) *beadslib.Issue {
	return &beadslib.Issue{
		ID:        id,
		Title:     id,
		Status:    beadslib.StatusOpen,
		IssueType: beadslib.TypeTask,
		Priority:  2,
		CreatedAt: updatedAt,
		UpdatedAt: updatedAt,
		Metadata:  json.RawMessage(fmt.Sprintf(`{"session_name":%q}`, sessionName)),
	}
}

func selectiveIssues(n int) []*beadslib.Issue {
	base := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	out := make([]*beadslib.Issue, 0, n+1)
	for i := 0; i < n; i++ {
		out = append(out, selectiveIssue(fmt.Sprintf("gc-%02d", i), "worker-1", base.Add(time.Duration(i)*time.Second)))
	}
	return append(out, selectiveIssue("gc-other", "worker-2", base))
}

func newSelectiveStore(issues []*beadslib.Issue) (*NativeDoltStore, *selectiveUpstream) {
	up := &selectiveUpstream{issues: issues}
	return newNativeDoltStoreForTest(&nativeDoltStorageSpy{searchIssues: up.search}), up
}

func selectiveLookup() ListQuery {
	return ListQuery{Metadata: map[string]string{"session_name": "worker-1"}, Selective: true}
}

func selectiveIDs(beads []Bead) []string {
	out := make([]string, len(beads))
	for i, b := range beads {
		out[i] = b.ID
	}
	return out
}

// The point of the hint: upstream must first see a BOUNDED query, because only
// a bounded query lets the vendored bd search take its narrow id-only scan.
func TestNativeDoltStoreListSelectiveProbesWithBoundedLimitFirst(t *testing.T) {
	store, up := newSelectiveStore(selectiveIssues(3))

	got, err := store.List(selectiveLookup())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("List returned %d beads, want 3", len(got))
	}
	if want := []int{SelectiveProbeLimit}; !reflect.DeepEqual(up.limits, want) {
		t.Fatalf("upstream limits = %v, want a single bounded probe %v", up.limits, want)
	}

	// Control: without the hint the read reaches upstream unbounded, as before.
	unhinted := selectiveLookup()
	unhinted.Selective = false
	store, up = newSelectiveStore(selectiveIssues(3))
	if _, err := store.List(unhinted); err != nil {
		t.Fatalf("List (no hint): %v", err)
	}
	if want := []int{0}; !reflect.DeepEqual(up.limits, want) {
		t.Fatalf("upstream limits without the hint = %v, want %v", up.limits, want)
	}
}

// The hint changes how the rows are read, never which rows come back.
func TestNativeDoltStoreListSelectiveResultMatchesUnhintedListForEveryMatchCount(t *testing.T) {
	for _, n := range []int{0, 1, SelectiveProbeLimit - 1, SelectiveProbeLimit, SelectiveProbeLimit + 1, SelectiveProbeLimit + 8} {
		t.Run(fmt.Sprintf("matches=%d", n), func(t *testing.T) {
			hintedStore, _ := newSelectiveStore(selectiveIssues(n))
			plainStore, _ := newSelectiveStore(selectiveIssues(n))
			plain := selectiveLookup()
			plain.Selective = false

			got, err := hintedStore.List(selectiveLookup())
			if err != nil {
				t.Fatalf("List (hinted): %v", err)
			}
			want, err := plainStore.List(plain)
			if err != nil {
				t.Fatalf("List (plain): %v", err)
			}
			if len(got) != n {
				t.Fatalf("hinted List returned %d beads, want all %d (a probe page must never truncate)", len(got), n)
			}
			if !reflect.DeepEqual(selectiveIDs(got), selectiveIDs(want)) {
				t.Fatalf("hinted List differs from plain List:\n got=%v\nwant=%v", selectiveIDs(got), selectiveIDs(want))
			}
		})
	}
}

// A full probe page cannot prove the result is complete, so the query is
// repeated unbounded. Exactly-full is the boundary a "len > limit" check gets wrong.
func TestNativeDoltStoreListSelectiveRepeatsUnboundedWhenProbePageIsFull(t *testing.T) {
	for _, n := range []int{SelectiveProbeLimit, SelectiveProbeLimit + 5} {
		t.Run(fmt.Sprintf("matches=%d", n), func(t *testing.T) {
			store, up := newSelectiveStore(selectiveIssues(n))

			if _, err := store.List(selectiveLookup()); err != nil {
				t.Fatalf("List: %v", err)
			}
			if want := []int{SelectiveProbeLimit, 0}; !reflect.DeepEqual(up.limits, want) {
				t.Fatalf("upstream limits = %v, want probe then unbounded %v", up.limits, want)
			}
		})
	}
}

// The probe is judged on the UPSTREAM row count. UpdatedBefore is applied only
// client-side, so a probe page whose every row it later drops must not be read
// as "complete": here the only surviving row sits beyond the first page.
func TestNativeDoltStoreListSelectiveJudgesTheProbeOnUpstreamRows(t *testing.T) {
	recent := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	old := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	var issues []*beadslib.Issue
	for i := 0; i < SelectiveProbeLimit; i++ {
		issues = append(issues, selectiveIssue(fmt.Sprintf("gc-recent-%02d", i), "worker-1", recent))
	}
	issues = append(issues, selectiveIssue("gc-old", "worker-1", old))
	store, up := newSelectiveStore(issues)

	query := selectiveLookup()
	query.UpdatedBefore = time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)

	got, err := store.List(query)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if ids := selectiveIDs(got); !reflect.DeepEqual(ids, []string{"gc-old"}) {
		t.Fatalf("List = %v, want [gc-old]: the first upstream page was full, so the query had to be repeated unbounded", ids)
	}
	if want := []int{SelectiveProbeLimit, 0}; !reflect.DeepEqual(up.limits, want) {
		t.Fatalf("upstream limits = %v, want probe then unbounded %v", up.limits, want)
	}
}

func TestNativeDoltStoreListSelectiveLeavesACallerLimitAlone(t *testing.T) {
	store, up := newSelectiveStore(selectiveIssues(10))
	query := selectiveLookup()
	query.Limit = 5

	got, err := store.List(query)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 5 {
		t.Fatalf("List returned %d beads, want the caller's limit of 5", len(got))
	}
	if want := []int{5}; !reflect.DeepEqual(up.limits, want) {
		t.Fatalf("upstream limits = %v, want the caller's own limit %v", up.limits, want)
	}
}

// Only the limit may differ between the probe and the unbounded repeat: a probe
// that rebuilt the filter and dropped a predicate would return rows the caller
// never asked for.
func TestNativeDoltStoreListSelectiveProbeChangesOnlyTheLimit(t *testing.T) {
	store, up := newSelectiveStore(selectiveIssues(SelectiveProbeLimit + 3))

	if _, err := store.List(selectiveLookup()); err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(up.filters) != 2 {
		t.Fatalf("upstream calls = %d, want the probe and the unbounded repeat", len(up.filters))
	}
	probe, full := up.filters[0], up.filters[1]
	if probe.Limit != SelectiveProbeLimit || full.Limit != 0 {
		t.Fatalf("limits = %d, %d, want %d, 0", probe.Limit, full.Limit, SelectiveProbeLimit)
	}
	probe.Limit = 0
	if !reflect.DeepEqual(probe, full) {
		t.Fatalf("probe filter differs from the unbounded filter beyond Limit:\n probe=%+v\n  full=%+v", probe, full)
	}
	// The status filter must survive: an open-only lookup must never see closed rows.
	if len(full.ExcludeStatus) != 1 || full.ExcludeStatus[0] != beadslib.StatusClosed {
		t.Fatalf("ExcludeStatus = %#v, want [closed]", full.ExcludeStatus)
	}
}

// The hint is only honored for a selective query. A query that includes closed
// rows can match a long history (a full probe page would double the cost of an
// unbounded scan), and a label-driven or unfiltered query is not an identity
// lookup, so all of them reach upstream unbounded, as before.
func TestNativeDoltStoreListSelectiveIgnoresQueriesThatCanMatchManyBeads(t *testing.T) {
	cases := []struct {
		name  string
		shape func(q *ListQuery)
	}{
		{"includes closed rows", func(q *ListQuery) { q.IncludeClosed = true }},
		{"closed-only status", func(q *ListQuery) { q.Status = "closed" }},
		{"label-driven search", func(q *ListQuery) { q.Label = "gc:session" }},
		{"no metadata filter", func(q *ListQuery) { q.Metadata = nil; q.AllowScan = true }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store, up := newSelectiveStore(selectiveIssues(3))
			query := selectiveLookup()
			tc.shape(&query)

			if _, err := store.List(query); err != nil {
				t.Fatalf("List: %v", err)
			}
			if want := []int{0}; !reflect.DeepEqual(up.limits, want) {
				t.Fatalf("upstream limits = %v, want %v: the hint must not bound this query", up.limits, want)
			}
		})
	}
}

// A local sort keeps the upstream limit at zero; the probe must still return the
// complete, locally sorted result.
func TestNativeDoltStoreListSelectiveWithLocalSortReturnsCompleteSortedResult(t *testing.T) {
	base := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	issues := []*beadslib.Issue{
		selectiveIssue("gc-new", "worker-1", base.Add(2*time.Minute)),
		selectiveIssue("gc-old", "worker-1", base),
		selectiveIssue("gc-mid", "worker-1", base.Add(time.Minute)),
	}
	store, up := newSelectiveStore(issues)
	query := selectiveLookup()
	query.Sort = SortCreatedAsc

	got, err := store.List(query)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if ids := selectiveIDs(got); !reflect.DeepEqual(ids, []string{"gc-old", "gc-mid", "gc-new"}) {
		t.Fatalf("List = %v, want every match sorted oldest first", ids)
	}
	if want := []int{SelectiveProbeLimit}; !reflect.DeepEqual(up.limits, want) {
		t.Fatalf("upstream limits = %v, want %v", up.limits, want)
	}
}

func TestNativeDoltStoreListSelectiveHardErrorIsNotRetried(t *testing.T) {
	store, up := newSelectiveStore(selectiveIssues(3))
	boom := errors.New("dolt: read timeout")
	up.err = boom

	got, err := store.List(selectiveLookup())
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want %v", err, boom)
	}
	if got != nil {
		t.Fatalf("beads = %+v, want nil on a hard error", got)
	}
	if len(up.limits) != 1 {
		t.Fatalf("upstream calls = %d, want 1 (a failed probe must not be retried unbounded)", len(up.limits))
	}
}
