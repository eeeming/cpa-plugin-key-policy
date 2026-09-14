package policy

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type Store struct {
	mu        sync.RWMutex
	updateMu  sync.Mutex
	persistMu sync.Mutex
	enabled   bool
	statePath string

	keys        map[string]*KeyConfig
	keysByHash  map[string]*KeyConfig
	keysByScope map[string]*KeyConfig
	limiter     *RateLimiter
	usage       *usageLedger
	flusher     *usageFlusher

	listPrices      PriceLister
	listAPIKeys     APIKeyLister
	priceMu         sync.Mutex
	priceCache      []ModelPrice
	pricesReady     bool
	priceFetchedAt  time.Time
	priceRefreshing bool
	// priceGen is bumped under s.mu in the same critical section that replaces
	// listPrices, so a fetch can snapshot the (lister, generation) pair
	// atomically and discard its result if the source was replaced meanwhile.
	// Without that pairing, a reconfigure landing between the two reads would
	// let the old source's table commit under the new generation.
	priceGen atomic.Uint64
	// priceWait is closed when the in-flight fetch finishes. Cold-start callers
	// wait on it so a request never admits with USD enforcement disabled just
	// because another request happened to start the first fetch.
	priceWait chan struct{}
}

type AdmitDecision struct {
	Known      bool
	Allowed    bool
	Terminate  bool
	StatusCode int
	KeyID      string
	Reason     string
}

func NewStore() *Store {
	return &Store{
		enabled:     DefaultConfig().Enabled,
		keys:        make(map[string]*KeyConfig),
		keysByHash:  make(map[string]*KeyConfig),
		keysByScope: make(map[string]*KeyConfig),
		limiter:     NewRateLimiter(),
		usage:       newUsageLedger(time.Now),
	}
}

func (s *Store) SetClock(now func() time.Time) {
	if now == nil {
		return
	}
	s.mu.Lock()
	s.limiter = NewRateLimiterWithClock(now)
	s.usage = newUsageLedger(now)
	s.mu.Unlock()
}

func (s *Store) SetPriceLister(fn PriceLister) {
	s.mu.Lock()
	s.listPrices = fn
	s.priceGen.Add(1)
	s.mu.Unlock()
	s.resetPriceCache()
}

// resetPriceCache drops the cached table and invalidates any in-flight fetch.
// It is called whenever the price source changes so the next Admit re-syncs the
// latest table from the currently configured source. The generation bump that
// invalidates the in-flight fetch happens in the caller, under s.mu, together
// with the source swap.
func (s *Store) resetPriceCache() {
	s.priceMu.Lock()
	s.priceCache = nil
	s.pricesReady = false
	s.priceFetchedAt = time.Time{}
	s.priceRefreshing = false
	s.priceMu.Unlock()
}

func (s *Store) SetAPIKeyLister(fn APIKeyLister) {
	s.mu.Lock()
	s.listAPIKeys = fn
	s.mu.Unlock()
}

func (s *Store) Configure(cfg Config) error {
	s.updateMu.Lock()
	defer s.updateMu.Unlock()
	if err := normalizeConfig(&cfg); err != nil {
		return err
	}
	statePath, err := ResolveStatePath(cfg.StateFile)
	if err != nil {
		return err
	}

	s.StopUsageFlusher()

	keys := cfg.Keys
	var loadedUsage map[string]*UsageState
	firstBoot := false
	if state, errLoad := LoadState(statePath); errLoad == nil {
		keys = state.Keys
		loadedUsage = state.Usage
		merged := Config{Enabled: cfg.Enabled, StateFile: cfg.StateFile, Keys: keys}
		if errNorm := normalizeConfig(&merged); errNorm != nil {
			return fmt.Errorf("load state: %w", errNorm)
		}
		keys = merged.Keys
	} else if !errors.Is(errLoad, os.ErrNotExist) {
		return fmt.Errorf("load state: %w", errLoad)
	} else {
		firstBoot = true
	}

	next := make(map[string]*KeyConfig, len(keys))
	now := time.Now().UTC()
	for i := range keys {
		item := keys[i]
		if item.CreatedAt.IsZero() {
			item.CreatedAt = now
		}
		if item.UpdatedAt.IsZero() {
			item.UpdatedAt = item.CreatedAt
		}
		next[item.ID] = &item
	}

	s.mu.Lock()
	if s.flusher != nil {
		s.flusher.stop()
		s.flusher = nil
	}
	s.enabled = cfg.Enabled
	s.statePath = statePath
	s.keys = next
	s.rebuildIndexesLocked()
	if s.limiter == nil {
		s.limiter = NewRateLimiter()
	}
	clockNow := s.usage.now
	s.usage = newUsageLedger(clockNow)
	s.usage.loadFromState(loadedUsage)
	if cfg.PlusBaseURL != "" {
		s.listPrices = HTTPPriceLister(nil, cfg.PlusBaseURL, cfg.PlusManagementKey)
		s.listAPIKeys = HTTPAPIKeyLister(nil, cfg.PlusBaseURL, cfg.PlusManagementKey)
	} else {
		// An empty plus_base_url means "RPM only": drop the price source so a
		// reconfigure cannot keep billing from a previously configured Plus.
		s.listPrices = nil
		s.listAPIKeys = nil
	}
	// Bump the generation in the same critical section as the source swap so a
	// concurrent fetch snapshots a consistent (lister, generation) pair.
	s.priceGen.Add(1)

	s.mu.Unlock()
	// A (re)configure always re-syncs the latest price table from the source
	// above, and discards any table fetched from a previous source.
	s.resetPriceCache()
	if firstBoot {
		if errSave := s.persistCurrentState(); errSave != nil {
			return fmt.Errorf("seed state: %w", errSave)
		}
	}
	return nil
}

func (s *Store) Enabled() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.enabled
}

func (s *Store) runtimeComponents() (*RateLimiter, *usageLedger) {
	s.mu.RLock()
	limiter := s.limiter
	usage := s.usage
	s.mu.RUnlock()
	return limiter, usage
}

func (s *Store) StatePath() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.statePath
}

// Admit is the request.intercept_before gate. Unknown and disabled policies
// are no-ops (Known=false or Allowed without Terminate). Bound enabled keys
// over RPM or (when prices are available) USD limits Terminate with 429.
//
// A model that bills nothing (explicitly priced at 0 in the price table, or
// absent from it, so it is never billed) is exempt from the daily/weekly USD
// limits: a free request cannot consume the budget, so the cap must not block
// it. RPM still applies — it is a rate limit on the host, not a spend limit.
func (s *Store) Admit(headers http.Header, query map[string][]string, metadata map[string]any) AdmitDecision {
	return s.AdmitRequest(GateRequest{
		Headers:  headers,
		Query:    query,
		Metadata: metadata,
	})
}

// GateRequest is one request.intercept_before evaluation.
type GateRequest struct {
	Headers  http.Header
	Query    map[string][]string
	Metadata map[string]any
	// Model / Alias / Provider / ServiceTier come from the intercept payload and
	// are used to price the request and decide whether it is free.
	Model       string
	Alias       string
	Provider    string
	ServiceTier string
}

// AdmitRequest evaluates the gate for a full request description.
func (s *Store) AdmitRequest(req GateRequest) AdmitDecision {
	rawKey := ExtractAPIKey(req.Headers, req.Query)
	key, pluginOn := s.findBoundWhenEnabled(rawKey, req.Metadata)
	if !pluginOn {
		return AdmitDecision{Reason: "plugin_disabled"}
	}
	if key == nil {
		return AdmitDecision{Reason: "unknown_key"}
	}
	s.touchLastAccess(key.ID)
	if !key.Enabled {
		return AdmitDecision{Known: true, Allowed: true, KeyID: key.ID, Reason: "policy_disabled"}
	}
	limiter, usageLedger := s.runtimeComponents()
	if limiter != nil && !limiter.Allow(key.ID, key.RPM) {
		return AdmitDecision{
			Known:      true,
			Terminate:  true,
			StatusCode: http.StatusTooManyRequests,
			KeyID:      key.ID,
			Reason:     "rpm_exceeded",
		}
	}
	if usageLedger != nil && !s.requestIsFree(req) {
		if s.PricesAvailable() {
			if reason, _ := usageLedger.OverLimit(*key); reason != "" {
				return AdmitDecision{
					Known:      true,
					Terminate:  true,
					StatusCode: http.StatusTooManyRequests,
					KeyID:      key.ID,
					Reason:     reason,
				}
			}
		}
	}
	return AdmitDecision{Known: true, Allowed: true, KeyID: key.ID, Reason: "allowed"}
}

// requestIsFree reports whether this request targets a model that bills
// nothing. It is judged against the same price table (and the same matcher)
// that billing uses, so the gate and the bill never disagree.
func (s *Store) requestIsFree(req GateRequest) bool {
	if strings.TrimSpace(req.Model) == "" && strings.TrimSpace(req.Alias) == "" {
		return false
	}
	prices, ok := s.cachedPrices()
	if !ok {
		return false
	}
	return FreeModel(prices, req.Provider, req.Model, req.Alias, req.ServiceTier, 0)
}

func (s *Store) PricesAvailable() bool {
	s.refreshPrices(false)
	s.priceMu.Lock()
	defer s.priceMu.Unlock()
	return s.pricesReady
}

func (s *Store) refreshPrices(force bool) {
	// Snapshot the source and its generation together: the generation is only
	// meaningful relative to the lister it was captured with, and a reconfigure
	// bumps both under s.mu.
	s.mu.RLock()
	lister := s.listPrices
	gen := s.priceGen.Load()
	s.mu.RUnlock()
	if lister == nil {
		s.priceMu.Lock()
		s.pricesReady = false
		s.priceCache = nil
		s.priceMu.Unlock()
		return
	}
	s.priceMu.Lock()
	if !force && !s.priceFetchedAt.IsZero() && time.Since(s.priceFetchedAt) < priceCacheTTL {
		s.priceMu.Unlock()
		return
	}
	if s.priceRefreshing {
		wait := s.priceWait
		ready := s.pricesReady
		s.priceMu.Unlock()
		if !ready && wait != nil {
			// Cold start with no table yet: wait for the in-flight fetch instead
			// of admitting requests with USD enforcement silently disabled.
			// Bounded so a hung Plus cannot stall requests indefinitely.
			select {
			case <-wait:
			case <-time.After(priceFetchWait):
			}
		}
		return
	}
	wait := make(chan struct{})
	s.priceRefreshing = true
	s.priceWait = wait
	s.priceMu.Unlock()
	list, err := lister()
	s.priceMu.Lock()
	defer s.priceMu.Unlock()
	if s.priceGen.Load() != gen {
		// The price source was reconfigured while this fetch was in flight:
		// discard the stale result instead of resurrecting the old table.
		if s.priceWait == wait {
			s.priceWait = nil
		}
		close(wait)
		return
	}
	s.priceRefreshing = false
	s.priceWait = nil
	s.priceFetchedAt = time.Now()
	if err != nil {
		// Keep the last good table so USD limits do not fail open.
		// Stamp priceFetchedAt so Admit does not retry Plus on every request.
		if len(s.priceCache) == 0 {
			s.pricesReady = false
		}
		close(wait)
		return
	}
	s.priceCache = list
	s.pricesReady = true
	// Wake cold-start waiters only after the table is published (the deferred
	// unlock keeps them from reading state before this point).
	close(wait)
}

func (s *Store) cachedPrices() ([]ModelPrice, bool) {
	s.refreshPrices(false)
	s.priceMu.Lock()
	defer s.priceMu.Unlock()
	if !s.pricesReady {
		return nil, false
	}
	out := make([]ModelPrice, len(s.priceCache))
	copy(out, s.priceCache)
	return out, true
}

// RecordUsage bills a finalized usage.handle record for a bound, enabled key.
func (s *Store) RecordUsage(apiKeyOrID, alias, model, provider, serviceTier string, failed bool, detail UsageDetail) float64 {
	return s.RecordUsageMeta(apiKeyOrID, alias, model, provider, serviceTier, failed, detail, nil)
}

func (s *Store) RecordUsageMeta(apiKeyOrID, alias, model, provider, serviceTier string, failed bool, detail UsageDetail, metadata map[string]any) float64 {
	if !s.Enabled() {
		return 0
	}
	key := s.resolveIdentity(apiKeyOrID, metadata)
	if key == nil || !key.Enabled {
		return 0
	}
	if failed {
		return 0
	}
	_, usageLedger := s.runtimeComponents()
	label := strings.TrimSpace(alias)
	if label == "" {
		label = strings.TrimSpace(model)
	}
	if label == "" {
		label = "_"
	}

	var cost, cacheCost float64
	var cacheReadTokens, nonCacheInput int64
	if prices, ok := s.cachedPrices(); ok {
		rule, matched := MatchPlusPrice(prices, provider, model, alias, serviceTier, detail.InputTokens)
		if matched {
			cost, cacheCost, cacheReadTokens = ComputeCacheCostBreakdown(
				provider,
				rule.InputPricePerMillion,
				rule.OutputPricePerMillion,
				rule.CacheReadPricePerMillion,
				true,
				detail,
			)
			if rule.CacheWritePricePerMillion != 0 && detail.CacheCreationTokens > 0 {
				// Plus bills cache writes at cacheCreation (often 1.25× prompt).
				// Subset providers already included those tokens at the input
				// price; additive providers added them at the input price too.
				// Reprice the write delta for both so we match Plus.
				cost += float64(detail.CacheCreationTokens) / 1_000_000 * (rule.CacheWritePricePerMillion - rule.InputPricePerMillion)
			}
			cost += rule.RequestPrice
			if isCacheAdditiveProvider(provider) {
				nonCacheInput = detail.InputTokens + detail.CacheCreationTokens
			} else {
				cr := detail.CacheReadTokens
				if cr == 0 {
					cr = detail.CachedTokens
				}
				if cr > detail.InputTokens {
					cr = detail.InputTokens
				}
				nonCacheInput = detail.InputTokens - cr
			}
		}
	}

	if usageLedger != nil {
		usageLedger.RecordCost(key.ID, label, cost, cacheCost, cacheReadTokens, nonCacheInput, detail.OutputTokens, 1)
	}
	return cost
}

func (s *Store) UsageSummaryFor(key KeyConfig) UsageSummary {
	_, usage := s.runtimeComponents()
	if usage == nil {
		return UsageSummary{DailyLimitUSD: key.DailyLimitUSD, WeeklyLimitUSD: key.WeeklyLimitUSD}
	}
	return usage.Summary(key)
}

func (s *Store) ModelUsageFor(keyID string) (KeyConfig, []AliasUsageEntry, bool) {
	key := s.findByID(keyID)
	if key == nil {
		return KeyConfig{}, nil, false
	}
	_, usage := s.runtimeComponents()
	if usage == nil {
		return *key, nil, true
	}
	return *key, usage.AliasUsage(*key), true
}

func (s *Store) resolveIdentity(raw string, metadata map[string]any) *KeyConfig {
	raw = strings.TrimSpace(raw)
	if raw != "" {
		if key := s.findBySecret(raw); key != nil {
			return key
		}
		if key := s.findByID(raw); key != nil {
			return key
		}
		if strings.HasPrefix(strings.ToLower(raw), HashPrefix) {
			if key := s.findByHash(raw); key != nil {
				return key
			}
		}
		if key := s.findByCallerScope(raw); key != nil {
			return key
		}
	}
	if scope := metadataString(metadata, "caller_scope"); scope != "" {
		if key := s.findByCallerScope(scope); key != nil {
			return key
		}
	}
	return nil
}

func metadataString(meta map[string]any, key string) string {
	if meta == nil {
		return ""
	}
	raw, ok := meta[key]
	if !ok || raw == nil {
		return ""
	}
	switch v := raw.(type) {
	case string:
		return strings.TrimSpace(v)
	default:
		return strings.TrimSpace(fmt.Sprintf("%v", v))
	}
}

func (s *Store) findBySecret(raw string) *KeyConfig {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	hash, err := HashKey(raw)
	if err != nil {
		return nil
	}
	return s.findByHash(hash)
}

func (s *Store) findByHash(hash string) *KeyConfig {
	hash = strings.ToLower(strings.TrimSpace(hash))
	if hash == "" {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	key := s.keysByHash[hash]
	if key == nil {
		return nil
	}
	copy := *key
	return &copy
}

func (s *Store) findByCallerScope(scope string) *KeyConfig {
	scope = strings.TrimSpace(scope)
	if scope == "" {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	key := s.keysByScope[scope]
	if key == nil {
		return nil
	}
	copy := *key
	return &copy
}

func (s *Store) findBoundWhenEnabled(raw string, metadata map[string]any) (*KeyConfig, bool) {
	s.mu.RLock()
	enabled := s.enabled
	s.mu.RUnlock()
	if !enabled {
		return nil, false
	}
	return s.resolveIdentity(raw, metadata), true
}

func (s *Store) findByID(id string) *KeyConfig {
	id = strings.TrimSpace(id)
	if id == "" {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	key := s.keys[id]
	if key == nil {
		for candidateID, candidate := range s.keys {
			if strings.EqualFold(candidateID, id) {
				key = candidate
				break
			}
		}
	}
	if key == nil {
		return nil
	}
	copy := *key
	return &copy
}

func (s *Store) rebuildIndexesLocked() {
	ids := make([]string, 0, len(s.keys))
	for id := range s.keys {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	byHash := make(map[string]*KeyConfig, len(ids))
	byScope := make(map[string]*KeyConfig, len(ids))
	for _, id := range ids {
		key := s.keys[id]
		if key == nil {
			continue
		}
		hash := strings.ToLower(strings.TrimSpace(key.KeyHash))
		if hash != "" {
			if _, exists := byHash[hash]; !exists {
				byHash[hash] = key
			}
		}
		scope := strings.TrimSpace(key.CallerScope)
		if scope != "" {
			if _, exists := byScope[scope]; !exists {
				byScope[scope] = key
			}
		}
	}
	s.keysByHash = byHash
	s.keysByScope = byScope
}

func (s *Store) Keys() []KeyConfig {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.keysSnapshotLocked()
}

func (s *Store) keysSnapshotLocked() []KeyConfig {
	keys := make([]KeyConfig, 0, len(s.keys))
	for _, key := range s.keys {
		keys = append(keys, *key)
	}
	sort.Slice(keys, func(i, j int) bool {
		ai, aj := keys[i].LastAccessAt, keys[j].LastAccessAt
		if !ai.Equal(aj) {
			return ai.After(aj)
		}
		ci, cj := keys[i].CreatedAt, keys[j].CreatedAt
		if !ci.Equal(cj) {
			return ci.After(cj)
		}
		return keys[i].ID < keys[j].ID
	})
	return keys
}

func (s *Store) touchLastAccess(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := s.keys[id]
	if k == nil {
		return
	}
	now := time.Now().UTC()
	if s.usage != nil && s.usage.now != nil {
		now = s.usage.now().UTC()
	}
	k.LastAccessAt = now
}

func (s *Store) SyncFromPlus() (SyncResult, error) {
	s.mu.RLock()
	lister := s.listAPIKeys
	s.mu.RUnlock()
	if lister == nil {
		return SyncResult{}, fmt.Errorf("plus_base_url is empty")
	}
	remote, err := lister()
	if err != nil {
		return SyncResult{}, err
	}
	out := SyncResult{Total: len(remote)}
	for _, item := range remote {
		plain := strings.TrimSpace(item.Plain)
		if plain == "" {
			continue
		}
		hash, err := HashKey(plain)
		if err != nil {
			return out, err
		}
		if existing := s.findByHash(hash); existing != nil {
			out.Skipped++
			continue
		}
		id := s.availableKeyID(hash)
		name := strings.TrimSpace(item.Name)
		if name == "" || name == plain || strings.Contains(name, plain) {
			name = PreviewKey(plain)
		}
		if err := s.UpsertKey(KeyConfig{
			ID:          id,
			Name:        name,
			Enabled:     true,
			KeyHash:     hash,
			KeyPreview:  PreviewKey(plain),
			CallerScope: CallerScope(plain),
		}, false); err != nil {
			return out, err
		}
		out.Added++
		out.AddedIDs = append(out.AddedIDs, id)
	}
	if out.Added > 0 {
		if err := s.persistCurrentState(); err != nil {
			return out, err
		}
	}
	return out, nil
}

// normalizeKey validates and canonicalizes one key policy the same way a
// config or state file entry is normalized.
func normalizeKey(input KeyConfig) (KeyConfig, error) {
	cfg := Config{Enabled: true, StateFile: DefaultConfig().StateFile, Keys: []KeyConfig{input}}
	if err := normalizeConfig(&cfg); err != nil {
		return KeyConfig{}, err
	}
	return cfg.Keys[0], nil
}

// applyKeyLocked writes one already-normalized key into the map, enforcing the
// unique-hash invariant and preserving immutable fields (created/last-access
// timestamps, and the stored secret when the update carries no new hash).
func (s *Store) applyKeyLocked(key KeyConfig) error {
	now := time.Now().UTC()
	s.mu.Lock()
	defer s.mu.Unlock()
	if hash := strings.ToLower(strings.TrimSpace(key.KeyHash)); hash != "" {
		if existing := s.keysByHash[hash]; existing != nil && existing.ID != key.ID {
			return fmt.Errorf("key hash already bound to %q", existing.ID)
		}
	}
	if old := s.keys[key.ID]; old != nil && !old.CreatedAt.IsZero() {
		key.CreatedAt = old.CreatedAt
		if key.LastAccessAt.IsZero() {
			key.LastAccessAt = old.LastAccessAt
		}
		if key.KeyHash == "" {
			key.KeyHash = old.KeyHash
			key.KeyPreview = old.KeyPreview
			key.CallerScope = old.CallerScope
		}
	} else if key.CreatedAt.IsZero() {
		key.CreatedAt = now
	}
	key.UpdatedAt = now
	s.keys[key.ID] = &key
	s.rebuildIndexesLocked()
	return nil
}

func (s *Store) UpsertKey(input KeyConfig, persist bool) error {
	s.updateMu.Lock()
	defer s.updateMu.Unlock()
	key, err := normalizeKey(input)
	if err != nil {
		return err
	}
	if err := s.applyKeyLocked(key); err != nil {
		return err
	}
	if persist {
		return s.persistCurrentState()
	}
	return nil
}

// UpdateKey applies mutate to a copy of the key identified by id while holding
// the store update lock. Doing the read-modify-write under that lock means two
// concurrent PATCHes of different fields cannot lose each other's changes.
// Lookup is exact: ids that differ only by case are distinct policies, so a
// case-folded fallback could silently mutate the wrong one.
func (s *Store) UpdateKey(id string, mutate func(*KeyConfig) error) (KeyConfig, error) {
	s.updateMu.Lock()
	defer s.updateMu.Unlock()
	id = strings.TrimSpace(id)
	if id == "" {
		return KeyConfig{}, errors.New("id is required")
	}
	s.mu.RLock()
	current := s.keys[id]
	var work KeyConfig
	if current != nil {
		work = *current
	}
	s.mu.RUnlock()
	if current == nil {
		return KeyConfig{}, ErrUnknownKey
	}
	if mutate != nil {
		if err := mutate(&work); err != nil {
			return KeyConfig{}, err
		}
	}
	key, err := normalizeKey(work)
	if err != nil {
		return KeyConfig{}, err
	}
	if err := s.applyKeyLocked(key); err != nil {
		return KeyConfig{}, err
	}
	if err := s.persistCurrentState(); err != nil {
		return key, err
	}
	return key, nil
}

// availableKeyID derives a short, readable id from a key hash and guarantees it
// is not already bound. It lengthens the hash prefix first, then falls back to a
// numeric suffix, so a sync can never silently overwrite an existing policy.
func (s *Store) availableKeyID(hash string) string {
	body := strings.TrimPrefix(hash, HashPrefix)
	candidates := []int{14, 18, 0}
	for _, n := range candidates {
		id := "k-" + body
		if n > 0 && len(id) > n {
			id = id[:n]
		}
		if s.findByID(id) == nil {
			return id
		}
	}
	base := "k-" + body
	for i := 2; ; i++ {
		suffix := fmt.Sprintf("-%d", i)
		id := base
		if len(id) > 24-len(suffix) {
			id = id[:24-len(suffix)]
		}
		id += suffix
		if s.findByID(id) == nil {
			return id
		}
	}
}

func (s *Store) DeleteKey(id string) error {
	s.updateMu.Lock()
	defer s.updateMu.Unlock()
	id = strings.TrimSpace(id)
	if id == "" {
		return errors.New("id is required")
	}
	s.mu.Lock()
	if _, ok := s.keys[id]; !ok {
		s.mu.Unlock()
		return ErrUnknownKey
	}
	delete(s.keys, id)
	s.rebuildIndexesLocked()
	limiter := s.limiter
	usageLedger := s.usage
	s.mu.Unlock()
	if limiter != nil {
		limiter.Reset(id)
	}
	if usageLedger != nil {
		usageLedger.resetUsage(id)
	}
	return s.persistCurrentState()
}

type ResetWindowsResult struct {
	Reset  int      `json:"reset"`
	IDs    []string `json:"ids"`
	Failed []string `json:"failed,omitempty"`
}

// ResetWindows zeros rolling usage and the RPM minute bucket for bound keys.
// Daily/weekly/RPM limit numbers, names, and enabled flags are unchanged.
func (s *Store) ResetWindows(ids []string) (ResetWindowsResult, error) {
	s.updateMu.Lock()
	defer s.updateMu.Unlock()
	out := ResetWindowsResult{IDs: []string{}}
	seen := map[string]struct{}{}
	for _, raw := range ids {
		id := strings.TrimSpace(raw)
		if id == "" {
			continue
		}
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		s.mu.RLock()
		_, ok := s.keys[id]
		limiter := s.limiter
		usageLedger := s.usage
		s.mu.RUnlock()
		if !ok {
			out.Failed = append(out.Failed, id)
			continue
		}
		if limiter != nil {
			limiter.Reset(id)
		}
		if usageLedger != nil {
			usageLedger.resetUsage(id)
		}
		out.Reset++
		out.IDs = append(out.IDs, id)
	}
	if out.Reset > 0 {
		if err := s.persistCurrentState(); err != nil {
			return out, err
		}
	}
	return out, nil
}

func (s *Store) usageSnapshotLocked() map[string]*UsageState {
	if s.usage == nil {
		return nil
	}
	return s.usage.snapshot()
}

func (s *Store) FlushUsage() error {
	return s.persistCurrentState()
}

// persistCurrentState writes keys+usage from memory under persistMu. The
// snapshot is taken after the persist lock so a flush cannot overwrite a
// concurrent UpsertKey/DeleteKey with a stale usage (or key) copy.
func (s *Store) persistCurrentState() error {
	s.persistMu.Lock()
	defer s.persistMu.Unlock()
	s.mu.RLock()
	path := s.statePath
	keys := s.keysSnapshotLocked()
	usage := s.usageSnapshotLocked()
	s.mu.RUnlock()
	if path == "" {
		return nil
	}
	return SaveState(path, keys, usage)
}

func (s *Store) StartUsageFlusher() func() {
	s.mu.Lock()
	if s.flusher != nil {
		stop := s.flusher.stop
		s.mu.Unlock()
		return stop
	}
	stopCh := make(chan struct{})
	doneCh := make(chan struct{})
	var stopOnce sync.Once
	stop := func() { stopOnce.Do(func() { close(stopCh) }) }
	f := &usageFlusher{stop: stop, stopCh: stopCh, doneCh: doneCh, store: s}
	s.flusher = f
	s.mu.Unlock()
	go f.loop()
	return stop
}

func (s *Store) StopUsageFlusher() {
	s.mu.Lock()
	f := s.flusher
	s.flusher = nil
	s.mu.Unlock()
	if f != nil {
		f.stop()
		<-f.doneCh
	}
	_ = s.FlushUsage()
}

type usageFlusher struct {
	stop   func()
	stopCh chan struct{}
	doneCh chan struct{}
	store  *Store
}

func (f *usageFlusher) loop() {
	defer close(f.doneCh)
	t := time.NewTicker(usageFlushInterval)
	defer t.Stop()
	for {
		select {
		case <-f.stopCh:
			return
		case <-t.C:
			_ = f.store.FlushUsage()
		}
	}
}

func (s *Store) Status() map[string]any {
	s.mu.RLock()
	enabled := s.enabled
	statePath := s.statePath
	keys := s.keysSnapshotLocked()
	limiter := s.limiter
	usage := s.usage
	s.mu.RUnlock()
	rpmUsage := map[string]int{}
	if limiter != nil {
		rpmUsage = limiter.Snapshot()
	}
	return map[string]any{
		"enabled":          enabled,
		"state_file":       statePath,
		"key_count":        len(keys),
		"rpm_usage":        rpmUsage,
		"prices_available": s.PricesAvailable(),
		"usage":            usageSummaryForKeys(usage, keys),
	}
}

func usageSummaryForKeys(usage *usageLedger, keys []KeyConfig) map[string]UsageSummary {
	if usage == nil {
		return map[string]UsageSummary{}
	}
	out := make(map[string]UsageSummary, len(keys))
	for _, key := range keys {
		out[key.ID] = usage.Summary(key)
	}
	return out
}
