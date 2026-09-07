package policy

import (
	"net/http"
	"strings"
	"testing"
)

func TestHashAndMatchKey(t *testing.T) {
	hash, err := HashKey("cpa_secret")
	if err != nil {
		t.Fatalf("HashKey() error = %v", err)
	}
	if !MatchHash("cpa_secret", hash) {
		t.Fatal("MatchHash() = false, want true")
	}
	if MatchHash("wrong", hash) {
		t.Fatal("MatchHash() = true for wrong key")
	}
}

func TestExtractAPIKey(t *testing.T) {
	cases := []struct {
		name    string
		headers http.Header
		query   map[string][]string
		want    string
	}{
		{
			name:    "bearer",
			headers: http.Header{"Authorization": []string{"Bearer cpa_123"}},
			want:    "cpa_123",
		},
		{
			name:    "x-api-key",
			headers: http.Header{"X-API-Key": []string{"cpa_456"}},
			want:    "cpa_456",
		},
		{
			name:  "query",
			query: map[string][]string{"api_key": {"cpa_789"}},
			want:  "cpa_789",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ExtractAPIKey(tc.headers, tc.query); got != tc.want {
				t.Fatalf("ExtractAPIKey() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestPreviewKey(t *testing.T) {
	if got := PreviewKey("cpa_abcdefghijklmnopqrstuvwxyz"); got != "cpa_abc...vwxyz" {
		t.Fatalf("PreviewKey() = %q", got)
	}
}

func TestCallerScopeIsSalted(t *testing.T) {
	scope := CallerScope("downstream-secret")
	if scope == "" || scope == "downstream-secret" {
		t.Fatalf("scope = %q", scope)
	}
	hash, err := HashKey("downstream-secret")
	if err != nil {
		t.Fatal(err)
	}
	if scope == hash || scope == strings.TrimPrefix(hash, HashPrefix) {
		t.Fatal("caller_scope must not equal HashKey")
	}
	if CallerScope("downstream-secret") != scope {
		t.Fatal("CallerScope must be stable")
	}
}
