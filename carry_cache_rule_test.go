package main

import (
	"encoding/json"
	"strings"
	"testing"
)

// The equal-history rule (guided_equivalent_cache.go). When both accounts
// hold the same conversation with the same messages, the destination copy is
// left alone only if nothing but reviewed cache details differ. For anything
// else the copy from the account being left wins. These cases were first
// written for the older planner, where "not equivalent" refused the whole
// switch.

func cacheDetails() map[string]any {
	return map[string]any{
		"lastFocusedAt":        12345,
		"cliBinaryPin":         map[string]any{"version": "2.1.1", "wrapper": "1.0.0", "currentAsOfMs": 12345},
		"cliMcpAppServerNames": []string{"destination-app"},
		"remoteMcpServersConfig": []any{map[string]any{
			"uuid": "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb", "name": "destination-connector", "url": "https://example.invalid/mcp",
			"tools": []any{map[string]any{"name": "read", "inputSchema": map[string]any{"type": "object"}, "annotations": map[string]any{"readOnlyHint": true}}},
		}},
	}
}

func withFields(base map[string]any, more map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range base {
		out[k] = v
	}
	for k, v := range more {
		out[k] = v
	}
	return out
}

// tieKeepsDestination plans one conversation that has the same activity on
// both accounts and reports whether the destination copy is left alone
// (true) or replaced by the source copy (false).
func tieKeepsDestination(t *testing.T, source, destination map[string]any) bool {
	t.Helper()
	f := newCarryFixture(t)
	f.put(f.src, 1, source)
	f.put(f.dst, 1, destination)
	before := read(t, f.file(f.dst, 1))
	p := f.plan()
	switch {
	case len(p.writes) == 0 && p.KeptCache == 1:
		f.apply(p)
		if read(t, f.file(f.dst, 1)) != before {
			t.Fatal("a kept destination must not change by a single byte")
		}
		return true
	case len(p.writes) == 1 && p.writes[0].Action == carryRefreshed && p.KeptCache == 0:
		f.apply(p)
		if read(t, f.file(f.dst, 1)) != read(t, f.file(f.src, 1)) {
			t.Fatal("when the source wins the destination becomes an exact copy")
		}
		return false
	}
	t.Fatalf("unexpected plan: writes=%v keptCache=%d identical=%d skipped=%v", carryActions(p), p.KeptCache, p.Identical, p.Skipped)
	return false
}

func TestEqualHistoryKeepsADestinationThatDiffersOnlyInCaches(t *testing.T) {
	if !tieKeepsDestination(t, nil, cacheDetails()) {
		t.Fatal("cache details alone must not make the source copy win")
	}
}

func TestEqualHistorySourceWinsOnMeaningfulOrUnknownDifferences(t *testing.T) {
	for _, key := range []string{"model", "effort", "permissionMode", "sessionSettings", "lastAssistantUuid", "stoppedUntilPersonSends", "interruptedAwaitingPerson", "armedWorkAtQuit", "unknownFutureNativeField", "newUnknown"} {
		t.Run(key, func(t *testing.T) {
			if tieKeepsDestination(t, nil, withFields(cacheDetails(), map[string]any{key: "different"})) {
				t.Fatal("a difference that is not a reviewed cache detail must not be kept")
			}
		})
	}
}

func TestEqualHistorySourceWinsOnMalformedOrOversizedCaches(t *testing.T) {
	for name, mutation := range map[string]func(map[string]any){
		"focus_type":         func(v map[string]any) { v["lastFocusedAt"] = "12345" },
		"focus_negative":     func(v map[string]any) { v["lastFocusedAt"] = -1 },
		"pin_unknown":        func(v map[string]any) { v["cliBinaryPin"].(map[string]any)["other"] = true },
		"pin_wrong_type":     func(v map[string]any) { v["cliBinaryPin"] = "2.1.1" },
		"pin_null_timestamp": func(v map[string]any) { v["cliBinaryPin"].(map[string]any)["currentAsOfMs"] = json.RawMessage("null") },
		"names_wrong_type":   func(v map[string]any) { v["cliMcpAppServerNames"] = []any{42} },
		"names_oversize":     func(v map[string]any) { v["cliMcpAppServerNames"] = []string{strings.Repeat("x", 1025)} },
		"config_wrong_type":  func(v map[string]any) { v["remoteMcpServersConfig"] = map[string]any{} },
		"config_unknown":     func(v map[string]any) { v["remoteMcpServersConfig"].([]any)[0].(map[string]any)["unknown"] = true },
		"tool_schema_wrong_type": func(v map[string]any) {
			v["remoteMcpServersConfig"].([]any)[0].(map[string]any)["tools"].([]any)[0].(map[string]any)["inputSchema"] = "invalid"
		},
	} {
		t.Run(name, func(t *testing.T) {
			destination := cacheDetails()
			mutation(destination)
			if tieKeepsDestination(t, nil, destination) {
				t.Fatal("a cache detail that is not in its reviewed form must not be kept")
			}
		})
	}
}

const tieAssistantMessage = "dddddddd-dddd-4ddd-8ddd-dddddddddddd"

// The record's own CLI session, as carryFixture.put writes it for conversation 1.
var tieCLISession = carryID(0xC001)

func TestEqualHistoryKeepsADestinationWithEquivalentAssistantProvenance(t *testing.T) {
	explicit := map[string]any{"lastAssistantUuid": tieAssistantMessage, "lastAssistantCliSessionId": tieCLISession}
	for name, other := range map[string]map[string]any{
		"absent": {"lastAssistantUuid": tieAssistantMessage},
		"null":   {"lastAssistantUuid": tieAssistantMessage, "lastAssistantCliSessionId": json.RawMessage("null")},
	} {
		t.Run("source_explicit_"+name, func(t *testing.T) {
			if !tieKeepsDestination(t, explicit, other) {
				t.Fatal("naming the record's own session says the same as leaving it out")
			}
		})
		t.Run("destination_explicit_"+name, func(t *testing.T) {
			if !tieKeepsDestination(t, other, explicit) {
				t.Fatal("naming the record's own session says the same as leaving it out")
			}
		})
	}
}

func TestEqualHistorySourceWinsWhenAssistantProvenanceMeansSomethingElse(t *testing.T) {
	for name, mutate := range map[string]func(map[string]any){
		"explicit_empty":    func(v map[string]any) { v["lastAssistantCliSessionId"] = "" },
		"different_cli":     func(v map[string]any) { v["lastAssistantCliSessionId"] = "eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee" },
		"missing_message":   func(v map[string]any) { delete(v, "lastAssistantUuid") },
		"invalid_message":   func(v map[string]any) { v["lastAssistantUuid"] = "invalid" },
		"different_message": func(v map[string]any) { v["lastAssistantUuid"] = "eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee" },
		"ssh_reattach":      func(v map[string]any) { v["sshReattach"] = map[string]any{"namedCliSessionId": tieCLISession} },
		"ssh_config":        func(v map[string]any) { v["sshConfig"] = map[string]any{} },
		"wsl_config":        func(v map[string]any) { v["wslConfig"] = map[string]any{} },
		"manual_stop":       func(v map[string]any) { v["stoppedUntilPersonSends"] = true },
		"permission_wait":   func(v map[string]any) { v["interruptedAwaitingPerson"] = true },
		"settings":          func(v map[string]any) { v["permissionMode"] = "plan" },
		"prompt_snapshot": func(v map[string]any) {
			v["promptAppendSnapshot"] = map[string]any{"append": "changed instructions", "cliVersion": "2.1.2"}
		},
		"pr_state": func(v map[string]any) { v["prs"] = []any{map[string]any{"prNumber": 1, "state": "MERGED"}} },
		"unknown":  func(v map[string]any) { v["unknownFutureNativeField"] = "different" },
	} {
		t.Run(name, func(t *testing.T) {
			source := map[string]any{"lastAssistantUuid": tieAssistantMessage, "lastAssistantCliSessionId": tieCLISession}
			mutate(source)
			if tieKeepsDestination(t, source, map[string]any{"lastAssistantUuid": tieAssistantMessage}) {
				t.Fatal("a difference in meaning must not be kept")
			}
		})
	}
}

func TestEqualHistoryDoesNotTreatAnUnrelatedNullAsTheSame(t *testing.T) {
	source := map[string]any{"lastAssistantUuid": tieAssistantMessage, "lastAssistantCliSessionId": json.RawMessage("null")}
	if tieKeepsDestination(t, source, map[string]any{"lastAssistantUuid": tieAssistantMessage}) {
		t.Fatal("null on one side and absent on the other is only the same when one side names the session")
	}
}
