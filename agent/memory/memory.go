// Package memory stores explicit, source-backed memories for the agent.
package memory

import (
	"strings"
	"sync"
	"time"

	"github.com/liuchong/lark-agent/agent/domain"
)

// Kind classifies memory records.
type Kind string

const (
	KindTrajectory Kind = "trajectory"
	KindEpisodic   Kind = "episodic"
	KindSemantic   Kind = "semantic"
	KindProcedural Kind = "procedural"
)

// Record is a source-backed memory entry.
type Record struct {
	ID         string           `json:"id" yaml:"id"`
	Kind       Kind             `json:"kind" yaml:"kind"`
	Text       string           `json:"text" yaml:"text"`
	Source     domain.SourceRef `json:"source" yaml:"source"`
	Confidence float64          `json:"confidence" yaml:"confidence"`
	CreatedAt  time.Time        `json:"created_at" yaml:"created_at"`
	Deleted    bool             `json:"deleted" yaml:"deleted"`
}

// Store keeps memory records. The V1 interface is intentionally small so a
// SQLite implementation can replace this in-memory implementation later
// without changing routing code.
type Store struct {
	mu      sync.Mutex
	records []Record
}

// NewStore creates an empty store.
func NewStore() *Store { return &Store{} }

// Add inserts a record.
func (s *Store) Add(record Record) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if record.CreatedAt.IsZero() {
		record.CreatedAt = time.Now().UTC()
	}
	s.records = append(s.records, record)
}

// Search returns non-deleted records containing query.
func (s *Store) Search(query string, limit int) []Record {
	s.mu.Lock()
	defer s.mu.Unlock()
	query = strings.ToLower(strings.TrimSpace(query))
	if limit <= 0 {
		return nil
	}
	out := make([]Record, 0, limit)
	for _, record := range s.records {
		if record.Deleted {
			continue
		}
		if query == "" || strings.Contains(strings.ToLower(record.Text), query) {
			out = append(out, record)
			if len(out) >= limit {
				break
			}
		}
	}
	return out
}

// Delete marks a record deleted.
func (s *Store) Delete(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.records {
		if s.records[i].ID == id {
			s.records[i].Deleted = true
			return true
		}
	}
	return false
}
