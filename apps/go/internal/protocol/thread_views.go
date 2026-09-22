package protocol

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"strings"
)

// PruneThreadView projects frontend state onto authoritative live thread IDs.
// Closed threads remain live. Unknown fields and other threads' raw values are
// preserved; unchanged payloads are returned byte-for-byte to support CAS
// reconciliation. A malformed known field fails instead of discarding drafts.
func PruneThreadView(data json.RawMessage, live map[string]bool) (json.RawMessage, error) {
	return PruneThreadViewCommands(data, live, nil)
}

// PruneThreadViewCommands also removes a pending creation command whose retained
// identity was explicitly deleted. New, unaccepted creations remain untouched.
func PruneThreadViewCommands(data json.RawMessage, live map[string]bool, deletedCommandIDs map[string]bool) (json.RawMessage, error) {
	fields := make(map[string]json.RawMessage)
	decoder := json.NewDecoder(bytes.NewReader(data))
	start, err := decoder.Token()
	if err != nil || start != json.Delim('{') {
		return nil, fmt.Errorf("view must be a JSON object")
	}
	changed := false
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return nil, err
		}
		key, ok := token.(string)
		if !ok {
			return nil, fmt.Errorf("invalid view field")
		}
		for _, known := range []string{"Threads", "Active", "Edit", "Pending", "PendingAction", "StartedDraft", "DraftThreads", "DraftProjectID"} {
			if strings.EqualFold(key, known) {
				changed = changed || key != known
				key = known
				break
			}
		}
		if _, exists := fields[key]; exists {
			return nil, fmt.Errorf("duplicate view field %q", key)
		}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return nil, err
		}
		fields[key] = value
	}
	if _, err = decoder.Token(); err != nil {
		return nil, err
	}
	if _, err = decoder.Token(); err != io.EOF {
		return nil, fmt.Errorf("unexpected trailing view data")
	}
	if raw, ok := fields["Threads"]; ok && !bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		var threads map[string]json.RawMessage
		if err := json.Unmarshal(raw, &threads); err != nil {
			return nil, fmt.Errorf("invalid Threads: %w", err)
		}
		pruned := false
		for id := range threads {
			if !live[id] {
				delete(threads, id)
				pruned = true
			}
		}
		if pruned {
			fields["Threads"] = rawObject(threads)
			changed = true
		}
	}
	if raw, ok := fields["Active"]; ok {
		var active string
		if err := json.Unmarshal(raw, &active); err != nil {
			return nil, fmt.Errorf("invalid Active: %w", err)
		}
		if active != "" && !live[active] {
			fields["Active"] = json.RawMessage(`""`)
			changed = true
		}
	}
	for _, field := range []string{"Edit", "Pending"} {
		raw, ok := fields[field]
		if !ok || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			continue
		}
		var scoped struct{ ID, ThreadID string }
		if err := json.Unmarshal(raw, &scoped); err != nil {
			return nil, fmt.Errorf("invalid %s: %w", field, err)
		}
		if (scoped.ThreadID != "" && !live[scoped.ThreadID]) || (field == "Pending" && deletedCommandIDs[scoped.ID]) {
			delete(fields, field)
			if field == "Pending" {
				delete(fields, "PendingAction")
			}
			changed = true
		}
	}
	// An acknowledged first send may still be awaiting its snapshot in the
	// frontend. Deletion removes that accepted capture without erasing text
	// typed after it was submitted or any other project's unsent draft.
	// Shapes are validated on every write, not only when pruning applies; a
	// stored view that cannot be projected would otherwise surface later,
	// inside an unrelated thread or project deletion.
	var drafts map[string]json.RawMessage
	if raw, ok := fields["DraftThreads"]; ok && !bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		if err := json.Unmarshal(raw, &drafts); err != nil {
			return nil, fmt.Errorf("invalid DraftThreads: %w", err)
		}
		for _, rawDraft := range drafts {
			var draft struct{ Draft string }
			if err := json.Unmarshal(rawDraft, &draft); err != nil {
				return nil, fmt.Errorf("invalid project draft: %w", err)
			}
		}
	}
	if raw, ok := fields["DraftProjectID"]; ok {
		var id string
		if err := json.Unmarshal(raw, &id); err != nil {
			return nil, fmt.Errorf("invalid DraftProjectID: %w", err)
		}
	}
	if raw, ok := fields["StartedDraft"]; ok && !bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		var started struct {
			ThreadID string
			Command  Command
		}
		if err := json.Unmarshal(raw, &started); err != nil {
			return nil, fmt.Errorf("invalid StartedDraft: %w", err)
		}
		if started.ThreadID != "" && !live[started.ThreadID] {
			delete(fields, "StartedDraft")
			changed = true
			if rawDraft, ok := drafts[started.Command.ProjectID]; ok {
				var draft struct{ Draft string }
				_ = json.Unmarshal(rawDraft, &draft)
				if draft.Draft == started.Command.Text {
					delete(drafts, started.Command.ProjectID)
					fields["DraftThreads"] = rawObject(drafts)
				}
			}
		}
	}
	// An older/incomplete save can retain a thread action without its command.
	// Other action IDs may identify prompts or surfaces, so do not guess their scope.
	if raw, ok := fields["PendingAction"]; ok {
		var action struct{ Kind, ID, ThreadID string }
		if err := json.Unmarshal(raw, &action); err != nil {
			return nil, fmt.Errorf("invalid PendingAction: %w", err)
		}
		invalid := action.ThreadID != "" && !live[action.ThreadID]
		invalid = invalid || (strings.HasPrefix(action.Kind, "thread") && action.ID != "" && !live[action.ID])
		if invalid {
			delete(fields, "PendingAction")
			changed = true
		}
	}
	if !changed {
		return data, nil
	}
	return rawObject(fields), nil
}

// Keep each untouched value's exact JSON representation, including numbers and
// unknown nested fields, while using deterministic field ordering.
func rawObject(fields map[string]json.RawMessage) json.RawMessage {
	keys := make([]string, 0, len(fields))
	for key := range fields {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	var out bytes.Buffer
	out.WriteByte('{')
	for i, key := range keys {
		if i > 0 {
			out.WriteByte(',')
		}
		encoded, _ := json.Marshal(key)
		out.Write(encoded)
		out.WriteByte(':')
		out.Write(fields[key])
	}
	out.WriteByte('}')
	return out.Bytes()
}
