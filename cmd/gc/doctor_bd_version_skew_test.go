package main

import (
	"errors"
	"strings"
	"testing"

	"github.com/gastownhall/gascity/internal/doctor"
)

func withStubBDProbe(t *testing.T, version string, err error) {
	t.Helper()
	prev := bdProbeVersion
	bdProbeVersion = func() (string, error) { return version, err }
	t.Cleanup(func() { bdProbeVersion = prev })
}

func TestBDVersionSkewMatchIsOK(t *testing.T) {
	withStubBDProbe(t, "1.2.2", nil)
	c := &bdVersionSkewCheck{embeddedVersion: "v1.2.2"}
	r := c.Run(&doctor.CheckContext{})
	if r.Status != doctor.StatusOK {
		t.Fatalf("status = %v, want OK; msg=%q", r.Status, r.Message)
	}
	if !strings.Contains(r.Message, "1.2.2") {
		t.Fatalf("message %q should report the agreed version", r.Message)
	}
}

func TestBDVersionSkewMismatchWarnsAdvisory(t *testing.T) {
	withStubBDProbe(t, "1.0.5", nil)
	c := &bdVersionSkewCheck{embeddedVersion: "1.2.2"}
	r := c.Run(&doctor.CheckContext{})
	if r.Status != doctor.StatusWarning {
		t.Fatalf("status = %v, want Warning", r.Status)
	}
	if r.Severity != doctor.SeverityAdvisory {
		t.Fatalf("severity = %v, want Advisory (skew must not gate)", r.Severity)
	}
	if !strings.Contains(r.Message, "1.0.5") || !strings.Contains(r.Message, "1.2.2") {
		t.Fatalf("message %q should name both versions", r.Message)
	}
	if r.FixHint == "" {
		t.Fatal("skew warning should carry a fix hint")
	}
}

func TestBDVersionSkewUnavailableBDIsSkippedOK(t *testing.T) {
	withStubBDProbe(t, "", errors.New("locate bd: not found"))
	c := &bdVersionSkewCheck{embeddedVersion: "1.2.2"}
	r := c.Run(&doctor.CheckContext{})
	if r.Status != doctor.StatusOK {
		t.Fatalf("status = %v, want OK (missing bd is not a skew finding)", r.Status)
	}
}

func TestBDVersionSkewDevelEmbeddedIsSkippedOK(t *testing.T) {
	withStubBDProbe(t, "1.2.2", nil)
	c := &bdVersionSkewCheck{embeddedVersion: "(devel)"}
	r := c.Run(&doctor.CheckContext{})
	if r.Status != doctor.StatusOK {
		t.Fatalf("status = %v, want OK ((devel) build cannot confirm skew)", r.Status)
	}
}

func TestNormalizeBeadsVersion(t *testing.T) {
	cases := map[string]string{
		"v1.2.2":  "1.2.2",
		" 1.2.2 ": "1.2.2",
		"(devel)": "",
		"":        "",
	}
	for in, want := range cases {
		if got := normalizeBeadsVersion(in); got != want {
			t.Errorf("normalizeBeadsVersion(%q) = %q, want %q", in, got, want)
		}
	}
}
