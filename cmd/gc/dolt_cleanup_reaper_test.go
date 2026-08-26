package main

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

// testNow anchors the (irrelevant-to-them) `now` argument every pre-existing
// call site below now must pass — none of those fixtures set
// StartTimeTicks/StartIdentity, so processAgeSeconds always reports
// ok=false for them regardless of what `now` is, and the value here is
// inert. Age-relevant coverage lives in the dedicated tests further down,
// which construct their own now/StartIdentity pairs by round-tripping
// through time.ANSIC (never hand-typed date strings) so they're correct
// regardless of the machine's local timezone.
var testNow = time.Now()

func TestExtractConfigPath_SpaceSeparated(t *testing.T) {
	argv := []string{"dolt", "sql-server", "--config", "/tmp/TestFoo123/config.yaml"}
	got := extractConfigPath(argv)
	want := "/tmp/TestFoo123/config.yaml"
	if got != want {
		t.Errorf("extractConfigPath() = %q, want %q", got, want)
	}
}

func TestExtractConfigPath_EqualsForm(t *testing.T) {
	argv := []string{"dolt", "sql-server", "--config=/tmp/TestFoo/config.yaml"}
	got := extractConfigPath(argv)
	want := "/tmp/TestFoo/config.yaml"
	if got != want {
		t.Errorf("extractConfigPath() = %q, want %q", got, want)
	}
}

func TestExtractConfigPath_Missing(t *testing.T) {
	argv := []string{"dolt", "sql-server", "--port", "3307"}
	got := extractConfigPath(argv)
	if got != "" {
		t.Errorf("extractConfigPath() = %q, want empty", got)
	}
}

func TestExtractConfigPath_FlagAtEnd(t *testing.T) {
	// --config with no value should return empty (malformed cmdline).
	argv := []string{"dolt", "sql-server", "--config"}
	got := extractConfigPath(argv)
	if got != "" {
		t.Errorf("extractConfigPath() = %q, want empty for trailing --config", got)
	}
}

func TestIsTestConfigPath_TmpTestPrefix(t *testing.T) {
	if !isTestConfigPath("/tmp/TestOrchestrator123/config.yaml", "/home/u", "") {
		t.Error("expected /tmp/Test* to be a test path")
	}
}

func TestIsTestConfigPath_CmdGCTestPrefix(t *testing.T) {
	if !isTestConfigPath("/tmp/gctest-123/TestCase/001/.gc/runtime/packs/dolt/dolt-config.yaml", "/home/u", "") {
		t.Error("expected /tmp/gctest-* to be a test path")
	}
}

func TestIsTestConfigPath_HomeGotmpTestPrefix(t *testing.T) {
	if !isTestConfigPath("/home/u/.gotmp/TestFuzz/config.yaml", "/home/u", "") {
		t.Error("expected $HOME/.gotmp/Test* to be a test path")
	}
}

func TestIsTestConfigPath_ProcessTempDirTestPrefix(t *testing.T) {
	if !isTestConfigPath("/var/tmp/go-test/TestRepro/config.yaml", "/home/u", "/var/tmp/go-test") {
		t.Error("expected os.TempDir()/Test* to be a test path")
	}
}

func TestIsTestConfigPath_KnownGCTestPrefix(t *testing.T) {
	if !isTestConfigPath("/data/tmp/gc-state-mutation-builtin-123/.gc/runtime/packs/dolt/dolt-config.yaml", "/home/u", "/data/tmp") {
		t.Error("expected known gc-* test prefix under os.TempDir() to be a test path")
	}
}

func TestIsTestConfigPath_IntegrationTempPrefixes(t *testing.T) {
	cases := []string{
		"/tmp/gcit-123/cities/x/.gc/runtime/packs/dolt/dolt-config.yaml",
		"/tmp/gc-int-env-123/.gc/runtime/packs/dolt/dolt-config.yaml",
	}
	for _, p := range cases {
		if !isTestConfigPath(p, "/home/u", "") {
			t.Errorf("isTestConfigPath(%q) = false, want true", p)
		}
	}
}

func TestIsTestConfigPath_NotTest(t *testing.T) {
	cases := []string{
		"/tmp/be-s9d-bench-dolt/config.yaml", // benchmark
		"/var/lib/dolt/config.yaml",          // production-ish
		"/tmp/random/config.yaml",            // tmp but not Test prefix
		"/home/u/.gotmp/other/config.yaml",   // gotmp but not Test prefix
		"/var/tmp/go-test/Other/config.yaml", // temp root but not Test prefix
		"",                                   // missing
	}
	for _, p := range cases {
		if isTestConfigPath(p, "/home/u", "/var/tmp/go-test") {
			t.Errorf("isTestConfigPath(%q) = true, want false", p)
		}
	}
}

func TestClassifyDoltProcess_ProtectedByRigPort(t *testing.T) {
	p := DoltProcInfo{
		PID:   1234,
		Argv:  []string{"dolt", "sql-server", "--config", "/tmp/TestFoo/config.yaml"},
		Ports: []int{28231},
	}
	got := classifyDoltProcess(p, map[int]string{28231: "beads"}, "/home/u", "", nil, testNow)

	if got.Action != "protect" {
		t.Errorf("Action = %q, want protect", got.Action)
	}
	if got.Reason == "" || !strings.Contains(got.Reason, "rig") || !strings.Contains(got.Reason, "beads") {
		t.Errorf("Reason = %q, want rig+beads reference", got.Reason)
	}
}

func TestClassifyDoltProcess_OrphanByTestPath(t *testing.T) {
	p := DoltProcInfo{
		PID:   2222,
		Argv:  []string{"dolt", "sql-server", "--config", "/tmp/TestMailRouter9182/config.yaml"},
		Ports: []int{},
	}
	got := classifyDoltProcess(p, nil, "/home/u", "", nil, testNow)

	if got.Action != "reap" {
		t.Errorf("Action = %q, want reap", got.Action)
	}
	if got.ConfigPath != "/tmp/TestMailRouter9182/config.yaml" {
		t.Errorf("ConfigPath = %q", got.ConfigPath)
	}
}

func TestClassifyDoltProcess_ReapsIntegrationTempRoots(t *testing.T) {
	cases := []string{
		"/tmp/gcit-123/cities/x/.gc/runtime/packs/dolt/dolt-config.yaml",
		"/tmp/gc-int-env-123/.gc/runtime/packs/dolt/dolt-config.yaml",
	}
	for _, cfg := range cases {
		p := DoltProcInfo{
			PID:   2224,
			Argv:  []string{"dolt", "sql-server", "--config", cfg},
			Ports: []int{},
		}
		got := classifyDoltProcess(p, nil, "/home/u", "", nil, testNow)
		if got.Action != "reap" {
			t.Errorf("classifyDoltProcess(%q).Action = %q, want reap", cfg, got.Action)
		}
		if got.ConfigPath != cfg {
			t.Errorf("classifyDoltProcess(%q).ConfigPath = %q, want %q", cfg, got.ConfigPath, cfg)
		}
	}
}

func TestClassifyDoltProcess_ProtectsActiveTestRoot(t *testing.T) {
	p := DoltProcInfo{
		PID:   2223,
		Argv:  []string{"dolt", "sql-server", "--config", "/tmp/TestPersonalWorkFormulaCompileAndRun123/001/city/.gc/runtime/packs/dolt/dolt-config.yaml"},
		Ports: []int{},
	}
	got := classifyDoltProcess(p, nil, "/home/u", "", []string{"/tmp/TestPersonalWorkFormulaCompileAndRun123"}, testNow)

	if got.Action != "protect" {
		t.Errorf("Action = %q, want protect", got.Action)
	}
	if !strings.Contains(got.Reason, "active test root") {
		t.Errorf("Reason = %q, want active-test-root reason", got.Reason)
	}
}

func TestClassifyDoltProcess_ProtectedByPathNotOnAllowlist(t *testing.T) {
	// Active benchmark — config path doesn't match /tmp/Test*.
	p := DoltProcInfo{
		PID:   3333,
		Argv:  []string{"dolt", "sql-server", "--config", "/tmp/be-s9d-bench-dolt/config.yaml"},
		Ports: []int{33400},
	}
	got := classifyDoltProcess(p, nil, "/home/u", "", nil, testNow)

	if got.Action != "protect" {
		t.Errorf("Action = %q, want protect", got.Action)
	}
	if !strings.Contains(got.Reason, "allowlist") {
		t.Errorf("Reason = %q, want mention of allowlist", got.Reason)
	}
	// Reason should echo the actual config path so operators can see it.
	if !strings.Contains(got.Reason, "/tmp/be-s9d-bench-dolt") {
		t.Errorf("Reason = %q, want config path echoed (architect Open Q 0)", got.Reason)
	}
}

func TestClassifyDoltProcess_ProtectsRealManagedConfig(t *testing.T) {
	cfg := "/home/u/projects/foo/.gc/runtime/packs/dolt/dolt-config.yaml"
	p := DoltProcInfo{
		PID:   3334,
		Argv:  []string{"dolt", "sql-server", "--config", cfg},
		Ports: []int{},
	}
	got := classifyDoltProcess(p, nil, "/home/u", "", nil, testNow)
	if got.Action != "protect" {
		t.Errorf("Action = %q, want protect", got.Action)
	}
	if !strings.Contains(got.Reason, "allowlist") || !strings.Contains(got.Reason, cfg) {
		t.Errorf("Reason = %q, want allowlist reason containing config path", got.Reason)
	}
}

func TestClassifyDoltProcess_ProtectedWhenConfigMissing(t *testing.T) {
	p := DoltProcInfo{
		PID:   4444,
		Argv:  []string{"dolt", "sql-server"},
		Ports: []int{},
	}
	got := classifyDoltProcess(p, nil, "/home/u", "", nil, testNow)

	if got.Action != "protect" {
		t.Errorf("Action = %q, want protect", got.Action)
	}
	if !strings.Contains(got.Reason, "config") {
		t.Errorf("Reason = %q, want config-path-related reason", got.Reason)
	}
}

func TestClassifyDoltProcess_RigPortBeatsConfigPath(t *testing.T) {
	// Even if the cmdline says /tmp/Test*, a rig-port match always protects.
	p := DoltProcInfo{
		PID:   5555,
		Argv:  []string{"dolt", "sql-server", "--config", "/tmp/TestSomething/config.yaml"},
		Ports: []int{28231},
	}
	got := classifyDoltProcess(p, map[int]string{28231: "beads"}, "/home/u", "", nil, testNow)

	if got.Action != "protect" {
		t.Errorf("Action = %q, want protect (rig port wins)", got.Action)
	}
}

func TestPlanReap_BuildsOrphanAndProtectedLists(t *testing.T) {
	procs := []DoltProcInfo{
		{PID: 1138290, Ports: []int{28231}, Argv: []string{"dolt", "sql-server"}},
		{PID: 1281044, Argv: []string{"dolt", "sql-server", "--config", "/tmp/TestA/config.yaml"}},
		{PID: 1319499, Ports: []int{33400}, Argv: []string{"dolt", "sql-server", "--config", "/tmp/be-s9d-bench-dolt/config.yaml"}},
		{PID: 1281099, Argv: []string{"dolt", "sql-server", "--config", "/tmp/TestB/config.yaml"}},
		{PID: 1281100, Argv: []string{"dolt", "sql-server", "--config", "/data/tmp/gc-state-runtime-builtin-1/.gc/runtime/packs/dolt/dolt-config.yaml"}},
		{PID: 1281101, Argv: []string{"dolt", "sql-server", "--config", "/tmp/TestActive/001/city/.gc/runtime/packs/dolt/dolt-config.yaml"}},
	}
	rigPorts := map[int]string{28231: "beads"}

	plan := planOrphanReap(procs, rigPorts, "/home/u", "/data/tmp", []string{"/tmp/TestActive"}, testNow)

	wantReap := []int{1281044, 1281099, 1281100}
	gotReap := make([]int, 0, len(plan.Reap))
	for _, target := range plan.Reap {
		gotReap = append(gotReap, target.PID)
	}
	if !reflect.DeepEqual(gotReap, wantReap) {
		t.Errorf("Reap PIDs = %v, want %v", gotReap, wantReap)
	}

	wantProtected := []int{1138290, 1319499, 1281101}
	gotProtected := make([]int, 0, len(plan.Protected))
	for _, e := range plan.Protected {
		gotProtected = append(gotProtected, e.PID)
	}
	if !reflect.DeepEqual(gotProtected, wantProtected) {
		t.Errorf("Protected PIDs = %v, want %v", gotProtected, wantProtected)
	}
}

// ── ga-b5l0v: age-gated fallback for orphans off the specific-prefix allowlist ──
//
// ROOT (measured live, 2026-08-21): two real orphaned `dolt sql-server` test
// processes sat for 2+ and 3.5+ days respectively — one config under
// /tmp/claude-<uid>/.../scratchpad/... (an agent session's own scratchpad,
// from a dog testing an engine fix), one under /tmp/city/.... Neither path
// matches isTestConfigPath's allowlist (Test*, gctest-*, gc-state-*, etc. —
// that list only recognizes the gc binary's OWN Go-test-harness temp-dir
// conventions), so classifyDoltProcess fell through to "protect... kill
// manually if not wanted" — forever, regardless of age, since nothing else
// in this city ever manually intervenes. One orphan briefly got misread as
// the city's OWN production Dolt server during an unrelated outage
// (distinguishable only by port), before the mistake was caught.

func TestUnderTempRoot(t *testing.T) {
	cases := []struct {
		name string
		path string
		root string
		want bool
	}{
		{"direct child", "/tmp/foo/config.yaml", "/tmp", true},
		{"nested child", "/tmp/claude-501/x/y/scratchpad/config.yaml", "/tmp", true},
		{"not under root", "/var/lib/dolt/config.yaml", "/tmp", false},
		{"root itself, not a child", "/tmp", "/tmp", false},
		{"empty root never matches", "/tmp/foo/config.yaml", "", false},
		{"root is bare slash, refuses (would match everything)", "/tmp/foo/config.yaml", "/", false},
		{"root is dot, refuses", "/tmp/foo/config.yaml", ".", false},
		{"sibling prefix collision", "/tmp/foobar/config.yaml", "/tmp/foo", false},
	}
	for _, c := range cases {
		if got := underTempRoot(c.path, c.root); got != c.want {
			t.Errorf("%s: underTempRoot(%q, %q) = %v, want %v", c.name, c.path, c.root, got, c.want)
		}
	}
}

func TestParseProcUptimeSeconds(t *testing.T) {
	if got, ok := parseProcUptimeSeconds([]byte("12345.67 9999.00\n")); !ok || got != 12345.67 {
		t.Errorf("parseProcUptimeSeconds(valid) = (%v, %v), want (12345.67, true)", got, ok)
	}
	if _, ok := parseProcUptimeSeconds([]byte("")); ok {
		t.Error("parseProcUptimeSeconds(empty) should be ok=false")
	}
	if _, ok := parseProcUptimeSeconds([]byte("not-a-number 1.0\n")); ok {
		t.Error("parseProcUptimeSeconds(malformed) should be ok=false")
	}
	if _, ok := parseProcUptimeSeconds([]byte("-5.0 1.0\n")); ok {
		t.Error("parseProcUptimeSeconds(negative) should be ok=false")
	}
}

func TestProcessAgeSeconds_FromStartIdentity_RoundTrip(t *testing.T) {
	// Round-tripped through the SAME layout (time.ANSIC) the production code
	// parses with, and anchored to a live now() rather than a hand-typed
	// date string — correct regardless of the test machine's local
	// timezone, exactly mirroring how a real `ps -o lstart=` value relates
	// to a real "now" on whatever host it runs on.
	now := time.Now()
	startedThreeHoursAgo := now.Add(-3 * time.Hour).Format(time.ANSIC)

	age, ok := processAgeSeconds(0, startedThreeHoursAgo, now)
	if !ok {
		t.Fatal("processAgeSeconds should report ok=true for a well-formed StartIdentity")
	}
	wantSeconds := (3 * time.Hour).Seconds()
	if diff := age - wantSeconds; diff < -2 || diff > 2 { // 2s slop for formatting truncation
		t.Errorf("age = %.1fs, want ~%.1fs (3h)", age, wantSeconds)
	}
}

func TestProcessAgeSeconds_MalformedStartIdentity(t *testing.T) {
	if _, ok := processAgeSeconds(0, "not a timestamp at all", time.Now()); ok {
		t.Error("processAgeSeconds(malformed StartIdentity) should be ok=false, not a guessed age")
	}
}

func TestProcessAgeSeconds_NoSignal(t *testing.T) {
	if _, ok := processAgeSeconds(0, "", time.Now()); ok {
		t.Error("processAgeSeconds with no StartTimeTicks and no StartIdentity should be ok=false")
	}
}

func TestClassifyDoltProcess_AgeGatedFallback_ReapsGenuineOrphan_AgentScratchpad(t *testing.T) {
	now := time.Now()
	oldEnough := now.Add(-3 * time.Hour).Format(time.ANSIC)
	p := DoltProcInfo{
		PID:  9566,
		Argv: []string{"dolt", "sql-server", "--config", "/tmp/claude-501/proj/session-abc/scratchpad/engine-fix-ga-s1d5o/city/.gc/runtime/packs/dolt/dolt-config.yaml"},
		Ports: []int{43147}, // not a registered rig port
		StartIdentity: oldEnough,
	}
	got := classifyDoltProcess(p, map[int]string{52756: "hq"}, "/home/u", "", nil, now)
	if got.Action != "reap" {
		t.Errorf("Action = %q, want reap (ga-b5l0v orphan #1 shape, 3h old, off-allowlist tmp path)", got.Action)
	}
}

func TestClassifyDoltProcess_AgeGatedFallback_ReapsGenuineOrphan_UnrecognizedTmpRoot(t *testing.T) {
	now := time.Now()
	oldEnough := now.Add(-84 * time.Hour).Format(time.ANSIC) // ~3.5 days, matches the live report
	p := DoltProcInfo{
		PID:           51917,
		Argv:          []string{"dolt", "sql-server", "--config", "/tmp/city/.gc/runtime/packs/dolt/dolt-config.yaml"},
		Ports:         []int{18845},
		StartIdentity: oldEnough,
	}
	got := classifyDoltProcess(p, map[int]string{52756: "hq"}, "/home/u", "", nil, now)
	if got.Action != "reap" {
		t.Errorf("Action = %q, want reap (ga-b5l0v orphan #2 shape, /tmp/city/..., 3.5d old)", got.Action)
	}
}

func TestClassifyDoltProcess_AgeGatedFallback_ProtectsYoungProcess(t *testing.T) {
	// Same path shape as the reap case above, but started 5 minutes ago —
	// this is the safety-preserving negative: a legitimate, still-running
	// test dolt server must NOT be killed just because its path happens to
	// be off the specific allowlist.
	now := time.Now()
	justStarted := now.Add(-5 * time.Minute).Format(time.ANSIC)
	p := DoltProcInfo{
		PID:           99001,
		Argv:          []string{"dolt", "sql-server", "--config", "/tmp/claude-501/proj/session-xyz/scratchpad/dolt-config.yaml"},
		Ports:         []int{40000},
		StartIdentity: justStarted,
	}
	got := classifyDoltProcess(p, map[int]string{52756: "hq"}, "/home/u", "", nil, now)
	if got.Action != "protect" {
		t.Errorf("Action = %q, want protect (only 5min old — must not reap a live run)", got.Action)
	}
}

func TestClassifyDoltProcess_AgeGatedFallback_ProtectsWhenAgeUnknown(t *testing.T) {
	// Under /tmp, off the allowlist, but NO start-time signal at all
	// (neither StartTimeTicks nor StartIdentity populated — e.g. a host
	// where both /proc and `ps -o lstart=` parsing failed). The third-state
	// rule: "can't determine age" must default to protect, never to "assume
	// old enough."
	p := DoltProcInfo{
		PID:   99002,
		Argv:  []string{"dolt", "sql-server", "--config", "/tmp/city/.gc/runtime/packs/dolt/dolt-config.yaml"},
		Ports: []int{40001},
	}
	got := classifyDoltProcess(p, map[int]string{52756: "hq"}, "/home/u", "", nil, time.Now())
	if got.Action != "protect" {
		t.Errorf("Action = %q, want protect (age unknown must never default to reap)", got.Action)
	}
}

func TestClassifyDoltProcess_AgeGatedFallback_NeverAppliesOutsideTmp(t *testing.T) {
	// A persistent, non-tmp config path (mirrors
	// TestClassifyDoltProcess_ProtectsRealManagedConfig) with a fabricated
	// VERY old StartIdentity — proves the "under /tmp" leg is a hard
	// requirement of the new fallback, not something age alone can satisfy.
	now := time.Now()
	veryOld := now.Add(-30 * 24 * time.Hour).Format(time.ANSIC)
	p := DoltProcInfo{
		PID:           99003,
		Argv:          []string{"dolt", "sql-server", "--config", "/home/u/projects/foo/.gc/runtime/packs/dolt/dolt-config.yaml"},
		Ports:         []int{40002},
		StartIdentity: veryOld,
	}
	got := classifyDoltProcess(p, nil, "/home/u", "", nil, now)
	if got.Action != "protect" {
		t.Errorf("Action = %q, want protect (not under /tmp at all — age must not matter)", got.Action)
	}
}

func TestClassifyDoltProcess_AgeGatedFallback_StillRespectsActiveTestRoot(t *testing.T) {
	// Old enough, under /tmp, off the allowlist — but another live process
	// still references this exact test root. Step 3 (active-test-root
	// protection) must still win; the new step 4 must never override it.
	now := time.Now()
	oldEnough := now.Add(-3 * time.Hour).Format(time.ANSIC)
	p := DoltProcInfo{
		PID:           99004,
		Argv:          []string{"dolt", "sql-server", "--config", "/tmp/city/001/.gc/runtime/packs/dolt/dolt-config.yaml"},
		Ports:         []int{40003},
		StartIdentity: oldEnough,
	}
	got := classifyDoltProcess(p, nil, "/home/u", "", []string{"/tmp/city"}, now)
	if got.Action != "protect" {
		t.Errorf("Action = %q, want protect (active test root must still win over the age-gated fallback)", got.Action)
	}
	if !strings.Contains(got.Reason, "active test root") {
		t.Errorf("Reason = %q, want active-test-root reason", got.Reason)
	}
}
