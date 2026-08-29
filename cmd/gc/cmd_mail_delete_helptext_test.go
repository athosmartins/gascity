package main

import (
	"bytes"
	"strings"
	"testing"
)

// TestMailDeleteHelpTextDoesNotClaimClose guards against ga-7olk0: `gc mail
// delete` performs an immediate, irreversible hard delete (see
// Provider.Delete/Archive in internal/mail/beadmail), but its help text used
// to say "closing the beads" — a soft-close claim the code never honored.
// Fails on the prior text ("Delete one or more messages (closes the beads)" /
// "...by closing the beads...").
func TestMailDeleteHelpTextDoesNotClaimClose(t *testing.T) {
	cmd := newMailDeleteCmd(&bytes.Buffer{}, &bytes.Buffer{})

	for _, text := range []string{cmd.Short, cmd.Long} {
		lower := strings.ToLower(text)
		if strings.Contains(lower, "closing the bead") || strings.Contains(lower, "closes the bead") {
			t.Errorf("help text falsely claims a soft-close: %q", text)
		}
	}

	lower := strings.ToLower(cmd.Long)
	if !strings.Contains(lower, "permanently") && !strings.Contains(lower, "irreversible") && !strings.Contains(lower, "cannot be undone") {
		t.Errorf("help text should disclose the delete is permanent/irreversible, got: %q", cmd.Long)
	}
}
