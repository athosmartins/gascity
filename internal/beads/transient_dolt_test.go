package beads

import (
	"context"
	"database/sql/driver"
	"errors"
	"fmt"
	"testing"
)

// TestIsTransientDoltError covers the ga-f8r9e classifier: it must be a strict
// superset of IsBadConnError AND additionally match the Dolt i/o-timeout /
// deadline-exceeded storm that IsBadConnError deliberately does not classify.
func TestIsTransientDoltError(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
		// alsoBadConn asserts the existing narrow classifier's verdict so the
		// test pins the superset relationship explicitly.
		alsoBadConn bool
	}{
		{"nil", nil, false, false},
		{"unrelated", errors.New("table beads doesn't exist"), false, false},

		// Stale-connection family — matched by BOTH classifiers (superset).
		{"typed ErrBadConn", driver.ErrBadConn, true, true},
		{"wrapped ErrBadConn", fmt.Errorf("query: %w", driver.ErrBadConn), true, true},
		{"invalid connection", errors.New("Error 1105: invalid connection"), true, true},
		{"bad connection", errors.New("dial dolt: bad connection"), true, true},
		{"connection reset", errors.New("read: connection reset by peer"), true, true},
		{"broken pipe", errors.New("write: broken pipe"), true, true},

		// i/o-timeout family — the ga-f8r9e gap: transient but NOT bad-conn.
		{"i/o timeout (live storm string)", errors.New("read tcp 127.0.0.1:1->127.0.0.1:52756: i/o timeout"), true, false},
		{"typed context.DeadlineExceeded", context.DeadlineExceeded, true, false},
		{"wrapped DeadlineExceeded", fmt.Errorf("start: %w", context.DeadlineExceeded), true, false},
		{"context deadline exceeded string", errors.New("context deadline exceeded"), true, false},
		{"deadline exceeded string", errors.New("operation deadline exceeded"), true, false},
		{"timed out after", errors.New("query timed out after 18s"), true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsTransientDoltError(tc.err); got != tc.want {
				t.Fatalf("IsTransientDoltError(%v) = %v, want %v", tc.err, got, tc.want)
			}
			if got := IsBadConnError(tc.err); got != tc.alsoBadConn {
				t.Fatalf("IsBadConnError(%v) = %v, want %v (superset invariant)", tc.err, got, tc.alsoBadConn)
			}
			// Superset invariant: anything IsBadConnError matches,
			// IsTransientDoltError must also match.
			if IsBadConnError(tc.err) && !IsTransientDoltError(tc.err) {
				t.Fatalf("superset violated: IsBadConnError matched but IsTransientDoltError did not for %v", tc.err)
			}
		})
	}
}
