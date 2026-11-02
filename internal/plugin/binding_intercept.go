package plugin

import (
	"net/http"
	"sort"
	"strings"

	"cpa-key-policy/internal/policy"
)

// Include received spellings as well as the canonical name so downstream
// header consumers cannot retain a client-supplied case variant.
func bindingHeaderNames(headers http.Header) []string {
	names := []string{bindingHeader}
	for name := range headers {
		if name != bindingHeader && strings.EqualFold(name, bindingHeader) {
			names = append(names, name)
		}
	}
	sort.Strings(names[1:])
	return names
}

// key is the copy returned by admission, never a second lookup of live policy.
func (a *App) bindingResponse(key *policy.KeyConfig, headers http.Header) ([]byte, error) {
	response := RequestInterceptResponse{ClearHeaders: bindingHeaderNames(headers)}
	if key == nil || len(key.CodexAuthIDs) == 0 && key.CodexGroup == "" {
		return OKEnvelope(response)
	}
	// Management IDs and display names are user-supplied. Carry the existing
	// irreversible key identity, not either label or the downstream credential.
	identity := key.KeyHash
	if identity == "" {
		identity = key.CallerScope
	}
	if identity == "" {
		identity = policy.CallerScope(key.ID)
	}
	token, err := a.binding.sign(bindingSnapshot{
		KeyID:      identity,
		AuthIDs:    key.CodexAuthIDs,
		Group:      key.CodexGroup,
		Fallback:   key.CodexFallback,
		Restricted: true,
	})
	if err != nil {
		return nil, err
	}
	response.Headers = http.Header{bindingHeader: {token}}
	return OKEnvelope(response)
}
