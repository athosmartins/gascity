// Package nudgepoller defines the shared nudge poller process contract.
package nudgepoller

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"strings"
	"time"

	"github.com/gastownhall/gascity/internal/pathutil"
	"github.com/gastownhall/gascity/internal/pidutil"
)

const (
	cityFlag     = "--city"
	sessionFlag  = "--session"
	intervalFlag = "--interval"

	// PollIntervalEnv overrides the poller's 2s default poll interval.
	//
	// ga-yxuab: one `gc nudge poll` process per LIVE SESSION queries the data
	// plane every 2s, which measures as the single largest consumer of Dolt —
	// 48.1% of connection-samples in a 20-cycle attribution run on 2026-08-05
	// (independently reproducing the 44/47/52% of the original report). That
	// keeps ambient Dolt CPU above the quality gate's GATE_DOLT_CPU_WARM=100
	// threshold essentially always, which pins the gate's dynamic ceiling to a
	// single run and stalls the review queue (38 markers queued at the time of
	// measurement).
	//
	// Deliberately an ENV KNOB and not a changed constant: with the variable
	// unset, CommandArgs emits byte-identical argv to the pre-change build, so
	// deploying this binary is a no-op and the behavior change is a config
	// value that can be set or reverted without another rebuild. Nudge latency
	// degrades to at most the chosen interval, which is bounded and far below
	// the 120s patrol-tick fallback that supervisor mode was willing to accept.
	PollIntervalEnv = "GC_NUDGE_POLL_INTERVAL"
)

// pollIntervalOverride returns a validated duration string for PollIntervalEnv,
// or "" when unset/invalid. Fail-soft by design: a malformed value must fall
// back to the compiled default rather than propagate a bad flag into every
// poller argv in the city (and, via CmdlineMatcher, risk poller identification).
func pollIntervalOverride() string {
	raw := strings.TrimSpace(os.Getenv(PollIntervalEnv))
	if raw == "" {
		return ""
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d <= 0 {
		return ""
	}
	return d.String()
}

// CommandArgs returns the argv tail for a nudge poller.
//
// When PollIntervalEnv is set to a valid positive duration, an explicit
// --interval flag is inserted. CmdlineMatcher/argvHasPollTarget already skip
// --interval and its value when locating the trailing agent positional, so the
// added flag does not disturb poller identification.
func CommandArgs(cityPath, sessionName, agentName string) []string {
	args := make([]string, 0, 9)
	args = append(args, "nudge", "poll")
	if iv := pollIntervalOverride(); iv != "" {
		args = append(args, intervalFlag, iv)
	}
	args = append(args, cityFlag, cityPath, sessionFlag, sessionName, agentName)
	return args
}

// CmdlineMatcher returns a predicate that recognizes the nudge poller command
// for the supplied city, session, and target key.
func CmdlineMatcher(cityPath, sessionName, agentName string) func([]string) bool {
	expectedCity := pathutil.NormalizePathForCompare(cityPath)
	expectedAgent := strings.TrimSpace(agentName)
	return func(argv []string) bool {
		if expectedCity == "" || sessionName == "" || expectedAgent == "" {
			return false
		}
		if !pidutil.ArgvContainsSequence(argv, "nudge", "poll") {
			return false
		}
		if !argvHasPathFlagValue(argv, cityFlag, expectedCity) {
			return false
		}
		if !pidutil.ArgvHasFlagValue(argv, sessionFlag, sessionName) {
			return false
		}
		return argvHasPollTarget(argv, expectedAgent)
	}
}

func argvHasPollTarget(argv []string, expected string) bool {
	for i := 0; i+1 < len(argv); i++ {
		if argv[i] != "nudge" || argv[i+1] != "poll" {
			continue
		}
		for j := i + 2; j < len(argv); j++ {
			arg := argv[j]
			switch {
			case arg == cityFlag || arg == sessionFlag || arg == "--interval" || arg == "--quiescence":
				j++
			case strings.HasPrefix(arg, cityFlag+"=") ||
				strings.HasPrefix(arg, sessionFlag+"=") ||
				strings.HasPrefix(arg, "--interval=") ||
				strings.HasPrefix(arg, "--quiescence="):
			case strings.HasPrefix(arg, "-"):
				if !strings.Contains(arg, "=") && j+1 < len(argv) && !strings.HasPrefix(argv[j+1], "-") {
					j++
				}
			default:
				return arg == expected
			}
		}
		return false
	}
	return false
}

// PollerFileStem returns the filesystem-safe stem for poller PID and log
// files owned by a concrete session/target tuple.
func PollerFileStem(sessionName, agentName string) string {
	sessionName = strings.TrimSpace(sessionName)
	agentName = strings.TrimSpace(agentName)
	digest := sha256.Sum256([]byte(sessionName + "\x00" + agentName))
	prefix := safeFileStemPart(sessionName)
	if prefix == "" {
		prefix = "session"
	}
	return prefix + "-" + hex.EncodeToString(digest[:8])
}

func safeFileStemPart(value string) string {
	var b strings.Builder
	for _, r := range strings.TrimSpace(value) {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r)
		case r >= 'A' && r <= 'Z':
			b.WriteRune(r)
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '-' || r == '_' || r == '.':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
		if b.Len() >= 48 {
			break
		}
	}
	return strings.Trim(b.String(), ".-")
}

func argvHasPathFlagValue(argv []string, flag, expected string) bool {
	for i, arg := range argv {
		if arg == flag && i+1 < len(argv) {
			if pathutil.NormalizePathForCompare(argv[i+1]) == expected {
				return true
			}
		}
		if strings.HasPrefix(arg, flag+"=") {
			if pathutil.NormalizePathForCompare(strings.TrimPrefix(arg, flag+"=")) == expected {
				return true
			}
		}
	}
	return false
}
