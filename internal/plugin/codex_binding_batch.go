package plugin

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	"cpa-key-policy/internal/policy"
)

func decodeCodexBindingBatch(body []byte) (policy.CodexBindingBatchRequest, error) {
	var req policy.CodexBindingBatchRequest
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&req); err != nil {
		return req, err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return req, errors.New("expected one JSON object")
	}
	// encoding/json otherwise accepts null for bools, strings and pointers.
	// This endpoint deliberately distinguishes omitted fields from invalid nulls.
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(body, &fields); err != nil {
		return req, err
	}
	if fields == nil {
		return req, errors.New("expected a JSON object")
	}
	for name, raw := range fields {
		if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			return req, fmt.Errorf("%s must not be null", name)
		}
	}
	for _, name := range []string{"ids", "codex_auth_ids"} {
		if raw, ok := fields[name]; ok {
			var ids []json.RawMessage
			if err := json.Unmarshal(raw, &ids); err != nil {
				return req, err
			}
			for _, id := range ids {
				if bytes.Equal(bytes.TrimSpace(id), []byte("null")) {
					return req, fmt.Errorf("%s entries must be strings", name)
				}
			}
		}
	}
	if raw, ok := fields["expected"]; ok {
		var entries []map[string]json.RawMessage
		if err := json.Unmarshal(raw, &entries); err != nil {
			return req, err
		}
		for _, entry := range entries {
			if len(entry) != 2 || entry["id"] == nil || entry["before"] == nil || bytes.Equal(bytes.TrimSpace(entry["id"]), []byte("null")) {
				return req, errors.New("expected entries require id and before")
			}
			var before map[string]json.RawMessage
			if err := json.Unmarshal(entry["before"], &before); err != nil {
				return req, err
			}
			if len(before) != 3 {
				return req, errors.New("expected before requires all binding fields")
			}
			for _, name := range []string{"codex_auth_ids", "codex_group", "codex_fallback"} {
				value, exists := before[name]
				if !exists || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
					return req, fmt.Errorf("expected before requires non-null %s", name)
				}
			}
			var ids []json.RawMessage
			if err := json.Unmarshal(before["codex_auth_ids"], &ids); err != nil {
				return req, err
			}
			for _, id := range ids {
				if bytes.Equal(bytes.TrimSpace(id), []byte("null")) {
					return req, errors.New("expected account ids must be strings")
				}
			}
		}
	}
	return req, nil
}

func (a *App) batchCodexBinding(body []byte) ManagementResponse {
	req, err := decodeCodexBindingBatch(body)
	if err != nil {
		return jsonError(http.StatusBadRequest, "invalid_json", err.Error())
	}
	result, err := a.store.BatchCodexBinding(req)
	if err != nil {
		switch {
		case errors.Is(err, policy.ErrUnknownKey):
			return jsonError(http.StatusNotFound, "not_found", err.Error())
		case errors.Is(err, policy.ErrCodexBindingConflict):
			return jsonError(http.StatusConflict, "codex_binding_conflict", err.Error())
		case errors.Is(err, policy.ErrInvalidCodexBindingBatch):
			return jsonError(http.StatusBadRequest, "invalid_codex_binding_batch", err.Error())
		default:
			return jsonError(http.StatusInternalServerError, "codex_binding_save_failed", err.Error())
		}
	}
	return jsonResponse(http.StatusOK, result)
}
