package main

import (
	"fmt"
	"runtime/debug"
	"strings"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/doctor"
)

// beadsModulePath is the linked beads library Go module. gc embeds this
// library and drives the native store through it; the standalone `bd` CLI on
// PATH is a separate binary built from the same project. When the two drift
// out of sync (e.g. standalone bd 1.0.5 installing legacy lifecycle hooks that
// gc's embedded native store rejects) the mismatch is silent and surfaces only
// as a downstream outage. This check makes that skew visible early.
const beadsModulePath = "github.com/steveyegge/beads"

// bdProbeVersion is overridable in tests so the check can be exercised without
// a real bd binary on PATH.
var bdProbeVersion = beads.ProbeBDVersion

// bdVersionSkewCheck compares the standalone `bd` binary version against the
// beads library version gc was built against and WARNs on incompatible skew.
type bdVersionSkewCheck struct {
	// embeddedVersion is the linked beads module version. Empty means infer
	// it from build info (the production path).
	embeddedVersion string
}

func newBDVersionSkewCheck() *bdVersionSkewCheck { return &bdVersionSkewCheck{} }

func (c *bdVersionSkewCheck) Name() string { return "bd-gc-version-skew" }

func (c *bdVersionSkewCheck) CanFix() bool { return false }

func (c *bdVersionSkewCheck) Fix(_ *doctor.CheckContext) error { return nil }

func (c *bdVersionSkewCheck) WarmupEligible() bool { return false }

func (c *bdVersionSkewCheck) Run(_ *doctor.CheckContext) *doctor.CheckResult {
	r := &doctor.CheckResult{Name: c.Name()}

	// Only fall back to build info when no explicit version was provided
	// (the production path). A provided-but-unconfirmable value such as
	// "(devel)" must stay empty rather than reach past to build info.
	rawEmbedded := strings.TrimSpace(c.embeddedVersion)
	if rawEmbedded == "" {
		rawEmbedded = embeddedBeadsVersion()
	}
	embedded := normalizeBeadsVersion(rawEmbedded)

	bdVersion, err := bdProbeVersion()
	bdVersion = normalizeBeadsVersion(bdVersion)

	// Can't determine one side → advisory, never block. A missing bd binary
	// or a (devel) gc build is not itself a skew finding.
	if err != nil {
		r.Status = doctor.StatusOK
		r.Message = fmt.Sprintf("skipped: standalone bd version unavailable (%v)", err)
		return r
	}
	if embedded == "" || bdVersion == "" {
		r.Status = doctor.StatusOK
		r.Message = "skipped: bd/gc-embedded beads version could not be confirmed"
		r.Details = []string{
			fmt.Sprintf("standalone bd: %s", emptyAs(bdVersion, "(unknown)")),
			fmt.Sprintf("gc embedded beads: %s", emptyAs(embedded, "(unknown)")),
		}
		return r
	}

	details := []string{
		fmt.Sprintf("standalone bd (%s on PATH): %s", "bd", bdVersion),
		fmt.Sprintf("gc embedded beads library (%s): %s", beadsModulePath, embedded),
	}

	if bdVersion == embedded {
		r.Status = doctor.StatusOK
		r.Message = fmt.Sprintf("standalone bd and gc-embedded beads agree (%s)", bdVersion)
		r.Details = details
		return r
	}

	r.Status = doctor.StatusWarning
	r.Severity = doctor.SeverityAdvisory
	r.Message = fmt.Sprintf("bd<->gc version skew: standalone bd %s != gc-embedded beads %s", bdVersion, embedded)
	r.Details = details
	r.FixHint = "align the standalone bd binary with gc's embedded beads version " +
		"(reinstall bd to match, or rebuild/upgrade gc); mismatched versions can install " +
		"lifecycle hooks the other rejects and cause a silent beads outage"
	return r
}

// embeddedBeadsVersion returns the version of the linked beads library this gc
// binary was built against, read from the Go build info. Empty when build info
// is unavailable or the module is replaced with a local (devel) checkout.
func embeddedBeadsVersion() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return ""
	}
	for _, dep := range info.Deps {
		if dep.Path != beadsModulePath {
			continue
		}
		if dep.Replace != nil && dep.Replace.Version != "" {
			return dep.Replace.Version
		}
		return dep.Version
	}
	return ""
}

// normalizeBeadsVersion strips a leading "v" and surrounding whitespace, and
// treats Go's "(devel)" sentinel as unknown so a local/replaced build does not
// produce a false skew finding.
func normalizeBeadsVersion(v string) string {
	v = strings.TrimSpace(v)
	v = strings.TrimPrefix(v, "v")
	if v == "(devel)" {
		return ""
	}
	return v
}

func emptyAs(v, fallback string) string {
	if strings.TrimSpace(v) == "" {
		return fallback
	}
	return v
}
