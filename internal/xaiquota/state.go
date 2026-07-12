package xaiquota

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const (
	StateActive             = "active"
	StateAutoDisabled       = "auto_disabled"
	StateUserManualDisabled = "user_manual_disabled"

	SourceNone       = "none"
	SourcePluginAuto = "plugin_auto"
	SourceUserManual = "user_manual"

	stateVersion = 1
)

// AccountRecord is one auth file's durable status tag.
type AccountRecord struct {
	AuthIndex     string `json:"auth_index"`
	FileName      string `json:"file_name,omitempty"`
	Provider      string `json:"provider,omitempty"`
	Account       string `json:"account,omitempty"`
	DisableSource string `json:"disable_source"`
	State         string `json:"state"`
	RecoverAtMS   int64  `json:"recover_at_ms,omitempty"`
	DisabledAtMS  int64  `json:"disabled_at_ms,omitempty"`
	PreDisabled   bool   `json:"pre_disabled,omitempty"`
	Owner         string `json:"owner,omitempty"`
	Reason        string `json:"reason,omitempty"`
	Signal        string `json:"signal,omitempty"`
	LastProbeModel string `json:"last_probe_model,omitempty"`
	LastEventHash string `json:"last_event_hash,omitempty"`
	UpdatedAtMS   int64  `json:"updated_at_ms,omitempty"`
}

// DeleteEvent records a plugin-side permission-denied deletion.
type DeleteEvent struct {
	AuthIndex   string `json:"auth_index"`
	FileName    string `json:"file_name,omitempty"`
	Account     string `json:"account,omitempty"`
	Provider    string `json:"provider,omitempty"`
	Reason      string `json:"reason,omitempty"`
	DeletedAtMS int64  `json:"deleted_at_ms"`
}

// Store is a JSON-backed durable state store.
type Store struct {
	mu       sync.Mutex
	path     string
	Version  int                      `json:"version"`
	Updated  int64                    `json:"updated_at_ms"`
	Accounts map[string]*AccountRecord `json:"accounts"`
	Usage         *UsageStats   `json:"usage,omitempty"`
	DeleteHistory []DeleteEvent `json:"delete_history,omitempty"`
}

// NewStore loads existing state or creates an empty one.
func NewStore(path string) (*Store, error) {
	s := &Store{
		path:     path,
		Version:  stateVersion,
		Accounts: map[string]*AccountRecord{},
	}
	if path == "" {
		return s, nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return s, nil
		}
		return nil, err
	}
	if len(raw) == 0 {
		return s, nil
	}
	if err := json.Unmarshal(raw, s); err != nil {
		return nil, err
	}
	s.path = path
	if s.Accounts == nil {
		s.Accounts = map[string]*AccountRecord{}
	}
	s.Version = stateVersion
	return s, nil
}

func (s *Store) Path() string {
	if s == nil {
		return ""
	}
	return s.path
}

func (s *Store) Get(authIndex string) *AccountRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec := s.Accounts[authIndex]
	if rec == nil {
		return nil
	}
	cp := *rec
	return &cp
}

func (s *Store) Snapshot() map[string]AccountRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]AccountRecord, len(s.Accounts))
	for k, v := range s.Accounts {
		if v == nil {
			continue
		}
		out[k] = *v
	}
	return out
}

func (s *Store) Upsert(rec AccountRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.Accounts == nil {
		s.Accounts = map[string]*AccountRecord{}
	}
	now := time.Now().UnixMilli()
	rec.UpdatedAtMS = now
	cp := rec
	s.Accounts[rec.AuthIndex] = &cp
	s.Updated = now
	return s.persistLocked()
}

func (s *Store) MarkActive(authIndex string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec := s.Accounts[authIndex]
	if rec == nil {
		return nil
	}
	rec.State = StateActive
	rec.DisableSource = SourceNone
	rec.RecoverAtMS = 0
	rec.DisabledAtMS = 0
	rec.PreDisabled = false
	rec.Owner = ""
	rec.Reason = ""
	rec.Signal = ""
	rec.UpdatedAtMS = time.Now().UnixMilli()
	s.Updated = rec.UpdatedAtMS
	return s.persistLocked()
}

// Remove deletes an account record entirely (credential gone from CPA).
func (s *Store) Remove(authIndex string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.Accounts == nil {
		return nil
	}
	if _, ok := s.Accounts[authIndex]; !ok {
		return nil
	}
	delete(s.Accounts, authIndex)
	s.Updated = time.Now().UnixMilli()
	return s.persistLocked()
}

func (s *Store) DueAutoDisabled(now time.Time) []AccountRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	nowMS := now.UnixMilli()
	var out []AccountRecord
	for _, rec := range s.Accounts {
		if rec == nil {
			continue
		}
		if rec.State != StateAutoDisabled {
			continue
		}
		if rec.DisableSource != SourcePluginAuto {
			continue
		}
		if rec.Owner != "" && rec.Owner != Owner {
			continue
		}
		if rec.PreDisabled {
			continue
		}
		if rec.RecoverAtMS <= 0 || nowMS < rec.RecoverAtMS {
			continue
		}
		out = append(out, *rec)
	}
	return out
}


func (s *Store) AppendDelete(ev DeleteEvent) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if ev.DeletedAtMS == 0 {
		ev.DeletedAtMS = time.Now().UnixMilli()
	}
	s.DeleteHistory = append(s.DeleteHistory, ev)
	// keep last 100
	if len(s.DeleteHistory) > 100 {
		s.DeleteHistory = s.DeleteHistory[len(s.DeleteHistory)-100:]
	}
	s.Updated = time.Now().UnixMilli()
	return s.persistLocked()
}

func (s *Store) ListDeletes(limit int) []DeleteEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	if limit <= 0 || limit > len(s.DeleteHistory) {
		limit = len(s.DeleteHistory)
	}
	if limit == 0 {
		return nil
	}
	src := s.DeleteHistory
	start := len(src) - limit
	out := make([]DeleteEvent, limit)
	copy(out, src[start:])
	// reverse newest first
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out
}

func (s *Store) persistLocked() error {
	if s.path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}