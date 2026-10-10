package beadmail

import (
	"testing"

	"github.com/gastownhall/gascity/internal/beads"
)

// ga-h9xpwb: IsRead is the ONE definition of "the recipient has read this mail
// bead". The reconciler's assigned-work checks (cmd/gc) use it to let a drained
// pool session go once its mail is read, so it must agree with what the inbox
// shows (beadToMessage.Read) in every case.
func TestIsRead(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		bead beads.Bead
		want bool
	}{
		{"metadata true", beads.Bead{Metadata: map[string]string{"mail.read": "true"}}, true},
		{"metadata true beats no label", beads.Bead{Labels: []string{"thread:x"}, Metadata: map[string]string{"mail.read": "true"}}, true},
		{"metadata false", beads.Bead{Metadata: map[string]string{"mail.read": "false"}}, false},
		{"metadata false beats a stale read label", beads.Bead{Labels: []string{"read"}, Metadata: map[string]string{"mail.read": "false"}}, false},
		{"legacy read label, no metadata", beads.Bead{Labels: []string{"read"}}, true},
		{"neither: read state unknown is not read", beads.Bead{}, false},
		{"nil metadata, unrelated labels", beads.Bead{Labels: []string{"thread:x", "reply-to:y"}}, false},
		{"unrecognised metadata value falls back to the label", beads.Bead{Labels: []string{"read"}, Metadata: map[string]string{"mail.read": "maybe"}}, true},
		{"unrecognised metadata value, no label", beads.Bead{Metadata: map[string]string{"mail.read": "maybe"}}, false},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := IsRead(tt.bead); got != tt.want {
				t.Fatalf("IsRead() = %v, want %v", got, tt.want)
			}
			if got := beadToMessage(tt.bead).Read; got != tt.want {
				t.Fatalf("beadToMessage().Read = %v, want %v (the inbox and IsRead must agree)", got, tt.want)
			}
		})
	}
}
