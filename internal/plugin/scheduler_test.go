package plugin

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

func bindingTestCandidates() []SchedulerAuthCandidate {
	return []SchedulerAuthCandidate{
		{ID: "b", Provider: "codex", Attributes: map[string]string{"plan_type": "team"}},
		{ID: "a", Provider: "codex", Attributes: map[string]string{"plan_type": "team"}},
	}
}

func bindingTestSelected(t *testing.T, s *bindingScheduler, req SchedulerPickRequest, want string) {
	t.Helper()
	envelope, response := bindingTestPick(t, s, req)
	if !envelope.OK || !response.Handled || response.Reject || response.AuthID != want {
		t.Fatalf("selection = %+v, envelope = %+v, want %q", response, envelope, want)
	}
}

func TestBindingSchedulerErrorEscapesMessage(t *testing.T) {
	message := "Codex binding snapshot rejected: quoted \"error\" at C:\\binding\nnext line"
	for _, tc := range []struct {
		code    string
		errType string
		status  int
	}{
		{"codex_binding_invalid", "permission_error", http.StatusForbidden},
		{"codex_binding_unavailable", "server_error", http.StatusServiceUnavailable},
	} {
		t.Run(tc.code, func(t *testing.T) {
			raw := bindingSchedulerError(tc.code, message, tc.errType, tc.status)
			var envelope Envelope
			if err := json.Unmarshal(raw, &envelope); err != nil {
				t.Fatal(err)
			}
			if got := bindingTestError(t, envelope, tc.code, tc.status); got != message {
				t.Fatalf("client message = %q, want %q", got, message)
			}
		})
	}
}

func TestBindingRoundRobinIsStableAndKeyIsolated(t *testing.T) {
	s := newBindingScheduler()
	candidates := bindingTestCandidates()
	first := bindingTestRequest(t, s, bindingTestSnapshot("key-one"), candidates)
	second := bindingTestRequest(t, s, bindingTestSnapshot("key-two"), candidates)
	for _, want := range []string{"a", "b", "a", "b"} {
		bindingTestSelected(t, s, first, want)
		bindingTestSelected(t, s, second, want)
	}
	if len(s.pools) != 2 {
		t.Fatalf("pool count = %d, want 2", len(s.pools))
	}
}

func TestBindingRoundRobinPoolContextAndCanonicalAssignment(t *testing.T) {
	cases := []struct {
		name   string
		change func(*bindingSnapshot, *SchedulerPickRequest)
	}{
		{"assignment IDs", func(snapshot *bindingSnapshot, _ *SchedulerPickRequest) {
			snapshot.AuthIDs = []string{"a", "b", "unused"}
		}},
		{"assignment group", func(snapshot *bindingSnapshot, _ *SchedulerPickRequest) { snapshot.Group = "team" }},
		{"assignment fallback", func(snapshot *bindingSnapshot, _ *SchedulerPickRequest) { snapshot.Fallback = true }},
		{"provider", func(_ *bindingSnapshot, req *SchedulerPickRequest) { req.Provider = "another-route" }},
		{"model", func(_ *bindingSnapshot, req *SchedulerPickRequest) { req.Model = "another-model" }},
		{"priority", func(_ *bindingSnapshot, req *SchedulerPickRequest) {
			req.Candidates = []SchedulerAuthCandidate{{ID: "a", Provider: "codex", Priority: 5}, {ID: "b", Provider: "codex", Priority: 5}}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := newBindingScheduler()
			snapshot := bindingTestSnapshot("key")
			base := bindingTestRequest(t, s, snapshot, bindingTestCandidates())
			bindingTestSelected(t, s, base, "a")
			changed := base
			tc.change(&snapshot, &changed)
			token, err := s.sign(snapshot)
			if err != nil {
				t.Fatal(err)
			}
			changed.Options.Headers = map[string][]string{bindingHeader: {token}}
			bindingTestSelected(t, s, changed, "a")
			bindingTestSelected(t, s, base, "b")
		})
	}

	s := newBindingScheduler()
	base := bindingTestRequest(t, s, bindingTestSnapshot("key"), bindingTestCandidates())
	bindingTestSelected(t, s, base, "a")
	reordered := bindingTestSnapshot("key")
	reordered.AuthIDs = []string{"a", "b", "a"}
	reorderedRequest := bindingTestRequest(t, s, reordered, bindingTestCandidates())
	bindingTestSelected(t, s, reorderedRequest, "b")
	if len(s.pools) != 1 {
		t.Fatalf("ID order/duplicates split the pool: %d", len(s.pools))
	}
}

func TestBindingFiltersBeforePriority(t *testing.T) {
	s := newBindingScheduler()
	snapshot := bindingTestSnapshot("key")
	snapshot.Group = "team"
	candidates := []SchedulerAuthCandidate{
		{ID: "a", Provider: "openai", Priority: 100, Attributes: map[string]string{"plan_type": "team"}},
		{ID: "not-bound", Provider: "codex", Priority: 90, Attributes: map[string]string{"plan_type": "team"}},
		{ID: "a", Provider: "codex", Priority: 80, Attributes: map[string]string{"plan_type": "pro"}},
		{ID: "a", Provider: "codex", Priority: -3, Attributes: map[string]string{"plan_type": "team"}},
		{ID: "b", Provider: "codex", Priority: -2, Attributes: map[string]string{"plan_type": "team"}},
	}
	req := bindingTestRequest(t, s, snapshot, candidates)
	bindingTestSelected(t, s, req, "b")
	bindingTestSelected(t, s, req, "b")
	if len(s.pools) != 0 {
		t.Fatal("singleton selection allocated a round-robin pool")
	}
}

func TestBindingGroupIntersectionAndTierCompatibility(t *testing.T) {
	s := newBindingScheduler()
	snapshot := bindingTestSnapshot("key")
	snapshot.Group = " TEAM "
	snapshot.AuthIDs = []string{"allowed-plan", "allowed-tier", "wrong-plan", "missing-group"}
	candidates := []SchedulerAuthCandidate{
		{ID: "outside-ids", Provider: "codex", Priority: 100, Attributes: map[string]string{"plan_type": "team"}},
		{ID: "wrong-plan", Provider: "codex", Priority: 90, Attributes: map[string]string{"plan_type": "pro", "tier": "team"}},
		{ID: "missing-group", Provider: "codex", Priority: 80},
		{ID: "allowed-tier", Provider: "codex", Attributes: map[string]string{"plan_type": "  ", "tier": "TEAM "}},
		{ID: "allowed-plan", Provider: " CODEX ", Attributes: map[string]string{"plan_type": " Team ", "tier": "pro"}},
	}
	req := bindingTestRequest(t, s, snapshot, candidates)
	for _, want := range []string{"allowed-plan", "allowed-tier", "allowed-plan"} {
		bindingTestSelected(t, s, req, want)
	}
	groupOnly := bindingSnapshot{KeyID: "group-only", Group: "team", Restricted: true}
	groupRequest := bindingTestRequest(t, s, groupOnly, candidates)
	bindingTestSelected(t, s, groupRequest, "outside-ids")
}

func TestBindingDefaultDenyAndExplicitFallback(t *testing.T) {
	candidates := []SchedulerAuthCandidate{
		{ID: "not-codex", Provider: "openai", Priority: 100},
		{ID: "fallback-low", Provider: "codex", Priority: 1},
		{ID: "fallback-high", Provider: "codex", Priority: 2},
	}
	for _, fallback := range []bool{false, true} {
		t.Run(fmt.Sprintf("fallback=%v", fallback), func(t *testing.T) {
			s := newBindingScheduler()
			snapshot := bindingTestSnapshot("key")
			snapshot.Group = "team"
			snapshot.Fallback = fallback
			req := bindingTestRequest(t, s, snapshot, candidates)
			if fallback {
				bindingTestSelected(t, s, req, "fallback-high")
			} else {
				envelope, _ := bindingTestPick(t, s, req)
				bindingTestError(t, envelope, "codex_binding_unavailable", http.StatusServiceUnavailable)
			}
			req.Candidates = candidates[:1]
			envelope, _ := bindingTestPick(t, s, req)
			bindingTestError(t, envelope, "codex_binding_unavailable", http.StatusServiceUnavailable)
		})
	}

	// Fallback is not a license to prefer a higher-priority unbound account.
	s := newBindingScheduler()
	snapshot := bindingTestSnapshot("key")
	snapshot.Fallback = true
	req := bindingTestRequest(t, s, snapshot, append(candidates, SchedulerAuthCandidate{ID: "a", Provider: "codex", Priority: -10}))
	bindingTestSelected(t, s, req, "a")
}

func TestBindingMissingHeaderAndUnrestrictedAreUnhandled(t *testing.T) {
	s := newBindingScheduler()
	requests := []SchedulerPickRequest{
		{},
		{Candidates: bindingTestCandidates(), Options: SchedulerOptions{Headers: map[string][]string{"Authorization": {"Bearer client"}}}},
		bindingTestRequest(t, s, bindingSnapshot{KeyID: "unrestricted"}, bindingTestCandidates()),
		bindingTestRequest(t, s, bindingSnapshot{KeyID: "unrestricted-fallback", Fallback: true}, nil),
	}
	for _, req := range requests {
		envelope, response := bindingTestPick(t, s, req)
		if !envelope.OK || response.Handled || response.Reject || response.AuthID != "" {
			t.Fatalf("request should delegate unchanged: envelope=%+v response=%+v", envelope, response)
		}
	}
	if s.pools != nil || len(s.poolOrder) != 0 {
		t.Fatal("unrestricted requests established round-robin pools")
	}
}

func TestBindingEmptyCandidatesAndDuplicateIDs(t *testing.T) {
	for _, fallback := range []bool{false, true} {
		s := newBindingScheduler()
		snapshot := bindingTestSnapshot("key")
		snapshot.Fallback = fallback
		for _, candidates := range [][]SchedulerAuthCandidate{
			nil,
			{},
			{{ID: "", Provider: "codex"}},
			{{ID: "a", Provider: "openai"}},
			{{ID: "a", Provider: ""}},
		} {
			req := bindingTestRequest(t, s, snapshot, candidates)
			envelope, _ := bindingTestPick(t, s, req)
			bindingTestError(t, envelope, "codex_binding_unavailable", http.StatusServiceUnavailable)
		}
	}
	s := newBindingScheduler()
	req := bindingTestRequest(t, s, bindingTestSnapshot("key"), []SchedulerAuthCandidate{
		{ID: "b", Provider: "codex"}, {ID: "a", Provider: "codex"}, {ID: "a", Provider: "codex"},
	})
	for _, want := range []string{"a", "b", "a", "b"} {
		bindingTestSelected(t, s, req, want)
	}
}

func TestBindingRetriesKeepTheSignedAssignment(t *testing.T) {
	s := newBindingScheduler()
	candidates := append(bindingTestCandidates(), SchedulerAuthCandidate{ID: "c", Provider: "codex"})
	oldReq := bindingTestRequest(t, s, bindingTestSnapshot("key"), candidates)
	newSnapshot := bindingTestSnapshot("key")
	newSnapshot.AuthIDs = []string{"b", "c"}
	newReq := bindingTestRequest(t, s, newSnapshot, candidates)
	bindingTestSelected(t, s, oldReq, "a")
	bindingTestSelected(t, s, newReq, "b")
	bindingTestSelected(t, s, oldReq, "b")
	bindingTestSelected(t, s, newReq, "c")
	bindingTestSelected(t, s, oldReq, "a")
}

func TestBindingRoundRobinConcurrentPicks(t *testing.T) {
	s := newBindingScheduler()
	req := bindingTestRequest(t, s, bindingTestSnapshot("key"), bindingTestCandidates())
	const workers = 12
	const perWorker = 20
	type result struct {
		raw []byte
		err error
	}
	selected := make(chan result, workers*perWorker)
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range perWorker {
				raw, err := s.pick(req)
				selected <- result{raw: raw, err: err}
			}
		}()
	}
	wg.Wait()
	close(selected)
	counts := map[string]int{}
	for item := range selected {
		if item.err != nil {
			t.Fatal(item.err)
		}
		var envelope Envelope
		if err := json.Unmarshal(item.raw, &envelope); err != nil {
			t.Fatal(err)
		}
		var response SchedulerPickResponse
		if err := json.Unmarshal(envelope.Result, &response); err != nil {
			t.Fatal(err)
		}
		if !envelope.OK || !response.Handled {
			t.Fatalf("unexpected concurrent decision: %+v %+v", envelope, response)
		}
		counts[response.AuthID]++
	}
	if counts["a"] != workers*perWorker/2 || counts["b"] != workers*perWorker/2 {
		t.Fatalf("concurrent round-robin counts = %v", counts)
	}
}

func TestBindingRoundRobinCacheIsBoundedAndEvictsDeterministically(t *testing.T) {
	s := newBindingScheduler()
	candidates := bindingTestCandidates()
	first := bindingTestRequest(t, s, bindingTestSnapshot("first"), candidates)
	bindingTestSelected(t, s, first, "a")
	var latest SchedulerPickRequest
	for i := range bindingPoolLimit {
		latest = bindingTestRequest(t, s, bindingTestSnapshot(fmt.Sprintf("key-%d", i)), candidates)
		bindingTestSelected(t, s, latest, "a")
		if len(s.pools) > bindingPoolLimit || len(s.poolOrder) > bindingPoolLimit {
			t.Fatalf("unbounded pool cache: pools=%d order=%d", len(s.pools), len(s.poolOrder))
		}
	}
	if len(s.pools) != bindingPoolLimit || len(s.poolOrder) != bindingPoolLimit {
		t.Fatalf("unexpected bounded cache size: pools=%d order=%d", len(s.pools), len(s.poolOrder))
	}
	bindingTestSelected(t, s, latest, "b")
	bindingTestSelected(t, s, first, "a")
	if len(s.pools) != bindingPoolLimit {
		t.Fatalf("reinsert exceeded cache bound: %d", len(s.pools))
	}
}

func bindingTestSessionRequest(t *testing.T, s *bindingScheduler, snapshot bindingSnapshot, session string, candidates []SchedulerAuthCandidate) SchedulerPickRequest {
	t.Helper()
	req := bindingTestRequest(t, s, snapshot, candidates)
	req.Options.Metadata = map[string]any{"canonical_session_id": session}
	return req
}

func TestBindingSessionStabilityAndColdSessionDistribution(t *testing.T) {
	s := newBindingScheduler()
	snapshot := bindingTestSnapshot("irreversible-key")
	first := bindingTestSessionRequest(t, s, snapshot, "session-one", bindingTestCandidates())
	second := bindingTestSessionRequest(t, s, snapshot, "session-two", bindingTestCandidates())
	third := bindingTestSessionRequest(t, s, snapshot, "session-three", bindingTestCandidates())
	bindingTestSelected(t, s, first, "a")
	for range 5 {
		bindingTestSelected(t, s, first, "a")
	}
	bindingTestSelected(t, s, second, "b")
	bindingTestSelected(t, s, third, "a")
	bindingTestSelected(t, s, second, "b")
	if len(s.sessions) != 3 || len(s.pools) != 1 {
		t.Fatalf("unexpected cache counts: sessions=%d pools=%d", len(s.sessions), len(s.pools))
	}
}

func TestBindingSessionFailoverOutranksRecoveredPriority(t *testing.T) {
	s := newBindingScheduler()
	snapshot := bindingTestSnapshot("irreversible-key")
	snapshot.AuthIDs = []string{"a", "b", "c"}
	all := []SchedulerAuthCandidate{
		{ID: "a", Provider: "codex", Priority: 10},
		{ID: "b", Provider: "codex", Priority: 0},
		{ID: "c", Provider: "codex", Priority: 0},
	}
	req := bindingTestSessionRequest(t, s, snapshot, "conversation", all)
	bindingTestSelected(t, s, req, "a")
	// The host has filtered the tried/cooling/disabled account out entirely.
	req.Candidates = all[1:]
	bindingTestSelected(t, s, req, "b")
	req.Candidates = all
	for range 3 {
		bindingTestSelected(t, s, req, "b")
	}
	// Multiple configured IDs, temporarily one candidate: update affinity even
	// though there is no RR choice. Recovery must not undo this failover.
	req.Candidates = all[2:]
	bindingTestSelected(t, s, req, "c")
	req.Candidates = all
	bindingTestSelected(t, s, req, "c")
	if len(s.sessions) != 1 {
		t.Fatalf("priority changes split affinity: %d", len(s.sessions))
	}
}

func TestBindingSessionScopeIsolation(t *testing.T) {
	cases := []struct {
		name   string
		change func(*bindingSnapshot, *SchedulerPickRequest)
	}{
		{"key", func(snapshot *bindingSnapshot, _ *SchedulerPickRequest) { snapshot.KeyID = "other-irreversible-key" }},
		{"fingerprint", func(snapshot *bindingSnapshot, _ *SchedulerPickRequest) {
			snapshot.AuthIDs = []string{"a", "b", "unused"}
		}},
		{"provider", func(_ *bindingSnapshot, req *SchedulerPickRequest) { req.Provider = "other-route" }},
		{"model", func(_ *bindingSnapshot, req *SchedulerPickRequest) { req.Model = "other-model" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := newBindingScheduler()
			snapshot := bindingTestSnapshot("irreversible-key")
			base := bindingTestSessionRequest(t, s, snapshot, "same-session", bindingTestCandidates())
			bindingTestSelected(t, s, base, "a")
			changed := base
			tc.change(&snapshot, &changed)
			token, err := s.sign(snapshot)
			if err != nil {
				t.Fatal(err)
			}
			changed.Options.Headers = map[string][]string{bindingHeader: {token}}
			changed.Candidates = bindingTestCandidates()[:1]
			bindingTestSelected(t, s, changed, "b")
			changed.Candidates = bindingTestCandidates()
			bindingTestSelected(t, s, changed, "b")
			bindingTestSelected(t, s, base, "a")
			if len(s.sessions) != 2 {
				t.Fatalf("isolated scopes shared affinity: %d", len(s.sessions))
			}
		})
	}
}

func TestBindingSessionOldAndNewSnapshotsCannotOverwriteEachOther(t *testing.T) {
	s := newBindingScheduler()
	all := append(bindingTestCandidates(), SchedulerAuthCandidate{ID: "c", Provider: "codex"})
	oldReq := bindingTestSessionRequest(t, s, bindingTestSnapshot("irreversible-key"), "same-session", all)
	newSnapshot := bindingTestSnapshot("irreversible-key")
	newSnapshot.AuthIDs = []string{"b", "c"}
	newReq := bindingTestSessionRequest(t, s, newSnapshot, "same-session", all)
	bindingTestSelected(t, s, oldReq, "a")
	bindingTestSelected(t, s, newReq, "b")
	oldReq.Candidates = []SchedulerAuthCandidate{{ID: "b", Provider: "codex"}}
	newReq.Candidates = []SchedulerAuthCandidate{{ID: "c", Provider: "codex"}}
	bindingTestSelected(t, s, oldReq, "b")
	bindingTestSelected(t, s, newReq, "c")
	oldReq.Candidates, newReq.Candidates = all, all
	bindingTestSelected(t, s, oldReq, "b")
	bindingTestSelected(t, s, newReq, "c")
}

func TestBindingSessionTTLIsSlidingAndExpiresAtBoundary(t *testing.T) {
	s := newBindingScheduler()
	now := time.Date(2040, 1, 2, 3, 4, 5, 0, time.UTC)
	s.now = func() time.Time { return now }
	req := bindingTestSessionRequest(t, s, bindingTestSnapshot("irreversible-key"), "session", bindingTestCandidates())
	bindingTestSelected(t, s, req, "a")
	now = now.Add(bindingSessionTTL / 2)
	bindingTestSelected(t, s, req, "a")
	now = now.Add(bindingSessionTTL / 2)
	bindingTestSelected(t, s, req, "a")
	now = now.Add(bindingSessionTTL)
	bindingTestSelected(t, s, req, "b")
	if len(s.sessions) != 1 || len(s.sessionOrder) != 1 {
		t.Fatalf("expiry leaked entries: sessions=%d order=%d", len(s.sessions), len(s.sessionOrder))
	}
}

func TestBindingSessionCacheIsBoundedAndEvictsFIFO(t *testing.T) {
	s := newBindingScheduler()
	req := bindingTestSessionRequest(t, s, bindingTestSnapshot("irreversible-key"), "session-0", bindingTestCandidates())
	bindingTestSelected(t, s, req, "a")
	for i := range bindingSessionLimit {
		req.Options.Metadata = map[string]any{"canonical_session_id": fmt.Sprintf("session-%d", i+1)}
		want := "b"
		if i%2 != 0 {
			want = "a"
		}
		bindingTestSelected(t, s, req, want)
		if len(s.sessions) > bindingSessionLimit || len(s.sessionOrder) > bindingSessionLimit {
			t.Fatalf("unbounded session cache: sessions=%d order=%d", len(s.sessions), len(s.sessionOrder))
		}
	}
	// The most recent session is retained, while the oldest re-enters RR.
	bindingTestSelected(t, s, req, "a")
	req.Options.Metadata = map[string]any{"canonical_session_id": "session-0"}
	bindingTestSelected(t, s, req, "b")
	if len(s.sessions) != bindingSessionLimit || len(s.sessionOrder) != bindingSessionLimit {
		t.Fatalf("incorrect bounded cache: sessions=%d order=%d", len(s.sessions), len(s.sessionOrder))
	}
}

func TestBindingSessionNeverChangesUnboundDelegation(t *testing.T) {
	s := newBindingScheduler()
	requests := []SchedulerPickRequest{
		bindingTestSessionRequest(t, s, bindingSnapshot{KeyID: "irreversible-key"}, "session", bindingTestCandidates()),
		{Options: SchedulerOptions{Headers: map[string][]string{"Session-Id": {"session"}}, Metadata: map[string]any{"canonical_session_id": "canonical"}}, Candidates: bindingTestCandidates()},
	}
	for _, req := range requests {
		envelope, response := bindingTestPick(t, s, req)
		if !envelope.OK || response.Handled {
			t.Fatalf("unbound request was handled: %+v %+v", envelope, response)
		}
	}
	if s.sessions != nil || s.pools != nil {
		t.Fatal("unbound requests created routing caches")
	}
}

func TestBindingSessionRawIdentityIsNotRetained(t *testing.T) {
	s := newBindingScheduler()
	const raw = "sk-client-plaintext-sent-as-session"
	req := bindingTestRequest(t, s, bindingTestSnapshot("irreversible-key"), bindingTestCandidates())
	req.Options.Headers["Session-Id"] = []string{raw}
	req.Options.Headers["Authorization"] = []string{"Bearer secret-not-a-session"}
	bindingTestSelected(t, s, req, "a")
	if len(s.sessionOrder) != 1 || s.sessionOrder[0].session != sha256.Sum256([]byte("codex:"+raw)) {
		t.Fatal("session cache does not use the expected SHA-256 identity")
	}
	state := fmt.Sprintf("%+v %+v %+v %+v", s.sessions, s.sessionOrder, s.pools, s.poolOrder)
	if strings.Contains(state, raw) || strings.Contains(state, "secret-not-a-session") {
		t.Fatal("routing caches retained plaintext request identifiers")
	}
	noSession := bindingTestRequest(t, s, bindingTestSnapshot("irreversible-key"), bindingTestCandidates())
	noSession.Options.Headers["Authorization"] = []string{"Bearer secret-not-a-session"}
	bindingTestSelected(t, s, noSession, "b")
	bindingTestSelected(t, s, noSession, "a")
	if len(s.sessions) != 1 {
		t.Fatal("Authorization was treated as a session identity")
	}
}

func TestBindingSessionIdentityUsesHostVisiblePrecedence(t *testing.T) {
	cases := []struct {
		name     string
		options  SchedulerOptions
		identity string
	}{
		{
			"canonical outranks header and all metadata fallbacks",
			SchedulerOptions{Headers: map[string][]string{"X-Claude-Code-Session-Id": {"header-session"}}, Metadata: map[string]any{"canonical_session_id": "claude:canonical:agent:worker", "execution_session_id": "execution", "lcp_affinity_session_id": "lcp:v1:inferred", "derived_session_id": "derived"}},
			"claude:canonical:agent:worker",
		},
		{
			"Claude header outranks Codex and execution metadata",
			SchedulerOptions{Headers: map[string][]string{"X-Claude-Code-Session-Id": {"claude"}, "Session-Id": {"codex"}}, Metadata: map[string]any{"execution_session_id": "execution"}},
			"claude:claude",
		},
		{
			"Claude named agent has separate identity",
			SchedulerOptions{Headers: map[string][]string{"X-Claude-Code-Session-Id": {"root"}, "X-Claude-Code-Agent-Id": {"worker"}}},
			"claude:root:agent:worker",
		},
		{
			"Claude main agent preserves root identity",
			SchedulerOptions{Headers: map[string][]string{"X-Claude-Code-Session-Id": {"root"}, "X-Claude-Code-Agent-Id": {"main"}}},
			"claude:root",
		},
		{
			"Codex hyphen alias outranks underscore and agy",
			SchedulerOptions{Headers: map[string][]string{"Session-Id": {"preferred"}, "Session_id": {"other"}, "X-Http-Session-Id": {"agy"}}},
			"codex:preferred",
		},
		{
			"Codex lowercase underscore alias",
			SchedulerOptions{Headers: map[string][]string{"session_id": {" underscore "}, "X-Http-Session-Id": {"agy"}}},
			"codex:underscore",
		},
		{
			"Codex child thread outranks parent session",
			SchedulerOptions{Headers: map[string][]string{"Session-Id": {"parent"}, "Thread-Id": {"child"}}},
			"codex:child",
		},
		{
			"Codex named turn agent",
			SchedulerOptions{Headers: map[string][]string{"Session-Id": {"root"}, "Thread-Id": {"child"}, "X-Codex-Turn-Metadata": {`{"agent_name":"/root/worker","subagent_kind":"thread_spawn"}`}}},
			"codex:root:agent:worker",
		},
		{
			"Codex fork identity precedes named agent",
			SchedulerOptions{Headers: map[string][]string{"Session-Id": {"root"}, "Thread_id": {"fork"}, "X-Codex-Turn-Metadata": {`{"agent_name":"worker","forked_from_thread_id":"root"}`}}},
			"codex:fork",
		},
		{
			"Codex turn metadata supplies missing IDs",
			SchedulerOptions{Headers: map[string][]string{"x-codex-turn-metadata": {`{"session_id":"turn-session"}`}, "X-Session-ID": {"generic"}}},
			"codex:turn-session",
		},
		{
			"agy outranks generic",
			SchedulerOptions{Headers: map[string][]string{"X-Http-Session-Id": {"agy"}, "X-Session-ID": {"generic"}}},
			"agy:agy",
		},
		{
			"generic outranks affinity",
			SchedulerOptions{Headers: map[string][]string{"x-session-id": {"generic"}, "X-Session-Affinity": {"affinity"}}},
			"header:generic",
		},
		{
			"affinity outranks slot",
			SchedulerOptions{Headers: map[string][]string{"X-Session-Affinity": {"affinity"}, "X-Slot-Session-Id": {"slot"}}},
			"affinity:affinity",
		},
		{
			"slot outranks task",
			SchedulerOptions{Headers: map[string][]string{"X-Slot-Session-Id": {"slot"}, "X-Task-ID": {"task"}}},
			"slot:slot",
		},
		{
			"task outranks conversation",
			SchedulerOptions{Headers: map[string][]string{"X-Task_ID": {"task"}, "X-Conversation-Id": {"conversation"}}},
			"task:task",
		},
		{
			"conversation outranks thread",
			SchedulerOptions{Headers: map[string][]string{"X-Conversation-Id": {"conversation"}, "X-Thread-Id": {"thread"}}},
			"conv:conversation",
		},
		{
			"thread outranks client request",
			SchedulerOptions{Headers: map[string][]string{"X-Thread-Id": {"thread"}, "X-Client-Request-Id": {"request"}}},
			"thread:thread",
		},
		{
			"client request outranks execution metadata",
			SchedulerOptions{Headers: map[string][]string{"X-Client-Request-Id": {"request"}}, Metadata: map[string]any{"execution_session_id": "execution"}},
			"clientreq:request",
		},
		{
			"execution outranks LCP and derived without canonical",
			SchedulerOptions{Metadata: map[string]any{"execution_session_id": "execution", "lcp_affinity_session_id": "lcp:v1:inferred", "derived_session_id": "derived"}},
			"execution:execution",
		},
		{
			"LCP outranks derived",
			SchedulerOptions{Metadata: map[string]any{"lcp_affinity_session_id": "lcp:v1:inferred", "derived_session_id": "derived"}},
			"lcp:v1:inferred",
		},
		{
			"derived fallback keeps namespace",
			SchedulerOptions{Metadata: map[string]any{"derived_session_id": "ctx:v1:derived"}},
			"derived:ctx:v1:derived",
		},
		{
			"invalid metadata and headers are skipped",
			SchedulerOptions{Headers: map[string][]string{"Session-Id": {"bad\nsession"}, "X-Http-Session-Id": {strings.Repeat("x", 257)}, "X-Session-ID": {" valid "}}, Metadata: map[string]any{"canonical_session_id": "bad\ridentity"}},
			"header:valid",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := newBindingScheduler()
			req := bindingTestRequest(t, s, bindingTestSnapshot("irreversible-key"), bindingTestCandidates())
			token := req.Options.Headers[bindingHeader]
			req.Options = tc.options
			if req.Options.Headers == nil {
				req.Options.Headers = make(map[string][]string)
			}
			req.Options.Headers[bindingHeader] = token
			bindingTestSelected(t, s, req, "a")
			if len(s.sessionOrder) != 1 || s.sessionOrder[0].session != sha256.Sum256([]byte(tc.identity)) {
				t.Fatalf("unexpected session identity, want hash of %q", tc.identity)
			}
		})
	}
}

func TestBindingSessionHeaderAndEnrichedCanonicalShareIdentity(t *testing.T) {
	s := newBindingScheduler()
	raw := strings.Repeat("s", 256)
	req := bindingTestRequest(t, s, bindingTestSnapshot("irreversible-key"), bindingTestCandidates())
	req.Options.Headers["Session-Id"] = []string{raw}
	bindingTestSelected(t, s, req, "a")
	full := "codex:" + raw
	sum := sha256.Sum256([]byte(full))
	canonical := full[:190] + "#" + fmt.Sprintf("%x", sum)
	req.Options.Metadata = map[string]any{"canonical_session_id": canonical}
	bindingTestSelected(t, s, req, "a")
	if len(s.sessions) != 1 {
		t.Fatalf("header/enriched identity split affinity: %d", len(s.sessions))
	}
}

func TestBindingSessionConcurrentColdPicksBindOnlyOnce(t *testing.T) {
	s := newBindingScheduler()
	req := bindingTestSessionRequest(t, s, bindingTestSnapshot("irreversible-key"), "shared-session", bindingTestCandidates())
	type result struct {
		raw []byte
		err error
	}
	const workers = 24
	results := make(chan result, workers)
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			raw, err := s.pick(req)
			results <- result{raw: raw, err: err}
		}()
	}
	wg.Wait()
	close(results)
	for item := range results {
		if item.err != nil {
			t.Fatal(item.err)
		}
		var envelope Envelope
		if err := json.Unmarshal(item.raw, &envelope); err != nil {
			t.Fatal(err)
		}
		var response SchedulerPickResponse
		if err := json.Unmarshal(envelope.Result, &response); err != nil {
			t.Fatal(err)
		}
		if !envelope.OK || !response.Handled || response.AuthID != "a" {
			t.Fatalf("concurrent same-session pick diverged: %+v %+v", envelope, response)
		}
	}
	if len(s.sessions) != 1 {
		t.Fatalf("concurrent session insertion created %d entries", len(s.sessions))
	}
	for _, cursor := range s.pools {
		if cursor != 1 {
			t.Fatalf("reused session advanced RR: %d", cursor)
		}
	}
}
