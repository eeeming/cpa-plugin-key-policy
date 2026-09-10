package policy

import (
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
// not be reported inside the current 24h window.
func TestAliasUsageDropsBucketFromPreviousKeyWindow(t *testing.T) {
	ledger, key := productionUsageState(t)
	rows := aliasRows(ledger, key)

	if sol := rows["gpt-5.6-sol"]; sol.Daily.TotalUSD != 0 || sol.Daily.CallCount != 0 || !sol.Daily.WindowStart.IsZero() {
		t.Fatalf("stale alias daily bucket must not be reported, got %+v", sol.Daily)
	}
	if fresh := rows["gpt-5.5"]; !nearly(fresh.Daily.TotalUSD, 2.782524) || fresh.Daily.CallCount != 35 {
		t.Fatalf("alias inside the current key window must be kept, got %+v", fresh.Daily)
	}
	// The key weekly window is still open, so the weekly rows stay intact — and
	// their sum matches the key weekly total exactly.
	var weekly float64
	for _, row := range rows {
		weekly += row.Weekly.TotalUSD
	}
	if !nearly(weekly, 103.68954040000008) {
		t.Fatalf("sum(alias weekly)=%v must equal key weekly=103.68954040000008", weekly)
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
	if got := rows["gpt-5.6-sol"].Daily.TotalUSD; got != 0 {
		t.Fatalf("stale alias bucket must stay cleared, got %v", got)
	}
}

// Steady state: once every bucket belongs to the current key window, the rows
// the UI sums add up to the key summary exactly, and they keep doing so across
// a window roll.
func TestAliasRowsSumMatchesKeySummaryAcrossRoll(t *testing.T) {
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
		if !nearly(weeklyUSD, summary.WeeklyUSD) || weeklyCalls != summary.WeeklyCallCount {
			t.Fatalf("%s: sum(alias weekly)=%v/%d != key %v/%d", step, weeklyUSD, weeklyCalls, summary.WeeklyUSD, summary.WeeklyCallCount)
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
