package plugin

import (
	"container/list"
	"sort"
	"strings"
	"sync"
	"time"
)

const codexRecentLimit = 1024

// HostAuthFile is the allowlisted projection of a host.auth.list entry. Never
// retain the host's credential JSON, file paths, or arbitrary metadata here.
type HostAuthFile struct {
	ID          string `json:"id"`
	Provider    string `json:"provider"`
	Name        string `json:"name"`
	Label       string `json:"label"`
	Email       string `json:"email"`
	Status      string `json:"status"`
	Disabled    bool   `json:"disabled"`
	Unavailable bool   `json:"unavailable"`
	Priority    int    `json:"priority"`
}

// AuthFileLister returns the complete host directory, or an error when the
// callback is unsupported or the directory could not be read.
type AuthFileLister func() ([]HostAuthFile, error)

type CodexAuthAccount struct {
	ID          string `json:"id"`
	Label       string `json:"label"`
	Name        string `json:"name"`
	Email       string `json:"email"`
	Status      string `json:"status"`
	Disabled    bool   `json:"disabled"`
	Unavailable bool   `json:"unavailable"`
	Priority    int    `json:"priority"`
	Source      string `json:"source"`
}

// BindingDiagnostics reports observed scheduler callback activity only.
type BindingDiagnostics struct {
	SchedulerPickCount  uint64 `json:"scheduler_pick_count"`
	LastSchedulerPickAt string `json:"last_scheduler_pick_at,omitempty"`
}

type CodexAuthListResponse struct {
	Available   bool               `json:"available"`
	Source      string             `json:"source"`
	Accounts    []CodexAuthAccount `json:"accounts"`
	Error       string             `json:"error,omitempty"`
	Diagnostics BindingDiagnostics `json:"diagnostics"`
}

type codexCatalog struct {
	mu        sync.Mutex
	lister    AuthFileLister
	recent    map[string]*list.Element
	order     list.List
	pickCount uint64
	lastPick  time.Time
}

func newCodexCatalog() *codexCatalog {
	return &codexCatalog{recent: make(map[string]*list.Element)}
}

// SetAuthFileLister also works in non-cshared builds; only the executable's C
// ABI bridge depends on the host allocator and callback function pointers.
func (a *App) SetAuthFileLister(fn AuthFileLister) {
	a.catalog.mu.Lock()
	a.catalog.lister = fn
	a.catalog.mu.Unlock()
}

func (c *codexCatalog) observe(candidates []SchedulerAuthCandidate) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.pickCount++
	c.lastPick = time.Now().UTC()
	for _, candidate := range candidates {
		if !isCodexProvider(candidate.Provider) || strings.TrimSpace(candidate.ID) == "" {
			continue
		}
		// Copy only display fields, never the attributes or metadata maps. In
		// particular, metadata can contain live credentials.
		account := CodexAuthAccount{
			ID:          candidate.ID,
			Name:        candidate.Attributes["name"],
			Label:       candidate.Attributes["label"],
			Email:       candidate.Attributes["email"],
			Status:      candidate.Status,
			Disabled:    strings.EqualFold(candidate.Status, "disabled"),
			Unavailable: strings.EqualFold(candidate.Status, "unavailable"),
			Priority:    candidate.Priority,
			Source:      "recent",
		}
		if element := c.recent[account.ID]; element != nil {
			element.Value = account
			c.order.MoveToBack(element)
			continue
		}
		if len(c.recent) == codexRecentLimit {
			oldest := c.order.Front()
			delete(c.recent, oldest.Value.(CodexAuthAccount).ID)
			c.order.Remove(oldest)
		}
		c.recent[account.ID] = c.order.PushBack(account)
	}
}

func (c *codexCatalog) list() CodexAuthListResponse {
	c.mu.Lock()
	lister := c.lister
	diagnostics := BindingDiagnostics{
		SchedulerPickCount: c.pickCount,
	}
	if !c.lastPick.IsZero() {
		diagnostics.LastSchedulerPickAt = c.lastPick.Format(time.RFC3339Nano)
	}
	accounts := make(map[string]CodexAuthAccount, len(c.recent))
	for id, element := range c.recent {
		accounts[id] = element.Value.(CodexAuthAccount)
	}
	c.mu.Unlock()

	response := CodexAuthListResponse{
		Source:      "unavailable",
		Accounts:    make([]CodexAuthAccount, 0, len(accounts)),
		Diagnostics: diagnostics,
	}
	if lister == nil {
		response.Error = "host.auth.list is unavailable"
	} else if files, err := lister(); err != nil {
		// Host errors may include paths or raw credential data. Expose the
		// failure, not the untrusted error payload.
		response.Error = "host.auth.list failed"
	} else {
		response.Available = true
		response.Source = "host"
		// A previously seen candidate absent from a successful host listing
		// is historical, not evidence that the credential remains usable.
		for id, account := range accounts {
			account.Unavailable = true
			accounts[id] = account
		}
		seen := make(map[string]bool, len(files))
		for _, file := range files {
			if !isCodexProvider(file.Provider) || strings.TrimSpace(file.ID) == "" || seen[file.ID] {
				continue
			}
			seen[file.ID] = true
			account := CodexAuthAccount{
				ID:          file.ID,
				Name:        file.Name,
				Label:       file.Label,
				Email:       file.Email,
				Status:      file.Status,
				Disabled:    file.Disabled || strings.EqualFold(file.Status, "disabled"),
				Unavailable: file.Unavailable || strings.EqualFold(file.Status, "unavailable"),
				Priority:    file.Priority,
				Source:      "host",
			}
			if recent, ok := accounts[file.ID]; ok {
				account.Source = "host+recent"
				if account.Name == "" {
					account.Name = recent.Name
				}
				if account.Label == "" {
					account.Label = recent.Label
				}
				if account.Email == "" {
					account.Email = recent.Email
				}
			}
			accounts[file.ID] = account
		}
	}
	if !response.Available && len(accounts) > 0 {
		response.Source = "recent"
	}
	for _, account := range accounts {
		response.Accounts = append(response.Accounts, account)
	}
	sort.Slice(response.Accounts, func(i, j int) bool {
		return response.Accounts[i].ID < response.Accounts[j].ID
	})
	return response
}

func isCodexProvider(provider string) bool {
	return strings.EqualFold(strings.TrimSpace(provider), "codex")
}
