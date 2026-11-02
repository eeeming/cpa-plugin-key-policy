package policy

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
)

var (
	ErrInvalidCodexBindingBatch = errors.New("invalid Codex binding batch")
	ErrCodexBindingConflict     = errors.New("Codex binding changed since preview")
	ErrCodexBindingSave         = errors.New("Codex binding batch save failed")
)

// CodexBinding always includes empty and false values in management responses.
type CodexBinding struct {
	CodexAuthIDs  []string `json:"codex_auth_ids"`
	CodexGroup    string   `json:"codex_group"`
	CodexFallback bool     `json:"codex_fallback"`
}

type CodexBindingExpected struct {
	ID     string       `json:"id"`
	Before CodexBinding `json:"before"`
}

type CodexBindingBatchRequest struct {
	IDs           []string               `json:"ids"`
	AccountsMode  *string                `json:"accounts_mode,omitempty"`
	CodexAuthIDs  *[]string              `json:"codex_auth_ids,omitempty"`
	CodexGroup    *string                `json:"codex_group,omitempty"`
	CodexFallback *bool                  `json:"codex_fallback,omitempty"`
	Clear         bool                   `json:"clear,omitempty"`
	DryRun        bool                   `json:"dry_run,omitempty"`
	Expected      []CodexBindingExpected `json:"expected,omitempty"`
}

type CodexBindingChange struct {
	ID     string       `json:"id"`
	Before CodexBinding `json:"before"`
	After  CodexBinding `json:"after"`
}

type CodexBindingBatchResult struct {
	DryRun  bool                 `json:"dry_run"`
	Updated int                  `json:"updated"`
	Changes []CodexBindingChange `json:"changes"`
}

func bindingOf(key KeyConfig) CodexBinding {
	ids := make([]string, len(key.CodexAuthIDs))
	copy(ids, key.CodexAuthIDs)
	return CodexBinding{CodexAuthIDs: ids, CodexGroup: key.CodexGroup, CodexFallback: key.CodexFallback}
}

func sameBinding(a, b CodexBinding) bool {
	return slices.Equal(a.CodexAuthIDs, b.CodexAuthIDs) && a.CodexGroup == b.CodexGroup && a.CodexFallback == b.CodexFallback
}

func (req CodexBindingBatchRequest) validate() (string, map[string]CodexBinding, error) {
	invalid := func(message string) (string, map[string]CodexBinding, error) {
		return "", nil, fmt.Errorf("%w: %s", ErrInvalidCodexBindingBatch, message)
	}
	if len(req.IDs) == 0 {
		return invalid("ids must not be empty")
	}
	selected := make(map[string]struct{}, len(req.IDs))
	for _, id := range req.IDs {
		if id == "" || strings.TrimSpace(id) != id {
			return invalid("ids must be nonempty exact trimmed policy ids")
		}
		if _, exists := selected[id]; exists {
			return invalid("duplicate policy id")
		}
		selected[id] = struct{}{}
	}
	mode := "keep"
	if req.AccountsMode != nil {
		mode = *req.AccountsMode
	}
	switch mode {
	case "keep":
		if req.CodexAuthIDs != nil {
			return invalid("codex_auth_ids requires set, add, or remove accounts_mode")
		}
	case "set", "add", "remove":
		if req.CodexAuthIDs == nil {
			return invalid("accounts_mode requires codex_auth_ids")
		}
		if mode != "set" && !slices.ContainsFunc(*req.CodexAuthIDs, func(id string) bool { return strings.TrimSpace(id) != "" }) {
			return invalid("add and remove require nonempty account ids")
		}
	default:
		return invalid("unknown accounts_mode")
	}
	if req.Clear {
		if req.AccountsMode != nil || req.CodexAuthIDs != nil || req.CodexGroup != nil || req.CodexFallback != nil {
			return invalid("clear cannot be combined with binding operations or fields")
		}
	} else if mode == "keep" && req.CodexGroup == nil && req.CodexFallback == nil {
		return invalid("a binding operation is required")
	}
	var expected map[string]CodexBinding
	if req.Expected != nil {
		if len(req.Expected) != len(selected) {
			return invalid("expected must contain every selected policy exactly once")
		}
		expected = make(map[string]CodexBinding, len(req.Expected))
		for _, entry := range req.Expected {
			if _, ok := selected[entry.ID]; !ok {
				return invalid("expected contains an unselected policy id")
			}
			if _, exists := expected[entry.ID]; exists {
				return invalid("expected contains a duplicate policy id")
			}
			expected[entry.ID] = entry.Before
		}
	}
	return mode, expected, nil
}

func (req CodexBindingBatchRequest) nextBinding(key KeyConfig, mode string, normalized KeyConfig) CodexBinding {
	if req.Clear {
		return CodexBinding{CodexAuthIDs: []string{}}
	}
	if req.CodexAuthIDs != nil {
		switch mode {
		case "set":
			key.CodexAuthIDs = normalized.CodexAuthIDs
		case "add":
			key.CodexAuthIDs = append(slices.Clone(key.CodexAuthIDs), normalized.CodexAuthIDs...)
			slices.Sort(key.CodexAuthIDs)
			key.CodexAuthIDs = slices.Compact(key.CodexAuthIDs)
		case "remove":
			ids := make([]string, 0, len(key.CodexAuthIDs))
			for _, id := range key.CodexAuthIDs {
				if _, found := slices.BinarySearch(normalized.CodexAuthIDs, id); !found {
					ids = append(ids, id)
				}
			}
			key.CodexAuthIDs = ids
		}
	}
	if req.CodexGroup != nil {
		key.CodexGroup = normalized.CodexGroup
	}
	if req.CodexFallback != nil {
		key.CodexFallback = *req.CodexFallback
	}
	return bindingOf(key)
}

// BatchCodexBinding stages a complete state file before publishing any binding.
// updateMu serializes policy writers; persistMu spans snapshot, save, and publish
// so a usage flusher cannot overwrite the staged file with old allocations.
func (s *Store) BatchCodexBinding(req CodexBindingBatchRequest) (CodexBindingBatchResult, error) {
	out := CodexBindingBatchResult{DryRun: req.DryRun, Changes: []CodexBindingChange{}}
	mode, expected, err := req.validate()
	if err != nil {
		return out, err
	}
	// Normalize account ids and group once using the single-PATCH convention.
	input := KeyConfig{ID: "binding"}
	if req.CodexAuthIDs != nil {
		input.CodexAuthIDs = *req.CodexAuthIDs
	}
	if req.CodexGroup != nil {
		input.CodexGroup = *req.CodexGroup
	}
	normalized, err := normalizeKey(input)
	if err != nil {
		return out, fmt.Errorf("%w: %w", ErrInvalidCodexBindingBatch, err)
	}
	s.updateMu.Lock()
	defer s.updateMu.Unlock()
	s.persistMu.Lock()
	defer s.persistMu.Unlock()
	s.mu.RLock()
	for _, id := range req.IDs {
		if s.keys[id] == nil {
			s.mu.RUnlock()
			return out, fmt.Errorf("%w: %s", ErrUnknownKey, id)
		}
	}
	for _, id := range req.IDs {
		key := s.keys[id]
		before := bindingOf(*key)
		if wanted, ok := expected[id]; ok && !sameBinding(before, wanted) {
			s.mu.RUnlock()
			return CodexBindingBatchResult{}, fmt.Errorf("%w: %s", ErrCodexBindingConflict, id)
		}
		out.Changes = append(out.Changes, CodexBindingChange{ID: id, Before: before, After: req.nextBinding(*key, mode, normalized)})
	}
	if req.DryRun {
		s.mu.RUnlock()
		return out, nil
	}
	path := s.statePath
	keys := s.keysSnapshotLocked()
	usage := s.usageSnapshotLocked()
	s.mu.RUnlock()
	staged := make(map[string]CodexBinding, len(out.Changes))
	for _, change := range out.Changes {
		staged[change.ID] = change.After
	}
	now := time.Now().UTC()
	for i := range keys {
		if binding, ok := staged[keys[i].ID]; ok {
			keys[i].CodexAuthIDs = binding.CodexAuthIDs
			keys[i].CodexGroup = binding.CodexGroup
			keys[i].CodexFallback = binding.CodexFallback
			keys[i].UpdatedAt = now
		}
	}
	if path != "" {
		if err := SaveState(path, keys, usage); err != nil {
			return CodexBindingBatchResult{}, fmt.Errorf("%w: %w", ErrCodexBindingSave, err)
		}
	}
	// Only binding fields and their update timestamp are published. Admissions
	// and usage events during the save retain their live timestamps and counters.
	s.mu.Lock()
	for _, change := range out.Changes {
		key := s.keys[change.ID]
		key.CodexAuthIDs = slices.Clone(change.After.CodexAuthIDs)
		key.CodexGroup = change.After.CodexGroup
		key.CodexFallback = change.After.CodexFallback
		key.UpdatedAt = now
	}
	s.mu.Unlock()
	out.Updated = len(req.IDs)
	return out, nil
}
