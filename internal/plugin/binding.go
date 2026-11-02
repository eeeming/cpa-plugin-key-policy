package plugin

import (
	"bytes"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	bindingHeader          = "X-Cpa-Key-Quota-Binding"
	bindingSnapshotVersion = 1
	bindingPoolLimit       = 4096
	bindingSessionLimit    = 4096
	bindingSessionTTL      = time.Hour
)

// bindingSnapshot travels with an admitted request, so retries never consult a
// newer key configuration. Its signature is private to one scheduler instance.
type bindingSnapshot struct {
	Version    int      `json:"version"`
	KeyID      string   `json:"key_id"`
	AuthIDs    []string `json:"ids"`
	Group      string   `json:"group"`
	Fallback   bool     `json:"fallback"`
	Restricted bool     `json:"restricted"`
}

type bindingPoolKey struct {
	keyID      string
	assignment [sha256.Size]byte
	provider   string
	model      string
	priority   int
}

type bindingSessionKey struct {
	scope   bindingPoolKey
	session [sha256.Size]byte
}

type bindingSessionEntry struct {
	authID  string
	expires time.Time
}

type bindingScheduler struct {
	secret           [32]byte
	secretErr        error
	mu               sync.Mutex
	pools            map[bindingPoolKey]uint64
	poolOrder        []bindingPoolKey
	evictNext        int
	now              func() time.Time
	sessions         map[bindingSessionKey]bindingSessionEntry
	sessionOrder     []bindingSessionKey
	sessionEvictNext int
}

func newBindingScheduler() *bindingScheduler {
	s := &bindingScheduler{now: time.Now}
	_, s.secretErr = rand.Read(s.secret[:])
	return s
}

func normalizeBindingGroup(group string) string {
	return strings.ToLower(strings.TrimSpace(group))
}

func (s *bindingScheduler) sign(snapshot bindingSnapshot) (string, error) {
	if s.secretErr != nil {
		return "", fmt.Errorf("initialize binding signing key: %w", s.secretErr)
	}
	if snapshot.Version == 0 {
		snapshot.Version = bindingSnapshotVersion
	}
	snapshot.Group = normalizeBindingGroup(snapshot.Group)
	if len(snapshot.AuthIDs) != 0 {
		// The caller owns the admission snapshot; canonicalizing must not mutate it.
		snapshot.AuthIDs = append([]string(nil), snapshot.AuthIDs...)
		sort.Strings(snapshot.AuthIDs)
		n := 0
		for _, id := range snapshot.AuthIDs {
			if n == 0 || snapshot.AuthIDs[n-1] != id {
				snapshot.AuthIDs[n] = id
				n++
			}
		}
		snapshot.AuthIDs = snapshot.AuthIDs[:n]
	} else {
		snapshot.AuthIDs = nil
	}
	if err := validateBindingSnapshot(snapshot); err != nil {
		return "", err
	}
	payload, err := json.Marshal(snapshot)
	if err != nil {
		return "", err
	}
	encoded := base64.RawURLEncoding.EncodeToString(payload)
	mac := hmac.New(sha256.New, s.secret[:])
	_, _ = io.WriteString(mac, encoded)
	return encoded + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil)), nil
}

func validateBindingSnapshot(snapshot bindingSnapshot) error {
	if snapshot.Version != bindingSnapshotVersion {
		return errors.New("unsupported binding snapshot version")
	}
	if snapshot.KeyID == "" || strings.TrimSpace(snapshot.KeyID) != snapshot.KeyID {
		return errors.New("invalid binding key ID")
	}
	if snapshot.Group != normalizeBindingGroup(snapshot.Group) {
		return errors.New("invalid binding group")
	}
	for i, id := range snapshot.AuthIDs {
		if id == "" || strings.TrimSpace(id) != id || (i > 0 && snapshot.AuthIDs[i-1] >= id) {
			return errors.New("invalid binding auth IDs")
		}
	}
	if snapshot.Restricted != (len(snapshot.AuthIDs) != 0 || snapshot.Group != "") {
		return errors.New("inconsistent binding restrictions")
	}
	return nil
}

func (s *bindingScheduler) snapshot(headers map[string][]string) (bindingSnapshot, [sha256.Size]byte, bool, error) {
	var empty bindingSnapshot
	var fingerprint [sha256.Size]byte
	var value string
	found := false
	for name, values := range headers {
		if !strings.EqualFold(name, bindingHeader) {
			continue
		}
		// Even equal duplicates are rejected: no intermediary may choose a
		// different header instance or silently merge multiple snapshots.
		if found || len(values) != 1 || values[0] == "" {
			return empty, fingerprint, true, errors.New("invalid duplicate or empty binding header")
		}
		found = true
		value = values[0]
	}
	if !found {
		return empty, fingerprint, false, nil
	}
	if s.secretErr != nil {
		return empty, fingerprint, true, errors.New("binding signing key unavailable")
	}
	encoded, signature, ok := strings.Cut(value, ".")
	if !ok || encoded == "" || signature == "" || strings.Contains(signature, ".") {
		return empty, fingerprint, true, errors.New("malformed binding snapshot")
	}
	provided, err := base64.RawURLEncoding.Strict().DecodeString(signature)
	if err != nil || len(provided) != sha256.Size || base64.RawURLEncoding.EncodeToString(provided) != signature {
		return empty, fingerprint, true, errors.New("malformed binding signature")
	}
	mac := hmac.New(sha256.New, s.secret[:])
	_, _ = io.WriteString(mac, encoded)
	if !hmac.Equal(provided, mac.Sum(nil)) {
		return empty, fingerprint, true, errors.New("invalid binding signature")
	}
	payload, err := base64.RawURLEncoding.Strict().DecodeString(encoded)
	if err != nil || base64.RawURLEncoding.EncodeToString(payload) != encoded {
		return empty, fingerprint, true, errors.New("malformed binding payload")
	}
	var snapshot bindingSnapshot
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&snapshot); err != nil {
		return empty, fingerprint, true, errors.New("invalid binding payload")
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return empty, fingerprint, true, errors.New("trailing binding payload")
	}
	if err := validateBindingSnapshot(snapshot); err != nil {
		return empty, fingerprint, true, err
	}
	// Requiring the exact encoder shape rejects missing/null/wrong-case and
	// duplicate JSON fields, even if a signed payload was otherwise parseable.
	canonical, err := json.Marshal(snapshot)
	if err != nil || !bytes.Equal(payload, canonical) {
		return empty, fingerprint, true, errors.New("noncanonical binding payload")
	}
	return snapshot, sha256.Sum256(payload), true, nil
}
