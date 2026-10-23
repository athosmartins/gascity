package mail_test

import (
	"strings"
	"testing"

	"github.com/gastownhall/gascity/internal/mail"
)

// TestFakeReplyToOwnMessageGoesToOriginalRecipient mirrors the beadmail rule
// (ga-yi067e) so tests that use the fake see the same addressing as production:
// replying to a message you sent continues the thread to its recipient instead
// of addressing the reply to yourself.
func TestFakeReplyToOwnMessageGoesToOriginalRecipient(t *testing.T) {
	f := mail.NewFake()
	sent, err := f.Send("alice", "bob", "Hello", "first")
	if err != nil {
		t.Fatalf("Send: %v", err)
	}

	reply, err := f.Reply(sent.ID, "alice", "RE: Hello", "follow-up")
	if err != nil {
		t.Fatalf("Reply: %v", err)
	}
	if reply.To != "bob" {
		t.Errorf("Reply To = %q, want %q (original recipient)", reply.To, "bob")
	}
	if reply.From != "alice" {
		t.Errorf("Reply From = %q, want %q", reply.From, "alice")
	}
	if reply.ThreadID != sent.ThreadID || reply.ReplyTo != sent.ID {
		t.Errorf("Reply thread/reply-to = %q/%q, want %q/%q", reply.ThreadID, reply.ReplyTo, sent.ThreadID, sent.ID)
	}
}

func TestFakeReplyToOtherSendersMessageStillGoesToSender(t *testing.T) {
	f := mail.NewFake()
	sent, err := f.Send("alice", "bob", "Hello", "first")
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	reply, err := f.Reply(sent.ID, "bob", "RE: Hello", "reply")
	if err != nil {
		t.Fatalf("Reply: %v", err)
	}
	if reply.To != "alice" {
		t.Errorf("Reply To = %q, want %q (original sender)", reply.To, "alice")
	}
}

// TestFakeReplyToOwnMessageWithoutRecipientFails: nobody to follow up with, so
// the reply errors instead of being sent to the replier.
func TestFakeReplyToOwnMessageWithoutRecipientFails(t *testing.T) {
	f := mail.NewFake()
	sent, err := f.Send("alice", "", "Orphan", "no recipient")
	if err != nil {
		t.Fatalf("Send: %v", err)
	}

	_, err = f.Reply(sent.ID, "alice", "RE: Orphan", "follow-up")
	if err == nil {
		t.Fatal("Reply to own message with no recipient succeeded, want error")
	}
	if !strings.Contains(err.Error(), "no recipient") {
		t.Errorf("Reply error = %q, want it to say the original has no recipient", err)
	}
	thread, err := f.Thread(sent.ThreadID)
	if err != nil {
		t.Fatalf("Thread: %v", err)
	}
	if len(thread) != 1 {
		t.Errorf("thread = %d messages, want 1 (a failed reply must not be recorded)", len(thread))
	}
}
