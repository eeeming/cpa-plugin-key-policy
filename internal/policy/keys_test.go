package policy

import (
	"net/http"
	"os"
	"path/filepath"
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
	short := "sk-short"
	if got := PreviewKey(short); got == short || !strings.Contains(got, "...") {
		t.Fatalf("short preview must not equal plaintext: %q", got)
	}
	if got := SanitizeStoredPreview("sk-bound"); got != "***" {
		t.Fatalf("stale short preview = %q", got)
	}
	masked := "sk-abcde...vwxyz"
	if got := SanitizeStoredPreview(masked); got != masked {
		t.Fatalf("already masked preview rewritten: %q", got)
	}
}

func TestLoadStateRemasksShortPreview(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	raw := []byte(`{"version":1,"keys":[{"id":"k1","name":"k1","key_hash":"sha256:abc","key_preview":"sk-bound"}]}`)
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	st, err := LoadState(path)
	if err != nil {
		t.Fatal(err)
	}
	if st.Keys[0].KeyPreview != "***" {
		t.Fatalf("preview = %q, want remasked", st.Keys[0].KeyPreview)
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
