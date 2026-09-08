package policy

import (
	"sort"
	"sync"
	"time"
)

const (
	dayWindow          = 24 * time.Hour
	weekWindow         = 7 * 24 * time.Hour
	usageFlushInterval = 15 * time.Second
)

// usageLedger tracks per-key dollar usage with rolling windows: 24 hours
// (daily) and 7×24 hours (weekly), each starting at the first billed request
// in that window. When the window ages out the accumulated total is dropped
// and the next write opens a new window at now (conservative — no sliding
// partial sums). Usage is also broken down per alias.
//
// It is the in-memory source of truth; a background flusher periodically
// persists it via Store.persistCurrentState. Reads for limit enforcement
// (Admit) and reporting (keys list) go through here.
type usageLedger struct {
	mu  sync.Mutex
	now func() time.Time
	// usage by key id; nil entry allowed when a key has no usage recorded yet.
	entries map[string]*UsageState
}

func newUsageLedger(now func() time.Time) *usageLedger {
	if now == nil {
		now = time.Now
	}
	return &usageLedger{now: now, entries: make(map[string]*UsageState)}
}

// cloneUsageState copies Daily/Weekly by value and clones ByAlias so a snapshot
// can be marshaled or mutated without racing the live ledger map.
func cloneUsageState(st *UsageState) *UsageState {
	if st == nil {
		return nil
	}
	cp := *st
	if st.ByAlias == nil {
		return &cp
	}
	cp.ByAlias = make(map[string]AliasUsageWindows, len(st.ByAlias))
	for alias, windows := range st.ByAlias {
		cp.ByAlias[alias] = windows
	}
	return &cp
}

// loadFromState seeds the ledger from a loaded state file (restart recovery).
func (l *usageLedger) loadFromState(usage map[string]*UsageState) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.entries = make(map[string]*UsageState, len(usage))
	for id, st := range usage {
		if cloned := cloneUsageState(st); cloned != nil {
			l.entries[id] = cloned
		}
	}
}

// snapshot returns a deep copy for persistence/reporting. The copy is
// independent of the live ledger: RecordCost may run while the caller marshals.
func (l *usageLedger) snapshot() map[string]*UsageState {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make(map[string]*UsageState, len(l.entries))
	for id, st := range l.entries {
		if cloned := cloneUsageState(st); cloned != nil {
			out[id] = cloned
		}
	}
	return out
}

func (l *usageLedger) entryLocked(id string) *UsageState {
	st := l.entries[id]
	if st == nil {
		st = &UsageState{ByAlias: make(map[string]AliasUsageWindows)}
		l.entries[id] = st
	}
	if st.ByAlias == nil {
		st.ByAlias = make(map[string]AliasUsageWindows)
	}
	return st
}

// rollWindowLocked clears a window that has aged out. It does not open a new
// window; the next RecordCost does that so idle expired keys show no reset time.
func (l *usageLedger) rollWindowLocked(w *UsageWindow, length time.Duration, now time.Time) {
	if w.WindowStart.IsZero() {
		return
	}
	if now.Sub(w.WindowStart) >= length {
		*w = UsageWindow{}
	}
}

func (l *usageLedger) openWindowLocked(w *UsageWindow, now time.Time) {
	if w.WindowStart.IsZero() {
		*w = UsageWindow{WindowStart: now.UTC()}
	}
}

func (l *usageLedger) ensureDailyWindowLocked(st *UsageState, now time.Time) {
	l.rollWindowLocked(&st.Daily, dayWindow, now)
}

func (l *usageLedger) ensureWeeklyWindowLocked(st *UsageState, now time.Time) {
	l.rollWindowLocked(&st.Weekly, weekWindow, now)
}

func (l *usageLedger) ensureAliasWindowLocked(w *UsageWindow, daily bool, now time.Time) {
	length := weekWindow
	if daily {
		length = dayWindow
	}
	l.rollWindowLocked(w, length, now)
}

// RecordCost adds a dollar amount for a key+alias to the daily, weekly, and
// per-alias buckets, advancing windows as needed. It also accumulates the
// cache-specific counters (cache-read tokens, cache spend, non-cache input
// tokens) used for the cache hit-rate / spend report — these do NOT feed limit
// enforcement, only the Summary the UI reads.
//
// callCount is the number of successful requests to add to CallCount for this
// record (1 for a normal request, 0 when billing a zero-cost/no-op record).
//
// amount is the total dollar bill for the record; cacheCost is the portion of
// that bill attributable to cache-hit input tokens priced at the cache price
// (0 when no cache price was configured); cacheReadTokens is the cache-hit
// count for the record; inputTokens is the non-cache input-token count charged
// at the regular input price (the denominator partner for hit-rate);
// outputTokens is the completion-token count charged at the output price.
func (l *usageLedger) RecordCost(id, alias string, amount, cacheCost float64, cacheReadTokens, inputTokens, outputTokens int64, callCount int64) {
	if id == "" {
		return
	}
	now := l.now()
	l.mu.Lock()
	defer l.mu.Unlock()
	st := l.entryLocked(id)
	l.ensureDailyWindowLocked(st, now)
	l.ensureWeeklyWindowLocked(st, now)
	l.openWindowLocked(&st.Daily, now)
	l.openWindowLocked(&st.Weekly, now)
	st.Daily.TotalUSD += amount
	st.Weekly.TotalUSD += amount
	st.Daily.CallCount += callCount
	st.Weekly.CallCount += callCount
	if cacheReadTokens > 0 {
		st.Daily.CacheReadTokens += cacheReadTokens
		st.Weekly.CacheReadTokens += cacheReadTokens
	}
	if cacheCost > 0 {
		st.Daily.CacheCostUSD += cacheCost
		st.Weekly.CacheCostUSD += cacheCost
	}
	if inputTokens > 0 {
		st.Daily.InputTokens += inputTokens
		st.Weekly.InputTokens += inputTokens
	}
	if outputTokens > 0 {
		st.Daily.OutputTokens += outputTokens
		st.Weekly.OutputTokens += outputTokens
	}

	aliasEntry := st.ByAlias[alias]
	l.ensureAliasWindowLocked(&aliasEntry.Daily, true, now)
	l.ensureAliasWindowLocked(&aliasEntry.Weekly, false, now)
	l.openWindowLocked(&aliasEntry.Daily, now)
	l.openWindowLocked(&aliasEntry.Weekly, now)
	aliasEntry.Daily.TotalUSD += amount
	aliasEntry.Weekly.TotalUSD += amount
	aliasEntry.Daily.CallCount += callCount
	aliasEntry.Weekly.CallCount += callCount
	aliasEntry.Daily.CacheReadTokens += cacheReadTokens
	aliasEntry.Weekly.CacheReadTokens += cacheReadTokens
	aliasEntry.Daily.CacheCostUSD += cacheCost
	aliasEntry.Weekly.CacheCostUSD += cacheCost
	aliasEntry.Daily.InputTokens += inputTokens
	aliasEntry.Weekly.InputTokens += inputTokens
	aliasEntry.Daily.OutputTokens += outputTokens
	aliasEntry.Weekly.OutputTokens += outputTokens
	st.ByAlias[alias] = aliasEntry
}

// UsageSummary is what the keys-list API reports for a key. The cache fields are
// reported for both the daily and weekly windows so the UI can show today's and
// the rolling-week's cache spend / hit-rate. CacheHitRate is not serialized
// here; the UI derives it as cacheRead / (cacheRead + input).
type UsageSummary struct {
	DailyUSD              float64    `json:"daily_usd"`
	WeeklyUSD             float64    `json:"weekly_usd"`
	DailyLimitUSD         float64    `json:"daily_limit_usd"`
	WeeklyLimitUSD        float64    `json:"weekly_limit_usd"`
	DailyWindowStart      *time.Time `json:"daily_window_start,omitempty"`
	WeeklyWindowStart     *time.Time `json:"weekly_window_start,omitempty"`
	DailyResetAt          *time.Time `json:"daily_reset_at,omitempty"`
	WeeklyResetAt         *time.Time `json:"weekly_reset_at,omitempty"`
	DailyCacheCostUSD     float64    `json:"daily_cache_cost_usd,omitempty"`
	WeeklyCacheCostUSD    float64    `json:"weekly_cache_cost_usd,omitempty"`
	DailyCacheReadTokens  int64      `json:"daily_cache_read_tokens,omitempty"`
	WeeklyCacheReadTokens int64      `json:"weekly_cache_read_tokens,omitempty"`
	DailyInputTokens      int64      `json:"daily_input_tokens,omitempty"`
	WeeklyInputTokens     int64      `json:"weekly_input_tokens,omitempty"`
	// DailyCallCount / WeeklyCallCount: number of successful requests billed
	// into the window (token-billed or per-call). Failed requests don't count.
	// Reported for display only; not used for limit enforcement.
	DailyCallCount  int64 `json:"daily_call_count,omitempty"`
	WeeklyCallCount int64 `json:"weekly_call_count,omitempty"`
}

// Summary returns the current usage + limits for a key. Limits come from the
// KeyConfig; usage from the ledger. daily_reset_at = window start + 24h;
// weekly_reset_at = window start + 7 days. Both are omitted until the key
// has billed at least once (the window starts on first use).
func (l *usageLedger) Summary(key KeyConfig) UsageSummary {
	now := l.now()
	l.mu.Lock()
	defer l.mu.Unlock()
	st := l.entries[key.ID]
	summary := UsageSummary{
		DailyLimitUSD:  key.DailyLimitUSD,
		WeeklyLimitUSD: key.WeeklyLimitUSD,
	}
	if st == nil {
		return summary
	}
	// Re-evaluate windows on read so a report never shows stale totals from a
	// window that already aged out.
	ensureSt := *st
	if ensureSt.ByAlias == nil {
		ensureSt.ByAlias = make(map[string]AliasUsageWindows)
	}
	l.ensureDailyWindowLocked(&ensureSt, now)
	l.ensureWeeklyWindowLocked(&ensureSt, now)
	summary.DailyUSD = ensureSt.Daily.TotalUSD
	summary.WeeklyUSD = ensureSt.Weekly.TotalUSD
	summary.DailyCacheCostUSD = ensureSt.Daily.CacheCostUSD
	summary.WeeklyCacheCostUSD = ensureSt.Weekly.CacheCostUSD
	summary.DailyCacheReadTokens = ensureSt.Daily.CacheReadTokens
	summary.WeeklyCacheReadTokens = ensureSt.Weekly.CacheReadTokens
	summary.DailyInputTokens = ensureSt.Daily.InputTokens
	summary.WeeklyInputTokens = ensureSt.Weekly.InputTokens
	summary.DailyCallCount = ensureSt.Daily.CallCount
	summary.WeeklyCallCount = ensureSt.Weekly.CallCount
	summary.DailyWindowStart, summary.DailyResetAt = windowBounds(ensureSt.Daily, dayWindow)
	summary.WeeklyWindowStart, summary.WeeklyResetAt = windowBounds(ensureSt.Weekly, weekWindow)
	return summary
}

func windowBounds(w UsageWindow, length time.Duration) (start, reset *time.Time) {
	if w.WindowStart.IsZero() {
		return nil, nil
	}
	s := w.WindowStart.UTC()
	r := s.Add(length)
	return &s, &r
}

// OverLimit reports whether a key is over its daily or weekly dollar limit.
// Returns the reason ("daily_exceeded"/"weekly_exceeded") and the offending
// summary when over; "" and zero summary otherwise.
func (l *usageLedger) OverLimit(key KeyConfig) (string, UsageSummary) {
	if key.DailyLimitUSD <= 0 && key.WeeklyLimitUSD <= 0 {
		return "", UsageSummary{}
	}
	s := l.Summary(key)
	if key.DailyLimitUSD > 0 && s.DailyUSD >= key.DailyLimitUSD {
		return "daily_exceeded", s
	}
	if key.WeeklyLimitUSD > 0 && s.WeeklyUSD >= key.WeeklyLimitUSD {
		return "weekly_exceeded", s
	}
	return "", UsageSummary{}
}

// resetUsage drops in-memory usage for a key (unbind).
func (l *usageLedger) resetUsage(id string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.entries, id)
}

// AliasUsageEntry is one billed model (CPA usage.handle Alias) in the
// per-key usage breakdown.
type AliasUsageEntry struct {
	Alias  string      `json:"alias"`
	Daily  UsageWindow `json:"daily"`
	Weekly UsageWindow `json:"weekly"`
}

// AliasUsage returns billed-model rows for a key. Windows are re-evaluated
// on read so an aged-out total resets for display (the read does not mutate
// the ledger; the next write commits the reset, mirroring Summary). Rows
// are sorted by alias for stable display.
func (l *usageLedger) AliasUsage(key KeyConfig) []AliasUsageEntry {
	now := l.now()
	l.mu.Lock()
	defer l.mu.Unlock()

	byAlias := make(map[string]AliasUsageEntry)
	if st := l.entries[key.ID]; st != nil {
		for alias, w := range st.ByAlias {
			l.ensureAliasWindowLocked(&w.Daily, true, now)
			l.ensureAliasWindowLocked(&w.Weekly, false, now)
			byAlias[alias] = AliasUsageEntry{
				Alias:  alias,
				Daily:  w.Daily,
				Weekly: w.Weekly,
			}
		}
	}

	out := make([]AliasUsageEntry, 0, len(byAlias))
	for _, entry := range byAlias {
		out = append(out, entry)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Alias < out[j].Alias })
	return out
}
