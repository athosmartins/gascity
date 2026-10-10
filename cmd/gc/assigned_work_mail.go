package main

import (
	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/mail/beadmail"
	sessionpkg "github.com/gastownhall/gascity/internal/session"
)

// isReadMailBead reports whether b is mail the recipient has positively
// already read. Read state is beadmail.IsRead's single definition, so "unknown"
// is never read: mail whose state cannot be established still counts as work.
func isReadMailBead(b beads.Bead) bool {
	return b.Type == "message" && beadmail.IsRead(b)
}

// countsAsAssignedWork reports whether a bead assigned to a session is work
// that keeps the session alive or awake. Session beads never are. Mail the
// recipient has already read never is either (ga-h9xpwb): it stays status=open
// until somebody archives it, and counting it made a drained pool session
// unable to close, so the reconciler woke it again about every 3.5 minutes
// (99 wake/drain cycles in 3 days on one wa-worker). UNREAD mail still counts —
// mail is meant to wake a session (see appendWorkUnique) — and reading it is
// what lets the session go.
func countsAsAssignedWork(b beads.Bead) bool {
	return !sessionpkg.IsSessionBeadOrRepairable(b) && !isReadMailBead(b)
}
