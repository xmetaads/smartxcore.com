package eventlog

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// CursorStore persists the last-seen event timestamp so restarts
// don't replay events already submitted.
type CursorStore struct {
	path string
	mu   sync.RWMutex
	last time.Time
}

type cursorFile struct {
	LastEventTime time.Time `json:"last_event_time"`
}

func NewCursorStore(dir string) *CursorStore {
	return &CursorStore{path: filepath.Join(dir, "event_cursor.json")}
}

func (s *CursorStore) Load() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	data, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}

	var f cursorFile
	if err := json.Unmarshal(data, &f); err != nil {
		return err
	}
	s.last = f.LastEventTime
	return nil
}

func (s *CursorStore) Get() time.Time {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.last
}

func (s *CursorStore) Set(t time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !t.After(s.last) {
		return
	}
	s.last = t
	_ = s.persistLocked()
}

func (s *CursorStore) persistLocked() error {
	data, err := json.Marshal(cursorFile{LastEventTime: s.last})
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}
