package policy

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
)

// Two admins editing different fields of the same policy at the same time must
// not lose each other's change.
func TestUpdateKeyPreservesConcurrentFieldUpdates(t *testing.T) {
	store := configureQuotaStore(t)
	bindTestKey(t, store, "k", "sk-k", 0, 0)

	const n = 40
	var wg sync.WaitGroup
	errs := make(chan error, 2*n)
	for i := 0; i < n; i++ {
		wg.Add(2)
		go func(i int) {
			defer wg.Done()
			_, err := store.UpdateKey("k", func(key *KeyConfig) error {
				key.Name = fmt.Sprintf("name-%d", i)
				return nil
			})
			errs <- err
		}(i)
		go func(i int) {
			defer wg.Done()
			_, err := store.UpdateKey("k", func(key *KeyConfig) error {
				key.RPM = i + 1
				return nil
			})
			errs <- err
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("UpdateKey: %v", err)
		}
	}

	got := store.Keys()[0]
	if !strings.HasPrefix(got.Name, "name-") {
		t.Fatalf("name update lost: %q", got.Name)
	}
	if got.RPM == 0 {
		t.Fatalf("rpm update lost: %d", got.RPM)
	}
	// The persisted state must match memory.
	st, err := LoadState(store.StatePath())
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Keys) != 1 || st.Keys[0].Name != got.Name || st.Keys[0].RPM != got.RPM {
		t.Fatalf("disk state diverged: %+v (memory %+v)", st.Keys, got)
	}
}

func TestUpdateKeyUnknownID(t *testing.T) {
	store := configureQuotaStore(t)
	if _, err := store.UpdateKey("missing", func(*KeyConfig) error { return nil }); !errors.Is(err, ErrUnknownKey) {
		t.Fatalf("err = %v, want ErrUnknownKey", err)
	}
	if _, err := store.UpdateKey("  ", func(*KeyConfig) error { return nil }); err == nil {
		t.Fatal("blank id must be rejected")
	}
}

// UpdateKey keeps the stored secret when the update carries no new plaintext,
// and rejects rebinding a hash owned by another policy.
func TestUpdateKeyKeepsSecretAndRejectsDuplicateHash(t *testing.T) {
	store := configureQuotaStore(t)
	bindTestKey(t, store, "a", "sk-a", 5, 0)
	bindTestKey(t, store, "b", "sk-b", 5, 0)
	original, _, ok := store.ModelUsageFor("a")
	if !ok {
		t.Fatal("key a missing")
	}

	if _, err := store.UpdateKey("a", func(key *KeyConfig) error {
		key.RPM = 42
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	updated, _, ok := store.ModelUsageFor("a")
	if !ok || updated.RPM != 42 {
		t.Fatalf("updated = %+v ok=%v", updated, ok)
	}
	if updated.KeyHash != original.KeyHash || updated.KeyPreview != original.KeyPreview || updated.CallerScope != original.CallerScope {
		t.Fatalf("secret fields changed without a new key: %+v", updated)
	}

	otherHash, err := HashKey("sk-b")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpdateKey("a", func(key *KeyConfig) error {
		key.KeyHash = otherHash
		return nil
	}); err == nil {
		t.Fatal("rebinding another policy's hash must fail")
	}
}

func TestAvailableKeyIDAvoidsCollisions(t *testing.T) {
	store := configureQuotaStore(t)
	hash := HashPrefix + strings.Repeat("ab", 32)

	first := store.availableKeyID(hash)
	if first != "k-"+strings.Repeat("ab", 6) {
		t.Fatalf("first id = %q", first)
	}
	if err := store.UpsertKey(KeyConfig{
		ID: first, Name: first, Enabled: true, KeyHash: hash, CallerScope: CallerScope("sk-1"),
	}, false); err != nil {
		t.Fatal(err)
	}
	second := store.availableKeyID(hash)
	if second == first || store.findByID(second) != nil {
		t.Fatalf("second id %q collides with %q", second, first)
	}
	if len(second) > 18 {
		t.Fatalf("second id %q should stay short", second)
	}
}
