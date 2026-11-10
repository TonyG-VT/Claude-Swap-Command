package main

// Carrying conversations from the account being left to the account being
// opened. Claude keeps one folder of conversation records per account
// (claude-code-sessions/<account>/<org>/local_<id>.json); the transcripts
// themselves are shared. This file plans and applies an exact copy of the
// source folder's records into the destination folder while Claude is closed.
//
// The rules:
//   - Everything follows. No approvals.
//   - Newer wins: a destination copy with newer activity is kept.
//   - Same activity, different bytes: a difference in pure cache details
//     leaves the destination alone; anything else and the source copy wins.
//   - One odd conversation never blocks the rest. It is skipped with a code.
//   - Copies are exact. Unknown fields are reported; unknown files and
//     folders are ignored and reported. Claude's archive index and its
//     schedule file are never read into the decision and never written.
//   - Nothing is ever removed from either folder.
//
// Every write goes through the active backup transaction, so the previous
// destination bytes stay recoverable.

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"
)

const carryUnknownFileLimit = 1 << 20

// carryMaxEntries is the most entries one conversation folder may hold and
// still be carried. It is a variable only so a test can lower it.
var carryMaxEntries = 2048

var carryIDPattern = regexp.MustCompile(`[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}`)

// carryNamespace names one account's conversation folder.
type carryNamespace struct{ Account, Org string }

func (ns carryNamespace) valid() bool { return validID(ns.Account) && validID(ns.Org) }

type carryAction string

const (
	carryNew       carryAction = "new"       // not on the destination yet
	carryUpdated   carryAction = "updated"   // the source has newer messages
	carryRefreshed carryAction = "refreshed" // same messages, other details differ; the source wins
)

type carryWrite struct {
	Name                   string
	Action                 carryAction
	Source, Target         string
	SourceFile, TargetFile fileFingerprint
}

// carrySkip is one conversation left as it was. The title is for the Status
// window only; it never goes to the journal or the log.
type carrySkip struct{ Name, Title, Code string }

type carryPlan struct {
	desktop                         desktop
	src, dst                        carryNamespace
	writes                          []carryWrite
	Identical, KeptNewer, KeptCache int
	Skipped                         []carrySkip
	Ignored                         []string // unknown entries, identifiers masked
	NewFields                       []string // record fields outside the baseline
	applied                         bool
}

type carryRecord struct {
	Name, ID, Path, Title, CLI, Project string
	Activity                            int64
	Claims                              []string
	File                                fileFingerprint
	Staged                              bool
	NewFields                           []string
	Problem                             string
}

type carryFolder struct {
	records    map[string]*carryRecord
	remote     []string
	deleted    map[string]bool
	queued     map[string]bool
	queuedAll  bool
	ignored    []string
	unknownIDs map[string]bool
}

// deletes reports whether this folder holds a "deleted" marker for the record.
// When a conversation is deleted Claude leaves a marker for the record and
// one for every transcript it owned, and honours any of them.
func (f *carryFolder) deletes(r *carryRecord) bool {
	if f.deleted[r.ID] {
		return true
	}
	for _, claim := range r.Claims {
		if f.deleted[claim] {
			return true
		}
	}
	return false
}

func carryRecordID(name string) string {
	stem := strings.TrimSuffix(name, ".json")
	if i := strings.IndexByte(stem, '_'); i >= 0 {
		return stem[i+1:]
	}
	return stem
}

func maskIDs(name string) string { return carryIDPattern.ReplaceAllString(name, "<id>") }

func scanCarryFolder(dir string) (*carryFolder, error) {
	f := &carryFolder{records: map[string]*carryRecord{}, deleted: map[string]bool{}, queued: map[string]bool{}, unknownIDs: map[string]bool{}}
	info, err := os.Lstat(dir)
	if errors.Is(err, os.ErrNotExist) {
		return f, nil
	}
	if err != nil {
		return nil, codedf("CARRY_FOLDER_UNREADABLE", "a conversation folder could not be read (%w)", err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, coded("CARRY_FOLDER_UNREADABLE", "a conversation folder is not an ordinary folder")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, codedf("CARRY_FOLDER_UNREADABLE", "a conversation folder could not be read (%w)", err)
	}
	if len(entries) > carryMaxEntries {
		return nil, coded("CARRY_TOO_MANY_ENTRIES", "a conversation folder holds more entries than this helper carries")
	}
	for _, entry := range entries {
		name := entry.Name()
		path := filepath.Join(dir, name)
		switch {
		case entry.Type()&os.ModeSymlink != 0:
			f.ignored = append(f.ignored, maskIDs(name)+" (link)")
		case name == "waiting-input" && entry.IsDir():
			f.scanQueued(path)
		case entry.IsDir():
			f.ignored = append(f.ignored, maskIDs(name)+"/")
		case name == "archived-sessions.idx" || name == "scheduled-tasks.json":
			// Claude's own index and its per-account schedule stay as they are.
		case strings.HasPrefix(name, "deleted_") && validID(strings.TrimPrefix(name, "deleted_")):
			f.deleted[strings.TrimPrefix(name, "deleted_")] = true
		case strings.HasPrefix(name, "remote_") && strings.HasSuffix(name, ".json"):
			f.remote = append(f.remote, name)
		case strings.HasPrefix(name, "local_") && strings.HasSuffix(name, ".json") && validID(carryRecordID(name)):
			f.records[name] = readCarryRecord(path, name)
		default:
			f.ignored = append(f.ignored, maskIDs(name))
			// A file this helper does not know may be a new kind of index or
			// marker. Any record it names is left alone on this account.
			if info, err := entry.Info(); err == nil && info.Mode().IsRegular() && info.Size() <= carryUnknownFileLimit {
				if data, err := os.ReadFile(path); err == nil {
					for _, id := range carryIDPattern.FindAll(data, -1) {
						f.unknownIDs[string(id)] = true
					}
				}
			}
		}
	}
	return f, nil
}

// scanQueued notes which conversations have input waiting to be sent. Claude
// restores that input for the account it was queued on, so such a conversation
// is neither copied from this folder nor overwritten in it.
func (f *carryFolder) scanQueued(root string) {
	entries, err := os.ReadDir(root)
	if err != nil {
		f.queuedAll = true
		return
	}
	for _, entry := range entries {
		name := entry.Name()
		stem := strings.TrimSuffix(name, ".json")
		id := strings.TrimPrefix(stem, "local_")
		if !strings.HasPrefix(stem, "local_") || !validID(id) {
			f.ignored = append(f.ignored, "waiting-input/"+maskIDs(name))
			continue
		}
		path := filepath.Join(root, name)
		if entry.IsDir() {
			if children, err := os.ReadDir(path); err != nil || len(children) > 0 {
				f.queued[id] = true
			}
			continue
		}
		data, _, err := readRecordFile(path, false, queuedManifestLimit)
		if err != nil || !carryEmptyWaitingManifest(data, id) {
			f.queued[id] = true
		}
	}
}

// carryEmptyWaitingManifest accepts only Claude's exact "nothing queued, shut
// down cleanly" manifest. Anything else counts as queued input.
func carryEmptyWaitingManifest(data []byte, id string) bool {
	if !validQueuedManifest(data, id) {
		return false
	}
	var raw map[string]json.RawMessage
	if json.Unmarshal(data, &raw) != nil || len(raw) != 5 {
		return false
	}
	var pending []json.RawMessage
	var clean bool
	var savedAt int64
	return json.Unmarshal(raw["entries"], &pending) == nil && pending != nil && len(pending) == 0 &&
		json.Unmarshal(raw["cleanShutdown"], &clean) == nil && clean &&
		json.Unmarshal(raw["savedAt"], &savedAt) == nil && savedAt > 0
}

func readCarryRecord(path, name string) *carryRecord {
	r := &carryRecord{Name: name, ID: carryRecordID(name), Path: path}
	data, fp, err := readRecordFile(path, false, recordSizeLimit)
	if err != nil {
		r.Problem = "RECORD_UNREADABLE"
		return r
	}
	r.File = fp
	var raw map[string]json.RawMessage
	if !jsonUnambiguous(data) || json.Unmarshal(data, &raw) != nil || raw == nil {
		r.Problem = "RECORD_UNREADABLE"
		return r
	}
	var sessionID string
	if json.Unmarshal(raw["sessionId"], &sessionID) != nil || sessionID+".json" != name ||
		json.Unmarshal(raw["lastActivityAt"], &r.Activity) != nil || r.Activity <= 0 {
		r.Problem = "RECORD_MALFORMED"
		return r
	}
	_ = json.Unmarshal(raw["cwd"], &r.Project)
	var title string
	if json.Unmarshal(raw["title"], &title) == nil {
		r.Title = carryTitle(title)
	}
	// Every identifier a record claims names a transcript it owns: its own id,
	// its current command-line session and any earlier ones.
	claims := []string{r.ID}
	for _, key := range []string{"cliSessionId", "unarchivedCliSessionId", "preClearCliSessionId"} {
		value, present := raw[key]
		if !present || recordValueEmpty(value) {
			continue
		}
		var id string
		if json.Unmarshal(value, &id) != nil || !validID(id) {
			r.Problem = "RECORD_MALFORMED"
			return r
		}
		if key == "cliSessionId" {
			r.CLI = id
		}
		claims = append(claims, id)
	}
	for _, key := range []string{"priorCliSessionIds", "priorRemoteCliSessionIds"} {
		value, present := raw[key]
		if !present || recordValueEmpty(value) {
			continue
		}
		var ids []string
		if json.Unmarshal(value, &ids) != nil || len(ids) > 256 {
			r.Problem = "RECORD_MALFORMED"
			return r
		}
		for _, id := range ids {
			if !validID(id) {
				r.Problem = "RECORD_MALFORMED"
				return r
			}
			claims = append(claims, id)
		}
	}
	sort.Strings(claims)
	for _, id := range claims {
		if len(r.Claims) == 0 || r.Claims[len(r.Claims)-1] != id {
			r.Claims = append(r.Claims, id)
		}
	}
	var staged string
	r.Staged = json.Unmarshal(raw["stagedTranscriptPath"], &staged) == nil && staged != ""
	r.NewFields = newRecordFields(raw)
	return r
}

func carryTitle(title string) string {
	title = strings.Join(strings.Fields(title), " ")
	if utf8.RuneCountInString(title) > 80 {
		runes := []rune(title)
		title = string(runes[:79]) + "…"
	}
	return title
}

func (r *carryRecord) forCacheRule() transferRecord {
	return transferRecord{Path: r.Path, Name: r.Name, Project: r.Project, CLI: r.CLI, Activity: r.Activity, Claims: r.Claims, File: r.File}
}

func claimsSubset(inner, outer []string) bool {
	have := make(map[string]bool, len(outer))
	for _, id := range outer {
		have[id] = true
	}
	for _, id := range inner {
		if !have[id] {
			return false
		}
	}
	return true
}

// planCarry performs no writes. skip names source records the caller leaves
// out entirely (neither written nor reported).
func planCarry(d desktop, src, dst carryNamespace, skip map[string]bool) (*carryPlan, error) {
	if !src.valid() || !dst.valid() || src == dst {
		return nil, coded("CARRY_ACCOUNTS_INVALID", "the two accounts to carry conversations between could not be identified")
	}
	srcDir, err := safeRelative(d.sessionsDir(), filepath.Join(src.Account, src.Org))
	if err != nil {
		return nil, coded("CARRY_FOLDER_UNREADABLE", "a conversation folder is not an ordinary folder")
	}
	dstDir, err := safeRelative(d.sessionsDir(), filepath.Join(dst.Account, dst.Org))
	if err != nil {
		return nil, coded("CARRY_FOLDER_UNREADABLE", "a conversation folder is not an ordinary folder")
	}
	source, err := scanCarryFolder(srcDir)
	if err != nil {
		return nil, err
	}
	target, err := scanCarryFolder(dstDir)
	if err != nil {
		return nil, err
	}
	p := &carryPlan{desktop: d, src: src, dst: dst, Ignored: mergeNames(mergeNames(nil, source.ignored), target.ignored)}

	// Which destination record owns each transcript.
	owner := map[string]string{}
	for name, r := range target.records {
		if r.Problem == "" {
			for _, id := range r.Claims {
				owner[id] = name
			}
		}
	}
	names := make([]string, 0, len(source.records))
	for name := range source.records {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		r := source.records[name]
		if skip[name] || source.deletes(r) {
			continue
		}
		p.NewFields = mergeNames(p.NewFields, r.NewFields)
		reason := ""
		existing := target.records[name]
		switch {
		case r.Problem != "":
			reason = r.Problem
		case target.deletes(r):
			reason = "DELETED_ON_DESTINATION"
		case source.queuedAll || target.queuedAll || source.queued[r.ID] || target.queued[r.ID]:
			reason = "QUEUED_MESSAGE"
		case r.Staged:
			reason = "STILL_IMPORTING"
		case target.unknownIDs[r.ID]:
			reason = "NAMED_IN_UNKNOWN_FILE"
		case existing != nil && existing.Problem != "":
			reason = "DESTINATION_UNREADABLE"
		}
		if reason == "" {
			for _, id := range r.Claims {
				if other, taken := owner[id]; taken && other != name {
					reason = "TRANSCRIPT_OWNED_ELSEWHERE"
					break
				}
			}
		}
		if reason != "" {
			p.Skipped = append(p.Skipped, carrySkip{Name: name, Title: r.Title, Code: reason})
			continue
		}
		write := carryWrite{Name: name, Source: r.Path, Target: filepath.Join(dstDir, name), SourceFile: r.File}
		switch {
		case existing == nil:
			write.Action = carryNew
		case existing.File.SHA256 == r.File.SHA256:
			p.Identical++
			continue
		case !claimsSubset(existing.Claims, r.Claims):
			// The destination copy owns a transcript the source copy does not
			// know about: the conversation continued separately there.
			p.Skipped = append(p.Skipped, carrySkip{Name: name, Title: r.Title, Code: "DESTINATION_BRANCHED"})
			continue
		case existing.Activity > r.Activity:
			p.KeptNewer++
			continue
		case existing.Activity < r.Activity:
			write.Action, write.TargetFile = carryUpdated, existing.File
		case equalHistoryKeepingDestination(r.forCacheRule(), existing.forCacheRule()):
			p.KeptCache++
			continue
		default:
			write.Action, write.TargetFile = carryRefreshed, existing.File
		}
		p.writes = append(p.writes, write)
	}
	sort.Strings(source.remote)
	for _, name := range source.remote {
		if !skip[name] {
			p.Skipped = append(p.Skipped, carrySkip{Name: name, Code: "REMOTE_RECORD"})
		}
	}
	return p, nil
}

// Apply copies the planned records. It needs the switch's active backup and
// runs at most once; a failure belongs to that backup's rollback.
func (p *carryPlan) Apply() (int, error) {
	if p == nil || p.applied {
		return 0, coded("CARRY_ALREADY_APPLIED", "this carry-over was already attempted")
	}
	root, err := canonicalDirectory(p.desktop.dir)
	if err != nil {
		return 0, codedf("CARRY_FOLDER_UNREADABLE", "Claude's data folder could not be read (%w)", err)
	}
	backupRegistry.Lock()
	tx := backupRegistry.active[root]
	backupDir := ""
	if tx != nil && (tx.manifest.Phase == "prepared" || tx.manifest.Phase == "mutating") {
		backupDir = tx.dir
	}
	backupRegistry.Unlock()
	if backupDir == "" {
		return 0, coded("BACKUP_NOT_ACTIVE", "conversations are only carried inside a switch's backup")
	}
	p.applied = true
	if len(p.writes) == 0 {
		return 0, nil
	}
	// An account that was never opened on this Mac has no folder yet.
	sessions := p.desktop.sessionsDir()
	for _, dir := range []string{sessions, filepath.Join(sessions, p.dst.Account), filepath.Join(sessions, p.dst.Account, p.dst.Org)} {
		if _, err := os.Lstat(dir); errors.Is(err, os.ErrNotExist) {
			if err := os.Mkdir(dir, 0o700); err != nil {
				return 0, codedf("CARRY_WRITE_FAILED", "the destination's conversation folder could not be created (%w)", err)
			}
		}
	}
	for i, w := range p.writes {
		data, fp, err := readRecordFile(w.Source, false, recordSizeLimit)
		if err != nil || fp != w.SourceFile {
			return i, coded("CARRY_SOURCE_CHANGED", "a conversation changed while it was being copied")
		}
		if err := writeRecordInBackup(p.desktop, w.Target, data, w.SourceFile, w.TargetFile, backupDir); err != nil {
			return i, coded("CARRY_WRITE_FAILED", "a conversation could not be written to the destination account")
		}
	}
	for _, dir := range []string{filepath.Join(sessions, p.dst.Account, p.dst.Org), filepath.Join(sessions, p.dst.Account), sessions} {
		if err := syncDirectory(dir); err != nil {
			return len(p.writes), codedf("CARRY_WRITE_FAILED", "the copied conversations could not be saved to disk (%w)", err)
		}
	}
	for _, w := range p.writes {
		_, fp, err := readRecordFile(w.Target, false, recordSizeLimit)
		if err != nil || fp.SHA256 != w.SourceFile.SHA256 {
			return len(p.writes), coded("CARRY_VERIFY_FAILED", "a copied conversation did not match its original")
		}
	}
	return len(p.writes), nil
}

func shortRecordID(name string) string {
	id := carryRecordID(name)
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

// summary is what the journal keeps about a carry-over: counts, codes and
// short record ids, never titles.
func (p *carryPlan) summary() *journalCarry {
	s := &journalCarry{Outcome: "full", Identical: p.Identical, KeptNewer: p.KeptNewer, KeptCache: p.KeptCache, Ignored: p.Ignored, NewFields: p.NewFields}
	for _, w := range p.writes {
		switch w.Action {
		case carryNew:
			s.New++
		case carryUpdated:
			s.Updated++
		case carryRefreshed:
			s.Refreshed++
		}
	}
	for _, skipped := range p.Skipped {
		s.Skipped = append(s.Skipped, journalSkip{Record: shortRecordID(skipped.Name), Code: skipped.Code})
	}
	if len(p.Skipped) > 0 {
		s.Outcome = "partial"
	}
	return s
}
