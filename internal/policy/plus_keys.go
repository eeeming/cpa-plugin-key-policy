package policy

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

const plusAPIKeysPath = "/v0/management/api-keys"

// PlusAPIKey is one Plus/CPA api-keys entry. Plain is used only to compute
// hash/preview/caller_scope and is never persisted.
type PlusAPIKey struct {
	Plain string
	Name  string
}

// APIKeyLister fetches the current Plus/CPA api-keys list.
type APIKeyLister func() ([]PlusAPIKey, error)

// SyncResult is the outcome of importing Plus api-keys into the plugin.
type SyncResult struct {
	Added    int      `json:"added"`
	Skipped  int      `json:"skipped"`
	Total    int      `json:"total"`
	AddedIDs []string `json:"added_ids,omitempty"`
}

// HTTPAPIKeyLister GETs CPA-Manager-Plus / CPA GET /v0/management/api-keys
// (Bearer admin or management key). The list is typically plaintext strings.
func HTTPAPIKeyLister(client *http.Client, baseURL, managementKey string) APIKeyLister {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	managementKey = strings.TrimSpace(managementKey)
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	return func() ([]PlusAPIKey, error) {
		if baseURL == "" {
			return nil, fmt.Errorf("plus_base_url is empty")
		}
		code, body, err := getPricePath(client, baseURL+plusAPIKeysPath, managementKey)
		if err != nil {
			return nil, err
		}
		if code < 200 || code >= 300 {
			return nil, fmt.Errorf("plus api-keys: HTTP %d", code)
		}
		return ParsePlusAPIKeys(body)
	}
}

func ParsePlusAPIKeys(raw []byte) ([]PlusAPIKey, error) {
	s := strings.TrimSpace(string(raw))
	if s == "" || s == "null" {
		return nil, nil
	}
	if s[0] == '[' {
		return parsePlusAPIKeyArray(raw)
	}
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(raw, &probe); err != nil {
		return nil, err
	}
	for _, key := range []string{"api-keys", "api_keys", "keys", "items", "data"} {
		v, ok := probe[key]
		if !ok || len(v) == 0 || string(v) == "null" {
			continue
		}
		return parsePlusAPIKeyArray(v)
	}
	return nil, nil
}

func parsePlusAPIKeyArray(raw []byte) ([]PlusAPIKey, error) {
	var plains []string
	if err := json.Unmarshal(raw, &plains); err == nil {
		out := make([]PlusAPIKey, 0, len(plains))
		seen := map[string]struct{}{}
		for _, p := range plains {
			p = strings.TrimSpace(p)
			if p == "" {
				continue
			}
			if _, dup := seen[p]; dup {
				continue
			}
			seen[p] = struct{}{}
			out = append(out, PlusAPIKey{Plain: p})
		}
		return out, nil
	}
	var rows []struct {
		Key    string `json:"key"`
		APIKey string `json:"api_key"`
		APIKEY string `json:"apiKey"`
		Token  string `json:"token"`
		Name   string `json:"name"`
	}
	if err := json.Unmarshal(raw, &rows); err != nil {
		return nil, err
	}
	out := make([]PlusAPIKey, 0, len(rows))
	seen := map[string]struct{}{}
	for _, row := range rows {
		p := strings.TrimSpace(row.Key)
		if p == "" {
			p = strings.TrimSpace(row.APIKey)
		}
		if p == "" {
			p = strings.TrimSpace(row.APIKEY)
		}
		if p == "" {
			p = strings.TrimSpace(row.Token)
		}
		if p == "" {
			continue
		}
		if _, dup := seen[p]; dup {
			continue
		}
		seen[p] = struct{}{}
		out = append(out, PlusAPIKey{Plain: p, Name: strings.TrimSpace(row.Name)})
	}
	return out, nil
}
