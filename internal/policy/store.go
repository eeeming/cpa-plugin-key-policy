package policy

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
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
	s.mu.Unlock()
	s.priceMu.Lock()
	s.priceCache = nil
	s.pricesReady = false
	s.priceFetchedAt = time.Time{}
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
		s.listAPIKeys = nil
	}

	s.mu.Unlock()
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
func (s *Store) Admit(headers http.Header, query map[string][]string, metadata map[string]any) AdmitDecision {
	rawKey := ExtractAPIKey(headers, query)
	key, pluginOn := s.findBoundWhenEnabled(rawKey, metadata)
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
	if s.PricesAvailable() && usageLedger != nil {
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
	return AdmitDecision{Known: true, Allowed: true, KeyID: key.ID, Reason: "allowed"}
}

func (s *Store) PricesAvailable() bool {
	s.refreshPrices(false)
	s.priceMu.Lock()
	defer s.priceMu.Unlock()
	return s.pricesReady
}

func (s *Store) refreshPrices(force bool) {
	s.mu.RLock()
	lister := s.listPrices
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
		s.priceMu.Unlock()
		return
	}
	s.priceRefreshing = true
	s.priceMu.Unlock()
	list, err := lister()
	s.priceMu.Lock()
	s.priceRefreshing = false
	s.priceFetchedAt = time.Now()
	defer s.priceMu.Unlock()
	if err != nil {
		// Keep the last good table so USD limits do not fail open.
		// Stamp priceFetchedAt so Admit does not retry Plus on every request.
		if len(s.priceCache) == 0 {
			s.pricesReady = false
		}
		return
	}
	s.priceCache = list
	s.pricesReady = true
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
		id := "k-" + strings.TrimPrefix(hash, HashPrefix)
		if len(id) > 14 {
			id = id[:14]
		}
		if s.findByID(id) != nil {
			id = "k-" + strings.TrimPrefix(hash, HashPrefix)
			if len(id) > 18 {
				id = id[:18]
			}
		}
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

func (s *Store) UpsertKey(input KeyConfig, persist bool) error {
	s.updateMu.Lock()
	defer s.updateMu.Unlock()
	cfg := Config{Enabled: true, StateFile: s.StatePath(), Keys: []KeyConfig{input}}
	if err := normalizeConfig(&cfg); err != nil {
		return err
	}
	key := cfg.Keys[0]
	now := time.Now().UTC()
	s.mu.Lock()
	if hash := strings.ToLower(strings.TrimSpace(key.KeyHash)); hash != "" {
		if existing := s.keysByHash[hash]; existing != nil && existing.ID != key.ID {
			s.mu.Unlock()
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
	s.mu.Unlock()
	if persist {
		return s.persistCurrentState()
	}
	return nil
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
