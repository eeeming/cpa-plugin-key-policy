package policy

import (
	"bytes"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
)

func batchPointer[T any](value T) *T { return &value }

func newBindingBatchStore(t *testing.T) *Store {
	t.Helper()
	store := configureQuotaStore(t)
	for _, id := range []string{"a", "b", "untouched"} {
		hash, err := HashKey("sk-" + id)
		if err != nil {
			t.Fatal(err)
		}
		if err := store.UpsertKey(KeyConfig{
			ID: id, Name: "Alias " + id, Enabled: true, KeyHash: hash,
			KeyPreview: PreviewKey("sk-" + id), CallerScope: CallerScope("sk-" + id),
			RPM: 8, DailyLimitUSD: 20, WeeklyLimitUSD: 80,
			CodexAuthIDs: []string{"old"}, CodexGroup: "pro", CodexFallback: true,
		}, true); err != nil {
			t.Fatal(err)
		}
	}
	return store
}

func readBatchState(t *testing.T, store *Store) []byte {
	t.Helper()
	body, err := os.ReadFile(store.StatePath())
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func TestBatchCodexBindingOperationsAndPreservedState(t *testing.T) {
	store := newBindingBatchStore(t)
	store.usage.RecordCost("a", "model", 1.25, 0.25, 4, 6, 8, 1)
	if !store.limiter.Allow("a", 8) {
		t.Fatal("initial limiter admission failed")
	}
	beforeUsage := store.usage.snapshot()
	beforeKeys := store.Keys()
	cases := []struct {
		name string
		req  CodexBindingBatchRequest
		want CodexBinding
	}{
		{"set", CodexBindingBatchRequest{AccountsMode: batchPointer("set"), CodexAuthIDs: batchPointer([]string{" b1 ", "a1", "", "a1"})}, CodexBinding{[]string{"a1", "b1"}, "pro", true}},
		{"add", CodexBindingBatchRequest{AccountsMode: batchPointer("add"), CodexAuthIDs: batchPointer([]string{"c1", "b1", " c1 "})}, CodexBinding{[]string{"a1", "b1", "c1"}, "pro", true}},
		{"remove", CodexBindingBatchRequest{AccountsMode: batchPointer("remove"), CodexAuthIDs: batchPointer([]string{" b1 ", "missing", "b1"})}, CodexBinding{[]string{"a1", "c1"}, "pro", true}},
		{"group", CodexBindingBatchRequest{CodexGroup: batchPointer(" FREE ")}, CodexBinding{[]string{"a1", "c1"}, "free", true}},
		{"fallback false", CodexBindingBatchRequest{CodexFallback: batchPointer(false)}, CodexBinding{[]string{"a1", "c1"}, "free", false}},
		{"clear ids only", CodexBindingBatchRequest{AccountsMode: batchPointer("set"), CodexAuthIDs: batchPointer([]string{})}, CodexBinding{[]string{}, "free", false}},
		{"clear", CodexBindingBatchRequest{Clear: true}, CodexBinding{[]string{}, "", false}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tc.req.IDs = []string{"b", "a"}
			result, err := store.BatchCodexBinding(tc.req)
			if err != nil {
				t.Fatal(err)
			}
			if result.DryRun || result.Updated != 2 || len(result.Changes) != 2 || result.Changes[0].ID != "b" || result.Changes[1].ID != "a" {
				t.Fatalf("batch result: %+v", result)
			}
			for _, change := range result.Changes {
				if !reflect.DeepEqual(change.After, tc.want) {
					t.Fatalf("after = %+v, want %+v", change.After, tc.want)
				}
				key, _, ok := store.ModelUsageFor(change.ID)
				if !ok || !sameBinding(bindingOf(key), tc.want) {
					t.Fatalf("memory allocation = %+v", key)
				}
			}
			state, err := LoadState(store.StatePath())
			if err != nil {
				t.Fatal(err)
			}
			for _, key := range state.Keys {
				if key.ID != "untouched" && !sameBinding(bindingOf(key), tc.want) {
					t.Fatalf("disk allocation = %+v", key)
				}
			}
			if !reflect.DeepEqual(state.Usage, beforeUsage) || !reflect.DeepEqual(store.usage.snapshot(), beforeUsage) {
				t.Fatal("binding batch changed usage")
			}
		})
	}
	for _, original := range beforeKeys {
		got, _, _ := store.ModelUsageFor(original.ID)
		if got.ID == "untouched" {
			if !reflect.DeepEqual(got, original) {
				t.Fatalf("unselected policy changed: %+v", got)
			}
			continue
		}
		got.CodexAuthIDs, got.CodexGroup, got.CodexFallback = original.CodexAuthIDs, original.CodexGroup, original.CodexFallback
		got.UpdatedAt = original.UpdatedAt
		if !reflect.DeepEqual(got, original) {
			t.Fatalf("binding update changed identity, limits, or timestamps: %+v", got)
		}
	}
	for range 7 {
		if !store.limiter.Allow("a", 8) {
			t.Fatal("batch consumed RPM")
		}
	}
	if store.limiter.Allow("a", 8) {
		t.Fatal("batch reset RPM bucket")
	}
}

func TestBatchCodexBindingPreviewExpectedAndAtomicFailures(t *testing.T) {
	store := newBindingBatchStore(t)
	req := CodexBindingBatchRequest{IDs: []string{"a", "b"}, AccountsMode: batchPointer("set"), CodexAuthIDs: batchPointer([]string{"new"}), DryRun: true}
	beforeKeys, beforeDisk := store.Keys(), readBatchState(t, store)
	preview, err := store.BatchCodexBinding(req)
	if err != nil || !preview.DryRun || preview.Updated != 0 || len(preview.Changes) != 2 {
		t.Fatalf("preview = %+v, err = %v", preview, err)
	}
	if !reflect.DeepEqual(beforeKeys, store.Keys()) || !bytes.Equal(beforeDisk, readBatchState(t, store)) {
		t.Fatal("dry-run mutated memory or state file")
	}
	for _, change := range preview.Changes {
		req.Expected = append(req.Expected, CodexBindingExpected{ID: change.ID, Before: change.Before})
	}
	req.DryRun = false
	result, err := store.BatchCodexBinding(req)
	if err != nil || result.Updated != 2 || !reflect.DeepEqual(result.Changes, preview.Changes) {
		t.Fatalf("apply = %+v, err = %v", result, err)
	}
	beforeKeys, beforeDisk = store.Keys(), readBatchState(t, store)
	failures := []struct {
		name string
		req  CodexBindingBatchRequest
		want error
	}{
		{"stale expected", req, ErrCodexBindingConflict},
		{"unknown after known", CodexBindingBatchRequest{IDs: []string{"a", "missing"}, Clear: true}, ErrUnknownKey},
		{"case variant", CodexBindingBatchRequest{IDs: []string{"A"}, Clear: true}, ErrUnknownKey},
		{"empty ids", CodexBindingBatchRequest{Clear: true}, ErrInvalidCodexBindingBatch},
		{"duplicate ids", CodexBindingBatchRequest{IDs: []string{"a", "a"}, Clear: true}, ErrInvalidCodexBindingBatch},
		{"untrimmed id", CodexBindingBatchRequest{IDs: []string{" a"}, Clear: true}, ErrInvalidCodexBindingBatch},
		{"empty action", CodexBindingBatchRequest{IDs: []string{"a"}}, ErrInvalidCodexBindingBatch},
		{"keep alone", CodexBindingBatchRequest{IDs: []string{"a"}, AccountsMode: batchPointer("keep")}, ErrInvalidCodexBindingBatch},
		{"invalid mode", CodexBindingBatchRequest{IDs: []string{"a"}, AccountsMode: batchPointer("overwrite")}, ErrInvalidCodexBindingBatch},
		{"ids without mode", CodexBindingBatchRequest{IDs: []string{"a"}, CodexAuthIDs: batchPointer([]string{"x"})}, ErrInvalidCodexBindingBatch},
		{"missing accounts", CodexBindingBatchRequest{IDs: []string{"a"}, AccountsMode: batchPointer("add")}, ErrInvalidCodexBindingBatch},
		{"empty add", CodexBindingBatchRequest{IDs: []string{"a"}, AccountsMode: batchPointer("add"), CodexAuthIDs: batchPointer([]string{" "})}, ErrInvalidCodexBindingBatch},
		{"clear with group", CodexBindingBatchRequest{IDs: []string{"a"}, Clear: true, CodexGroup: batchPointer("")}, ErrInvalidCodexBindingBatch},
		{"clear with mode", CodexBindingBatchRequest{IDs: []string{"a"}, Clear: true, AccountsMode: batchPointer("keep")}, ErrInvalidCodexBindingBatch},
		{"missing expected", CodexBindingBatchRequest{IDs: []string{"a"}, Clear: true, Expected: []CodexBindingExpected{}}, ErrInvalidCodexBindingBatch},
		{"duplicate expected", CodexBindingBatchRequest{IDs: []string{"a", "b"}, Clear: true, Expected: []CodexBindingExpected{{ID: "a"}, {ID: "a"}}}, ErrInvalidCodexBindingBatch},
		{"extra expected", CodexBindingBatchRequest{IDs: []string{"a"}, Clear: true, Expected: []CodexBindingExpected{{ID: "b"}}}, ErrInvalidCodexBindingBatch},
	}
	for _, tc := range failures {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := store.BatchCodexBinding(tc.req); !errors.Is(err, tc.want) {
				t.Fatalf("error = %v, want %v", err, tc.want)
			}
			if !reflect.DeepEqual(beforeKeys, store.Keys()) || !bytes.Equal(beforeDisk, readBatchState(t, store)) {
				t.Fatal("failed batch changed memory or disk")
			}
		})
	}
}

func TestBatchCodexBindingSaveFailureLeavesDiskAndMemoryUntouched(t *testing.T) {
	store := newBindingBatchStore(t)
	beforeKeys, beforeDisk := store.Keys(), readBatchState(t, store)
	path := store.StatePath()
	// Rename over a directory must fail even when the test runs as root.
	blocked := filepath.Join(t.TempDir(), "state-directory")
	if err := os.Mkdir(blocked, 0o700); err != nil {
		t.Fatal(err)
	}
	store.mu.Lock()
	store.statePath = blocked
	store.mu.Unlock()
	_, err := store.BatchCodexBinding(CodexBindingBatchRequest{IDs: []string{"a", "b"}, Clear: true})
	store.mu.Lock()
	store.statePath = path
	store.mu.Unlock()
	if !errors.Is(err, ErrCodexBindingSave) {
		t.Fatalf("save failure = %v", err)
	}
	if !reflect.DeepEqual(beforeKeys, store.Keys()) || !bytes.Equal(beforeDisk, readBatchState(t, store)) {
		t.Fatal("save failure changed bindings or existing state")
	}
	entries, err := os.ReadDir(blocked)
	if err != nil || len(entries) != 0 {
		t.Fatalf("failed save changed destination: entries=%v err=%v", entries, err)
	}
}

func TestBatchCodexBindingPublishesTogetherAndKeepsAdmissionSnapshots(t *testing.T) {
	store := newBindingBatchStore(t)
	decision := store.Admit(http.Header{"Authorization": {"Bearer sk-a"}}, nil, nil)
	if !decision.Allowed || decision.Policy == nil {
		t.Fatalf("admission = %+v", decision)
	}
	var wg sync.WaitGroup
	done := make(chan struct{})
	errCh := make(chan string, 1)
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-done:
				return
			default:
			}
			keys := store.Keys()
			var a, b CodexBinding
			for _, key := range keys {
				if key.ID == "a" {
					a = bindingOf(key)
				}
				if key.ID == "b" {
					b = bindingOf(key)
				}
			}
			if !sameBinding(a, b) {
				errCh <- "reader observed a partial batch"
				return
			}
		}
	}()
	for _, id := range []string{"new", "old", "new"} {
		if _, err := store.BatchCodexBinding(CodexBindingBatchRequest{IDs: []string{"a", "b"}, AccountsMode: batchPointer("set"), CodexAuthIDs: batchPointer([]string{id})}); err != nil {
			close(done)
			wg.Wait()
			t.Fatal(err)
		}
	}
	close(done)
	wg.Wait()
	select {
	case message := <-errCh:
		t.Fatal(message)
	default:
	}
	if !reflect.DeepEqual(decision.Policy.CodexAuthIDs, []string{"old"}) {
		t.Fatal("batch changed an admission-time copy")
	}
	newDecision := store.Admit(http.Header{"Authorization": {"Bearer sk-a"}}, nil, nil)
	if newDecision.Policy == nil || !reflect.DeepEqual(newDecision.Policy.CodexAuthIDs, []string{"new"}) {
		t.Fatalf("new request did not use new assignment: %+v", newDecision)
	}
}

func TestBatchCodexBindingPreservesConcurrentUsageAndFlushes(t *testing.T) {
	store := newBindingBatchStore(t)
	usage, limiter := store.usage, store.limiter
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	wg.Add(2)
	go func() {
		defer wg.Done()
		for range 12 {
			if _, err := store.BatchCodexBinding(CodexBindingBatchRequest{IDs: []string{"a", "b"}, CodexGroup: batchPointer(" FREE ")}); err != nil {
				errs <- err
				return
			}
		}
	}()
	go func() {
		defer wg.Done()
		for range 12 {
			if err := store.FlushUsage(); err != nil {
				errs <- err
				return
			}
		}
	}()
	for range 50 {
		store.usage.RecordCost("a", "model", 1, 0, 0, 1, 2, 1)
		store.touchLastAccess("a")
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	if store.usage != usage || store.limiter != limiter {
		t.Fatal("batch replaced live usage or RPM components")
	}
	if err := store.FlushUsage(); err != nil {
		t.Fatal(err)
	}
	state, err := LoadState(store.StatePath())
	if err != nil {
		t.Fatal(err)
	}
	if got := state.Usage["a"]; got == nil || got.Daily.TotalUSD != 50 || got.Daily.CallCount != 50 {
		t.Fatalf("concurrent usage was lost: %+v", got)
	}
	for _, key := range state.Keys {
		if key.ID == "a" && !key.LastAccessAt.Equal(store.usage.now()) {
			t.Fatalf("concurrent last access was lost: %+v", key)
		}
		if key.ID != "untouched" && key.CodexGroup != "free" {
			t.Fatalf("flusher overwrote a committed allocation: %+v", key)
		}
	}
}
