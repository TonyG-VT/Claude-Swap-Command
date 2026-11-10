package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// carryFixture is a pretend Claude data folder with two account namespaces.
type carryFixture struct {
	t        *testing.T
	d        desktop
	root     string
	src, dst carryNamespace
}

func carryID(n int) string { return fmt.Sprintf("%08x-0000-4000-8000-%012x", n, n) }

func newCarryFixture(t *testing.T) *carryFixture {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	f := &carryFixture{t: t, root: root, d: desktop{dir: filepath.Join(root, "desktop")},
		src: carryNamespace{Account: carryID(0xA1), Org: carryID(0xA2)},
		dst: carryNamespace{Account: carryID(0xB1), Org: carryID(0xB2)}}
	write(t, f.d.configPath(), "{}")
	for _, ns := range []carryNamespace{f.src, f.dst} {
		if err := os.MkdirAll(f.dir(ns), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	return f
}

func (f *carryFixture) dir(ns carryNamespace) string {
	return filepath.Join(f.d.sessionsDir(), ns.Account, ns.Org)
}

func (f *carryFixture) file(ns carryNamespace, n int) string {
	return filepath.Join(f.dir(ns), "local_"+carryID(n)+".json")
}

// put writes conversation n into a namespace. Fields override the defaults;
// a nil value removes a default field.
func (f *carryFixture) put(ns carryNamespace, n int, fields map[string]any) string {
	f.t.Helper()
	record := map[string]any{
		"sessionId": "local_" + carryID(n), "cliSessionId": carryID(0xC000 + n), "cwd": "/work/project",
		"lastActivityAt": 1000, "title": fmt.Sprintf("Conversation %d", n), "isArchived": false,
	}
	for k, v := range fields {
		if v == nil {
			delete(record, k)
		} else {
			record[k] = v
		}
	}
	data, err := json.Marshal(record)
	if err != nil {
		f.t.Fatal(err)
	}
	path := f.file(ns, n)
	write(f.t, path, string(data))
	return path
}

func (f *carryFixture) plan() *carryPlan {
	f.t.Helper()
	p, err := planCarry(f.d, f.src, f.dst, nil)
	if err != nil {
		f.t.Fatalf("plan: %v", err)
	}
	return p
}

func (f *carryFixture) apply(p *carryPlan) string {
	f.t.Helper()
	bk := mustBackup(f.t, f.d, filepath.Join(f.root, "backups"))
	if n, err := p.Apply(); err != nil || n != len(p.writes) {
		f.t.Fatalf("apply: %d of %d, %v", n, len(p.writes), err)
	}
	return bk
}

func carryActions(p *carryPlan) map[string]carryAction {
	out := map[string]carryAction{}
	for _, w := range p.writes {
		out[w.Name] = w.Action
	}
	return out
}

func carrySkips(p *carryPlan) map[string]string {
	out := map[string]string{}
	for _, s := range p.Skipped {
		out[s.Name] = s.Code
	}
	return out
}

func TestCarryCopiesNewAndNewerAndKeepsANewerDestination(t *testing.T) {
	f := newCarryFixture(t)
	f.put(f.src, 1, nil)                                                                // only on the source
	f.put(f.src, 2, map[string]any{"lastActivityAt": 2000, "title": "newer on source"}) // newer on the source
	f.put(f.dst, 2, map[string]any{"lastActivityAt": 1500})
	f.put(f.src, 3, map[string]any{"lastActivityAt": 1000}) // newer on the destination
	kept := f.put(f.dst, 3, map[string]any{"lastActivityAt": 3000, "title": "newer on destination"})
	f.put(f.src, 4, nil) // identical on both
	f.put(f.dst, 4, nil)
	f.put(f.dst, 5, nil) // only on the destination: never touched, never removed
	keptBefore := read(t, kept)

	p := f.plan()
	want := map[string]carryAction{"local_" + carryID(1) + ".json": carryNew, "local_" + carryID(2) + ".json": carryUpdated}
	if got := carryActions(p); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("writes = %v, want %v", got, want)
	}
	if p.Identical != 1 || p.KeptNewer != 1 || p.KeptCache != 0 || len(p.Skipped) != 0 {
		t.Fatalf("counts: identical=%d keptNewer=%d keptCache=%d skipped=%v", p.Identical, p.KeptNewer, p.KeptCache, p.Skipped)
	}
	f.apply(p)
	for _, n := range []int{1, 2, 4} {
		if read(t, f.file(f.dst, n)) != read(t, f.file(f.src, n)) {
			t.Fatalf("conversation %d is not an exact copy", n)
		}
	}
	if read(t, kept) != keptBefore {
		t.Fatal("a destination with newer messages was overwritten")
	}
	if _, err := os.Stat(f.file(f.dst, 5)); err != nil {
		t.Fatal("a conversation that only the destination has must stay")
	}
	if again := f.plan(); len(again.writes) != 0 {
		t.Fatalf("a second plan right after applying must have nothing to write: %v", carryActions(again))
	}
}

func TestCarryEqualActivitySourceWinsUnlessOnlyCachesDiffer(t *testing.T) {
	f := newCarryFixture(t)
	// Same messages (same activity time), but archived on the source only.
	f.put(f.src, 1, map[string]any{"isArchived": true})
	old := f.put(f.dst, 1, map[string]any{"isArchived": false})
	oldBytes := read(t, old)
	// Same messages, only a cache detail differs: the destination is left alone.
	f.put(f.src, 2, map[string]any{"lastFocusedAt": 5000})
	cacheOnly := f.put(f.dst, 2, map[string]any{"lastFocusedAt": 4000})
	cacheBytes := read(t, cacheOnly)

	p := f.plan()
	if got := carryActions(p); len(got) != 1 || got["local_"+carryID(1)+".json"] != carryRefreshed {
		t.Fatalf("writes = %v, want only conversation 1 refreshed", got)
	}
	if p.KeptCache != 1 {
		t.Fatalf("keptCache = %d, want 1", p.KeptCache)
	}
	bk := f.apply(p)
	if read(t, old) != read(t, f.file(f.src, 1)) {
		t.Fatal("the source copy must win when more than caches differ")
	}
	if read(t, cacheOnly) != cacheBytes {
		t.Fatal("a cache-only difference must leave the destination alone")
	}
	// The replaced copy is still in the backup: rolling back brings it back.
	if err := sealBackup(f.d, bk); err != nil {
		t.Fatal(err)
	}
	if err := restoreBackup(f.d, bk); err != nil {
		t.Fatal(err)
	}
	if read(t, old) != oldBytes {
		t.Fatal("the backup must hold the destination's previous copy")
	}
}

func TestCarrySkipsOddConversationsWithoutRefusing(t *testing.T) {
	f := newCarryFixture(t)
	name := func(n int) string { return "local_" + carryID(n) + ".json" }
	f.put(f.src, 1, nil) // the healthy one that must still be carried

	write(t, f.file(f.src, 2), `{"sessionId": not json`)
	f.put(f.src, 3, map[string]any{"sessionId": "local_" + carryID(99)})
	f.put(f.src, 4, map[string]any{"lastActivityAt": nil})
	f.put(f.src, 5, nil) // a message is queued for it on the source
	write(t, filepath.Join(f.dir(f.src), "waiting-input", "local_"+carryID(5)+".json"),
		`{"v":1,"sessionId":"local_`+carryID(5)+`","entries":[{"id":"queued"}],"cleanShutdown":true,"savedAt":10}`)
	f.put(f.src, 6, nil) // a message is queued for it on the destination
	f.put(f.dst, 6, map[string]any{"lastActivityAt": 900})
	write(t, filepath.Join(f.dir(f.dst), "waiting-input", "local_"+carryID(6), "blob"), "queued")
	write(t, filepath.Join(f.dir(f.src), "remote_"+carryID(7)+".json"), `{"sessionId":"remote_`+carryID(7)+`","lastActivityAt":1000}`)
	f.put(f.src, 8, map[string]any{"stagedTranscriptPath": "/somewhere/imported-staging/x.jsonl"})
	f.put(f.src, 9, nil) // deleted on the destination
	write(t, filepath.Join(f.dir(f.dst), "deleted_"+carryID(9)), "1700000000000")
	f.put(f.src, 10, map[string]any{"cliSessionId": carryID(0xD000)}) // its transcript belongs to another conversation there
	f.put(f.dst, 11, map[string]any{"cliSessionId": carryID(0xD000)})
	f.put(f.src, 12, nil) // the destination continued separately
	f.put(f.dst, 12, map[string]any{"cliSessionId": carryID(0xE000), "lastActivityAt": 900})
	f.put(f.src, 13, nil) // named by a file this helper does not know
	write(t, filepath.Join(f.dir(f.dst), "new-index.json"), `{"gone":["`+carryID(13)+`"]}`)
	f.put(f.src, 14, map[string]any{"lastActivityAt": 2000}) // the destination copy cannot be read
	write(t, f.file(f.dst, 14), `not json`)
	f.put(f.src, 15, nil) // deleted on the source but its record is still there
	write(t, filepath.Join(f.dir(f.src), "deleted_"+carryID(15)), "1700000000000")

	p := f.plan()
	if got := carryActions(p); len(got) != 1 || got[name(1)] != carryNew {
		t.Fatalf("only the healthy conversation is written, got %v", got)
	}
	want := map[string]string{
		name(2): "RECORD_UNREADABLE", name(3): "RECORD_MALFORMED", name(4): "RECORD_MALFORMED",
		name(5): "QUEUED_MESSAGE", name(6): "QUEUED_MESSAGE", "remote_" + carryID(7) + ".json": "REMOTE_RECORD",
		name(8): "STILL_IMPORTING", name(9): "DELETED_ON_DESTINATION", name(10): "TRANSCRIPT_OWNED_ELSEWHERE",
		name(12): "DESTINATION_BRANCHED", name(13): "NAMED_IN_UNKNOWN_FILE", name(14): "DESTINATION_UNREADABLE",
	}
	got := carrySkips(p)
	for n, code := range want {
		if got[n] != code {
			t.Errorf("%s: skip code %q, want %q", n, got[n], code)
		}
	}
	if len(got) != len(want) {
		t.Errorf("skips = %v, want exactly %v", got, want)
	}
	if got[name(15)] != "" {
		t.Error("a conversation deleted on the source is left out silently")
	}
	f.apply(p)
	if read(t, f.file(f.dst, 1)) != read(t, f.file(f.src, 1)) {
		t.Fatal("the healthy conversation must be carried despite the odd ones")
	}
	for _, n := range []int{5, 8, 9, 10, 13} {
		if _, err := os.Stat(f.file(f.dst, n)); !os.IsNotExist(err) {
			t.Fatalf("skipped conversation %d must not be written", n)
		}
	}
	if s := p.summary(); s.Outcome != "partial" || s.New != 1 || len(s.Skipped) != len(want) {
		t.Fatalf("summary: %+v", s)
	}
	for _, s := range p.summary().Skipped {
		if len(s.Record) != 8 {
			t.Fatalf("the journal holds a short record id only: %q", s.Record)
		}
	}
}

func TestCarryIgnoresUnknownEntriesAndAStaleArchiveIndex(t *testing.T) {
	f := newCarryFixture(t)
	f.put(f.src, 1, map[string]any{"isArchived": false})
	// The destination's archive hint still lists it; Claude repairs that itself.
	write(t, filepath.Join(f.dir(f.dst), "archived-sessions.idx"), `{"v":1,"archived":["local_`+carryID(1)+`"]}`)
	write(t, filepath.Join(f.dir(f.src), "scheduled-tasks.json"), `{"scheduledTasks":[]}`)
	write(t, filepath.Join(f.dir(f.src), "imported-staging", "x.jsonl"), "staged")
	write(t, filepath.Join(f.dir(f.src), "future-file-"+carryID(77)+".bin"), "opaque")
	link := filepath.Join(f.dir(f.src), "local_"+carryID(2)+".json")
	if err := os.Symlink(f.file(f.src, 1), link); err != nil {
		t.Fatal(err)
	}
	index := read(t, filepath.Join(f.dir(f.dst), "archived-sessions.idx"))

	p := f.plan()
	if got := carryActions(p); len(got) != 1 || got["local_"+carryID(1)+".json"] != carryNew {
		t.Fatalf("writes = %v", got)
	}
	sort.Strings(p.Ignored)
	if want := []string{"future-file-<id>.bin", "imported-staging/", "local_<id>.json (link)"}; fmt.Sprint(p.Ignored) != fmt.Sprint(want) {
		t.Fatalf("ignored = %v, want %v", p.Ignored, want)
	}
	// The planner never follows a link. (The backup itself refuses a folder
	// that contains one, so it is removed before applying.)
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	f.apply(p)
	if read(t, filepath.Join(f.dir(f.dst), "archived-sessions.idx")) != index {
		t.Fatal("the archive index is Claude's to repair; the helper never rewrites it")
	}
	if _, err := os.Stat(filepath.Join(f.dir(f.dst), "scheduled-tasks.json")); !os.IsNotExist(err) {
		t.Fatal("scheduled tasks stay with their account")
	}
	if _, err := os.Stat(filepath.Join(f.dir(f.dst), "imported-staging")); !os.IsNotExist(err) {
		t.Fatal("unknown folders are never copied")
	}
}

func TestCarryReportsNewRecordFieldsAndNeverRefuses(t *testing.T) {
	f := newCarryFixture(t)
	f.put(f.src, 1, map[string]any{"someFieldFromAFutureClaude": map[string]any{"x": 1}, "anotherNewField": "v", "emptyNewField": ""})
	p := f.plan()
	if fmt.Sprint(p.NewFields) != "[anotherNewField someFieldFromAFutureClaude]" {
		t.Fatalf("new fields = %v", p.NewFields)
	}
	f.apply(p)
	if read(t, f.file(f.dst, 1)) != read(t, f.file(f.src, 1)) {
		t.Fatal("a record with unknown fields is copied exactly")
	}
}

func TestCarryApplyNeedsItsBackupAndAnUnchangedSource(t *testing.T) {
	f := newCarryFixture(t)
	f.put(f.src, 1, nil)
	p := f.plan()
	if _, err := p.Apply(); errorCode(err) != "BACKUP_NOT_ACTIVE" {
		t.Fatalf("apply without a backup: %v", err)
	}
	p = f.plan()
	f.put(f.src, 1, map[string]any{"title": "changed after planning"})
	mustBackup(t, f.d, filepath.Join(f.root, "backups"))
	if _, err := p.Apply(); errorCode(err) != "CARRY_SOURCE_CHANGED" {
		t.Fatalf("apply after the source changed: %v", err)
	}
	if _, err := os.Stat(f.file(f.dst, 1)); !os.IsNotExist(err) {
		t.Fatal("nothing may be written when the source changed since planning")
	}
	if _, err := p.Apply(); err == nil {
		t.Fatal("a plan is applied at most once")
	}
}

func TestCarryCreatesAMissingDestinationFolderAndToleratesAMissingSource(t *testing.T) {
	f := newCarryFixture(t)
	f.put(f.src, 1, nil)
	if err := os.RemoveAll(filepath.Join(f.d.sessionsDir(), f.dst.Account)); err != nil {
		t.Fatal(err)
	}
	p := f.plan()
	f.apply(p)
	if read(t, f.file(f.dst, 1)) != read(t, f.file(f.src, 1)) {
		t.Fatal("the destination folder is created when the account never had one")
	}
	if info, err := os.Stat(f.dir(f.dst)); err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("the new folder is private: %v", err)
	}

	empty := newCarryFixture(t)
	if err := os.RemoveAll(filepath.Join(empty.d.sessionsDir(), empty.src.Account)); err != nil {
		t.Fatal(err)
	}
	if p := empty.plan(); len(p.writes) != 0 || len(p.Skipped) != 0 {
		t.Fatal("a source with no conversations is an empty plan, not an error")
	}
	if _, err := planCarry(empty.d, empty.src, empty.src, nil); err == nil {
		t.Fatal("source and destination must differ")
	}
}

func TestCarryThreeAccountsConvergeWithoutPingPong(t *testing.T) {
	f := newCarryFixture(t)
	third := carryNamespace{Account: carryID(0xC1), Org: carryID(0xC2)}
	if err := os.MkdirAll(f.dir(third), 0o700); err != nil {
		t.Fatal(err)
	}
	a, b, c := f.src, f.dst, third
	f.put(a, 1, map[string]any{"isArchived": true})
	f.put(b, 1, map[string]any{"isArchived": false})
	f.put(c, 1, map[string]any{"title": "third account's stale title"})
	f.put(b, 2, map[string]any{"lastFocusedAt": 1})
	f.put(c, 3, nil)
	hop := func(from, to carryNamespace) int {
		t.Helper()
		p, err := planCarry(f.d, from, to, nil)
		if err != nil {
			t.Fatal(err)
		}
		bk := mustBackup(t, f.d, filepath.Join(f.root, "backups"))
		if _, err := p.Apply(); err != nil {
			t.Fatal(err)
		}
		if err := sealBackup(f.d, bk); err != nil {
			t.Fatal(err)
		}
		if err := settleBackup(f.d, bk); err != nil {
			t.Fatal(err)
		}
		return len(p.writes)
	}
	hop(a, b)
	hop(b, c)
	hop(c, a)
	hop(a, b)
	// Everything has now reached every account: further hops write nothing.
	for _, pair := range [][2]carryNamespace{{a, b}, {b, c}, {c, a}, {a, c}, {c, b}, {b, a}} {
		if n := hop(pair[0], pair[1]); n != 0 {
			t.Fatalf("accounts did not converge: %d writes on a later hop", n)
		}
	}
	for n := 1; n <= 3; n++ {
		if read(t, f.file(a, n)) != read(t, f.file(b, n)) || read(t, f.file(b, n)) != read(t, f.file(c, n)) {
			t.Fatalf("conversation %d differs between accounts after a full round", n)
		}
	}
	if !strings.Contains(read(t, f.file(c, 1)), `"isArchived":true`) {
		t.Fatal("the first source's copy should have travelled the whole round")
	}
}

func TestCarryPlanFailsAsAWholeOnlyWhenTheFolderCannotBeUnderstood(t *testing.T) {
	f := newCarryFixture(t)
	for n := 1; n <= carryMaxEntries+1; n++ {
		write(t, filepath.Join(f.dir(f.src), fmt.Sprintf("filler-%d", n)), "x")
	}
	if _, err := planCarry(f.d, f.src, f.dst, nil); errorCode(err) != "CARRY_TOO_MANY_ENTRIES" {
		t.Fatalf("an implausibly large folder fails the plan with its own code: %v", err)
	}
	skip := newCarryFixture(t)
	skip.put(skip.src, 1, nil)
	skip.put(skip.src, 2, nil)
	p, err := planCarry(skip.d, skip.src, skip.dst, map[string]bool{"local_" + carryID(2) + ".json": true})
	if err != nil || len(p.writes) != 1 || len(p.Skipped) != 0 {
		t.Fatalf("records the caller leaves out are neither written nor reported: %v %v", p, err)
	}
}

// When a conversation is deleted, Claude leaves a marker for the record and
// one for each transcript it owned. A marker under any of those names means
// the conversation is deleted there.
func TestCarryTreatsAMarkerForTheTranscriptAsDeletedToo(t *testing.T) {
	f := newCarryFixture(t)
	f.put(f.src, 1, nil) // its transcript was deleted on the destination
	write(t, filepath.Join(f.dir(f.dst), "deleted_"+carryID(0xC001)), "1700000000000")
	f.put(f.src, 2, nil) // its transcript was deleted on the source, but the record is still there
	write(t, filepath.Join(f.dir(f.src), "deleted_"+carryID(0xC002)), "1700000000000")
	f.put(f.src, 3, nil) // untouched
	p := f.plan()
	if got := carryActions(p); len(got) != 1 || got["local_"+carryID(3)+".json"] != carryNew {
		t.Fatalf("only the conversation nobody deleted is carried: %v", got)
	}
	if got := carrySkips(p); len(got) != 1 || got["local_"+carryID(1)+".json"] != "DELETED_ON_DESTINATION" {
		t.Fatalf("the one deleted on the destination is reported, the one deleted on the source is left out silently: %v", got)
	}
}
