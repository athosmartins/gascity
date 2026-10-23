package main

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Managed-Dolt hold (ga-3risy2).
//
// Stopping the managed Dolt on purpose (swapping a database directory, an
// offline GC, a restore drill) used to be impossible without shutting the
// whole city down: the controller tick (ensureManagedDoltPublishedForRuntime),
// the reactive bd retry (recoverManagedBDCommandAllowed) and
// healthBeadsProvider each re-start a stopped Dolt within seconds, and so do
// the city's own watchdogs. A hold is the supported way to say "Dolt is down
// on purpose; nobody starts it until I release the hold".
//
// The hold is a file, <pack-state-dir>/dolt-hold (GC_DOLT_HOLD_FILE
// overrides), so that shell watchdogs and the lifecycle scripts read the very
// same signal. Format: optional "key=value" lines, "#" starts a comment line.
//
//	expires_at=<unix seconds>   optional; from then on the hold is released
//	owner=<who placed it>       optional, shown in messages
//	reason=<free text>          optional, shown in messages
//
// A bare `touch` is a valid hold and lasts until the file is removed. The
// examples/bd/assets/scripts/dolt-hold.sh helper implements the same reading
// for shell; keep the two in step.
//
// There are four states, never two: no hold, active hold, expired hold, and
// "could not tell". The last one blocks, exactly like an active hold: the
// action a hold guards (starting or killing a Dolt on a directory somebody is
// swapping) is the destructive one, so under doubt the inert answer is "do
// not touch Dolt". Only a missing file (ENOENT) or an expiry that has passed
// releases the hold.
const managedDoltHoldFileName = "dolt-hold"

// managedDoltHoldReadLimit bounds how much of the hold file is read. A real
// hold is a few lines; the cap keeps a wrong file (a log, a dump) from being
// slurped into memory by every tick.
const managedDoltHoldReadLimit = 4096

type managedDoltHoldState int

const (
	// managedDoltHoldNone: the hold file does not exist.
	managedDoltHoldNone managedDoltHoldState = iota
	// managedDoltHoldActive: the file exists and has no expiry, or an expiry
	// still in the future.
	managedDoltHoldActive
	// managedDoltHoldExpired: the file exists but its expires_at has passed.
	// Read-only: nothing here deletes the file.
	managedDoltHoldExpired
	// managedDoltHoldUnreadable: the file (or the path to it) could not be
	// read or understood. Blocks, like an active hold.
	managedDoltHoldUnreadable
)

func (s managedDoltHoldState) String() string {
	switch s {
	case managedDoltHoldNone:
		return "none"
	case managedDoltHoldActive:
		return "active"
	case managedDoltHoldExpired:
		return "expired"
	case managedDoltHoldUnreadable:
		return "unreadable"
	default:
		return "unknown"
	}
}

type managedDoltHold struct {
	State  managedDoltHoldState
	Path   string
	Owner  string
	Reason string
	// ExpiresUnix is expires_at in unix seconds, 0 when the hold has no
	// expiry. Deliberately not a time.Time: time.Unix adds 62135596800 to the
	// seconds, so a hand-written "forever" (MaxInt64) wraps to the year
	// -292277022657 and would read as long expired. Expiry is decided on these
	// integer seconds, exactly like the shell reader does.
	ExpiresUnix int64
	Err         error // set when State is managedDoltHoldUnreadable
}

// managedDoltHoldMaxDisplayUnix is 9999-12-31T23:59:59Z, the last second
// Describe prints as a date; a later expires_at is printed as raw seconds.
const managedDoltHoldMaxDisplayUnix = 253402300799

// managedDoltHoldSpace is the whitespace the shell reader trims: [:space:] in
// the C locale, which is what launchd runs the scripts in. strings.TrimSpace
// would also trim Unicode spaces (U+00A0, U+0085) that the shell keeps, and a
// value the shell cannot parse blocks while one Go trims could read as expired.
const managedDoltHoldSpace = " \t\n\v\f\r"

func trimManagedDoltHoldSpace(s string) string {
	return strings.Trim(s, managedDoltHoldSpace)
}

// Blocks reports whether lifecycle actions on the managed Dolt (start,
// recover, restart) must be refused.
func (h managedDoltHold) Blocks() bool {
	return h.State == managedDoltHoldActive || h.State == managedDoltHoldUnreadable
}

// Describe is the one-line human explanation used in logs and errors.
func (h managedDoltHold) Describe() string {
	var b strings.Builder
	switch h.State {
	case managedDoltHoldUnreadable:
		fmt.Fprintf(&b, "dolt hold file %s could not be read (%v); treating it as a hold", h.Path, h.Err)
	default:
		fmt.Fprintf(&b, "dolt is on hold (%s)", h.Path)
		if h.Owner != "" {
			fmt.Fprintf(&b, " by %s", h.Owner)
		}
		if h.Reason != "" {
			fmt.Fprintf(&b, ": %s", h.Reason)
		}
		switch {
		case h.ExpiresUnix <= 0:
		case h.ExpiresUnix <= managedDoltHoldMaxDisplayUnix:
			fmt.Fprintf(&b, "; expires %s", time.Unix(h.ExpiresUnix, 0).UTC().Format(time.RFC3339))
		default:
			fmt.Fprintf(&b, "; expires at unix %d", h.ExpiresUnix)
		}
		b.WriteString("; remove the file to release it")
	}
	return b.String()
}

// errManagedDoltHeld is wrapped by every error returned because a hold
// refused a lifecycle action; callers can tell "held on purpose" from
// "broken" with errors.Is.
var errManagedDoltHeld = errors.New("managed dolt hold active")

// managedDoltHoldNow is the clock hold expiry is judged against. Stubbable.
var managedDoltHoldNow = time.Now

// managedDoltHoldPath resolves the hold file for the city.
func managedDoltHoldPath(cityPath string) (string, error) {
	if override := strings.TrimSpace(os.Getenv("GC_DOLT_HOLD_FILE")); override != "" {
		return normalizePathForCompare(override), nil
	}
	layout, err := resolveManagedDoltRuntimeLayout(cityPath)
	if err != nil {
		return "", err
	}
	return filepath.Join(layout.PackStateDir, managedDoltHoldFileName), nil
}

// managedDoltHoldPathAbsent reports whether nothing at all is on the hold
// path: the parent directory is really gone, or it is there and the file is
// not. A symlink counts as something even when its target is missing, for the
// file, for the parent and for any directory above the parent, which is what
// the shell reader does (examples/bd/assets/scripts/dolt-hold.sh). Anything it
// cannot tell comes back as an error, which the caller reads as a hold.
func managedDoltHoldPathAbsent(path string) (bool, error) {
	dir := filepath.Dir(path)
	if _, err := os.Stat(dir); err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			return false, err
		}
		// The parent does not resolve: gone for real, or a link that leads
		// nowhere — its own, or one on a component above it, whose ENOENT
		// would otherwise read as the parent's. Only the first says "no hold".
		return managedDoltHoldAncestorsAbsent(dir, err)
	}
	if _, err := os.Lstat(path); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return true, nil
		}
		return false, err
	}
	return false, nil
}

// managedDoltHoldAncestorsAbsent decides what an ENOENT on dir means. It walks
// up to the nearest component that exists: if that is a real directory (or a
// link that resolves) the rest of the path really is gone and nobody can have
// put a hold under it; if it is a link that leads nowhere, the ENOENT is the
// link's and something IS on the path, so the answer is "cannot tell", which
// the caller reads as a hold. enoent is what Stat(dir) reported.
func managedDoltHoldAncestorsAbsent(dir string, enoent error) (bool, error) {
	for p := dir; ; {
		fi, err := os.Lstat(p)
		switch {
		case err == nil:
			if fi.Mode()&os.ModeSymlink != 0 {
				if _, serr := os.Stat(p); serr != nil {
					return false, enoent
				}
			}
			return true, nil
		case !errors.Is(err, fs.ErrNotExist):
			return false, err
		}
		up := filepath.Dir(p)
		if up == p {
			// Walked past the root without finding anything that exists:
			// not a path anybody could have put a hold on, but also not
			// something worth calling "no hold" on a guess.
			return false, enoent
		}
		p = up
	}
}

// readManagedDoltHold reads the hold for the city. It never returns a Go
// error: a failure to read is the managedDoltHoldUnreadable state, which
// blocks.
func readManagedDoltHold(cityPath string) managedDoltHold {
	// no resolvable path → unreadable (blocks): without the path there is no
	// way to know whether a hold exists, and the same layout is what start and
	// recover would use.
	path, err := managedDoltHoldPath(cityPath)
	if err != nil {
		return managedDoltHold{State: managedDoltHoldUnreadable, Err: err}
	}
	// file absent (ENOENT, nothing on the path) → no hold; ENOENT with a
	// symlink on the path, not a regular file (a FIFO), or stat/open failed for
	// any other reason (EACCES, EIO, ENOTDIR on the parent...) → unreadable,
	// which blocks.
	//
	// Stat comes first because opening a FIFO blocks until somebody writes to
	// it, which would hang the controller tick; the shell reader answers "not
	// a regular file → hold" without opening it too. Stat follows symlinks, as
	// Open does, so a link to a regular file still reads as that file.
	fi, err := os.Stat(path)
	var f *os.File
	if err == nil {
		if !fi.Mode().IsRegular() {
			return managedDoltHold{
				State: managedDoltHoldUnreadable,
				Path:  path,
				Err:   fmt.Errorf("not a regular file (%s)", fi.Mode()),
			}
		}
		f, err = os.Open(path)
	}
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			// Stat and Open follow symlinks, so ENOENT is also what a link
			// whose target is gone reports. Only a path with nothing on it
			// counts as "no hold".
			absent, aerr := managedDoltHoldPathAbsent(path)
			if aerr != nil {
				return managedDoltHold{State: managedDoltHoldUnreadable, Path: path, Err: aerr}
			}
			if absent {
				return managedDoltHold{State: managedDoltHoldNone, Path: path}
			}
			return managedDoltHold{State: managedDoltHoldUnreadable, Path: path, Err: err}
		}
		return managedDoltHold{State: managedDoltHoldUnreadable, Path: path, Err: err}
	}
	defer f.Close() //nolint:errcheck // read-only
	// empty file → an active hold with no expiry (a bare touch); read failed
	// (EIO...) → unreadable, which blocks. A directory never gets here: the
	// Stat above already called it not a regular file.
	data, err := io.ReadAll(io.LimitReader(f, managedDoltHoldReadLimit))
	if err != nil {
		return managedDoltHold{State: managedDoltHoldUnreadable, Path: path, Err: err}
	}
	return parseManagedDoltHold(path, data, managedDoltHoldNow())
}

// parseManagedDoltHold interprets the content of an existing hold file.
func parseManagedDoltHold(path string, data []byte, now time.Time) managedDoltHold {
	hold := managedDoltHold{State: managedDoltHoldActive, Path: path}
	sawExpiry := false
	for _, line := range strings.Split(string(data), "\n") {
		line = trimManagedDoltHoldSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = trimManagedDoltHoldSpace(key)
		value = trimManagedDoltHoldSpace(value)
		switch key {
		case "expires_at":
			if sawExpiry {
				continue // first one wins; the shell reader does the same
			}
			sawExpiry = true
			// no expires_at line → the hold lasts until the file is removed;
			// an expires_at that is blank, not plain digits or <= 0 →
			// unreadable (blocks): the hold is there, but when to release it
			// is unknown. Digits only, so the shell reader agrees on "+5".
			secs, err := strconv.ParseInt(value, 10, 64)
			if err != nil || secs <= 0 || strings.Trim(value, "0123456789") != "" {
				return managedDoltHold{
					State: managedDoltHoldUnreadable,
					Path:  path,
					Err:   fmt.Errorf("expires_at=%q is not a positive unix time", value),
				}
			}
			hold.ExpiresUnix = secs
		case "owner":
			hold.Owner = value
		case "reason":
			hold.Reason = value
		}
	}
	// Integer seconds on both sides: equal to "now" is expired, and any int64
	// the digits can spell (MaxInt64 included) compares correctly.
	if hold.ExpiresUnix > 0 && hold.ExpiresUnix <= now.Unix() {
		hold.State = managedDoltHoldExpired
	}
	return hold
}

// managedDoltHoldError is the error a refused lifecycle action returns.
func managedDoltHoldError(h managedDoltHold) error {
	return fmt.Errorf("%w: %s", errManagedDoltHeld, h.Describe())
}

// managedDoltHoldNoticeInterval limits how often a repeated, expected refusal
// (the controller tick fires every few seconds) is written to the log.
var managedDoltHoldNoticeInterval = 10 * time.Minute

var managedDoltHoldNoticeLast sync.Map // normalized city path -> time.Time

// noteManagedDoltHeld logs that a lifecycle action was skipped because of the
// hold, at most once per managedDoltHoldNoticeInterval per city.
func noteManagedDoltHeld(cityPath string, stderr io.Writer, logPrefix string, h managedDoltHold) {
	if stderr == nil {
		return
	}
	key := normalizePathForCompare(cityPath)
	now := managedDoltHoldNow()
	if v, ok := managedDoltHoldNoticeLast.Load(key); ok {
		if last, ok := v.(time.Time); ok && now.Sub(last) < managedDoltHoldNoticeInterval {
			return
		}
	}
	managedDoltHoldNoticeLast.Store(key, now)
	fmt.Fprintf(stderr, "%s: managed dolt not started/recovered: %s\n", logPrefix, h.Describe()) //nolint:errcheck // best-effort stderr
}
