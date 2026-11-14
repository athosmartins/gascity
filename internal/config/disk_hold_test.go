package config

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// ga-z1drlu7: the routed pool tier of the default work query, and the reconciler's
// count form (the default scale_check), hold back NEW STORIES while the free disk is
// under the new-work floor. The lib that reads the disk and owns the definition of a
// "story" (disk-headroom-lib.sh) lives in the city repo; here a stand-in with the same
// contract keeps the engine's plumbing testable alone:
//
//   - disk_headroom_state prints exactly ok | low | unknown;
//   - DISK_HOLD_NEW_STORIES_JQ is the jq filter that keeps the beads the pool may
//     still hand out (everything that is not a positively recognised story).
//
// Set GC_DISK_HEADROOM_LIB_REAL to the real lib's path to run every scenario against it
// as well (skipped otherwise). DISK_FREE_OVERRIDE_MB is the real lib's own test seam
// and the stand-in reads it too.

const dhStubLib = `DISK_HOLD_NEW_STORIES_JQ='[ .[] | select( (((.issue_type // "") as $t | ["task","bug","feature","chore","epic","spike"] | any(. == $t)) | not) or ((.metadata["gc.kind"] // "") != "") or ((.metadata["gc.step_ref"] // "") != "") or ((.metadata["gc.pool_demand"] // "") != "") ) ]'
disk_headroom_state() {
  if [ "${DISK_HEADROOM_ENABLED:-1}" = "0" ]; then printf ok; return 0; fi
  o="${DISK_FREE_OVERRIDE_MB:-}"
  case "$o" in ''|*[!0-9]*) printf unknown; return 0 ;; esac
  if [ "$o" -lt 5120 ]; then printf low; else printf ok; fi
}
`

const (
	// The hold text is gone: a low disk then has no way to say what is safe to hand out.
	dhEmptyHoldLib = `DISK_HOLD_NEW_STORIES_JQ=''
disk_headroom_state() { printf low; }
`
	// The hold text is not a jq program.
	dhBrokenHoldLib = `DISK_HOLD_NEW_STORIES_JQ='this is ( not jq'
disk_headroom_state() { printf low; }
`
	// An echo left in the lib: it prints while sourced, so the state is not exactly ok|low.
	dhBannerLib = "echo 'debug: disk-headroom-lib sourced'\n" + dhStubLib
	dhExitLib   = "exit 3\n"
)

type dhSite struct {
	name    string
	query   string
	env     map[string]string
	fixture string
	count   bool // the count form prints a number, not a one-bead array
}

func dhSites() []dhSite {
	worker := Agent{Name: "worker", Dir: "hello-world"}
	// WORK_ORDER_LIB at a file that does not exist: wo_pick keeps its previous order, which is not what is under test.
	worker1 := map[string]string{"GC_SESSION_ORIGIN": "ephemeral", "GC_SESSION_ID": "worker-bead", "WORK_ORDER_LIB": "/nonexistent/work-order.sh"}
	return []dhSite{
		{name: "work query (routed tier 1)", query: worker.EffectiveWorkQuery(), env: worker1, fixture: "routed"},
		{name: "routed pool query", query: worker.EffectiveRoutedPoolQuery(), env: worker1, fixture: "routed"},
		{name: "count form (scale_check)", query: worker.EffectivePoolDemandQuery(), env: map[string]string{}, fixture: "routed", count: true},
	}
}

// dhBead is a routed, unassigned bead. meta is the extra metadata, e.g. `"gc.step_ref":"x"`.
func dhBead(id, typ, meta string) string {
	m := `"gc.routed_to":"hello-world/worker"`
	if meta != "" {
		m += "," + meta
	}
	return fmt.Sprintf(`{"id":%q,"priority":1,"issue_type":%q,"created_at":"2026-11-09T12:00:00Z","updated_at":"2026-11-09T12:00:00Z","labels":[],"assignee":"","metadata":{%s},"dependencies":[]}`, id, typ, m)
}

var (
	dhStory   = dhBead("story-1", "bug", "")
	dhFormula = dhBead("formula-step", "task", `"gc.step_ref":"mol-x.step-1"`)
)

func (s dhSite) run(t *testing.T, lib string, extra map[string]string, beads ...string) woRun {
	t.Helper()
	return s.runQuery(t, s.query, lib, extra, beads...)
}

func (s dhSite) runQuery(t *testing.T, query, lib string, extra map[string]string, beads ...string) woRun {
	t.Helper()
	env := map[string]string{}
	for k, v := range s.env {
		env[k] = v
	}
	if lib != "" {
		env["DISK_HEADROOM_LIB"] = lib
	}
	for k, v := range extra {
		env[k] = v
	}
	return runWorkOrderQuery(t, query, env, s.fixture, "["+strings.Join(beads, ",")+"]")
}

// got is what the site answered: the head bead's id ("" = nothing) for a probe, the
// demand as a decimal string for the count form.
func (s dhSite) got(r woRun) string {
	if s.count {
		return strings.TrimSpace(r.stdout)
	}
	if len(r.ids) == 0 {
		return ""
	}
	return r.ids[0]
}

func writeDiskLib(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "disk-headroom-lib.sh")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write lib: %v", err)
	}
	return path
}

// want is the expected answer for a probe (head id) and for the count form (demand).
type dhWant struct {
	head  string
	count string
}

func (w dhWant) forSite(s dhSite) string {
	if s.count {
		return w.count
	}
	return w.head
}

type dhCase struct {
	name     string
	lib      string // lib body; "" = the stand-in
	libPath  string // an explicit path instead (e.g. one that does not exist)
	env      map[string]string
	beads    []string
	want     dhWant
	wantWarn string // a substring of "disk-hold WARN:" the stderr must carry; "" = no disk-hold WARN at all
}

func dhCases() []dhCase {
	low := map[string]string{"DISK_FREE_OVERRIDE_MB": "2000"}
	ok := map[string]string{"DISK_FREE_OVERRIDE_MB": "9000"}
	return []dhCase{
		{name: "low: a story is held back, nothing is offered or counted", env: low, beads: []string{dhStory}, want: dhWant{head: "", count: "0"}},
		{name: "low: formula work still flows", env: low, beads: []string{dhFormula}, want: dhWant{head: "formula-step", count: "1"}},
		{name: "low: a story and formula work: the formula bead is what is offered", env: low, beads: []string{dhStory, dhFormula}, want: dhWant{head: "formula-step", count: "1"}},
		{name: "ok: the story is offered and counted", env: ok, beads: []string{dhStory}, want: dhWant{head: "story-1", count: "1"}},
		{name: "exactly at the floor is ok", env: map[string]string{"DISK_FREE_OVERRIDE_MB": "5120"}, beads: []string{dhStory}, want: dhWant{head: "story-1", count: "1"}},
		{name: "kill switch DISK_HEADROOM_ENABLED=0 on a low disk", env: map[string]string{"DISK_FREE_OVERRIDE_MB": "2000", "DISK_HEADROOM_ENABLED": "0"}, beads: []string{dhStory}, want: dhWant{head: "story-1", count: "1"}},
		// Fail OPEN, never closed and never a crash: a df hiccup must not starve the pool.
		{name: "unreadable reading: fail-open + WARN", env: map[string]string{"DISK_FREE_OVERRIDE_MB": "unreadable"}, beads: []string{dhStory}, want: dhWant{head: "story-1", count: "1"}, wantWarn: "unreadable"},
		{name: "lib file absent: fail-open + WARN", libPath: "/nonexistent/disk-headroom-lib.sh", env: low, beads: []string{dhStory}, want: dhWant{head: "story-1", count: "1"}, wantWarn: "unreadable"},
		{name: "lib prints a banner while sourced: not exactly ok|low, fail-open + WARN", lib: dhBannerLib, env: low, beads: []string{dhStory}, want: dhWant{head: "story-1", count: "1"}, wantWarn: "unreadable"},
		{name: "lib exits at source: fail-open + WARN", lib: dhExitLib, env: low, beads: []string{dhStory}, want: dhWant{head: "story-1", count: "1"}, wantWarn: "unreadable"},
		// Low but the hold cannot be applied: INERT. Not "go ahead", not "no work" by accident, not a crash.
		{name: "low, hold text empty: offers nothing + WARN", lib: dhEmptyHoldLib, env: nil, beads: []string{dhStory}, want: dhWant{head: "", count: "0"}, wantWarn: "could not be applied"},
		{name: "low, hold text is not jq: offers nothing + WARN", lib: dhBrokenHoldLib, env: nil, beads: []string{dhStory}, want: dhWant{head: "", count: "0"}, wantWarn: "could not be applied"},
		{name: "low, hold text empty, formula work present: still nothing (inert)", lib: dhEmptyHoldLib, env: nil, beads: []string{dhFormula}, want: dhWant{head: "", count: "0"}, wantWarn: "could not be applied"},
	}
}

func dhLibFor(t *testing.T, c dhCase) string {
	t.Helper()
	switch {
	case c.libPath != "":
		return c.libPath
	case c.lib != "":
		return writeDiskLib(t, c.lib)
	default:
		return writeDiskLib(t, dhStubLib)
	}
}

func TestDiskHoldEverySiteEveryState(t *testing.T) {
	for _, s := range dhSites() {
		for _, c := range dhCases() {
			t.Run(s.name+"/"+c.name, func(t *testing.T) {
				r := s.run(t, dhLibFor(t, c), c.env, c.beads...)
				if got, want := s.got(r), c.want.forSite(s); got != want {
					t.Fatalf("answer = %q, want %q\nstdout: %s\nstderr: %s", got, want, r.stdout, r.stderr)
				}
				if c.wantWarn == "" {
					if strings.Contains(r.stderr, "disk-hold WARN:") {
						t.Fatalf("unexpected disk-hold WARN on stderr: %s", r.stderr)
					}
					return
				}
				if !regexp.MustCompile(`disk-hold WARN:[^\n]*` + regexp.QuoteMeta(c.wantWarn)).MatchString(r.stderr) {
					t.Fatalf("stderr lacks a disk-hold WARN that says %q: %s", c.wantWarn, r.stderr)
				}
			})
		}
	}
}

// The default query's tier 1 (and only it) is held; assigned work, the crash-recovery
// path and the pre-assigned beads, is NEVER held, whatever the disk says.
func TestDiskHoldNeverHoldsAssignedWork(t *testing.T) {
	worker := Agent{Name: "worker", Dir: "hello-world"}
	env := map[string]string{
		"GC_SESSION_ORIGIN": "ephemeral", "GC_SESSION_ID": "worker-bead",
		"WORK_ORDER_LIB": "/nonexistent/work-order.sh", "DISK_FREE_OVERRIDE_MB": "2000",
	}
	story := `{"id":"assigned-story","priority":1,"issue_type":"bug","created_at":"2026-11-09T12:00:00Z","updated_at":"2026-11-09T12:00:00Z","labels":[],"assignee":"worker-bead","metadata":{},"dependencies":[]}`
	lib := writeDiskLib(t, dhStubLib)
	env["DISK_HEADROOM_LIB"] = lib
	for _, tc := range []struct{ name, query, fixture string }{
		{"work query -> assigned in-progress", worker.EffectiveWorkQuery(), "inprogress"},
		{"work query -> assigned ready", worker.EffectiveWorkQuery(), "ready"},
		{"assigned in-progress query", worker.EffectiveAssignedInProgressQuery(), "inprogress"},
		{"assigned ready query", worker.EffectiveAssignedReadyQuery(), "ready"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := runWorkOrderQuery(t, tc.query, env, tc.fixture, "["+story+"]")
			if len(r.ids) != 1 || r.ids[0] != "assigned-story" {
				t.Fatalf("assigned story held back on a low disk: ids=%v\nstdout: %s\nstderr: %s", r.ids, r.stdout, r.stderr)
			}
		})
	}
	// The two stand-alone assigned queries do not even carry the stage.
	for _, q := range []string{worker.EffectiveAssignedInProgressQuery(), worker.EffectiveAssignedReadyQuery()} {
		if strings.Contains(q, "disk_hold") {
			t.Fatalf("an assigned-work query carries the disk hold: %s", q)
		}
	}
}

func TestDiskHoldIsDefinedBeforeEveryUse(t *testing.T) {
	use := regexp.MustCompile(`\| disk_hold\b`)
	for _, s := range dhSites() {
		t.Run(s.name, func(t *testing.T) {
			def := strings.Index(s.query, "disk_hold() {")
			loc := use.FindStringIndex(s.query)
			if def < 0 || loc == nil {
				t.Fatalf("query lacks the disk_hold definition (%d) or its use (%v): %s", def, loc, s.query)
			}
			if def > loc[0] {
				t.Fatalf("disk_hold is used (offset %d) before it is defined (offset %d)", loc[0], def)
			}
			if strings.Count(s.query, "disk_hold() {") != 1 {
				t.Fatalf("disk_hold defined %d times, want 1", strings.Count(s.query, "disk_hold() {"))
			}
		})
	}
}

// With the disk low, a held-back probe leaves a line for whoever looks at the log
// (the hook runner drops stderr on success, so the WARN alone leaves no trace); with
// the disk fine it writes nothing.
func TestDiskHoldLogsOnlyWhenItActs(t *testing.T) {
	s := dhSites()[0]
	lib := writeDiskLib(t, dhStubLib)
	for _, tc := range []struct {
		name     string
		free     string
		wantLine string
	}{
		{"low", "2000", "hold applied: new stories held back"},
		{"unknown", "unreadable", "fail-open"},
		{"ok", "9000", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			logPath := filepath.Join(t.TempDir(), "disk-hold.log")
			// the dir exists (t.TempDir), the file does not: the append creates it
			s.run(t, lib, map[string]string{"DISK_FREE_OVERRIDE_MB": tc.free, "DISK_HOLD_LOG": logPath}, dhStory)
			b, err := os.ReadFile(logPath)
			if tc.wantLine == "" {
				if err == nil {
					t.Fatalf("log written on an ok disk: %s", b)
				}
				return
			}
			if err != nil || !strings.Contains(string(b), tc.wantLine) {
				t.Fatalf("log = %q (err %v), want a line with %q", b, err, tc.wantLine)
			}
		})
	}
	t.Run("low, hold unusable", func(t *testing.T) {
		logPath := filepath.Join(t.TempDir(), "disk-hold.log")
		s.run(t, writeDiskLib(t, dhEmptyHoldLib), map[string]string{"DISK_HOLD_LOG": logPath}, dhStory)
		if b, err := os.ReadFile(logPath); err != nil || !strings.Contains(string(b), "hold could not be applied") {
			t.Fatalf("log = %q (err %v), want a line with %q", b, err, "hold could not be applied")
		}
	})
	t.Run("unwritable log dir does not change the answer", func(t *testing.T) {
		r := s.run(t, lib, map[string]string{"DISK_FREE_OVERRIDE_MB": "2000", "DISK_HOLD_LOG": "/nonexistent-dir/disk-hold.log"}, dhStory)
		if got := s.got(r); got != "" {
			t.Fatalf("answer = %q, want nothing (held back)", got)
		}
	})
}

// The stand-in is the contract, the real lib is the truth: run the headline scenarios
// against it when its path is given.
func TestDiskHoldAgainstTheRealLib(t *testing.T) {
	real := os.Getenv("GC_DISK_HEADROOM_LIB_REAL")
	if real == "" {
		t.Skip("GC_DISK_HEADROOM_LIB_REAL not set")
	}
	if _, err := os.Stat(real); err != nil {
		t.Fatalf("GC_DISK_HEADROOM_LIB_REAL=%q: %v", real, err)
	}
	for _, s := range dhSites() {
		for _, c := range dhCases() {
			if c.lib != "" || c.libPath != "" {
				continue // those scenarios swap the lib itself
			}
			t.Run(s.name+"/"+c.name, func(t *testing.T) {
				r := s.run(t, real, c.env, c.beads...)
				if got, want := s.got(r), c.want.forSite(s); got != want {
					t.Fatalf("answer = %q, want %q\nstdout: %s\nstderr: %s", got, want, r.stdout, r.stderr)
				}
			})
		}
	}
}

// Every guard above must be able to fail: each mutant of the generated query has to be
// caught by the scenario that pins the branch it breaks (the scenario's expected answer
// must NOT be what the mutant produces).
func TestDiskHoldMutationControl(t *testing.T) {
	type mutant struct {
		name     string
		mutate   func(string) string
		scenario dhCase
	}
	replaceOnce := func(old, new string) func(string) string {
		return func(q string) string {
			if n := strings.Count(q, old); n != 1 {
				t.Fatalf("mutant anchor %q found %d times, want 1", old, n)
			}
			return strings.Replace(q, old, new, 1)
		}
	}
	low := map[string]string{"DISK_FREE_OVERRIDE_MB": "2000"}
	mutants := []mutant{
		{
			name:     "the low state passes the array through",
			mutate:   replaceOnce(`low) ;;`, `low) printf "%s" "$dh_in"; return 0;;`),
			scenario: dhCase{env: low, beads: []string{dhStory}, want: dhWant{head: "", count: "0"}},
		},
		{
			name:     "the unknown state fails closed (offers nothing)",
			mutate:   replaceOnce(`|| true; printf "%s" "$dh_in"; return 0;; esac;`, `|| true; printf "[]"; return 0;; esac;`),
			scenario: dhCase{env: map[string]string{"DISK_FREE_OVERRIDE_MB": "unreadable"}, beads: []string{dhStory}, want: dhWant{head: "story-1", count: "1"}},
		},
		{
			name:     "an unusable hold falls through to the whole array",
			mutate:   replaceOnce(`|| true; printf "[]"; `, `|| true; printf "%s" "$dh_in"; `),
			scenario: dhCase{lib: dhEmptyHoldLib, beads: []string{dhStory}, want: dhWant{head: "", count: "0"}},
		},
		{
			name:     "the hold keeps stories and drops formula work",
			mutate:   replaceOnce(`jq -c "$dh_jq"`, `jq -c "map(select(.issue_type == \"bug\"))"`),
			scenario: dhCase{env: low, beads: []string{dhFormula}, want: dhWant{head: "formula-step", count: "1"}},
		},
		{
			name:     "the stage is removed from the pipeline",
			mutate:   replaceOnce(`| disk_hold`, `| cat`),
			scenario: dhCase{env: low, beads: []string{dhStory}, want: dhWant{head: "", count: "0"}},
		},
	}
	for _, s := range dhSites() {
		for _, m := range mutants {
			t.Run(s.name+"/"+m.name, func(t *testing.T) {
				// The scenario must PASS on the real query (it is a real guard) ...
				good := s.run(t, dhLibFor(t, m.scenario), m.scenario.env, m.scenario.beads...)
				if got, want := s.got(good), m.scenario.want.forSite(s); got != want {
					t.Fatalf("the unmutated query already answers %q, scenario wants %q\nstderr: %s", got, want, good.stderr)
				}
				// ... and must FAIL on the mutant.
				r := s.runQuery(t, m.mutate(s.query), dhLibFor(t, m.scenario), m.scenario.env, m.scenario.beads...)
				if got, want := s.got(r), m.scenario.want.forSite(s); got == want {
					t.Fatalf("mutant %q survived: the scenario still answers %q\nstderr: %s", m.name, got, r.stderr)
				}
			})
		}
	}
}
