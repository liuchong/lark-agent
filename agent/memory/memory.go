// Package memory stores explicit, source-backed memories for the agent.
package memory

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/liuchong/lark-agent/agent/domain"
)

// Kind classifies memory records.
type Kind string

const (
	KindFact             Kind = "fact"
	KindPreference       Kind = "preference"
	KindProject          Kind = "project"
	KindResponseFeedback Kind = "response_feedback"

	// Legacy names remain source-compatible for tests and old callers. Durable
	// entries use the explicit kinds above.
	KindTrajectory Kind = KindFact
	KindEpisodic   Kind = KindResponseFeedback
	KindSemantic   Kind = KindFact
	KindProcedural Kind = KindPreference
)

// Status controls whether a memory is eligible for model retrieval.
type Status string

const (
	StatusCandidate Status = "candidate"
	StatusConfirmed Status = "confirmed"
)

// Record is a source-backed memory entry.
type Record struct {
	ID               string           `json:"id" yaml:"id"`
	Kind             Kind             `json:"kind" yaml:"kind"`
	Scope            string           `json:"scope" yaml:"scope"`
	Status           Status           `json:"status" yaml:"status"`
	Text             string           `json:"text" yaml:"text"`
	Source           domain.SourceRef `json:"source" yaml:"source"`
	SourceWorkItemID int64            `json:"source_work_item_id,omitempty" yaml:"source_work_item_id,omitempty"`
	SourceMessageID  string           `json:"source_message_id,omitempty" yaml:"source_message_id,omitempty"`
	Confidence       float64          `json:"confidence" yaml:"confidence"`
	CreatedAt        time.Time        `json:"created_at" yaml:"created_at"`
	UpdatedAt        time.Time        `json:"updated_at" yaml:"updated_at"`
	DeletedAt        *time.Time       `json:"deleted_at,omitempty" yaml:"deleted_at,omitempty"`
	Deleted          bool             `json:"deleted" yaml:"deleted"`
}

// Query bounds memory retrieval before records enter model context.
type Query struct {
	Text          string
	Scopes        []string
	Status        Status
	MinConfidence float64
	Limit         int
	MaxBytes      int
}

// Reader is the context builder's durable memory boundary.
type Reader interface {
	SearchMemories(context.Context, Query) ([]Record, error)
}

// FeedbackVerdict classifies an owner verdict.
type FeedbackVerdict string

const (
	FeedbackConfirm   FeedbackVerdict = "confirm"
	FeedbackReject    FeedbackVerdict = "reject"
	FeedbackHelpful   FeedbackVerdict = "helpful"
	FeedbackUnhelpful FeedbackVerdict = "unhelpful"
)

// Feedback is durable owner evidence about one memory entry.
type Feedback struct {
	ID              int64           `json:"id"`
	MemoryEntryID   string          `json:"memory_entry_id"`
	Verdict         FeedbackVerdict `json:"verdict"`
	Note            string          `json:"note"`
	SourceMessageID string          `json:"source_message_id,omitempty"`
	CreatedAt       time.Time       `json:"created_at"`
}

// Store keeps memory records. The V1 interface is intentionally small so a
// test can assemble a context without opening SQLite. Production injects the
// durable storage.Store implementation.
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
	if record.UpdatedAt.IsZero() {
		record.UpdatedAt = record.CreatedAt
	}
	if record.Scope == "" {
		record.Scope = "global"
	}
	if record.Status == "" {
		record.Status = StatusConfirmed
	}
	s.records = append(s.records, record)
}

// Search returns non-deleted records containing query.
func (s *Store) Search(query string, limit int) []Record {
	records, _ := s.SearchMemories(context.Background(), Query{
		Text:     query,
		Scopes:   []string{"global"},
		Status:   StatusConfirmed,
		Limit:    limit,
		MaxBytes: 8 * 1024,
	})
	return records
}

// SearchMemories implements Reader for bounded in-memory tests.
func (s *Store) SearchMemories(_ context.Context, query Query) ([]Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	query.Text = strings.ToLower(strings.TrimSpace(query.Text))
	if query.Limit <= 0 {
		return nil, nil
	}
	if query.MaxBytes <= 0 {
		query.MaxBytes = 8 * 1024
	}
	status := query.Status
	if status == "" {
		status = StatusConfirmed
	}
	scopes := make(map[string]bool, len(query.Scopes))
	for _, scope := range query.Scopes {
		scopes[scope] = true
	}
	out := make([]Record, 0, query.Limit)
	usedBytes := 0
	for _, record := range s.records {
		if record.Deleted || record.DeletedAt != nil || record.Status != status ||
			record.Confidence < query.MinConfidence ||
			(len(scopes) > 0 && !scopes[record.Scope]) {
			continue
		}
		if query.Text != "" && !matchesAnyMemoryTerm(record.Text, query.Text) {
			continue
		}
		if usedBytes+len(record.Text) > query.MaxBytes {
			break
		}
		usedBytes += len(record.Text)
		out = append(out, record)
		if len(out) >= query.Limit {
			break
		}
	}
	return out, nil
}

func matchesAnyMemoryTerm(text, query string) bool {
	text = strings.ToLower(text)
	for _, term := range strings.Fields(strings.ToLower(query)) {
		term = strings.Trim(term, " \t\r\n,.?!:;()[]{}\"'")
		if len([]rune(term)) >= 2 && strings.Contains(text, term) {
			return true
		}
	}
	return false
}

// Delete marks a record deleted.
func (s *Store) Delete(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.records {
		if s.records[i].ID == id {
			now := time.Now().UTC()
			s.records[i].Deleted = true
			s.records[i].DeletedAt = &now
			s.records[i].UpdatedAt = now
			return true
		}
	}
	return false
}
