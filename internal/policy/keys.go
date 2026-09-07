package policy

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strings"
)

const HashPrefix = "sha256:"

var ErrUnknownKey = errors.New("unknown key")

const callerScopePrefix = "cli-proxy-api:caller-scope:v1\x00"

// CallerScope matches CPA's irreversible downstream-key namespace
// (sha256("cli-proxy-api:caller-scope:v1\x00" + principal)).
func CallerScope(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(callerScopePrefix + value))
	return hex.EncodeToString(sum[:])
}

func HashKey(key string) (string, error) {
	key = strings.TrimSpace(key)
	if key == "" {
		return "", errors.New("key is required")
	}
	sum := sha256.Sum256([]byte(key))
	return HashPrefix + hex.EncodeToString(sum[:]), nil
}

func MatchHash(key, hash string) bool {
	key = strings.TrimSpace(key)
	hash = strings.TrimSpace(hash)
	if key == "" || hash == "" || !strings.HasPrefix(hash, HashPrefix) {
		return false
	}
	got, err := HashKey(key)
	if err != nil {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(got), []byte(hash)) == 1
}

func PreviewKey(key string) string {
	key = strings.TrimSpace(key)
	if len(key) <= 12 {
		return key
	}
	return fmt.Sprintf("%s...%s", key[:7], key[len(key)-5:])
}

func ExtractAPIKey(headers http.Header, query map[string][]string) string {
	for _, name := range []string{"Authorization", "X-API-Key", "api-key", "x-api-key", "x-goog-api-key"} {
		value := strings.TrimSpace(headerValue(headers, name))
		if value == "" {
			continue
		}
		if strings.EqualFold(name, "Authorization") {
			if token := bearerToken(value); token != "" {
				return token
			}
			continue
		}
		return value
	}
	if query != nil {
		for _, name := range []string{"api_key", "key"} {
			values := query[name]
			if len(values) > 0 && strings.TrimSpace(values[0]) != "" {
				return strings.TrimSpace(values[0])
			}
		}
	}
	return ""
}

func headerValue(headers http.Header, name string) string {
	if headers == nil {
		return ""
	}
	if value := headers.Get(name); value != "" {
		return value
	}
	for key, values := range headers {
		if strings.EqualFold(key, name) && len(values) > 0 {
			return values[0]
		}
	}
	return ""
}

func bearerToken(value string) string {
	parts := strings.Fields(value)
	if len(parts) == 2 && strings.EqualFold(parts[0], "Bearer") {
		return strings.TrimSpace(parts[1])
	}
	return ""
}
