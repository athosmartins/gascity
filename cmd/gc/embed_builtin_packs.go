package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/BurntSushi/toml"
	"github.com/gastownhall/gascity/internal/builtinpacks"
	"github.com/gastownhall/gascity/internal/citylayout"
	"github.com/gastownhall/gascity/internal/fsys"
	"github.com/gastownhall/gascity/internal/orders"
)

const (
	legacyOrderConfigFile = "order.toml"
)

// builtinPacks lists all packs embedded in the gc binary. These are
// materialized to .gc/system/packs/ on every gc start and gc init.
var builtinPacks = builtinpacks.All()

var builtinPackRefreshCache sync.Map

type builtinPackRefreshState struct {
	mu          sync.Mutex
	ready       bool
	lastWarning string
}

type builtinPackRefreshResult struct {
	ready   bool
	warning error
	fatal   error
}

type builtinPackFile struct {
	data []byte
	perm os.FileMode
}

// MaterializeBuiltinPacks writes all embedded pack files to
// .gc/system/packs/{name}/ in the city directory. Files whose content and mode
// already match are left in place; changed content or mode is repaired with an
// atomic rename so readers never observe a truncated file. Executable scripts
// get 0755; everything else 0644.
//
// Required packs (core, maintenance, and the provider-dependent bd/dolt) are
// always refreshed and validated, so a stale or corrupt required pack on disk
// is repaired rather than silently accepted.
//
// Non-required packs distinguish "stale" from "operator-edited" by CONTENT
// HASH, never by mtime (ga-o99jf; mtime is destroyed by checkout/cp/rsync and
// cannot tell an edit made before a binary swap from one made after). Each
// non-required pack keeps a sidecar hash manifest (packHashManifestFile)
// recording the SHA-256 of every file the LAST successful materialize wrote.
// On the next materialize, a preserved file is re-materialized from the new
// embedded bytes when its on-disk hash still matches that record (nobody
// touched it — genuinely stale, not an edit) and left alone — with a warning —
// when it does not (see materializeFSCollectingDrift for the full 3-way
// comparison, including the pre-ga-o99jf migration path with no manifest at
// all). This replaces ga-8j89i's warning-only stopgap: that patch could only
// ever say "this differs from the binary," never say which of the two causes
// it was, so it never touched disk. This one can, safely, in the common case.
// Idempotent: safe to call on every gc start and gc init.
func MaterializeBuiltinPacks(cityPath string) error {
	required := requiredBuiltinPackSet(cityPath)
	var drift []packDriftReport
	for _, bp := range builtinPacks {
		dst := filepath.Join(cityPath, citylayout.SystemPacksRoot, bp.Name)
		_, isRequired := required[bp.Name]
		report := packDriftReport{pack: bp.Name}
		desired, err := materializeFSCollectingDrift(bp.FS, dst, !isRequired, &report)
		if err != nil {
			return fmt.Errorf("materializing %s pack: %w", bp.Name, err)
		}
		if report.hasFindings() {
			drift = append(drift, report)
		}
		if err := pruneStaleGeneratedPackFiles(dst, desired); err != nil {
			return fmt.Errorf("pruning stale %s pack files: %w", bp.Name, err)
		}
		if err := pruneLegacyEmbeddedOrders(bp.FS, dst); err != nil {
			return fmt.Errorf("pruning legacy %s order paths: %w", bp.Name, err)
		}
	}
	if err := repairLegacyGcBeadsBdScript(cityPath); err != nil {
		return fmt.Errorf("repairing legacy gc-beads-bd script: %w", err)
	}
	emitBuiltinPackDriftWarnings(cityPath, drift)
	return nil
}

func builtinPackIncludesForConfigLoad(fs fsys.FS, tomlPath string, warningWriter io.Writer) ([]string, error) {
	if !usesOSFS(fs) {
		return nil, nil
	}
	cityPath := filepath.Dir(tomlPath)
	if err := ensureBuiltinPacksReadyForConfigLoad(cityPath, warningWriter); err != nil {
		return nil, err
	}
	return builtinPackIncludes(cityPath), nil
}

func usesOSFS(fs fsys.FS) bool {
	switch fs.(type) {
	case fsys.OSFS, *fsys.OSFS:
		return true
	default:
		return false
	}
}

func ensureBuiltinPacksReadyForConfigLoad(cityPath string, warningWriter io.Writer) error {
	key := normalizePathForCompare(cityPath)
	stateAny, _ := builtinPackRefreshCache.LoadOrStore(key, &builtinPackRefreshState{})
	state := stateAny.(*builtinPackRefreshState)
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.ready {
		if len(unusableRequiredBuiltinPackNames(cityPath)) == 0 {
			return nil
		}
		state.ready = false
	}
	result := materializeBuiltinPacksForConfigLoad(cityPath)
	if result.fatal != nil {
		state.lastWarning = ""
		return result.fatal
	}
	if result.warning != nil {
		const warningKey = "builtin-pack-refresh-incomplete"
		if state.lastWarning != warningKey {
			emitBuiltinPackRefreshWarning(warningWriter, result.warning)
			state.lastWarning = warningKey
		}
		return nil
	}
	if result.ready {
		state.ready = true
		state.lastWarning = ""
	}
	return nil
}

func materializeBuiltinPacksForConfigLoad(cityPath string) builtinPackRefreshResult {
	if err := MaterializeBuiltinPacks(cityPath); err != nil {
		if missing := unusableRequiredBuiltinPackNames(cityPath); len(missing) > 0 {
			return builtinPackRefreshResult{
				fatal: fmt.Errorf("materializing builtin packs: required builtin packs remain unusable (%s): %w", strings.Join(missing, ", "), err),
			}
		}
		return builtinPackRefreshResult{
			warning: fmt.Errorf("builtin pack refresh incomplete; using existing materialized packs: %w", err),
		}
	}
	return builtinPackRefreshResult{ready: true}
}

func unusableRequiredBuiltinPackNames(cityPath string) []string {
	systemRoot := filepath.Join(cityPath, citylayout.SystemPacksRoot)
	var missing []string
	for _, name := range requiredBuiltinPackNames(cityPath) {
		bp, ok := builtinPackByName(name)
		if !ok || !packContainsEmbeddedState(bp.FS, filepath.Join(systemRoot, name)) {
			missing = append(missing, name)
		}
	}
	return missing
}

func builtinPackByName(name string) (builtinpacks.Pack, bool) {
	for _, bp := range builtinPacks {
		if bp.Name == name {
			return bp, true
		}
	}
	return builtinpacks.Pack{}, false
}

func packContainsEmbeddedState(embedded fs.FS, dstDir string) bool {
	manifest, err := embeddedPackManifest(embedded)
	if err != nil {
		return false
	}
	return packContainsEmbeddedManifest(manifest, dstDir)
}

func packContainsEmbeddedManifest(manifest map[string]builtinPackFile, dstDir string) bool {
	fi, err := os.Stat(dstDir)
	if err != nil || !fi.IsDir() {
		return false
	}
	for rel, want := range manifest {
		dstPath := filepath.Join(dstDir, filepath.FromSlash(rel))
		info, err := os.Lstat(dstPath)
		if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != want.perm {
			return false
		}
		got, err := os.ReadFile(dstPath)
		if err != nil || !bytes.Equal(got, want.data) {
			return false
		}
	}
	return true
}

func embeddedPackManifest(embedded fs.FS) (map[string]builtinPackFile, error) {
	manifest := make(map[string]builtinPackFile)
	err := fs.WalkDir(embedded, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		data, err := fs.ReadFile(embedded, path)
		if err != nil {
			return fmt.Errorf("reading embedded %s: %w", path, err)
		}
		manifest[filepath.ToSlash(path)] = builtinPackFile{
			data: data,
			perm: builtinpacks.MaterializedFileMode(path),
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return manifest, nil
}

// requiredBuiltinPackSet returns the set of builtin pack names that must stay
// in lockstep with the embedded bytes for the city at cityPath. Required packs
// are refreshed and validated on every materialize; operator edits to them are
// not preserved. Derived from requiredBuiltinPackNames so the set tracks the
// provider-dependent membership (bd/dolt) exactly.
func requiredBuiltinPackSet(cityPath string) map[string]struct{} {
	names := requiredBuiltinPackNames(cityPath)
	set := make(map[string]struct{}, len(names))
	for _, name := range names {
		set[name] = struct{}{}
	}
	return set
}

func requiredBuiltinPackNames(cityPath string) []string {
	required := []string{"core", "maintenance"}

	provider := strings.TrimSpace(configuredBeadsProviderValue(cityPath))
	normalizedProvider := normalizeRawBeadsProvider(cityPath, provider)
	if providerUsesBdStoreContract(normalizedProvider) {
		required = append(required, "bd")
	}
	usesDirectExecLifecycle := strings.HasPrefix(provider, "exec:") &&
		execProviderBase(provider) == "gc-beads-bd" &&
		normalizedProvider != "bd"
	if usesDirectExecLifecycle {
		required = append(required, "dolt")
	}
	return required
}

func emitBuiltinPackRefreshWarning(w io.Writer, err error) {
	if w == nil || err == nil {
		return
	}
	fmt.Fprintf(w, "warning: %v\n", err) //nolint:errcheck // best-effort warning emission
}

// packHashManifestFile is the per-pack sidecar (ga-o99jf) recording, for every
// file a non-required pack's materialize has WRITTEN to disk, the SHA-256 of
// the bytes it wrote. It lives inside the pack's own materialized directory,
// sibling to pack.toml — so it travels with the `mv <pack> <pack>.bak` reset
// ga-8j89i's warning already recommends, and pruneStaleGeneratedPackFiles has
// to know to exempt it (it is bookkeeping, not embedded pack content).
//
// This hash — "what did gc itself last write here" — is the whole mechanism:
// comparing the CURRENT on-disk bytes against it (not against mtime, and not
// only against the CURRENT embedded bytes) is what tells "nobody has touched
// this since gc wrote it" (stale, safe to refresh even though the embedded
// bytes have since changed) apart from "something else wrote it since" (an
// edit, must be preserved) — see reconcilePreservedPackFile.
const packHashManifestFile = ".gc-pack-hashes.json"

// sha256Hex returns the hex-encoded SHA-256 digest of data.
func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// readPackHashManifest reads dstDir's hash manifest. An absent or corrupt
// manifest — including every pack materialized before ga-o99jf shipped, which
// never wrote one — returns an empty map, so every file in dstDir is treated
// as having no recorded history. That is deliberately the most conservative
// branch of reconcilePreservedPackFile: preserve, and warn only if the file
// actually looks different from what the binary would write.
func readPackHashManifest(dstDir string) map[string]string {
	data, err := os.ReadFile(filepath.Join(dstDir, packHashManifestFile))
	if err != nil {
		return map[string]string{}
	}
	var manifest map[string]string
	if err := json.Unmarshal(data, &manifest); err != nil {
		return map[string]string{}
	}
	if manifest == nil {
		return map[string]string{}
	}
	return manifest
}

// writePackHashManifest atomically writes dstDir's hash manifest. encoding/json
// sorts map keys, so byte-identical content across repeated calls is
// guaranteed and WriteFileIfContentOrModeChangedAtomic correctly no-ops —
// required for MaterializeBuiltinPacks to stay idempotent.
//
// A write failure here is non-fatal by design (callers route it through
// packDriftReport.manifestWriteErr as a warning, not an error): losing this
// bookkeeping only widens the next materialize's "no recorded history" branch
// back to fully conservative preserve-and-warn. It never risks overwriting an
// operator's file — the reconciliation that actually touches disk always runs
// before this write, never depends on it having already succeeded.
func writePackHashManifest(dstDir string, manifest map[string]string) error {
	data, err := json.Marshal(manifest)
	if err != nil {
		return fmt.Errorf("marshaling pack hash manifest: %w", err)
	}
	return fsys.WriteFileIfContentOrModeChangedAtomic(fsys.OSFS{}, filepath.Join(dstDir, packHashManifestFile), data, 0o644)
}

// maxReportedDriftedPackFiles bounds how many paths a single drift warning
// names. A pack can hold dozens of files; the operator needs enough to
// recognize what drifted, not a wall of text.
const maxReportedDriftedPackFiles = 5

// packDriftReport records, for one builtin pack, how the preserve-operator-
// edits branch disposed of each file it left in place. The buckets answer
// different questions and must not collapse into each other:
//
//   - operatorEdited: the hash manifest PROVES this file no longer matches
//     what gc itself last wrote here (ga-o99jf). Confirmed, not guessed —
//     the warning can say so with confidence, and gc never overwrites it.
//   - changed: no manifest entry exists for this file (a pack materialized
//     before ga-o99jf shipped, or a lost/corrupt manifest) AND it differs
//     from the CURRENT embedded bytes. Provenance is genuinely unknown: it
//     might be a long-standing operator edit or unrecorded staleness, and
//     gc cannot tell which without the operator's help (ga-8j89i's original,
//     necessarily hedged case).
//   - unreadable: an I/O error prevented the comparison. Drift *unknown*,
//     never conflated with drift *absent* — a permission error must not
//     read as a clean pack.
type packDriftReport struct {
	pack             string
	operatorEdited   []string
	changed          []string
	unreadable       []string
	manifestWriteErr error
}

func (r *packDriftReport) hasFindings() bool {
	return r != nil && (len(r.operatorEdited) > 0 || len(r.changed) > 0 || len(r.unreadable) > 0 || r.manifestWriteErr != nil)
}

// reconcilePreservedPackFile is the ga-o99jf 3-way comparison. It decides,
// for one non-required-pack file that materializeFSCollectingDrift is about
// to preserve, whether it is actually safe to refresh instead — and records
// the outcome in report (when non-nil; passing nil skips all bookkeeping, for
// callers that want the old preserve-unconditionally behavior).
//
// knownHash/haveKnownHash come from the pack's hash manifest: the SHA-256 this
// same pack's materialize last WROTE to relPath, i.e. the hash of whatever the
// binary embedded at the time of the PREVIOUS materialize (not necessarily the
// currently-running binary). Comparing the CURRENT on-disk bytes against that
// — never against mtime, which cannot tell "edited before the swap" from
// "edited after" and is destroyed by checkout/cp/rsync — is what makes the
// three-way split possible:
//
//	have history, on-disk == knownHash   -> nobody touched it since gc wrote
//	                                         it; stale, safe to refresh even
//	                                         though the embedded bytes have
//	                                         since changed. No drift to
//	                                         report: this is the silent
//	                                         self-heal.
//	have history, on-disk != knownHash   -> something rewrote the file after
//	                                         gc did; a CONFIRMED edit. Must be
//	                                         preserved. Recorded in
//	                                         operatorEdited, and the manifest
//	                                         entry is carried forward
//	                                         unchanged — the file was not
//	                                         rewritten, so what gc last wrote
//	                                         there has not changed.
//	no history recorded                  -> pre-ga-o99jf pack, or a lost
//	                                         manifest. Falls back to comparing
//	                                         on-disk against the CURRENT
//	                                         embedded bytes (ga-8j89i's
//	                                         original comparison): equal means
//	                                         nothing to warn about, and the
//	                                         hash is safe to backfill (true
//	                                         regardless of provenance, since
//	                                         it is not adopting anything —
//	                                         on-disk already IS what gc would
//	                                         write); different means
//	                                         provenance is genuinely ambiguous
//	                                         and must be preserved WITHOUT
//	                                         ever being adopted as a new
//	                                         trusted baseline. Backfilling
//	                                         from the mismatched on-disk bytes
//	                                         here would make an old operator
//	                                         edit look "stale" and get
//	                                         silently overwritten on the very
//	                                         next materialize — recorded in
//	                                         changed instead, and left out of
//	                                         the manifest so it keeps warning
//	                                         until the operator resolves it
//	                                         (ga-8j89i's `mv ... .bak` reset).
//
// Returns the hash to persist for relPath in the pack's hash manifest ("" when
// nothing should be recorded: an unreadable file, a confirmed edit that
// carries its old hash forward via the caller, or a still-ambiguous
// no-history mismatch) and whether the caller should fall through and refresh
// the file from the embedded bytes.
func reconcilePreservedPackFile(report *packDriftReport, embedded fs.FS, relPath, dstPath string, knownHash string, haveKnownHash bool) (persistHash string, refresh bool) {
	rel := filepath.ToSlash(relPath)
	onDisk, err := os.ReadFile(dstPath)
	if err != nil {
		if report != nil {
			report.unreadable = append(report.unreadable, fmt.Sprintf("%s (%v)", rel, err))
		}
		return "", false
	}

	if haveKnownHash {
		if sha256Hex(onDisk) == knownHash {
			return "", true // stale, not edited: caller refreshes from the new embedded bytes.
		}
		if report != nil {
			report.operatorEdited = append(report.operatorEdited, rel)
		}
		return knownHash, false
	}

	// No recorded history: fall back to ga-8j89i's original comparison
	// against the CURRENT embedded bytes.
	want, err := fs.ReadFile(embedded, relPath)
	if err != nil {
		if report != nil {
			report.unreadable = append(report.unreadable, fmt.Sprintf("%s (embedded: %v)", rel, err))
		}
		return "", false
	}
	if bytes.Equal(onDisk, want) {
		return sha256Hex(onDisk), false // matches what gc would write anyway: safe to backfill.
	}
	if report != nil {
		report.changed = append(report.changed, rel)
	}
	return "", false
}

// builtinPackDriftWarningWriter resolves the destination for pack drift
// warnings. Overridable so tests can capture the output without racing on
// os.Stderr.
var builtinPackDriftWarningWriter = func() io.Writer {
	return os.Stderr
}

// builtinPackDriftWarningCache remembers the last drift signature emitted per
// city so a long-lived process (supervisor, daemon) warns once per distinct
// drift state instead of on every materialize. Keyed the same way as
// builtinPackRefreshCache.
var builtinPackDriftWarningCache sync.Map

// emitBuiltinPackDriftWarnings tells the operator when a materialized pack
// file could NOT be safely reconciled automatically — i.e. what survives
// after materializeFSCollectingDrift's 3-way hash comparison (ga-o99jf) has
// already silently re-materialized every file it could prove was merely
// stale. What remains here is either a CONFIRMED edit (operatorEdited) or a
// pre-ga-o99jf file with no recorded history to disambiguate (changed) — both
// genuinely need the operator, which is why this warning still exists at all
// instead of ga-8j89i's warn-on-any-drift now being fully retired.
//
// It writes nothing to disk and changes no materialize decision.
func emitBuiltinPackDriftWarnings(cityPath string, reports []packDriftReport) {
	signature := builtinPackDriftSignature(reports)
	key := normalizePathForCompare(cityPath)
	if prev, ok := builtinPackDriftWarningCache.Load(key); ok && prev.(string) == signature {
		return
	}
	builtinPackDriftWarningCache.Store(key, signature)
	if signature == "" {
		return
	}
	w := builtinPackDriftWarningWriter()
	if w == nil {
		return
	}
	for _, report := range reports {
		for _, line := range builtinPackDriftWarningLines(cityPath, report) {
			fmt.Fprintln(w, line) //nolint:errcheck // best-effort warning emission
		}
	}
}

// builtinPackDriftSignature collapses a drift set into a comparable string so
// repeated materializes stay quiet while the state is unchanged, and a *new*
// drift (or a repaired one) is reported again rather than suppressed forever.
// All four packDriftReport dimensions participate — omitting any one would
// let a change confined to that dimension alone (e.g. a second file joining
// operatorEdited while changed/unreadable stay empty) produce the same
// signature as before it happened, silently swallowing a real, new warning.
func builtinPackDriftSignature(reports []packDriftReport) string {
	var parts []string
	for _, report := range reports {
		if !report.hasFindings() {
			continue
		}
		operatorEdited := append([]string(nil), report.operatorEdited...)
		changed := append([]string(nil), report.changed...)
		unreadable := append([]string(nil), report.unreadable...)
		sort.Strings(operatorEdited)
		sort.Strings(changed)
		sort.Strings(unreadable)
		manifestErr := ""
		if report.manifestWriteErr != nil {
			manifestErr = report.manifestWriteErr.Error()
		}
		parts = append(parts, report.pack+"|"+strings.Join(operatorEdited, ",")+"|"+strings.Join(changed, ",")+"|"+strings.Join(unreadable, ",")+"|"+manifestErr)
	}
	sort.Strings(parts)
	return strings.Join(parts, ";")
}

func builtinPackDriftWarningLines(cityPath string, report packDriftReport) []string {
	packDir := filepath.Join(cityPath, citylayout.SystemPacksRoot, report.pack)
	var lines []string
	if len(report.operatorEdited) > 0 {
		lines = append(lines, fmt.Sprintf(
			"warning: builtin pack %q on disk differs from the copy embedded in this gc binary (%s), "+
				"and gc's own hash record confirms this is a local edit, not stale content — "+
				"your edit is preserved. To adopt the embedded copy instead: mv %q %q && gc config show --validate",
			report.pack,
			summarizeDriftedPackFiles(report.operatorEdited),
			packDir,
			packDir+".bak",
		))
	}
	if len(report.changed) > 0 {
		lines = append(lines, fmt.Sprintf(
			"warning: builtin pack %q on disk differs from the copy embedded in this gc binary (%s), "+
				"and gc has no materialize-history record for it (pre-dates this gc version, or the "+
				"record was lost) — this is either a local edit or content left over from an older "+
				"binary, and gc cannot safely tell which. Preserving it. "+
				"To adopt the embedded copy: mv %q %q && gc config show --validate",
			report.pack,
			summarizeDriftedPackFiles(report.changed),
			packDir,
			packDir+".bak",
		))
	}
	if len(report.unreadable) > 0 {
		lines = append(lines, fmt.Sprintf(
			"warning: builtin pack %q could not be compared against the embedded copy (%s); "+
				"drift is unknown, not absent.",
			report.pack,
			summarizeDriftedPackFiles(report.unreadable),
		))
	}
	if report.manifestWriteErr != nil {
		lines = append(lines, fmt.Sprintf(
			"warning: could not update the hash manifest for builtin pack %q (%v); "+
				"future drift detection for this pack may fall back to the less precise no-history comparison.",
			report.pack, report.manifestWriteErr,
		))
	}
	return lines
}

func summarizeDriftedPackFiles(files []string) string {
	sorted := append([]string(nil), files...)
	sort.Strings(sorted)
	noun := "files"
	if len(sorted) == 1 {
		noun = "file"
	}
	shown := sorted
	suffix := ""
	if len(sorted) > maxReportedDriftedPackFiles {
		shown = sorted[:maxReportedDriftedPackFiles]
		suffix = fmt.Sprintf(", +%d more", len(sorted)-maxReportedDriftedPackFiles)
	}
	return fmt.Sprintf("%d %s: %s%s", len(sorted), noun, strings.Join(shown, ", "), suffix)
}

// builtinPackIncludes returns the system pack paths that should be
// auto-included in config loading. These are appended as extraIncludes
// to LoadWithIncludes so they go through normal pack expansion
// (ExpandCityPacks) with dedup/fallback resolution.
//
// Core and maintenance are always included. Core ships the role prompts
// referenced by implicit agents and the overlay/per-provider hook files,
// so its content must reach PackOverlayDirs even when the user has never
// run `gc init` (and therefore has no implicit-import.toml written to
// $GC_HOME). When the beads provider is "bd" (the default), include bd
// and let its own pack includes pull in dolt transitively. Gastown is
// never auto-included — it requires an explicit workspace.includes entry.
func builtinPackIncludes(cityPath string) []string {
	systemRoot := filepath.Join(cityPath, citylayout.SystemPacksRoot)

	var includes []string
	for _, name := range requiredBuiltinPackNames(cityPath) {
		packPath := filepath.Join(systemRoot, name)
		if packExists(packPath) {
			includes = append(includes, packPath)
		}
	}

	return includes
}

// packExists checks if a pack.toml exists in the given directory.
func packExists(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, "pack.toml"))
	return err == nil
}

// peekBeadsProvider reads just the beads.provider field from a city.toml
// without doing full config parsing. Returns "" if not set or on error.
func peekBeadsProvider(tomlPath string) string {
	data, err := os.ReadFile(tomlPath)
	if err != nil {
		return ""
	}
	var peek struct {
		Beads struct {
			Provider string `toml:"provider"`
			Backend  string `toml:"backend"`
		} `toml:"beads"`
	}
	if _, err := toml.Decode(string(data), &peek); err != nil {
		return ""
	}
	return peek.Beads.Provider
}

func peekBeadsBackend(tomlPath string) string {
	data, err := os.ReadFile(tomlPath)
	if err != nil {
		return ""
	}
	var peek struct {
		Beads struct {
			Backend string `toml:"backend"`
		} `toml:"beads"`
	}
	if _, err := toml.Decode(string(data), &peek); err != nil {
		return ""
	}
	return peek.Beads.Backend
}

// peekEventsProvider reads just the events.provider field from a city.toml
// without doing full config parsing. Returns "" if not set or on error.
//
// Used by gc event emit (called from bd hooks on every bead write) to avoid
// the full loadCityConfig path, which resolves [imports] and runs
// `git status --porcelain --ignored` against every cached pack-source repo
// — slow on hosts where a pack source is a large monorepo, and fan-out
// concurrent across a bd-write burst (see gastownhall/gascity#2099).
//
// Trade-off: include/import/pack-provided overrides of [events].provider are
// not honored on this hook fast path. Operators that need this path to bypass
// city.toml should use the GC_EVENTS env var.
func peekEventsProvider(tomlPath string) string {
	data, err := os.ReadFile(tomlPath)
	if err != nil {
		return ""
	}
	var peek struct {
		Events struct {
			Provider string `toml:"provider"`
		} `toml:"events"`
	}
	if _, err := toml.Decode(string(data), &peek); err != nil {
		return ""
	}
	return peek.Events.Provider
}

// materializeFS walks an embed.FS, writes all files to dstDir, and returns the
// relative file paths that belong in the generated directory.
//
// When preserveOperatorEdits is true, a preserved candidate (an existing
// regular file with the correct mode) is reconciled against the pack's hash
// manifest by reconcilePreservedPackFile (ga-o99jf) instead of being kept
// unconditionally: content proven stale (matches what gc itself last wrote)
// is refreshed silently, and only a confirmed edit — or, for a pack with no
// recorded history, content that looks different with no way to tell why —
// is left in place. This protects genuine operator-authored edits to
// non-required pack files (formula TOMLs, command scripts, etc.) from being
// silently reverted, without also letting merely-stale content survive every
// future binary upgrade forever (see gastownhall/gascity#2429 for the
// original protection, and ga-8j89i/ga-o99jf for why mtime cannot drive this
// decision and content hashing must).
//
// When preserveOperatorEdits is false (required packs), the preservation skip
// is disabled and the hash manifest is not consulted: every file is refreshed
// and validated against the embedded bytes, so a stale or corrupt required
// pack is repaired rather than silently accepted.
//
// The remaining repair semantics are independent of the flag: missing files are
// written (initial scaffolding), wrong-mode files are rewritten (e.g., script
// that lost its +x bit), and non-regular files (symlinks, etc.) are replaced
// with the embedded content.
func materializeFS(embedded fs.FS, dstDir string, preserveOperatorEdits bool) (map[string]struct{}, error) {
	return materializeFSCollectingDrift(embedded, dstDir, preserveOperatorEdits, nil)
}

// materializeFSCollectingDrift is materializeFS plus drift reporting. When
// preserveOperatorEdits is true, it also reads and rewrites the pack's hash
// manifest (packHashManifestFile) so the reconciliation above has the history
// it needs — this part is unconditional on drift, since ga-o99jf, unlike
// ga-8j89i's pure reporting, changes what actually gets WRITTEN to disk, not
// just what gets reported. Passing drift as nil only disables the optional
// operator-facing report; it never disables the manifest-driven decision.
func materializeFSCollectingDrift(embedded fs.FS, dstDir string, preserveOperatorEdits bool, drift *packDriftReport) (map[string]struct{}, error) {
	desired := make(map[string]struct{})

	var knownHashes map[string]string
	if preserveOperatorEdits {
		knownHashes = readPackHashManifest(dstDir)
	}
	pendingHashes := make(map[string]string)

	err := fs.WalkDir(embedded, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		dst := filepath.Join(dstDir, path)

		if d.IsDir() {
			return os.MkdirAll(dst, 0o755)
		}
		rel := filepath.ToSlash(path)
		desired[rel] = struct{}{}

		perm := builtinpacks.MaterializedFileMode(path)

		// Preserve operator-authored content for non-required packs. A
		// preserve CANDIDATE is an existing regular file with the correct
		// mode — that's a file the operator might have edited. Non-regular
		// files (symlinks) and wrong-mode files always fall through to the
		// repair path below, matching the prior contract. Mode comparison
		// uses fsys.ComparableMode (perm + setuid/setgid/sticky) so it agrees
		// with the WriteFileIfContentOrModeChangedAtomic repair path.
		// Required packs (preserveOperatorEdits == false) skip this branch
		// entirely so stale content is always refreshed.
		if preserveOperatorEdits {
			if info, statErr := os.Lstat(dst); statErr == nil {
				if info.Mode().IsRegular() && fsys.ComparableMode(info.Mode()) == fsys.ComparableMode(perm) {
					knownHash, haveKnownHash := knownHashes[rel]
					persistHash, refresh := reconcilePreservedPackFile(drift, embedded, path, dst, knownHash, haveKnownHash)
					if persistHash != "" {
						pendingHashes[rel] = persistHash
					}
					if !refresh {
						return nil
					}
					// Proven stale, not edited: fall through to the same
					// refresh path a missing or wrong-mode file takes.
				}
			} else if !os.IsNotExist(statErr) {
				return fmt.Errorf("stat %s: %w", dst, statErr)
			}
		}

		data, err := fs.ReadFile(embedded, path)
		if err != nil {
			return fmt.Errorf("reading embedded %s: %w", path, err)
		}

		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}

		if err := fsys.WriteFileIfContentOrModeChangedAtomic(fsys.OSFS{}, dst, data, perm); err != nil {
			return err
		}
		if preserveOperatorEdits {
			pendingHashes[rel] = sha256Hex(data)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	if preserveOperatorEdits {
		if writeErr := writePackHashManifest(dstDir, pendingHashes); writeErr != nil && drift != nil {
			drift.manifestWriteErr = writeErr
		}
	}
	return desired, nil
}

func repairLegacyGcBeadsBdScript(cityPath string) error {
	path := legacyGcBeadsBdScriptPath(cityPath)
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.IsDir() {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if !looksLikeGeneratedGcBeadsBdScript(data) {
		return nil
	}
	return fsys.WriteFileIfContentOrModeChangedAtomic(fsys.OSFS{}, path, legacyGcBeadsBdShim(), 0o755)
}

func looksLikeGeneratedGcBeadsBdScript(data []byte) bool {
	text := string(data)
	return strings.Contains(text, "gc-beads-bd") && strings.Contains(text, "exec: beads provider")
}

func legacyGcBeadsBdShim() []byte {
	return []byte(`#!/bin/sh
set -eu

script_dir=$(dirname "$0")
city_root=$(cd "$script_dir/../.." && pwd)

exec "$city_root/.gc/system/packs/bd/assets/scripts/gc-beads-bd.sh" "$@"
`)
}

// pruneLegacyEmbeddedOrders removes deprecated order directory layouts when the
// embedded pack already provides the flat orders/<name>.toml form.
func pruneLegacyEmbeddedOrders(embedded fs.FS, dstDir string) error {
	entries, err := fs.ReadDir(embedded, "orders")
	if err != nil {
		return nil
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		orderName, ok := orders.TrimFlatOrderFilename(name)
		if !ok {
			continue
		}
		for _, legacyPath := range []string{
			filepath.Join(dstDir, "orders", orderName, legacyOrderConfigFile),
			filepath.Join(dstDir, "formulas", "orders", orderName, legacyOrderConfigFile),
		} {
			if err := os.Remove(legacyPath); err != nil && !os.IsNotExist(err) {
				return err
			}
			pruneEmptyDirs(filepath.Dir(legacyPath), dstDir)
		}
	}
	return nil
}

// pruneStaleGeneratedPackFiles treats the current binary's embedded pack tree
// as the source of truth for generated files. Concurrent older/newer binaries
// can briefly prune each other's obsolete generated-only files, but the next
// successful materialization self-heals the directory to the active binary.
func pruneStaleGeneratedPackFiles(dstDir string, desired map[string]struct{}) error {
	if _, err := os.Stat(dstDir); os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return err
	}

	dirsToPrune := make(map[string]struct{})
	if err := filepath.WalkDir(dstDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(dstDir, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if _, ok := desired[rel]; ok {
			return nil
		}
		// Ignore in-flight atomic temp files so concurrent refreshes do not
		// delete each other's rename targets mid-write.
		if isGeneratedPackAtomicTempRel(rel, func(path string) bool {
			_, ok := desired[path]
			return ok
		}) {
			return nil
		}
		// Preserve the pack hash manifest and its atomic-write temp siblings
		// (ga-o99jf): they are runtime bookkeeping materializeFSCollectingDrift
		// produces, not embedded pack content, so they are never part of
		// desired — without this exemption the very next materialize would
		// prune the manifest it just wrote.
		if rel == packHashManifestFile || strings.HasPrefix(rel, packHashManifestFile+".tmp.") {
			return nil
		}
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
		dirsToPrune[filepath.Dir(path)] = struct{}{}
		return nil
	}); err != nil {
		return err
	}

	pruneDirs := make([]string, 0, len(dirsToPrune))
	for dir := range dirsToPrune {
		pruneDirs = append(pruneDirs, dir)
	}
	sort.Slice(pruneDirs, func(i, j int) bool {
		left := filepath.Clean(pruneDirs[i])
		right := filepath.Clean(pruneDirs[j])
		leftDepth := strings.Count(left, string(filepath.Separator))
		rightDepth := strings.Count(right, string(filepath.Separator))
		if leftDepth != rightDepth {
			return leftDepth > rightDepth
		}
		return left > right
	})
	for _, dir := range pruneDirs {
		pruneEmptyDirs(dir, dstDir)
	}
	return nil
}

func isGeneratedPackAtomicTempRel(rel string, hasDesired func(string) bool) bool {
	idx := strings.LastIndex(rel, ".tmp.")
	return idx > 0 && hasDesired(rel[:idx])
}

func pruneEmptyDirs(dir, stop string) {
	stop = filepath.Clean(stop)
	for {
		cleanDir := filepath.Clean(dir)
		if cleanDir == stop || cleanDir == "." || cleanDir == string(filepath.Separator) {
			return
		}
		entries, err := os.ReadDir(cleanDir)
		if err != nil || len(entries) > 0 {
			return
		}
		if err := os.Remove(cleanDir); err != nil {
			return
		}
		dir = filepath.Dir(cleanDir)
	}
}
