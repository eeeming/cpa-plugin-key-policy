package plugin

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

// These wire types mirror the host SDK scheduler contract. In particular, an
// auth candidate has no top-level email, disabled flag, or scheduling weight.
type SchedulerPickRequest struct {
	Plugin     Metadata                 `json:"Plugin"`
	Provider   string                   `json:"Provider"`
	Providers  []string                 `json:"Providers"`
	Model      string                   `json:"Model"`
	Stream     bool                     `json:"Stream"`
	Options    SchedulerOptions         `json:"Options"`
	Candidates []SchedulerAuthCandidate `json:"Candidates"`
}

type SchedulerOptions struct {
	Headers  map[string][]string `json:"Headers"`
	Metadata map[string]any      `json:"Metadata"`
}

type SchedulerAuthCandidate struct {
	ID         string            `json:"ID"`
	Provider   string            `json:"Provider"`
	Priority   int               `json:"Priority"`
	Status     string            `json:"Status"`
	Attributes map[string]string `json:"Attributes"`
	Metadata   map[string]any    `json:"Metadata"`
}

type SchedulerPickResponse struct {
	AuthID          string `json:"AuthID"`
	DelegateBuiltin string `json:"DelegateBuiltin"`
	Handled         bool   `json:"Handled"`
	Reject          bool   `json:"Reject"`
	RejectReason    string `json:"RejectReason"`
	RejectCode      string `json:"RejectCode"`
}

func (s *bindingScheduler) pick(req SchedulerPickRequest) ([]byte, error) {
	snapshot, fingerprint, present, err := s.snapshot(req.Options.Headers)
	if err != nil {
		return bindingSchedulerError("codex_binding_invalid", "Codex binding snapshot rejected: "+err.Error(), "permission_error", http.StatusForbidden), nil
	}
	if !present || !snapshot.Restricted {
		return OKEnvelope(SchedulerPickResponse{Handled: false})
	}

	ids, priority := bindingMatches(req.Candidates, snapshot, false)
	fallback := len(ids) == 0 && snapshot.Fallback
	if fallback {
		ids, priority = bindingMatches(req.Candidates, snapshot, true)
	}
	if len(ids) == 0 {
		return bindingSchedulerError("codex_binding_unavailable", "No available Codex account matches this key's binding", "server_error", http.StatusServiceUnavailable), nil
	}
	sort.Strings(ids)
	// A duplicated host record must not become an implicit scheduling weight.
	n := 1
	for i := 1; i < len(ids); i++ {
		if ids[i] != ids[n-1] {
			ids[n] = ids[i]
			n++
		}
	}
	ids = ids[:n]
	if !fallback && len(snapshot.AuthIDs) == 1 {
		return OKEnvelope(SchedulerPickResponse{AuthID: ids[0], Handled: true})
	}
	key := bindingPoolKey{
		keyID:      snapshot.KeyID,
		assignment: fingerprint,
		provider:   strings.ToLower(strings.TrimSpace(req.Provider)),
		model:      req.Model,
		priority:   priority,
	}
	session, hasSession := bindingSessionIdentity(req.Options)
	selected := s.selectAuth(key, session, hasSession, ids, req.Candidates, snapshot, fallback)
	return OKEnvelope(SchedulerPickResponse{AuthID: selected, Handled: true})
}

// The host preserves only Message and HTTPStatus when converting scheduler
// errors, so Message must carry the complete client-facing OpenAI error body.
func bindingSchedulerError(code, message, errType string, status int) []byte {
	body, _ := json.Marshal(map[string]any{
		"error": map[string]any{
			"message": message,
			"type":    errType,
			"param":   nil,
			"code":    code,
		},
	})
	return ErrorEnvelope(code, string(body), status)
}

// bindingMatches filters before comparing priorities. The host already supplies
// available candidates; status text is diagnostic, not a second readiness gate.
func bindingMatches(candidates []SchedulerAuthCandidate, snapshot bindingSnapshot, fallback bool) ([]string, int) {
	var ids []string
	var priority int
	for i := range candidates {
		candidate := &candidates[i]
		if !bindingCandidateAllowed(candidate, snapshot, fallback) {
			continue
		}
		if len(ids) == 0 || candidate.Priority > priority {
			ids = append(ids[:0], candidate.ID)
			priority = candidate.Priority
		} else if candidate.Priority == priority {
			ids = append(ids, candidate.ID)
		}
	}
	return ids, priority
}

func bindingCandidateAllowed(candidate *SchedulerAuthCandidate, snapshot bindingSnapshot, fallback bool) bool {
	if candidate.ID == "" || !strings.EqualFold(strings.TrimSpace(candidate.Provider), "codex") {
		return false
	}
	if fallback {
		return true
	}
	if len(snapshot.AuthIDs) != 0 {
		index := sort.SearchStrings(snapshot.AuthIDs, candidate.ID)
		if index == len(snapshot.AuthIDs) || snapshot.AuthIDs[index] != candidate.ID {
			return false
		}
	}
	if snapshot.Group == "" {
		return true
	}
	group := normalizeBindingGroup(candidate.Attributes["plan_type"])
	if group == "" {
		group = normalizeBindingGroup(candidate.Attributes["tier"])
	}
	return group == snapshot.Group
}

func (s *bindingScheduler) selectAuth(pool bindingPoolKey, session [sha256.Size]byte, hasSession bool, ids []string, candidates []SchedulerAuthCandidate, snapshot bindingSnapshot, fallback bool) string {
	if !hasSession && len(ids) == 1 {
		return ids[0]
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !hasSession {
		return ids[s.nextLocked(pool, len(ids))]
	}
	now := s.now()
	scope := pool
	// Existing affinity outranks priority; only cold binds/failover use the
	// highest filtered tier. Assignment identity still isolates old retries.
	scope.priority = 0
	key := bindingSessionKey{scope: scope, session: session}
	previous, exists := s.sessions[key]
	if exists && now.Before(previous.expires) {
		for i := range candidates {
			if candidates[i].ID == previous.authID && bindingCandidateAllowed(&candidates[i], snapshot, fallback) {
				previous.expires = now.Add(bindingSessionTTL)
				s.sessions[key] = previous
				return previous.authID
			}
		}
	}
	selected := ids[0]
	if len(ids) > 1 {
		selected = ids[s.nextLocked(pool, len(ids))]
	}
	if s.sessions == nil {
		s.sessions = make(map[bindingSessionKey]bindingSessionEntry)
	}
	if !exists {
		if len(s.sessionOrder) == bindingSessionLimit {
			delete(s.sessions, s.sessionOrder[s.sessionEvictNext])
			s.sessionOrder[s.sessionEvictNext] = key
			s.sessionEvictNext = (s.sessionEvictNext + 1) % bindingSessionLimit
		} else {
			s.sessionOrder = append(s.sessionOrder, key)
		}
	}
	// Expired entries are overwritten on access. FIFO replacement bounds
	// retained hashes even when idle TTLs are never revisited.
	s.sessions[key] = bindingSessionEntry{authID: selected, expires: now.Add(bindingSessionTTL)}
	return selected
}

func (s *bindingScheduler) nextLocked(key bindingPoolKey, count int) int {
	if s.pools == nil {
		s.pools = make(map[bindingPoolKey]uint64)
	}
	cursor, exists := s.pools[key]
	if !exists {
		// FIFO eviction is deterministic and only forgets a cursor: it never
		// widens a binding or starts an unbounded collection of idle pools.
		if len(s.poolOrder) == bindingPoolLimit {
			delete(s.pools, s.poolOrder[s.evictNext])
			s.poolOrder[s.evictNext] = key
			s.evictNext = (s.evictNext + 1) % bindingPoolLimit
		} else {
			s.poolOrder = append(s.poolOrder, key)
		}
	}
	selected := cursor % uint64(count)
	s.pools[key] = selected + 1
	return int(selected)
}

// canonical_session_id is enriched by the host from header/body identities
// before scheduling (session/identity.go:232-318). The ABI has no request body:
// only reproduce header-visible extraction when canonical identity is absent.
func bindingSessionIdentity(options SchedulerOptions) ([sha256.Size]byte, bool) {
	if id := bindingMetadataSession(options.Metadata, "canonical_session_id"); id != "" {
		return sha256.Sum256([]byte(id)), true
	}
	id := bindingHeaderSession(options.Headers)
	if id == "" {
		// selector.go:1853-1888 and session/info.go:861-895: explicit
		// execution identity precedes LCP; derived identity is last. Enrich's
		// LCP-first path already supplies canonical_session_id above.
		if execution := bindingMetadataSession(options.Metadata, "execution_session_id"); execution != "" {
			id = "execution:" + execution
		} else if lcp := bindingMetadataSession(options.Metadata, "lcp_affinity_session_id"); lcp != "" {
			id = lcp
		} else if derived := bindingMetadataSession(options.Metadata, "derived_session_id"); derived != "" {
			id = "derived:" + derived
		}
	}
	if id == "" {
		return [sha256.Size]byte{}, false
	}
	// Match BoundSessionIdentity, including generated namespace/agent strings.
	if len(id) > 256 {
		sum := sha256.Sum256([]byte(id))
		prefix := id[:190]
		for !utf8.ValidString(prefix) && len(prefix) > 0 {
			prefix = prefix[:len(prefix)-1]
		}
		id = prefix + "#" + hex.EncodeToString(sum[:])
	}
	return sha256.Sum256([]byte(id)), true
}

func bindingMetadataSession(metadata map[string]any, key string) string {
	value, _ := metadata[key].(string)
	return normalizeBindingSession(value)
}

func normalizeBindingSession(value string) string {
	// Mirrors session.NormalizeExplicitID, including its byte-length bound.
	for _, r := range value {
		if unicode.IsControl(r) {
			return ""
		}
	}
	value = strings.TrimSpace(value)
	if len(value) > 256 {
		return ""
	}
	return value
}

func bindingSessionHeader(headers map[string][]string, name string) string {
	canonical := http.CanonicalHeaderKey(name)
	for _, value := range headers[canonical] {
		if normalized := normalizeBindingSession(value); normalized != "" {
			return normalized
		}
	}
	// JSON can carry noncanonical case variants. Prefer the canonical name,
	// then a deterministic spelling rather than Go map iteration order.
	var chosenName, chosenValue string
	for key, values := range headers {
		if key == canonical || !strings.EqualFold(key, name) || (chosenName != "" && key >= chosenName) {
			continue
		}
		for _, value := range values {
			if normalized := normalizeBindingSession(value); normalized != "" {
				chosenName, chosenValue = key, normalized
				break
			}
		}
	}
	return chosenValue
}

func bindingHeaderSession(headers map[string][]string) string {
	// session/info.go:173-222: Claude sessions and agents outrank all generic
	// headers. Parent identities are not aliases: this cache never merges them.
	if sid := bindingSessionHeader(headers, "X-Claude-Code-Session-Id"); sid != "" {
		id := "claude:" + sid
		if agent := bindingSessionHeader(headers, "X-Claude-Code-Agent-Id"); agent != "" && agent != "main" {
			id += ":agent:" + agent
		}
		return id
	}
	// session/info.go:283-446: Codex turn metadata can supply missing IDs,
	// forks and named subagents. It is a header, not an intercepted body.
	sid := bindingSessionHeader(headers, "Session-Id")
	if sid == "" {
		sid = bindingSessionHeader(headers, "Session_id")
	}
	tid := bindingSessionHeader(headers, "Thread-Id")
	if tid == "" {
		tid = bindingSessionHeader(headers, "Thread_id")
	}
	var turn struct {
		SessionID          string `json:"session_id"`
		ThreadID           string `json:"thread_id"`
		ParentThreadID     string `json:"parent_thread_id"`
		ForkedFromThreadID string `json:"forked_from_thread_id"`
		ForkedFromID       string `json:"forked_from_id"`
		AgentName          string `json:"agent_name"`
		SubagentKind       string `json:"subagent_kind"`
	}
	// Unlike session IDs, the JSON turn-metadata header may exceed 256 bytes.
	if raw := http.Header(headers).Get("X-Codex-Turn-Metadata"); raw != "" {
		_ = json.Unmarshal([]byte(raw), &turn)
	} else {
		for name, values := range headers {
			if strings.EqualFold(name, "X-Codex-Turn-Metadata") && len(values) > 0 {
				_ = json.Unmarshal([]byte(values[0]), &turn)
				break
			}
		}
	}
	if sid == "" {
		sid = normalizeBindingSession(turn.SessionID)
	}
	if tid == "" {
		tid = normalizeBindingSession(turn.ThreadID)
	}
	if sid != "" || tid != "" {
		forkedFrom := normalizeBindingSession(turn.ForkedFromThreadID)
		if forkedFrom == "" {
			forkedFrom = normalizeBindingSession(turn.ForkedFromID)
		}
		if forkedFrom != "" {
			fork := tid
			if fork == "" || (fork == forkedFrom && sid != "" && sid != forkedFrom) {
				fork = sid
			}
			return "codex:" + fork
		}
		parent := bindingSessionHeader(headers, "X-Codex-Parent-Thread-Id")
		if parent == "" {
			parent = normalizeBindingSession(turn.ParentThreadID)
		}
		sub := bindingSessionHeader(headers, "X-Openai-Subagent")
		isSubagent := (sub != "" && !strings.EqualFold(sub, "false") && sub != "0") || turn.SubagentKind == "thread_spawn" || (tid != "" && sid != "" && tid != sid) || (parent != "" && parent != tid && parent != sid)
		if isSubagent {
			agent := strings.TrimPrefix(strings.TrimPrefix(turn.AgentName, "/root/"), "/")
			agent = normalizeBindingSession(agent)
			if agent != "" && agent != "root" && agent != "main" && sid != "" {
				return "codex:" + sid + ":agent:" + agent
			}
			if tid != "" {
				return "codex:" + tid
			}
		}
		if sid != "" {
			return "codex:" + sid
		}
		return "codex:" + tid
	}
	// session/info.go:449-643. Distinct namespaces preserve host identities.
	for _, header := range []struct{ name, prefix string }{
		{"X-Http-Session-Id", "agy:"},
		{"X-Session-ID", "header:"},
		{"X-Session-Affinity", "affinity:"},
		{"X-Slot-Session-Id", "slot:"},
		{"X-Task-ID", "task:"},
		{"X-Task_ID", "task:"},
		{"X-Conversation-Id", "conv:"},
		{"X-Thread-Id", "thread:"},
		{"X-Client-Request-Id", "clientreq:"},
	} {
		if id := bindingSessionHeader(headers, header.name); id != "" {
			return header.prefix + id
		}
	}
	return ""
}
