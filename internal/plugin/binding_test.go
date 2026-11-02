package plugin

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
)

func bindingTestSnapshot(key string) bindingSnapshot {
	return bindingSnapshot{KeyID: key, AuthIDs: []string{"b", "a"}, Restricted: true}
}

func bindingTestRequest(t *testing.T, s *bindingScheduler, snapshot bindingSnapshot, candidates []SchedulerAuthCandidate) SchedulerPickRequest {
	t.Helper()
	token, err := s.sign(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	return SchedulerPickRequest{
		Provider:   "codex",
		Model:      "gpt-test",
		Options:    SchedulerOptions{Headers: map[string][]string{bindingHeader: {token}}},
		Candidates: candidates,
	}
}

func bindingTestPick(t *testing.T, s *bindingScheduler, req SchedulerPickRequest) (Envelope, SchedulerPickResponse) {
	t.Helper()
	raw, err := s.pick(req)
	if err != nil {
		t.Fatal(err)
	}
	var envelope Envelope
	if err := json.Unmarshal(raw, &envelope); err != nil {
		t.Fatal(err)
	}
	var response SchedulerPickResponse
	if envelope.OK {
		if err := json.Unmarshal(envelope.Result, &response); err != nil {
			t.Fatal(err)
		}
	}
	return envelope, response
}

func bindingTestError(t *testing.T, envelope Envelope, code string, status int) string {
	t.Helper()
	if envelope.OK || envelope.Error == nil || envelope.Error.Code != code || envelope.Error.HTTPStatus != status {
		t.Fatalf("expected %s/%d, got %+v", code, status, envelope)
	}
	var body struct {
		Error *struct {
			Message string          `json:"message"`
			Type    string          `json:"type"`
			Param   json.RawMessage `json:"param"`
			Code    string          `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(envelope.Error.Message), &body); err != nil {
		t.Fatalf("scheduler error message is not a JSON response body: %v", err)
	}
	errType := "permission_error"
	if status == http.StatusServiceUnavailable {
		errType = "server_error"
	}
	if body.Error == nil || body.Error.Code != code || body.Error.Type != errType || body.Error.Message == "" || string(body.Error.Param) != "null" {
		t.Fatalf("expected OpenAI %s/%s error with message and null param, got %s", code, errType, envelope.Error.Message)
	}
	return body.Error.Message
}

// This intentionally bypasses sign's validation to exercise the verifier with
// authenticated but invalid payloads, rather than only bad MACs.
func bindingTestRawToken(s *bindingScheduler, payload string) string {
	encoded := base64.RawURLEncoding.EncodeToString([]byte(payload))
	mac := hmac.New(sha256.New, s.secret[:])
	_, _ = io.WriteString(mac, encoded)
	return encoded + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func TestBindingSnapshotCanonicalRoundTrip(t *testing.T) {
	s := newBindingScheduler()
	snapshot := bindingTestSnapshot("key-a")
	snapshot.AuthIDs = []string{"b", "a", "b"}
	snapshot.Group = " TEAM "
	token, err := s.sign(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(snapshot.AuthIDs, []string{"b", "a", "b"}) {
		t.Fatalf("sign mutated caller's IDs: %v", snapshot.AuthIDs)
	}
	got, _, present, err := s.snapshot(map[string][]string{strings.ToLower(bindingHeader): {token}})
	if err != nil || !present {
		t.Fatalf("decode: present=%v err=%v", present, err)
	}
	want := bindingSnapshot{Version: bindingSnapshotVersion, KeyID: "key-a", AuthIDs: []string{"a", "b"}, Group: "team", Restricted: true}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("snapshot = %+v, want %+v", got, want)
	}
	canonicalToken, err := s.sign(want)
	if err != nil || canonicalToken != token {
		t.Fatalf("noncanonical signing: token differs=%v err=%v", canonicalToken != token, err)
	}
	other := newBindingScheduler()
	if _, _, _, err := other.snapshot(map[string][]string{bindingHeader: {token}}); err == nil {
		t.Fatal("another scheduler instance accepted the signature")
	}
}

func TestBindingRejectsTamperingAndDuplicateHeaders(t *testing.T) {
	s := newBindingScheduler()
	req := bindingTestRequest(t, s, bindingTestSnapshot("key"), nil)
	token := req.Options.Headers[bindingHeader][0]
	different, err := s.sign(bindingTestSnapshot("other"))
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string]map[string][]string{
		"tampered payload":               {bindingHeader: {"A" + token[1:]}},
		"tampered signature":             {bindingHeader: {token + "A"}},
		"malformed":                      {bindingHeader: {"not-a-snapshot"}},
		"extra segment":                  {bindingHeader: {token + ".extra"}},
		"equal duplicate values":         {bindingHeader: {token, token}},
		"conflicting duplicate values":   {bindingHeader: {token, different}},
		"equal mixed-case headers":       {bindingHeader: {token}, strings.ToLower(bindingHeader): {token}},
		"conflicting mixed-case headers": {bindingHeader: {token}, strings.ToLower(bindingHeader): {different}},
		"merged values":                  {bindingHeader: {token + ", " + different}},
		"empty value":                    {bindingHeader: {""}},
		"no values":                      {bindingHeader: nil},
	}
	for name, headers := range cases {
		t.Run(name, func(t *testing.T) {
			req.Options.Headers = headers
			envelope, _ := bindingTestPick(t, s, req)
			bindingTestError(t, envelope, "codex_binding_invalid", http.StatusForbidden)
		})
	}
	if len(s.pools) != 0 {
		t.Fatal("invalid requests created round-robin pools")
	}
}

func TestBindingRejectsInvalidSignedPayloads(t *testing.T) {
	s := newBindingScheduler()
	valid := `{"version":1,"key_id":"key","ids":["a"],"group":"","fallback":false,"restricted":true}`
	cases := map[string]string{
		"malformed JSON":             `{`,
		"unknown version":            strings.Replace(valid, `"version":1`, `"version":2`, 1),
		"empty key":                  strings.Replace(valid, `"key_id":"key"`, `"key_id":""`, 1),
		"whitespace key":             strings.Replace(valid, `"key_id":"key"`, `"key_id":" key "`, 1),
		"missing field":              strings.Replace(valid, `"fallback":false,`, ``, 1),
		"null field":                 strings.Replace(valid, `"fallback":false`, `"fallback":null`, 1),
		"unknown field":              strings.Replace(valid, `"group":""`, `"group":"","extra":true`, 1),
		"duplicate field":            strings.Replace(valid, `"version":1`, `"version":1,"version":1`, 1),
		"empty ID":                   strings.Replace(valid, `["a"]`, `[""]`, 1),
		"whitespace ID":              strings.Replace(valid, `["a"]`, `[" a "]`, 1),
		"duplicate IDs":              strings.Replace(valid, `["a"]`, `["a","a"]`, 1),
		"unsorted IDs":               strings.Replace(valid, `["a"]`, `["b","a"]`, 1),
		"noncanonical group":         strings.Replace(valid, `"group":""`, `"group":" TEAM "`, 1),
		"unrestricted with IDs":      strings.Replace(valid, `"restricted":true`, `"restricted":false`, 1),
		"restricted without filters": strings.Replace(valid, `["a"]`, `[]`, 1),
		"trailing JSON":              valid + `{}`,
	}
	for name, payload := range cases {
		t.Run(name, func(t *testing.T) {
			req := SchedulerPickRequest{Options: SchedulerOptions{Headers: map[string][]string{bindingHeader: {bindingTestRawToken(s, payload)}}}}
			envelope, _ := bindingTestPick(t, s, req)
			bindingTestError(t, envelope, "codex_binding_invalid", http.StatusForbidden)
		})
	}
}

func TestBindingSignRejectsInvalidSnapshots(t *testing.T) {
	s := newBindingScheduler()
	cases := []bindingSnapshot{
		{Version: 2, KeyID: "key"},
		{},
		{KeyID: " key "},
		{KeyID: "key", AuthIDs: []string{""}, Restricted: true},
		{KeyID: "key", AuthIDs: []string{" a "}, Restricted: true},
		{KeyID: "key", AuthIDs: []string{"a"}},
		{KeyID: "key", Restricted: true},
	}
	for _, snapshot := range cases {
		if _, err := s.sign(snapshot); err == nil {
			t.Fatalf("signed invalid snapshot: %+v", snapshot)
		}
	}
}

func TestBindingCanonicalizesEmptyIDs(t *testing.T) {
	s := newBindingScheduler()
	snapshot := bindingSnapshot{KeyID: "group", Group: "team", Restricted: true}
	withoutIDs, err := s.sign(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	snapshot.AuthIDs = []string{}
	emptyIDs, err := s.sign(snapshot)
	if err != nil || emptyIDs != withoutIDs {
		t.Fatalf("equivalent empty ID assignments differ: equal=%v err=%v", emptyIDs == withoutIDs, err)
	}
}
