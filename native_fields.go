package main

// Claude's conversation records are copied byte-for-byte, so a field this
// helper has never seen cannot break a carry-over: unknown fields are
// information, never a refusal. knownRecordFields is the baseline of field
// names seen up to Claude Desktop 4.23118.1 (the Windows review of November
// 7, 2026 plus everything observed on the Mac). Anything outside it is
// reported once in the journal and in Status, and copied like everything else.
//
// The first evening of real use on Windows showed why: Claude wrote five new
// fields, then a sixth within the hour, and an allowlist refused every switch.

import (
	"bytes"
	"encoding/json"
	"sort"
	"strings"
)

var knownRecordFields = func() map[string]bool {
	known := map[string]bool{}
	for _, name := range strings.Fields(`
		alwaysAllowedReasons armedWorkAtQuit autoChosenInApp autoFixEnabled autoFixNoticeSent
		autoModeServerFallbackPrompt branch bridgeSessionIds bypassChosenInApp chromePermissionMode
		chromeTabGroupId classifierSummaryEnabled cliBinaryPin cliMcpAppServerNames cliSessionId color
		completedTurns connectorWireNames contextExceededCount createdAt cuAllowedApps cuGrantFlags
		cuLastScreenshotDims cuSelectedDisplayId cwd devIntentTriggers devIntents durableCronMade effort
		enabledMcpTools endedIdleAsOf envScopeId errorCode folderWorktrees forkedFromSessionId
		fromCliTranscript gitAnchors gitAnchorsFolderRealpath gitAnchorsLookupOnly importedFrom indexedAt
		interruptedAwaitingPerson interruptedByQuitAt interruptedByQuitFor isArchived isStarred
		keepCoversOwnTree lastActivityAt lastAssistantCliSessionId lastAssistantUuid lastFocusedAt
		lastSpawnRootDetected lastTurnCutShort lastTurnEdgeAt lastTurnReport lastTurnStopped
		latestUserFrameAt launcherUsedAppSignIn lineageDetachedBy model movedToCloud originCwd
		peerMessaged peerReceipts pendingFirstStart pendingSystemReminder permissionMode
		permissionModePicked postTurnSummary postTurnSummaryFor preClearCliSessionId previousTitles
		priorCliSessionIds priorCliSessionLeftAt priorRemoteCliSessionIds prNumber prRepository prState
		prUrl promptAppendSnapshot promptSuggestion prs publishedArtifacts remoteControlAutoEligible
		remoteControlUserEnabled remoteMcpServersConfig reportFindingsCard requestedAdditionalFolders
		resolvedBackgroundTaskSuggestions rewindEdges scheduledTaskId scratchFilesLeftIn
		scratchPromptRecents seenCommentIds servedConnectorCalls sessionId sessionPermissionUpdates
		sessionSettings setupTools shellFolders sideSessionNotes sideSessionWakesSinceUserMessage
		sourceBranch spaceId spawnSeed spawnedFrom sshConfig sshReattach stagedTranscriptPath
		startedFromEnvironmentId steeredByRemoteClient stoppedUntilPersonSends terminalClaudeTabOrdinal
		title titleSource titleTurn toolSurfaceSnapshot transcriptModelStates turnBoxDeclared
		turnBoxMounted turnWrapUp unarchivedCliSessionId worktreeName worktreePath writtenBranches
		writtenRepositories wslConfig`) {
		known[name] = true
	}
	return known
}()

func recordValueEmpty(raw json.RawMessage) bool {
	switch string(bytes.TrimSpace(raw)) {
	case "", "null", "false", "0", `""`, "[]", "{}":
		return true
	}
	return false
}

// newRecordFields lists a record's non-empty field names that are outside the
// baseline, sorted. Field names are schema, not content, so they may be shown.
func newRecordFields(record map[string]json.RawMessage) []string {
	var names []string
	for name, raw := range record {
		if !knownRecordFields[name] && !recordValueEmpty(raw) {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}

// mergeNames returns the sorted union of two name lists.
func mergeNames(have, more []string) []string {
	if len(more) == 0 {
		return have
	}
	seen := make(map[string]bool, len(have)+len(more))
	var out []string
	for _, list := range [][]string{have, more} {
		for _, name := range list {
			if !seen[name] {
				seen[name] = true
				out = append(out, name)
			}
		}
	}
	sort.Strings(out)
	return out
}
