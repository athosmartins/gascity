package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// ga-9t9acg.7: the dog pool probe and the assigned-work queries order their
// survivors with the town's work-order lib (priority > type > age, via
// `work_order_sort --age reclaim`) instead of bd's own order. These tests drive
// the REAL generated shell commands through a fake `bd`, so they pin behavior,
// not just strings. The lib itself lives in the city repo
// (packs/town-deltas/assets/scripts/work-order.sh, its own selftest); here a
// stand-in implements the same contract so the engine's plumbing is tested in
// isolation:
//
//   - stdout = the JSON array, ordered;
//   - empty stdout + exit 2 = "cannot tell" — the caller must keep its previous
//     order and say so on stderr, never turn it into "no bead".
//
// Set GC_WORK_ORDER_LIB_REAL to the real lib's path to run the ordering
// scenarios against it as well (skipped otherwise).

// woStubLib is the stand-in for work-order.sh: the rule in miniature.
const woStubLib = `work_order_sort() {
  age=created
  while [ $# -gt 0 ]; do
    case "$1" in
      --age) age="$2"; shift 2 ;;
      --age=*) age="${1#--age=}"; shift ;;
      *) echo "stub: bad argument $1" >&2; return 2 ;;
    esac
  done
  case "$age" in created|field|reclaim) ;; *) echo "stub: bad --age $age" >&2; return 2 ;; esac
  jq -c --arg age "$age" '
    def reclaims: [(.labels // [])[] | select(startswith("pilot:reclaim-count:")) | ltrimstr("pilot:reclaim-count:") | (tonumber? // 0)] | max // 0;
    def agekey: if $age == "created" then (.created_at // "")
                elif $age == "field" then (.updated_at // .created_at // "")
                elif reclaims >= 1 then (.updated_at // .created_at // "")
                else (.created_at // "") end;
    sort_by(.priority // 99, (if (.issue_type // "") == "feature" then 0 else 1 end), agekey)
  '
}
`

// Libs that cannot tell: each must degrade to the previous order plus a WARN.
const (
	woErroringLib     = "work_order_sort() { cat >/dev/null; echo 'work-order ERROR: stub: cannot tell' >&2; return 2; }\n"
	woSyntaxBrokenLib = "work_order_sort() { this is ( not valid\n"
	woExitingLib      = "exit 3\n"
)

// Libs whose stdout is NOT the one JSON array of the input's length that the
// next stage (`jq '.[0:1]'`) acts on. Every one is "cannot tell". Several print
// something NON-EMPTY, which a `[ -n "$out" ]` check reads as success and jq then
// turns into `[]`: the dog is told "no work" while beads exist (gate review of
// ga-9t9acg.7, round 2: the variable decided on was not the variable acted on).
const (
	// An `echo` left in a work-order.sh edited in place: it prints while sourced.
	woBannerLib = "echo 'debug: work-order.sh sourced'\n" + woStubLib
	// ...but the banner can be valid JSON, so `jq -e` alone would judge the array after it.
	woJSONBannerLib = "echo 1\n" + woStubLib
	// Non-JSON on stdout, then the failure; and the same without failing.
	woNonJSONExit2Lib = "work_order_sort() { cat >/dev/null; echo 'sort failed: not json'; return 2; }\n"
	woNonJSONExit0Lib = "work_order_sort() { cat >/dev/null; echo 'ok'; }\n"
	// An answer that would change the head (the input reversed), with the wrong status:
	// the lib's contract says success is exit 0, so the answer is not taken.
	woRightArrayExit1Lib = "work_order_sort() { jq -c 'reverse'; return 1; }\n"
	// Two documents on stdout (the input array printed twice): a head taken from it is two lines.
	woTwoArraysLib = `work_order_sort() { in=$(cat); printf '%s\n' "$in"; printf '%s\n' "$in"; }` + "\n"
	// An array that is not a permutation of the input: `[]` is "no bead" to the dog.
	woEmptyArrayLib   = "work_order_sort() { cat >/dev/null; echo '[]'; }\n"
	woInventedBeadLib = `work_order_sort() { cat >/dev/null; echo '[{"id":"ghost"}]'; }` + "\n"
)

// woFakeBd serves $FX_DIR/<kind>.json for the query shape it is asked, honoring
// --limit N / --limit=N (0 = unlimited, as in bd), and logs every call so a test
// can prove no ordering query ever asked for a window.
const woFakeBd = `#!/bin/sh
echo "$*" >> "$FX_DIR/calls.log"
lim=0
prev=""
for a in "$@"; do
  case "$a" in --limit=*) lim="${a#--limit=}" ;; esac
  if [ "$prev" = "--limit" ]; then lim="$a"; fi
  prev="$a"
done
case "$*" in
  "ready --metadata-field gc.routed_to="*|"ready --include-ephemeral --metadata-field gc.routed_to="*) f=routed ;;
  "ready --metadata-field gc.run_target="*|"ready --include-ephemeral --metadata-field gc.run_target="*) f=migration ;;
  "ready "*"--assignee="*) f=ready ;;
  "list --status in_progress --assignee="*) f=inprogress ;;
  "query --json ephemeral=true AND status=in_progress"*) f=eph_inprogress ;;
  "query --json ephemeral=true AND status=open AND assignee=none"*) f=eph_unassigned ;;
  "query --json ephemeral=true AND status=open"*) f=eph_open ;;
  *) f= ;;
esac
if [ -z "$f" ] || [ ! -r "$FX_DIR/$f.json" ]; then printf '[]'; exit 0; fi
if [ "$lim" -gt 0 ] 2>/dev/null; then jq -c ".[:$lim]" "$FX_DIR/$f.json"; else jq -c . "$FX_DIR/$f.json"; fi
`

type woRun struct {
	ids    []string
	stdout string
	stderr string
	calls  string
}

// runWorkOrderQuery runs one generated query under a hermetic environment with
// the fake bd serving body as fixture.json.
func runWorkOrderQuery(t *testing.T, query string, env map[string]string, fixture, body string) woRun {
	t.Helper()
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq not available; the work queries are jq pipelines")
	}
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available; wo_pick runs the lib in a child bash")
	}

	tmp := t.TempDir()
	binDir := filepath.Join(tmp, "bin")
	fxDir := filepath.Join(tmp, "fx")
	for _, d := range []string{binDir, fxDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
	}
	if err := os.WriteFile(filepath.Join(binDir, "bd"), []byte(woFakeBd), 0o755); err != nil {
		t.Fatalf("write fake bd: %v", err)
	}
	if fixture != "" {
		if err := os.WriteFile(filepath.Join(fxDir, fixture+".json"), []byte(body), 0o644); err != nil {
			t.Fatalf("write fixture: %v", err)
		}
	}

	cmd := exec.Command("sh", "-c", query)
	cmd.Env = []string{"PATH=" + binDir + ":" + os.Getenv("PATH"), "FX_DIR=" + fxDir}
	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("run query: %v\nstderr: %s", err, stderr.String())
	}

	run := woRun{stdout: stdout.String(), stderr: stderr.String()}
	if calls, err := os.ReadFile(filepath.Join(fxDir, "calls.log")); err == nil {
		run.calls = string(calls)
	}
	var rows []struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(run.stdout)), &rows); err == nil {
		for _, r := range rows {
			run.ids = append(run.ids, r.ID)
		}
	}
	return run
}

func writeWorkOrderLib(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "work-order.sh")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write lib: %v", err)
	}
	return path
}

// woSite is one place the engine picks a bead: the generated command, the
// environment it needs, and the fixture file + bead shape the fake bd serves it.
type woSite struct {
	name     string
	query    string
	env      map[string]string
	fixture  string
	assignee string
	meta     string
}

func (s woSite) bead(id string, prio int, typ, created, updated, labels string) string {
	return fmt.Sprintf(`{"id":%q,"priority":%d,"issue_type":%q,"created_at":%q,"updated_at":%q,"labels":[%s],"assignee":%q,"metadata":%s,"dependencies":[]}`,
		id, prio, typ, created, updated, labels, s.assignee, s.meta)
}

func (s woSite) run(t *testing.T, lib string, beads []string) woRun {
	t.Helper()
	return s.runWithEnv(t, lib, beads, nil)
}

// runWithEnv is run plus extra environment for the query (e.g. a BASH_ENV file).
func (s woSite) runWithEnv(t *testing.T, lib string, beads []string, extra map[string]string) woRun {
	t.Helper()
	env := map[string]string{}
	for k, v := range s.env {
		env[k] = v
	}
	for k, v := range extra {
		env[k] = v
	}
	if lib != "" {
		env["WORK_ORDER_LIB"] = lib
	}
	return runWorkOrderQuery(t, s.query, env, s.fixture, "["+strings.Join(beads, ",")+"]")
}

// woSites lists every picking site ga-9t9acg.7 puts behind the rule: the three
// pool-probe tiers, the assigned in-progress / ready queries (standard,
// bd-1.0.5, legacy control-dispatcher) and the ephemeral-wisp siblings, both as
// the stand-alone prompt queries and as the tiers of the full work query.
func woSites() []woSite {
	worker := Agent{Name: "worker", Dir: "hello-world"}
	control := Agent{Name: ControlDispatcherAgentName, Dir: "gascity"}
	routedMeta := `{"gc.routed_to":"hello-world/worker"}`
	migrationMeta := `{"gc.kind":"workflow","gc.run_target":"hello-world/worker","gc.routed_to":""}`
	worker1 := map[string]string{"GC_SESSION_ORIGIN": "ephemeral", "GC_SESSION_ID": "worker-bead"}
	legacy := map[string]string{"GC_SESSION_ORIGIN": "ephemeral", "GC_SESSION_NAME": "gascity--control-dispatcher"}
	bd105 := BeadsConfig{BDCompatibility: BeadsBDCompatibility105}
	return []woSite{
		{name: "pool tier 1 (work query)", query: worker.EffectiveWorkQuery(), env: worker1, fixture: "routed", meta: routedMeta},
		{name: "pool tier 1 (routed pool query)", query: worker.EffectiveRoutedPoolQuery(), env: worker1, fixture: "routed", meta: routedMeta},
		{name: "pool tier 2 (run_target migration)", query: worker.EffectiveWorkQuery(), env: worker1, fixture: "migration", meta: migrationMeta},
		{name: "pool tier 3 (ephemeral wisps)", query: worker.EffectiveWorkQuery(), env: worker1, fixture: "eph_unassigned", meta: routedMeta},
		{name: "assigned in-progress", query: worker.EffectiveAssignedInProgressQuery(), env: worker1, fixture: "inprogress", assignee: "worker-bead", meta: "{}"},
		{name: "assigned ready", query: worker.EffectiveAssignedReadyQuery(), env: worker1, fixture: "ready", assignee: "worker-bead", meta: "{}"},
		{name: "assigned ready (bd 1.0.5)", query: worker.EffectiveAssignedReadyQueryForBeads(bd105), env: worker1, fixture: "ready", assignee: "worker-bead", meta: "{}"},
		{name: "work query -> assigned in-progress", query: worker.EffectiveWorkQuery(), env: worker1, fixture: "inprogress", assignee: "worker-bead", meta: "{}"},
		{name: "work query -> assigned ready", query: worker.EffectiveWorkQuery(), env: worker1, fixture: "ready", assignee: "worker-bead", meta: "{}"},
		{name: "ephemeral assigned in-progress", query: worker.EffectiveAssignedInProgressQuery(), env: worker1, fixture: "eph_inprogress", assignee: "worker-bead", meta: "{}"},
		{name: "ephemeral assigned ready", query: worker.EffectiveAssignedReadyQuery(), env: worker1, fixture: "eph_open", assignee: "worker-bead", meta: "{}"},
		{name: "legacy control assigned in-progress", query: control.EffectiveAssignedInProgressQuery(), env: legacy, fixture: "inprogress", assignee: "gascity--control-dispatcher", meta: "{}"},
		{name: "legacy control assigned ready", query: control.EffectiveAssignedReadyQuery(), env: legacy, fixture: "ready", assignee: "gascity--control-dispatcher", meta: "{}"},
	}
}

type woScenario struct {
	name  string
	beads func(s woSite) []string
	want  string
}

func woScenarios() []woScenario {
	return []woScenario{
		{
			// The bead's headline case: same priority, the feature is NEWER than the
			// bug. bd's default order (and the old LRU) serves the bug first.
			name: "P0 feature newer than P0 bug",
			beads: func(s woSite) []string {
				return []string{
					s.bead("p0-bug-older", 0, "bug", "2026-09-01T12:00:00Z", "2026-09-01T12:00:00Z", ""),
					s.bead("p0-feature-newer", 0, "feature", "2026-09-30T12:00:00Z", "2026-09-30T12:00:00Z", ""),
				}
			},
			want: "p0-feature-newer",
		},
		{
			// 25 beads: the P0 feature is the NEWEST, i.e. the 25th by age — past any
			// 20-row window cut by age (ga-g7yt's window was taken before ordering).
			name: "25 beads, P0 feature last by age",
			beads: func(s woSite) []string {
				var beads []string
				for i := 0; i < 24; i++ {
					ts := fmt.Sprintf("2026-09-%02dT10:00:00Z", i+1)
					beads = append(beads, s.bead(fmt.Sprintf("p2-bug-%02d", i), 2, "bug", ts, ts, ""))
				}
				return append(beads, s.bead("p0-feature-newest-of-25", 0, "feature", "2026-09-30T10:00:00Z", "2026-09-30T10:00:00Z", ""))
			},
			want: "p0-feature-newest-of-25",
		},
		{
			// ga-w4k2z LRU, kept only for a bead carrying pilot:reclaim-count: a bead
			// reclaimed twice (updated_at bumped by each reclaim, created_at not)
			// goes behind an untouched peer.
			name: "reclaimed twice goes behind its untouched peer",
			beads: func(s woSite) []string {
				return []string{
					s.bead("p1-bug-reclaimed", 1, "bug", "2026-09-01T12:00:00Z", "2026-10-05T12:00:00Z", `"pilot:reclaim-count:2"`),
					s.bead("p1-bug-untouched", 1, "bug", "2026-09-20T12:00:00Z", "2026-09-20T12:00:00Z", ""),
				}
			},
			want: "p1-bug-untouched",
		},
		{
			// ...but updated_at alone (a comment, a label) is NOT a reclaim: age stays
			// created_at, so the older bead keeps its place.
			name: "touched but never reclaimed keeps its place",
			beads: func(s woSite) []string {
				return []string{
					s.bead("p1-bug-commented", 1, "bug", "2026-09-01T12:00:00Z", "2026-10-05T12:00:00Z", ""),
					s.bead("p1-bug-newer", 1, "bug", "2026-09-20T12:00:00Z", "2026-09-20T12:00:00Z", ""),
				}
			},
			want: "p1-bug-commented",
		},
		{
			// pilot:starvation-count is NOT a reclaim. inflight-reclaim-guard.py
			// do_reclaim stamps it INSTEAD of pilot:reclaim-count for a bead no worker
			// ever claimed (audit-only: nothing reads it), so the `reclaim` age rule
			// leaves such a bead on created_at and it keeps its place. Pinned because
			// it is a decision, not an accident: the old tier 1 demoted any bead whose
			// updated_at had moved (see the README, "Decision" table).
			name: "starvation-count alone is not a reclaim",
			beads: func(s woSite) []string {
				return []string{
					s.bead("p1-bug-starved", 1, "bug", "2026-09-01T12:00:00Z", "2026-10-05T12:00:00Z", `"pilot:starvation-count:1"`),
					s.bead("p1-bug-newer", 1, "bug", "2026-09-20T12:00:00Z", "2026-09-20T12:00:00Z", ""),
				}
			},
			want: "p1-bug-starved",
		},
		{
			// The vetoes still run BEFORE the order: a refused P0 feature never wins.
			name: "refused P0 feature stays vetoed",
			beads: func(s woSite) []string {
				return []string{
					s.bead("plain-p2-bug", 2, "bug", "2026-09-01T12:00:00Z", "2026-09-01T12:00:00Z", ""),
					s.bead("refused-p0-feature", 0, "feature", "2026-09-30T12:00:00Z", "2026-09-30T12:00:00Z", `"pool:refused:engine-rebuild-required"`),
				}
			},
			want: "plain-p2-bug",
		},
	}
}

func woLibsUnderTest(t *testing.T) map[string]string {
	t.Helper()
	libs := map[string]string{"stub": writeWorkOrderLib(t, woStubLib)}
	if real := os.Getenv("GC_WORK_ORDER_LIB_REAL"); real != "" {
		if _, err := os.Stat(real); err != nil {
			t.Fatalf("GC_WORK_ORDER_LIB_REAL=%q: %v", real, err)
		}
		libs["real"] = real
	}
	return libs
}

// TestWorkOrderPickOrdersEverySiteByTheRule is the ga-9t9acg.7 acceptance test:
// at every site that picks a bead, the head is the one the rule puts first.
// Fails on the prior HEAD (bd's order / LRU / a 20-row window).
func TestWorkOrderPickOrdersEverySiteByTheRule(t *testing.T) {
	for libName, lib := range woLibsUnderTest(t) {
		for _, s := range woSites() {
			for _, sc := range woScenarios() {
				t.Run(libName+"/"+s.name+"/"+sc.name, func(t *testing.T) {
					run := s.run(t, lib, sc.beads(s))
					if len(run.ids) != 1 || run.ids[0] != sc.want {
						t.Fatalf("head = %v, want [%s]\nstdout: %s\nstderr: %s", run.ids, sc.want, run.stdout, run.stderr)
					}
				})
			}
		}
	}
}

// TestWorkOrderPickFetchesWholePopulation: ordering needs the whole population,
// so no ordering query may ask bd for a window (--limit N, N>0) — the
// ga-g7yt filter-then-slice trap again, one level up. The molecule-live-step
// existence probe (`bd list --parent … --limit 1`) is not an ordering query.
func TestWorkOrderPickFetchesWholePopulation(t *testing.T) {
	window := regexp.MustCompile(`--limit[= ][1-9]`)
	scenario := woScenarios()[1]
	lib := writeWorkOrderLib(t, woStubLib)
	for _, s := range woSites() {
		t.Run(s.name, func(t *testing.T) {
			run := s.run(t, lib, scenario.beads(s))
			if !strings.Contains(run.calls, "--limit 0") && !strings.Contains(run.calls, "--limit=0") {
				t.Fatalf("no whole-population call logged: %q", run.calls)
			}
			for _, line := range strings.Split(run.calls, "\n") {
				if line == "" || strings.HasPrefix(line, "list --parent") {
					continue
				}
				if window.MatchString(line) {
					t.Errorf("bd was asked for a window before ordering: %q", line)
				}
			}
		})
	}
}

// TestWorkOrderPickFallsBackWhenLibAbsent: no lib must never mean no work. The
// query keeps the PREVIOUS order (LRU for the routed tier, bd's own for the
// rest — here both put the older bug first) and says so on stderr.
func TestWorkOrderPickFallsBackWhenLibAbsent(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "no-such-dir", "work-order.sh")
	for _, s := range woSites() {
		t.Run(s.name, func(t *testing.T) {
			sc := woScenarios()[0]
			run := s.run(t, missing, sc.beads(s))
			if len(run.ids) != 1 || run.ids[0] != "p0-bug-older" {
				t.Fatalf("head = %v, want the previous order's [p0-bug-older]\nstdout: %q\nstderr: %q", run.ids, run.stdout, run.stderr)
			}
			if !strings.Contains(run.stderr, "work-order WARN: wo_pick: work-order.sh not readable") {
				t.Fatalf("stderr = %q, want the wo_pick WARN", run.stderr)
			}
		})
	}
}

// TestWorkOrderPickFallsBackWhenLibErrors: a lib that cannot tell (exit 2 and
// empty stdout), does not parse, exits while being sourced, or answers with
// anything but ONE JSON array of the input's length is the same "don't know" —
// previous order, WARN, never an empty answer. The lib runs in a child bash, so
// none of these can end the query's own sh. The rows from "banner" on are the
// gate's round-2 finding: stdout NON-empty but not the array the next stage
// takes the head of, which an `-n` check trusted and `jq '.[0:1]'` turned into `[]`.
func TestWorkOrderPickFallsBackWhenLibErrors(t *testing.T) {
	bashEnv := filepath.Join(t.TempDir(), "bash_env")
	if err := os.WriteFile(bashEnv, []byte("echo 'banner from BASH_ENV'\n"), 0o644); err != nil {
		t.Fatalf("write BASH_ENV file: %v", err)
	}
	cases := []struct {
		name string
		body string
		env  map[string]string
	}{
		{"exit 2", woErroringLib, nil},
		{"syntax error", woSyntaxBrokenLib, nil},
		{"exit on source", woExitingLib, nil},
		{"banner printed while sourced", woBannerLib, nil},
		{"banner that is valid JSON", woJSONBannerLib, nil},
		{"BASH_ENV file prints a banner", woStubLib, map[string]string{"BASH_ENV": bashEnv}},
		{"non-JSON on stdout, exit 2", woNonJSONExit2Lib, nil},
		{"non-JSON on stdout, exit 0", woNonJSONExit0Lib, nil},
		{"right array, exit 1", woRightArrayExit1Lib, nil},
		{"two arrays on stdout", woTwoArraysLib, nil},
		{"empty array for a non-empty input", woEmptyArrayLib, nil},
		{"array of another length", woInventedBeadLib, nil},
	}
	for _, c := range cases {
		lib := writeWorkOrderLib(t, c.body)
		for _, s := range woSites() {
			t.Run(c.name+"/"+s.name, func(t *testing.T) {
				run := s.runWithEnv(t, lib, woScenarios()[0].beads(s), c.env)
				if len(run.ids) != 1 || run.ids[0] != "p0-bug-older" {
					t.Fatalf("head = %v, want the previous order's [p0-bug-older]\nstdout: %q\nstderr: %q", run.ids, run.stdout, run.stderr)
				}
				if !strings.Contains(run.stderr, "work-order WARN: wo_pick: work_order_sort could not tell") {
					t.Fatalf("stderr = %q, want the wo_pick WARN", run.stderr)
				}
			})
		}
	}
}

// TestWorkOrderPickLibLookupOrder: $WORK_ORDER_LIB, else $GC_CITY_PATH, else
// $GC_CITY (each + packs/town-deltas/assets/scripts/work-order.sh). The first
// one set is THE lib — an unusable $WORK_ORDER_LIB does not cascade to the next.
func TestWorkOrderPickLibLookupOrder(t *testing.T) {
	cityWith := func(t *testing.T, body string) string {
		t.Helper()
		city := t.TempDir()
		dir := filepath.Join(city, "packs", "town-deltas", "assets", "scripts")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(filepath.Join(dir, "work-order.sh"), []byte(body), 0o644); err != nil {
			t.Fatalf("write lib: %v", err)
		}
		return city
	}
	site := woSites()[0]
	pair := woScenarios()[0].beads(site)
	runWith := func(t *testing.T, extra map[string]string) woRun {
		t.Helper()
		s := site
		s.env = map[string]string{"GC_SESSION_ORIGIN": "ephemeral"}
		for k, v := range extra {
			s.env[k] = v
		}
		return s.run(t, "", pair)
	}

	t.Run("GC_CITY_PATH", func(t *testing.T) {
		run := runWith(t, map[string]string{"GC_CITY_PATH": cityWith(t, woStubLib)})
		if len(run.ids) != 1 || run.ids[0] != "p0-feature-newer" {
			t.Fatalf("head = %v, want [p0-feature-newer]; stderr: %q", run.ids, run.stderr)
		}
	})
	t.Run("GC_CITY", func(t *testing.T) {
		run := runWith(t, map[string]string{"GC_CITY": cityWith(t, woStubLib)})
		if len(run.ids) != 1 || run.ids[0] != "p0-feature-newer" {
			t.Fatalf("head = %v, want [p0-feature-newer]; stderr: %q", run.ids, run.stderr)
		}
	})
	t.Run("WORK_ORDER_LIB wins and does not cascade", func(t *testing.T) {
		run := runWith(t, map[string]string{
			"WORK_ORDER_LIB": writeWorkOrderLib(t, woErroringLib),
			"GC_CITY_PATH":   cityWith(t, woStubLib),
		})
		if len(run.ids) != 1 || run.ids[0] != "p0-bug-older" {
			t.Fatalf("head = %v, want the previous order's [p0-bug-older]; stderr: %q", run.ids, run.stderr)
		}
		if !strings.Contains(run.stderr, "could not tell") {
			t.Fatalf("stderr = %q, want the wo_pick WARN", run.stderr)
		}
	})
	t.Run("nothing set", func(t *testing.T) {
		run := runWith(t, nil)
		if len(run.ids) != 1 || run.ids[0] != "p0-bug-older" {
			t.Fatalf("head = %v, want the previous order's [p0-bug-older]; stderr: %q", run.ids, run.stderr)
		}
		if !strings.Contains(run.stderr, "work-order.sh not readable") {
			t.Fatalf("stderr = %q, want the wo_pick WARN", run.stderr)
		}
	})
}

// woAllQueries are the generated commands that contain wo_pick, with whether
// each also carries the pool-demand tiers (and so the two pool vetoes).
func woAllQueries() map[string]bool {
	worker := Agent{Name: "worker", Dir: "hello-world"}
	control := Agent{Name: ControlDispatcherAgentName, Dir: "gascity"}
	bd105 := BeadsConfig{BDCompatibility: BeadsBDCompatibility105}
	return map[string]bool{
		worker.EffectiveWorkQuery():                            true,
		worker.EffectiveWorkQueryForBeads(bd105):               true,
		worker.EffectiveRoutedPoolQuery():                      true,
		worker.EffectiveRoutedPoolQueryForBeads(bd105):         true,
		control.EffectiveWorkQuery():                           true,
		worker.EffectiveAssignedInProgressQuery():              false,
		worker.EffectiveAssignedInProgressQueryForBeads(bd105): false,
		worker.EffectiveAssignedReadyQuery():                   false,
		worker.EffectiveAssignedReadyQueryForBeads(bd105):      false,
		control.EffectiveAssignedInProgressQuery():             false,
		control.EffectiveAssignedReadyQuery():                  false,
	}
}

// TestWorkOrderPickIsDefinedBeforeEveryUse: every entry point defines wo_pick
// exactly once and before its first use. The assigned-work scripts run BEFORE
// poolDemandFirstRowFunctionScript in the full work query, so defining the
// function inside that script (where the tiers use it) would leave the
// assigned tiers calling a name that does not exist yet: sh prints
// "wo_pick: not found", the pipeline yields nothing, and the query silently
// falls through to the next tier — an assigned bead lost without an error.
func TestWorkOrderPickIsDefinedBeforeEveryUse(t *testing.T) {
	use := regexp.MustCompile(`wo_pick ('|\|)`)
	for q := range woAllQueries() {
		if n := strings.Count(q, "wo_pick() {"); n != 1 {
			t.Fatalf("wo_pick defined %d times, want exactly 1 in %q", n, q)
		}
		first := use.FindStringIndex(q)
		if first == nil {
			t.Fatalf("no wo_pick use found in %q", q)
		}
		if def := strings.Index(q, "wo_pick() {"); def > first[0] {
			t.Fatalf("wo_pick used at %d before its definition at %d in %q", first[0], def, q)
		}
	}
}

// TestWorkOrderSitesKeepVetoesAndCarryTheRule: every picking command orders
// with the lib's reclaim rule over the whole population, and the vetoes it ran
// before ga-9t9acg.7 — poolDemandLabelFilterJQ and moleculeLiveStepFilterShell
// on the pool tiers, assignedTierExclusionFilterJQ on the assigned ones — are
// still wired in verbatim (modulo the quoting shellquote.Join adds).
func TestWorkOrderSitesKeepVetoesAndCarryTheRule(t *testing.T) {
	escape := func(s string) string { return strings.ReplaceAll(s, "'", `'\''`) }
	window := regexp.MustCompile(`--limit[= ][1-9]`)
	molecule := escape(moleculeLiveStepFilterShell())
	for q, pool := range woAllQueries() {
		if !strings.Contains(q, "work_order_sort --age reclaim") {
			t.Errorf("query does not order with `work_order_sort --age reclaim`: %q", q)
		}
		if !strings.Contains(q, "--limit 0") {
			t.Errorf("query does not fetch the whole population (--limit 0): %q", q)
		}
		if window.MatchString(strings.ReplaceAll(q, molecule, "")) {
			t.Errorf("query still asks bd for a window (--limit N, N>0): %q", q)
		}
		if pool {
			for _, veto := range []string{escape(poolDemandLabelFilterJQ()), molecule} {
				if !strings.Contains(q, veto) {
					t.Errorf("query lost a pool veto %q: %q", veto, q)
				}
			}
		} else if !strings.Contains(q, escape(assignedTierExclusionFilterJQ())) {
			t.Errorf("query lost the assigned-tier exclusion filter: %q", q)
		}
	}
}

// TestWorkOrderPickMutationControl proves the fixtures above can fail: each
// mutant re-breaks one thing the patch fixed, in the generated command itself,
// and the property that guards it must then report a violation. A mutant that
// still passes would mean the corresponding test above is not testing it.
func TestWorkOrderPickMutationControl(t *testing.T) {
	stub := writeWorkOrderLib(t, woStubLib)
	missing := filepath.Join(t.TempDir(), "no-such-dir", "work-order.sh")
	sites := woSites()
	pick := func(t *testing.T, name string) woSite {
		t.Helper()
		for _, s := range sites {
			if s.name == name {
				return s
			}
		}
		t.Fatalf("no site %q", name)
		return woSite{}
	}
	headIs := func(want string, scenario int, lib string) func(*testing.T, woSite) bool {
		return func(t *testing.T, s woSite) bool {
			t.Helper()
			run := s.run(t, lib, woScenarios()[scenario].beads(s))
			return len(run.ids) == 1 && run.ids[0] == want
		}
	}
	warns := func(t *testing.T, s woSite) bool {
		t.Helper()
		return strings.Contains(s.run(t, missing, woScenarios()[0].beads(s)).stderr, "work-order WARN")
	}
	// headWithLib: a lib that is NOT a usable answer must leave the previous order's head.
	headWithLib := func(body string) func(*testing.T, woSite) bool {
		lib := writeWorkOrderLib(t, body)
		return func(t *testing.T, s woSite) bool {
			t.Helper()
			run := s.run(t, lib, woScenarios()[0].beads(s))
			return len(run.ids) == 1 && run.ids[0] == "p0-bug-older"
		}
	}
	// The queries are rendered through shellquote, which spells ' as '\''.
	quoted := func(s string) string { return strings.ReplaceAll(s, "'", `'\''`) }

	// The ephemeral-wisp path reads through `bd query --limit=0`, which the window
	// mutant never touches — it is only meaningful where the first stage is `bd ready`
	// / `bd list` with a limit flag.
	everywhere := []string{"pool tier 1 (work query)", "assigned in-progress", "ephemeral assigned ready"}
	windowed := []string{"pool tier 1 (work query)", "assigned in-progress"}
	mutants := []struct {
		name   string
		sites  []string
		mutate func(string) string
		holds  func(*testing.T, woSite) bool
	}{
		{"window of 20 before ordering", windowed, func(q string) string { return strings.ReplaceAll(q, "--limit 0", "--limit=20") }, headIs("p0-feature-newest-of-25", 1, stub)},
		{"no ordering", everywhere, func(q string) string { return strings.ReplaceAll(q, "work_order_sort --age reclaim", "cat") }, headIs("p0-feature-newer", 0, stub)},
		{"age = created_at only", everywhere, func(q string) string { return strings.ReplaceAll(q, "--age reclaim", "--age created") }, headIs("p1-bug-untouched", 2, stub)},
		{"age = updated_at only", everywhere, func(q string) string { return strings.ReplaceAll(q, "--age reclaim", "--age field") }, headIs("p1-bug-commented", 3, stub)},
		{"WARN swallowed", everywhere, func(q string) string { return strings.ReplaceAll(q, ">&2", ">/dev/null") }, warns},
		// Gate round 2: the lib's answer was trusted when stdout was merely non-empty.
		{"lib trusted when stdout is non-empty (the round-1 check)", everywhere, func(q string) string {
			return strings.ReplaceAll(q, quoted(workOrderPickTrustsLib), `[ -n "$wo_out" ]`)
		}, headWithLib(woBannerLib)},
		{"lib exit status ignored", everywhere, func(q string) string { return strings.ReplaceAll(q, `[ "$wo_rc" -eq 0 ] && `, "") }, headWithLib(woRightArrayExit1Lib)},
		{"lib answer's length not compared with the input's", everywhere, func(q string) string { return strings.ReplaceAll(q, ` and length == $n`, "") }, headWithLib(woEmptyArrayLib)},
		{"more than one document on the lib's stdout accepted", everywhere, func(q string) string { return strings.ReplaceAll(q, `length == 1 and `, "") }, headWithLib(woTwoArraysLib)},
	}
	for _, m := range mutants {
		for _, name := range m.sites {
			t.Run(m.name+"/"+name, func(t *testing.T) {
				s := pick(t, name)
				if !m.holds(t, s) {
					t.Fatalf("control: the unmutated query must satisfy the property")
				}
				mutated := s
				mutated.query = m.mutate(s.query)
				if mutated.query == s.query {
					t.Fatalf("mutation %q changed nothing; the control is stale", m.name)
				}
				if m.holds(t, mutated) {
					t.Fatalf("mutation %q went undetected: the fixtures do not guard it", m.name)
				}
			})
		}
	}
}
