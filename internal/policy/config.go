package policy

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Enabled            bool        `yaml:"enabled" json:"enabled"`
	StateFile          string      `yaml:"state_file" json:"state_file"`
	PlusBaseURL        string      `yaml:"plus_base_url" json:"plus_base_url"`
	PlusManagementKey  string      `yaml:"plus_management_key" json:"plus_management_key"`
	Keys               []KeyConfig `yaml:"keys" json:"keys"`
}

type KeyConfig struct {
	ID           string    `yaml:"id" json:"id"`
	Name         string    `yaml:"name" json:"name"`
	Enabled      bool      `yaml:"enabled" json:"enabled"`
	KeyHash      string    `yaml:"key_hash" json:"key_hash"`
	KeyPreview   string    `yaml:"key_preview" json:"key_preview"`
	CallerScope  string    `yaml:"caller_scope,omitempty" json:"caller_scope,omitempty"`
	RPM          int       `yaml:"rpm" json:"rpm"`
	DailyLimitUSD  float64 `yaml:"daily_limit_usd,omitempty" json:"daily_limit_usd,omitempty"`
	WeeklyLimitUSD float64 `yaml:"weekly_limit_usd,omitempty" json:"weekly_limit_usd,omitempty"`
	CreatedAt    time.Time `yaml:"created_at,omitempty" json:"created_at,omitempty"`
	UpdatedAt    time.Time `yaml:"updated_at,omitempty" json:"updated_at,omitempty"`
}

type UsageState struct {
	Daily   UsageWindow                  `json:"daily"`
	Weekly  UsageWindow                  `json:"weekly"`
	ByAlias map[string]AliasUsageWindows `json:"by_alias,omitempty"`
}

type AliasUsageWindows struct {
	Daily  UsageWindow `json:"daily"`
	Weekly UsageWindow `json:"weekly"`
}

func (s *UsageState) UnmarshalJSON(raw []byte) error {
	var p struct {
		Daily   UsageWindow     `json:"daily"`
		Weekly  UsageWindow     `json:"weekly"`
		ByAlias json.RawMessage `json:"by_alias,omitempty"`
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return err
	}
	s.Daily = p.Daily
	s.Weekly = p.Weekly
	s.ByAlias = make(map[string]AliasUsageWindows)
	if len(p.ByAlias) == 0 || string(p.ByAlias) == "null" {
		return nil
	}
	var entries map[string]json.RawMessage
	if err := json.Unmarshal(p.ByAlias, &entries); err != nil {
		return err
	}
	for alias, rawEntry := range entries {
		if len(rawEntry) == 0 || string(rawEntry) == "null" {
			continue
		}
		if bytes.Contains(rawEntry, []byte(`"daily"`)) || bytes.Contains(rawEntry, []byte(`"weekly"`)) {
			var w AliasUsageWindows
			if err := json.Unmarshal(rawEntry, &w); err == nil {
				s.ByAlias[alias] = w
			}
			continue
		}
		var w UsageWindow
		if err := json.Unmarshal(rawEntry, &w); err == nil {
			s.ByAlias[alias] = AliasUsageWindows{Daily: w}
		}
	}
	return nil
}

type UsageWindow struct {
	TotalUSD        float64   `json:"total_usd"`
	WindowStart     time.Time `json:"window_start,omitempty"`
	CacheReadTokens int64     `json:"cache_read_tokens,omitempty"`
	CacheCostUSD    float64   `json:"cache_cost_usd,omitempty"`
	InputTokens     int64     `json:"input_tokens,omitempty"`
	OutputTokens    int64     `json:"output_tokens,omitempty"`
	CallCount       int64     `json:"call_count,omitempty"`
}

type State struct {
	Version   int                    `json:"version"`
	Keys      []KeyConfig            `json:"keys"`
	Usage     map[string]*UsageState `json:"usage,omitempty"`
	UpdatedAt time.Time              `json:"updated_at"`
}

func DefaultConfig() Config {
	return Config{
		Enabled:   true,
		StateFile: "cpa-key-quota-state.json",
	}
}

func DecodeConfig(raw []byte) (Config, error) {
	cfg := DefaultConfig()
	if len(strings.TrimSpace(string(raw))) == 0 {
		return cfg, nil
	}
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		return Config{}, err
	}
	if strings.TrimSpace(cfg.StateFile) == "" {
		cfg.StateFile = DefaultConfig().StateFile
	}
	if err := normalizeConfig(&cfg); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func normalizeConfig(cfg *Config) error {
	seen := map[string]struct{}{}
	seenHash := map[string]string{}
	for i := range cfg.Keys {
		key := &cfg.Keys[i]
		key.ID = strings.TrimSpace(key.ID)
		key.Name = strings.TrimSpace(key.Name)
		key.KeyHash = strings.TrimSpace(key.KeyHash)
		key.KeyPreview = strings.TrimSpace(key.KeyPreview)
		key.CallerScope = strings.TrimSpace(key.CallerScope)
		if key.ID == "" {
			return errors.New("key id is required")
		}
		if _, exists := seen[key.ID]; exists {
			return fmt.Errorf("duplicate key id %q", key.ID)
		}
		seen[key.ID] = struct{}{}
		if key.Name == "" {
			key.Name = key.ID
		}
		if key.RPM < 0 {
			return fmt.Errorf("key %q rpm cannot be negative", key.ID)
		}
		if key.DailyLimitUSD < 0 {
			return fmt.Errorf("key %q daily_limit_usd cannot be negative", key.ID)
		}
		if key.WeeklyLimitUSD < 0 {
			return fmt.Errorf("key %q weekly_limit_usd cannot be negative", key.ID)
		}
		hash := strings.ToLower(key.KeyHash)
		if hash != "" {
			if owner, exists := seenHash[hash]; exists {
				return fmt.Errorf("duplicate key hash on %q and %q", owner, key.ID)
			}
			seenHash[hash] = key.ID
		}
	}
	cfg.PlusBaseURL = strings.TrimRight(strings.TrimSpace(cfg.PlusBaseURL), "/")
	cfg.PlusManagementKey = strings.TrimSpace(cfg.PlusManagementKey)
	return nil
}

func ResolveStatePath(path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		path = DefaultConfig().StateFile
	}
	if filepath.IsAbs(path) {
		return filepath.Clean(path), nil
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	return abs, nil
}

func LoadState(path string) (*State, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var state State
	if err := json.Unmarshal(raw, &state); err != nil {
		return nil, err
	}
	if state.Version == 0 {
		state.Version = 1
	}
	if state.Usage == nil {
		state.Usage = make(map[string]*UsageState)
	}
	return &state, nil
}

func SaveState(path string, keys []KeyConfig, usage map[string]*UsageState) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	state := State{Version: 1, Keys: keys, Usage: usage, UpdatedAt: time.Now().UTC()}
	raw, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	return atomicWriteStateFile(path, raw)
}

func SaveUsageOnly(path string, usage map[string]*UsageState) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	var keys []KeyConfig
	if cur, err := LoadState(path); err == nil {
		keys = cur.Keys
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	state := State{Version: 1, Keys: keys, Usage: usage, UpdatedAt: time.Now().UTC()}
	raw, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	return atomicWriteStateFile(path, raw)
}

func atomicWriteStateFile(path string, raw []byte) error {
	dir := filepath.Dir(path)
	temp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tempName := temp.Name()
	defer func() { _ = os.Remove(tempName) }()
	if err := temp.Chmod(0o600); err != nil {
		_ = temp.Close()
		return err
	}
	if _, err := temp.Write(raw); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	return os.Rename(tempName, path)
}
