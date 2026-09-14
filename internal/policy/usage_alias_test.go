package policy

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// The window timestamps/totals below are a real production snapshot (key
// k-684c73e7eb8a). The card reported a 24h total of $3.2602 while the per-model
// rows summed to $88.3892, because the gpt-5.6-sol bucket was anchored a day
// earlier and was never rolled.
func productionUsageState(t *testing.T) (*usageLedger, KeyConfig) {
	t.Helper()
	now := time.Date(2026, 9, 10, 1, 41, 0, 0, time.UTC)
	ledger := newUsageLedger(func() time.Time { return now })
	key := KeyConfig{ID: "k-684c73e7eb8a"}
	at := func(s string) time.Time {
		ts, err := time.Parse(time.RFC3339Nano, s)
		if err != nil {
			t.Fatalf("fixture time %q: %v", s, err)
		}
		return ts
	}
	ledger.entries[key.ID] = &UsageState{
		Daily: UsageWindow{
			TotalUSD:    3.2602176000000003,
			WindowStart: at("2026-09-10T01:06:05.673238348Z"),
			CallCount:   43,
		},
		Weekly: UsageWindow{
			TotalUSD:    103.68954040000008,
			WindowStart: at("2026-09-09T01:00:48.802572681Z"),
			CallCount:   853,
		},
		ByAlias: map[string]AliasUsageWindows{
			"gpt-5.5": {
				Daily:  UsageWindow{TotalUSD: 2.782524, WindowStart: at("2026-09-10T01:15:57.875428861Z"), CallCount: 35},
				Weekly: UsageWindow{TotalUSD: 18.08283800000001, WindowStart: at("2026-09-09T01:00:57.008341068Z"), CallCount: 192},
			},
			"gpt-5.6-luna": {
				Daily:  UsageWindow{WindowStart: at("2026-09-09T01:00:48.802572681Z"), CallCount: 1},
				Weekly: UsageWindow{WindowStart: at("2026-09-09T01:00:48.802572681Z"), CallCount: 1},
			},
			// Anchored before the current key window: stale, must not be reported.
			"gpt-5.6-sol": {
				Daily:  UsageWindow{TotalUSD: 85.60670240000009, WindowStart: at("2026-09-09T01:43:54.676458344Z"), CallCount: 660},
				Weekly: UsageWindow{TotalUSD: 85.60670240000009, WindowStart: at("2026-09-09T01:43:54.676458344Z"), CallCount: 660},
			},
		},
	}
	return ledger, key
}

func aliasRows(ledger *usageLedger, key KeyConfig) map[string]AliasUsageEntry {
	rows := map[string]AliasUsageEntry{}
	for _, row := range ledger.AliasUsage(key) {
		rows[row.Alias] = row
	}
	return rows
}

// The regression from the bug report: a bucket from a previous key window must
// not be reported inside the current 24h window. Reporting it as a zeroed
// $0.0000 row would leave the model listed as if it were used in this window.
func TestAliasUsageDropsBucketFromPreviousKeyWindow(t *testing.T) {
	ledger, key := productionUsageState(t)
	rows := aliasRows(ledger, key)

	if sol, ok := rows["gpt-5.6-sol"]; ok {
		t.Fatalf("stale alias bucket must not be reported at all, got %+v", sol.Daily)
	}
	// gpt-5.6-luna billed one call in a previous window, so it is stale too —
	// and its zeroed bucket would otherwise sit in the report forever.
	if luna, ok := rows["gpt-5.6-luna"]; ok {
		t.Fatalf("stale free-call bucket must not be reported at all, got %+v", luna.Daily)
	}
	if len(rows) != 1 {
		t.Fatalf("rows = %v, want only gpt-5.5 (the model billed in this window)", rows)
	}
	fresh := rows["gpt-5.5"]
	if !nearly(fresh.Daily.TotalUSD, 2.782524) || fresh.Daily.CallCount != 35 {
		t.Fatalf("alias inside the current key window must be kept, got %+v", fresh.Daily)
	}
	// Only live buckets are summed, and they still match the key weekly total.
	var weekly float64
	for _, row := range rows {
		weekly += row.Weekly.TotalUSD
	}
	if !nearly(weekly, 18.08283800000001) {
		t.Fatalf("sum(alias weekly)=%v must equal the live bucket's weekly=18.08283800000001", weekly)
	}
}

// No row may ever report more than the key-level window it sits under.
func TestAliasRowsNeverExceedKeyWindow(t *testing.T) {
	ledger, key := productionUsageState(t)
	summary := ledger.Summary(key)

	var dailyUSD, weeklyUSD float64
	for _, row := range aliasRows(ledger, key) {
		if row.Daily.TotalUSD > summary.DailyUSD+1e-9 {
			t.Fatalf("row %q daily %v exceeds key daily %v", row.Alias, row.Daily.TotalUSD, summary.DailyUSD)
		}
		if row.Daily.TotalUSD < 0 || row.Weekly.TotalUSD < 0 {
			t.Fatalf("row %q reported a negative total: %+v", row.Alias, row)
		}
		dailyUSD += row.Daily.TotalUSD
		weeklyUSD += row.Weekly.TotalUSD
	}
	if dailyUSD > summary.DailyUSD+1e-9 {
		t.Fatalf("sum(alias daily)=%v exceeds key daily=%v", dailyUSD, summary.DailyUSD)
	}
	if weeklyUSD > summary.WeeklyUSD+1e-9 {
		t.Fatalf("sum(alias weekly)=%v exceeds key weekly=%v", weeklyUSD, summary.WeeklyUSD)
	}
}

// Reading the breakdown commits the reset, so a later bill on the same model
// opens a fresh bucket inside the current key window instead of resuming the
// stale one.
func TestAliasUsageResetIsCommitted(t *testing.T) {
	ledger, key := productionUsageState(t)
	_ = ledger.AliasUsage(key)

	st := ledger.entries[key.ID]
	if got := st.ByAlias["gpt-5.6-sol"].Daily; got.TotalUSD != 0 || !got.WindowStart.IsZero() {
		t.Fatalf("reset was not committed to the ledger: %+v", got)
	}

	ledger.RecordCost(key.ID, "gpt-5.6-sol", 1, 0, 0, 10, 1, 1)
	sol := ledger.entries[key.ID].ByAlias["gpt-5.6-sol"]
	if !nearly(sol.Daily.TotalUSD, 1) || sol.Daily.CallCount != 1 {
		t.Fatalf("new bill must open a fresh alias bucket, got %+v", sol.Daily)
	}
	if sol.Daily.WindowStart.Before(st.Daily.WindowStart) {
		t.Fatalf("fresh bucket %v must not precede the key window %v", sol.Daily.WindowStart, st.Daily.WindowStart)
	}
}

// A bill on one model must not disturb another model's current bucket.
func TestRecordCostKeepsOtherAliasBucketsAligned(t *testing.T) {
	ledger, key := productionUsageState(t)
	ledger.RecordCost(key.ID, "gpt-5.5", 0.5, 0, 0, 10, 1, 1)

	rows := aliasRows(ledger, key)
	if got := rows["gpt-5.5"].Daily.TotalUSD; !nearly(got, 3.282524) {
		t.Fatalf("existing alias bucket must accumulate, got %v", got)
	}
	if stale, ok := rows["gpt-5.6-sol"]; ok {
		t.Fatalf("stale alias bucket must stay out of the report, got %+v", stale)
	}
}

// Steady state: the displayed rows account for the key's whole 24h window
// exactly, and never exceed the key's weekly window — a model that went quiet
// drops out of the report, so the visible rows can cover less than the 7d total
// (its spend is still counted by the key's own meter).
func TestAliasRowsAccountForKeyWindowAcrossRoll(t *testing.T) {
	now := time.Date(2026, 9, 10, 0, 0, 0, 0, time.UTC)
	ledger := newUsageLedger(func() time.Time { return now })
	key := KeyConfig{ID: "k"}

	check := func(step string) {
		t.Helper()
		summary := ledger.Summary(key)
		var dailyUSD, weeklyUSD float64
		var dailyCalls, weeklyCalls int64
		for _, row := range ledger.AliasUsage(key) {
			dailyUSD += row.Daily.TotalUSD
			weeklyUSD += row.Weekly.TotalUSD
			dailyCalls += row.Daily.CallCount
			weeklyCalls += row.Weekly.CallCount
		}
		if !nearly(dailyUSD, summary.DailyUSD) || dailyCalls != summary.DailyCallCount {
			t.Fatalf("%s: sum(alias daily)=%v/%d != key %v/%d", step, dailyUSD, dailyCalls, summary.DailyUSD, summary.DailyCallCount)
		}
		if weeklyUSD > summary.WeeklyUSD+1e-9 || weeklyCalls > summary.WeeklyCallCount {
			t.Fatalf("%s: sum(alias weekly)=%v/%d exceeds key %v/%d", step, weeklyUSD, weeklyCalls, summary.WeeklyUSD, summary.WeeklyCallCount)
		}
	}

	bill := func(alias string, usd float64) {
		ledger.RecordCost(key.ID, alias, usd, 0, 0, 10, 1, 1)
	}

	bill("a", 1)
	bill("b", 2)
	check("first window")

	now = now.Add(30 * time.Minute)
	bill("a", 0.5)
	check("same window")

	// Let the model "b" go quiet while "a" keeps billing across the roll.
	now = now.Add(23*time.Hour + 40*time.Minute)
	bill("a", 3)
	check("after the 24h roll")

	now = now.Add(2 * time.Hour)
	bill("a", 4)
	bill("b", 5)
	check("revived model in the new window")

	now = now.Add(8 * 24 * time.Hour)
	bill("a", 6)
	check("after the weekly roll")
}

// The keys-list/card report must read as "what this key did in this window":
// a model with no traffic in the current window is not a $0.0000 row, it is
// absent — however long the key has been around.
func TestAliasUsageReportsOnlyModelsUsedInThisWindow(t *testing.T) {
	now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	ledger := newUsageLedger(func() time.Time { return now })
	key := KeyConfig{ID: "k", DailyLimitUSD: 10, WeeklyLimitUSD: 50}

	// aliases returns the reported model names, so a stale row fails loudly.
	aliases := func() []string {
		t.Helper()
		rows := ledger.AliasUsage(key)
		out := make([]string, 0, len(rows))
		for _, row := range rows {
			out = append(out, row.Alias)
		}
		return out
	}

	// A model that goes quiet drops out of the 24h report, even while the key
	// keeps receiving traffic (so its own window stays open).
	ledger.RecordCost("k", "quiet", 1, 0, 0, 10, 1, 1)
	ledger.RecordCost("k", "loud", 1, 0, 0, 10, 1, 1)
	if got := aliases(); len(got) != 2 {
		t.Fatalf("same-window rows = %v, want [loud quiet]", got)
	}

	now = now.Add(25 * time.Hour)
	ledger.RecordCost("k", "loud", 2, 0, 0, 10, 1, 1)
	if got := aliases(); len(got) != 1 || got[0] != "loud" {
		t.Fatalf("after the daily roll rows = %v, want [loud]: a model with no "+
			"traffic in this window must not be reported", got)
	}
	if daily := ledger.Summary(key).DailyUSD; !nearly(daily, 2) {
		t.Fatalf("key daily = %v, want 2; rows must not change enforcement", daily)
	}

	// An unpriced (free) model still bills calls, so it stays visible at $0.
	ledger.RecordCost("k", "unpriced", 0, 0, 0, 0, 0, 1)
	rows := ledger.AliasUsage(key)
	if len(rows) != 2 {
		t.Fatalf("rows after a free call = %+v, want loud and unpriced", rows)
	}
	for _, row := range rows {
		if row.Alias == "unpriced" && (row.Daily.CallCount != 1 || row.Daily.TotalUSD != 0) {
			t.Fatalf("free row = %+v, want 1 call at $0", row.Daily)
		}
	}

	// A key with no traffic for a full week reports nothing at all — the card
	// then shows "no per-model usage in this window yet" instead of a table of
	// zeroes.
	now = now.Add(8 * 24 * time.Hour)
	if got := aliases(); len(got) != 0 {
		t.Fatalf("after the weekly roll rows = %v, want none", got)
	}
}

// The filter runs on the report the UI reads, not only on a live ledger: a
// state file restored after a restart still carries every alias the key was
// ever billed on, and only the current window's models may come back out.
func TestModelUsageForDropsModelsBilledBeforeTheCurrentWindow(t *testing.T) {
	now := time.Date(2026, 9, 16, 8, 21, 0, 0, time.UTC)
	statePath := filepath.Join(t.TempDir(), "state.json")
	state := State{
		Version:   1,
		UpdatedAt: now,
		Keys:      []KeyConfig{{ID: "cpamp", Name: "cpamp", Enabled: true, KeyHash: "sha256:deadbeef"}},
		Usage: map[string]*UsageState{
			"cpamp": {
				// Key window opened 20h ago and is still live.
				Daily:  UsageWindow{TotalUSD: 2, CallCount: 2, WindowStart: now.Add(-20 * time.Hour)},
				Weekly: UsageWindow{TotalUSD: 6, CallCount: 6, WindowStart: now.Add(-20 * time.Hour)},
				ByAlias: map[string]AliasUsageWindows{
					"gpt-6-astra": {Daily: UsageWindow{TotalUSD: 2, CallCount: 2, WindowStart: now.Add(-time.Hour)}},
					// Anchored before the live key window: stale.
					"gpt-5.6-sol": {Daily: UsageWindow{TotalUSD: 4, CallCount: 4, WindowStart: now.Add(-4 * 24 * time.Hour)}},
					// Older than the key's week, and billed nothing.
					"deepseek-flash": {Weekly: UsageWindow{TotalUSD: 3, CallCount: 3, WindowStart: now.Add(-30 * 24 * time.Hour)}},
				},
			},
		},
	}
	raw, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(statePath, raw, 0o600); err != nil {
		t.Fatal(err)
	}

	store := NewStore()
	store.SetClock(func() time.Time { return now })
	if err := store.Configure(Config{Enabled: true, StateFile: statePath}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.StopUsageFlusher)

	key, rows, ok := store.ModelUsageFor("cpamp")
	if !ok || key.ID != "cpamp" {
		t.Fatalf("ModelUsageFor = %+v ok=%v", key, ok)
	}
	if len(rows) != 1 || rows[0].Alias != "gpt-6-astra" {
		t.Fatalf("rows = %+v, want only gpt-6-astra (the model billed in this "+
			"window); historical aliases must not be listed at $0", rows)
	}
	if got := store.UsageSummaryFor(key).DailyUSD; !nearly(got, 2) {
		t.Fatalf("key daily = %v, want 2; the report must not change enforcement", got)
	}
}
