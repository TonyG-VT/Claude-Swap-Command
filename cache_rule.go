package main

import (
	"bytes"
	"encoding/json"
	"reflect"
)

// transferRecord is what the equal-history rule needs to know about one
// conversation record on disk.
type transferRecord struct {
	Path, Name, Project, CLI string
	Activity                 int64
	Claims                   []string
	File                     fileFingerprint
}

func sameSession(a, b transferRecord) bool {
	return a.Name == b.Name && a.CLI == b.CLI && a.Project == b.Project && reflect.DeepEqual(a.Claims, b.Claims)
}

// A session may be focused or have its native connector/binary cache refreshed
// without a new conversation event. When activity, ownership and every other
// raw field match, retain the complete destination record. Never merge caches,
// copy source connector settings or infer connector readiness from this rule.
// A separately reviewed assistant-provenance fallback is accepted only when it
// resolves to the same current transcript and assistant message on both sides.
func equalHistoryKeepingDestination(a, b transferRecord) bool {
	if !sameSession(a, b) || a.Activity != b.Activity {
		return false
	}
	var decoded [2]map[string]json.RawMessage
	for i, r := range []transferRecord{a, b} {
		data, fp, err := readRecordFile(r.Path, false, recordSizeLimit)
		if err != nil || fp != r.File || !jsonUnambiguous(data) || json.Unmarshal(data, &decoded[i]) != nil || decoded[i] == nil {
			return false
		}
		for _, key := range []string{"lastFocusedAt", "cliBinaryPin", "cliMcpAppServerNames", "remoteMcpServersConfig"} {
			if !reviewedCacheValue(key, decoded[i][key]) {
				return false
			}
			delete(decoded[i], key)
		}
	}
	if !bytes.Equal(decoded[0]["lastAssistantCliSessionId"], decoded[1]["lastAssistantCliSessionId"]) {
		if !equalCurrentAssistantProvenance(decoded[0], decoded[1]) {
			return false
		}
		delete(decoded[0], "lastAssistantCliSessionId")
		delete(decoded[1], "lastAssistantCliSessionId")
	}
	if len(decoded[0]) != len(decoded[1]) {
		return false
	}
	for key, value := range decoded[0] {
		other, exists := decoded[1][key]
		if !exists || !bytes.Equal(value, other) {
			return false
		}
	}
	return true
}

// Native HE (4.16336) / ms (4.23118) resolve absent/null provenance to
// cliSessionId when lastAssistantUuid exists. An explicit empty string does not
// take that fallback. Admit only one explicit current-CLI reference and one
// absent/null fallback, with identical valid current/message identities. This
// comparison never writes the native fallback or changes the retained record.
func equalCurrentAssistantProvenance(a, b map[string]json.RawMessage) bool {
	var current, message string
	explicit := 0
	for i, record := range []map[string]json.RawMessage{a, b} {
		for _, key := range []string{"sshConfig", "wslConfig", "sshReattach"} {
			if raw, exists := record[key]; exists && !bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
				return false
			}
		}
		var cli, assistant string
		if json.Unmarshal(record["cliSessionId"], &cli) != nil || !validID(cli) ||
			json.Unmarshal(record["lastAssistantUuid"], &assistant) != nil || !validID(assistant) {
			return false
		}
		if i == 0 {
			current, message = cli, assistant
		} else if cli != current || assistant != message {
			return false
		}
		raw := record["lastAssistantCliSessionId"]
		if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			continue
		}
		var provenance string
		if json.Unmarshal(raw, &provenance) != nil || provenance != current {
			return false
		}
		explicit++
	}
	return explicit == 1
}

func reviewedCacheValue(key string, raw json.RawMessage) bool {
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return true
	}
	switch key {
	case "lastFocusedAt":
		return cacheTimestamp(raw)
	case "cliBinaryPin":
		v, ok := cacheObject(raw, "version", "wrapper", "currentAsOfMs")
		return ok && len(v) == 3 && cacheString(v["version"], 256) && cacheString(v["wrapper"], 256) && cacheTimestamp(v["currentAsOfMs"])
	case "cliMcpAppServerNames":
		var names []json.RawMessage
		if len(raw) > 256<<10 || json.Unmarshal(raw, &names) != nil || names == nil || len(names) > 256 {
			return false
		}
		for _, name := range names {
			if !cacheString(name, 1024) {
				return false
			}
		}
		return true
	case "remoteMcpServersConfig":
		var servers []json.RawMessage
		if len(raw) > 1<<20 || json.Unmarshal(raw, &servers) != nil || servers == nil || len(servers) > 128 {
			return false
		}
		for _, server := range servers {
			v, ok := cacheObject(server, "uuid", "name", "url", "tools")
			var id string
			if !ok || json.Unmarshal(v["uuid"], &id) != nil || !validID(id) || !cacheString(v["name"], 1024) {
				return false
			}
			if url, exists := v["url"]; exists && !cacheString(url, 8192) {
				return false
			}
			var tools []json.RawMessage
			if json.Unmarshal(v["tools"], &tools) != nil || tools == nil || len(tools) > 2048 {
				return false
			}
			for _, tool := range tools {
				t, ok := cacheObject(tool, "name", "description", "inputSchema", "annotations", "_meta")
				if !ok || !cacheString(t["name"], 1024) || !cacheJSONMap(t["inputSchema"]) {
					return false
				}
				if description, exists := t["description"]; exists {
					var text string
					if json.Unmarshal(description, &text) != nil || len(text) > 65536 {
						return false
					}
				}
				for _, key := range []string{"annotations", "_meta"} {
					if value, exists := t[key]; exists && !bytes.Equal(value, []byte("null")) && !cacheJSONMap(value) {
						return false
					}
				}
			}
		}
		return true
	}
	return false
}

func cacheTimestamp(raw json.RawMessage) bool {
	var n int64
	return !bytes.Equal(raw, []byte("null")) && json.Unmarshal(raw, &n) == nil && n >= 0 && n <= 9007199254740991
}
func cacheString(raw json.RawMessage, limit int) bool {
	var s string
	return json.Unmarshal(raw, &s) == nil && len(s) > 0 && len(s) <= limit
}
func cacheJSONMap(raw json.RawMessage) bool {
	var v map[string]json.RawMessage
	return json.Unmarshal(raw, &v) == nil && v != nil
}
func cacheObject(raw json.RawMessage, keys ...string) (map[string]json.RawMessage, bool) {
	var v map[string]json.RawMessage
	if json.Unmarshal(raw, &v) != nil || v == nil {
		return nil, false
	}
	for key := range v {
		found := false
		for _, allowed := range keys {
			if key == allowed {
				found = true
				break
			}
		}
		if !found {
			return nil, false
		}
	}
	return v, true
}
