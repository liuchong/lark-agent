package storage

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode"

	"github.com/liuchong/lark-agent/agent/domain"
	"github.com/liuchong/lark-agent/agent/memory"
	"github.com/liuchong/lark-agent/internal/apperr"
)

const (
	maxMemoryContentBytes = 2 * 1024
	maxMemoryNoteBytes    = 1024
	maxMemoryScopeBytes   = 256
	maxMemoryQueryRecords = 32
	maxMemoryQueryBytes   = 16 * 1024
)

var credentialPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)\bsk-(?:proj-)?[a-z0-9_-]{16,}\b`),
	regexp.MustCompile(`(?i)\bgh[pousr]_[a-z0-9]{20,}\b`),
	regexp.MustCompile(`(?i)\bgithub_pat_[a-z0-9_]{20,}\b`),
	regexp.MustCompile(`\b(?:AKIA|ASIA)[A-Z0-9]{16}\b`),
	regexp.MustCompile(`(?i)\bxox[baprs]-[a-z0-9-]{10,}\b`),
	regexp.MustCompile(`(?i)\beyJ[a-z0-9_-]{10,}\.[a-z0-9_-]{10,}\.[a-z0-9_-]{8,}\b`),
	regexp.MustCompile(`(?i)\b(?:password|passwd|pwd|secret|token|api[_ -]?key|client[_ -]?secret|aws_secret_access_key)\s*[:=]\s*\S+`),
	regexp.MustCompile(`(?:密码|密钥|令牌)\s*[:：=]\s*\S+`),
}

// AddMemory inserts one bounded durable entry.
func (s *Store) AddMemory(ctx context.Context, record memory.Record) (memory.Record, error) {
	if s == nil || s.db == nil {
		return memory.Record{}, errs.NewInternalError(errs.SubtypeStorage, "memory store is not open")
	}
	if err := validateMemoryRecord(record); err != nil {
		return memory.Record{}, err
	}
	if strings.TrimSpace(record.ID) == "" {
		id, err := randomID()
		if err != nil {
			return memory.Record{}, err
		}
		record.ID = id
	}
	record.Scope = strings.TrimSpace(record.Scope)
	if record.Scope == "" {
		record.Scope = "global"
	}
	record.Text = strings.TrimSpace(record.Text)
	if record.Status == "" {
		record.Status = memory.StatusCandidate
	}
	if record.CreatedAt.IsZero() {
		record.CreatedAt = time.Now().UTC()
	}
	if record.UpdatedAt.IsZero() {
		record.UpdatedAt = record.CreatedAt
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO memory_entries(
		id, kind, scope, content, status, source_work_item_id,
		source_message_id, confidence, created_at, updated_at, deleted_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, NULL)`,
		record.ID,
		record.Kind,
		record.Scope,
		record.Text,
		record.Status,
		nullablePositiveInt64(record.SourceWorkItemID),
		nullableMemoryString(record.SourceMessageID),
		record.Confidence,
		record.CreatedAt.Format(time.RFC3339Nano),
		record.UpdatedAt.Format(time.RFC3339Nano),
	)
	if err != nil {
		return memory.Record{}, errs.NewInternalError(errs.SubtypeStorage, "insert memory entry").WithCause(err)
	}
	record.Source = memorySource(record)
	return record, nil
}

// SearchMemories returns only bounded, non-deleted entries matching Query.
func (s *Store) SearchMemories(ctx context.Context, query memory.Query) ([]memory.Record, error) {
	if s == nil || s.db == nil {
		return nil, errs.NewInternalError(errs.SubtypeStorage, "memory store is not open")
	}
	if query.Status == "" {
		query.Status = memory.StatusConfirmed
	}
	if query.Limit <= 0 || query.Limit > maxMemoryQueryRecords {
		query.Limit = 8
	}
	if query.MaxBytes <= 0 || query.MaxBytes > maxMemoryQueryBytes {
		query.MaxBytes = 8 * 1024
	}
	if query.MinConfidence < 0 {
		query.MinConfidence = 0
	}
	clauses := []string{
		"deleted_at IS NULL",
		"status = ?",
		"confidence >= ?",
	}
	args := []any{query.Status, query.MinConfidence}
	scopes := normalizedMemoryScopes(query.Scopes)
	if len(scopes) == 0 {
		scopes = []string{"global"}
	}
	scopePlaceholders := make([]string, len(scopes))
	for index, scope := range scopes {
		scopePlaceholders[index] = "?"
		args = append(args, scope)
	}
	clauses = append(clauses, "scope IN ("+strings.Join(scopePlaceholders, ",")+")")
	if terms := memoryQueryTerms(query.Text); len(terms) > 0 {
		termClauses := make([]string, len(terms))
		for index, term := range terms {
			termClauses[index] = "instr(lower(content), ?) > 0"
			args = append(args, strings.ToLower(term))
		}
		clauses = append(clauses, "("+strings.Join(termClauses, " OR ")+")")
	}
	args = append(args, query.Limit)
	rows, err := s.db.QueryContext(ctx, `SELECT
		id, kind, scope, content, status, source_work_item_id,
		source_message_id, confidence, created_at, updated_at, deleted_at
		FROM memory_entries
		WHERE `+strings.Join(clauses, " AND ")+`
		ORDER BY confidence DESC, updated_at DESC, id DESC
		LIMIT ?`, args...)
	if err != nil {
		return nil, errs.NewInternalError(errs.SubtypeStorage, "query memories").WithCause(err)
	}
	defer rows.Close() //nolint:errcheck // iteration reports row errors below
	records := make([]memory.Record, 0, query.Limit)
	usedBytes := 0
	for rows.Next() {
		record, err := scanMemoryRecord(rows)
		if err != nil {
			return nil, err
		}
		recordBytes := len(record.Text)
		if usedBytes+recordBytes > query.MaxBytes {
			break
		}
		usedBytes += recordBytes
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return nil, errs.NewInternalError(errs.SubtypeStorage, "iterate memories").WithCause(err)
	}
	return records, nil
}

// ListMemories returns bounded entries for owner control surfaces.
func (s *Store) ListMemories(
	ctx context.Context,
	scope string,
	includeDeleted bool,
	limit int,
) ([]memory.Record, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	clauses := []string{"scope = ?"}
	args := []any{firstNonEmptyMemoryScope(scope)}
	if !includeDeleted {
		clauses = append(clauses, "deleted_at IS NULL")
	}
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, `SELECT
		id, kind, scope, content, status, source_work_item_id,
		source_message_id, confidence, created_at, updated_at, deleted_at
		FROM memory_entries
		WHERE `+strings.Join(clauses, " AND ")+`
		ORDER BY updated_at DESC, id DESC
		LIMIT ?`, args...)
	if err != nil {
		return nil, errs.NewInternalError(errs.SubtypeStorage, "list memories").WithCause(err)
	}
	defer rows.Close() //nolint:errcheck // iteration reports row errors below
	var records []memory.Record
	for rows.Next() {
		record, err := scanMemoryRecord(rows)
		if err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return nil, errs.NewInternalError(errs.SubtypeStorage, "iterate memory list").WithCause(err)
	}
	return records, nil
}

// DeleteMemory tombstones one entry.
func (s *Store) DeleteMemory(ctx context.Context, id string) (bool, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return false, errs.NewValidationError(errs.SubtypeInvalidArgument, "memory id is required")
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	result, err := s.db.ExecContext(ctx, `UPDATE memory_entries
		SET deleted_at = ?, updated_at = ?
		WHERE id = ? AND deleted_at IS NULL`, now, now, id)
	if err != nil {
		return false, errs.NewInternalError(errs.SubtypeStorage, "delete memory entry").WithCause(err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, errs.NewInternalError(errs.SubtypeStorage, "read deleted memory count").WithCause(err)
	}
	return affected == 1, nil
}

// RecordMemoryFeedback records an owner verdict and applies confirm/reject state.
func (s *Store) RecordMemoryFeedback(
	ctx context.Context,
	feedback memory.Feedback,
) (memory.Feedback, error) {
	feedback.MemoryEntryID = strings.TrimSpace(feedback.MemoryEntryID)
	feedback.Note = strings.TrimSpace(feedback.Note)
	feedback.SourceMessageID = strings.TrimSpace(feedback.SourceMessageID)
	if feedback.MemoryEntryID == "" {
		return memory.Feedback{}, errs.NewValidationError(errs.SubtypeInvalidArgument, "memory id is required")
	}
	if !validMemoryFeedbackVerdict(feedback.Verdict) {
		return memory.Feedback{}, errs.NewValidationError(
			errs.SubtypeInvalidArgument,
			"invalid memory feedback verdict: %s",
			feedback.Verdict,
		)
	}
	if len(feedback.Note) > maxMemoryNoteBytes {
		return memory.Feedback{}, errs.NewValidationError(
			errs.SubtypeInvalidArgument,
			"memory feedback note exceeds %d bytes",
			maxMemoryNoteBytes,
		)
	}
	if containsCredentialLikeContent(feedback.Note) {
		return memory.Feedback{}, errs.NewValidationError(errs.SubtypeInvalidArgument, "memory feedback contains credential-like content")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return memory.Feedback{}, errs.NewInternalError(errs.SubtypeStorage, "begin memory feedback").WithCause(err)
	}
	defer tx.Rollback() //nolint:errcheck // commit path below closes the transaction
	var deletedAt sql.NullString
	if err := tx.QueryRowContext(
		ctx,
		`SELECT deleted_at FROM memory_entries WHERE id = ?`,
		feedback.MemoryEntryID,
	).Scan(&deletedAt); err != nil {
		if err == sql.ErrNoRows {
			return memory.Feedback{}, errs.NewValidationError(errs.SubtypeInvalidArgument, "memory entry not found: %s", feedback.MemoryEntryID)
		}
		return memory.Feedback{}, errs.NewInternalError(errs.SubtypeStorage, "locate memory entry").WithCause(err)
	}
	if deletedAt.Valid && feedback.Verdict != memory.FeedbackConfirm {
		return memory.Feedback{}, errs.NewValidationError(errs.SubtypeFailedPrecondition, "memory entry is deleted: %s", feedback.MemoryEntryID)
	}
	if feedback.CreatedAt.IsZero() {
		feedback.CreatedAt = time.Now().UTC()
	}
	result, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO memory_feedback(
		memory_entry_id, verdict, note, source_message_id, created_at
	) VALUES (?, ?, ?, ?, ?)`,
		feedback.MemoryEntryID,
		feedback.Verdict,
		feedback.Note,
		nullableMemoryString(feedback.SourceMessageID),
		feedback.CreatedAt.Format(time.RFC3339Nano),
	)
	if err != nil {
		return memory.Feedback{}, errs.NewInternalError(errs.SubtypeStorage, "insert memory feedback").WithCause(err)
	}
	if id, err := result.LastInsertId(); err == nil {
		feedback.ID = id
	}
	now := feedback.CreatedAt.Format(time.RFC3339Nano)
	switch feedback.Verdict {
	case memory.FeedbackConfirm:
		_, err = tx.ExecContext(ctx, `UPDATE memory_entries
			SET status = 'confirmed', deleted_at = NULL, updated_at = ?
			WHERE id = ?`, now, feedback.MemoryEntryID)
	case memory.FeedbackReject:
		_, err = tx.ExecContext(ctx, `UPDATE memory_entries
			SET deleted_at = ?, updated_at = ?
			WHERE id = ?`, now, now, feedback.MemoryEntryID)
	}
	if err != nil {
		return memory.Feedback{}, errs.NewInternalError(errs.SubtypeStorage, "apply memory feedback").WithCause(err)
	}
	if err := tx.Commit(); err != nil {
		return memory.Feedback{}, errs.NewInternalError(errs.SubtypeStorage, "commit memory feedback").WithCause(err)
	}
	return feedback, nil
}

// ListMemoryFeedback returns recent owner verdicts for one entry.
func (s *Store) ListMemoryFeedback(
	ctx context.Context,
	memoryID string,
	limit int,
) ([]memory.Feedback, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	rows, err := s.db.QueryContext(ctx, `SELECT
		id, memory_entry_id, verdict, note, source_message_id, created_at
		FROM memory_feedback
		WHERE memory_entry_id = ?
		ORDER BY created_at DESC, id DESC
		LIMIT ?`, strings.TrimSpace(memoryID), limit)
	if err != nil {
		return nil, errs.NewInternalError(errs.SubtypeStorage, "list memory feedback").WithCause(err)
	}
	defer rows.Close() //nolint:errcheck // iteration reports row errors below
	var feedback []memory.Feedback
	for rows.Next() {
		var item memory.Feedback
		var sourceMessageID sql.NullString
		var createdAt string
		if err := rows.Scan(
			&item.ID,
			&item.MemoryEntryID,
			&item.Verdict,
			&item.Note,
			&sourceMessageID,
			&createdAt,
		); err != nil {
			return nil, errs.NewInternalError(errs.SubtypeStorage, "scan memory feedback").WithCause(err)
		}
		item.SourceMessageID = sourceMessageID.String
		item.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
		feedback = append(feedback, item)
	}
	if err := rows.Err(); err != nil {
		return nil, errs.NewInternalError(errs.SubtypeStorage, "iterate memory feedback").WithCause(err)
	}
	return feedback, nil
}

type memoryRecordScanner interface {
	Scan(...any) error
}

func scanMemoryRecord(scanner memoryRecordScanner) (memory.Record, error) {
	var record memory.Record
	var sourceWorkItemID sql.NullInt64
	var sourceMessageID sql.NullString
	var createdAt string
	var updatedAt string
	var deletedAt sql.NullString
	if err := scanner.Scan(
		&record.ID,
		&record.Kind,
		&record.Scope,
		&record.Text,
		&record.Status,
		&sourceWorkItemID,
		&sourceMessageID,
		&record.Confidence,
		&createdAt,
		&updatedAt,
		&deletedAt,
	); err != nil {
		return memory.Record{}, errs.NewInternalError(errs.SubtypeStorage, "scan memory entry").WithCause(err)
	}
	record.SourceWorkItemID = sourceWorkItemID.Int64
	record.SourceMessageID = sourceMessageID.String
	record.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
	record.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updatedAt)
	if deletedAt.Valid {
		value, _ := time.Parse(time.RFC3339Nano, deletedAt.String)
		record.DeletedAt = &value
		record.Deleted = true
	}
	record.Source = memorySource(record)
	return record, nil
}

func validateMemoryRecord(record memory.Record) error {
	switch record.Kind {
	case memory.KindFact, memory.KindPreference, memory.KindProject, memory.KindResponseFeedback:
	default:
		return errs.NewValidationError(errs.SubtypeInvalidArgument, "invalid memory kind: %s", record.Kind)
	}
	switch record.Status {
	case "", memory.StatusCandidate, memory.StatusConfirmed:
	default:
		return errs.NewValidationError(errs.SubtypeInvalidArgument, "invalid memory status: %s", record.Status)
	}
	content := strings.TrimSpace(record.Text)
	if content == "" {
		return errs.NewValidationError(errs.SubtypeInvalidArgument, "memory content is required")
	}
	if len(content) > maxMemoryContentBytes {
		return errs.NewValidationError(
			errs.SubtypeInvalidArgument,
			"memory content exceeds %d bytes",
			maxMemoryContentBytes,
		)
	}
	if len(strings.TrimSpace(record.Scope)) > maxMemoryScopeBytes {
		return errs.NewValidationError(errs.SubtypeInvalidArgument, "memory scope exceeds %d bytes", maxMemoryScopeBytes)
	}
	if record.Confidence < 0 || record.Confidence > 1 {
		return errs.NewValidationError(errs.SubtypeInvalidArgument, "memory confidence must be between 0 and 1")
	}
	if containsCredentialLikeContent(content) {
		return errs.NewValidationError(errs.SubtypeInvalidArgument, "memory content contains credential-like data")
	}
	return nil
}

func validMemoryFeedbackVerdict(verdict memory.FeedbackVerdict) bool {
	switch verdict {
	case memory.FeedbackConfirm, memory.FeedbackReject, memory.FeedbackHelpful, memory.FeedbackUnhelpful:
		return true
	default:
		return false
	}
}

func containsCredentialLikeContent(content string) bool {
	lower := strings.ToLower(content)
	for _, marker := range []string{
		"authorization:",
		"bearer ",
		"api_key",
		"api-key",
		"apikey",
		"app_secret",
		"app-secret",
		"access_token",
		"access-token",
		"refresh_token",
		"refresh-token",
		"private key",
		"begin rsa private key",
		"begin openssh private key",
		"password=",
	} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	for _, pattern := range credentialPatterns {
		if pattern.MatchString(content) {
			return true
		}
	}
	return false
}

func memoryQueryTerms(text string) []string {
	seen := make(map[string]bool)
	var terms []string
	for _, field := range strings.Fields(text) {
		term := strings.TrimFunc(field, func(r rune) bool {
			return unicode.IsPunct(r) || unicode.IsSpace(r)
		})
		if len([]rune(term)) < 2 {
			continue
		}
		lower := strings.ToLower(term)
		if seen[lower] {
			continue
		}
		seen[lower] = true
		terms = append(terms, term)
		if len(terms) == 8 {
			break
		}
	}
	return terms
}

func normalizedMemoryScopes(scopes []string) []string {
	seen := make(map[string]bool)
	var out []string
	for _, scope := range scopes {
		scope = strings.TrimSpace(scope)
		if scope == "" || seen[scope] {
			continue
		}
		seen[scope] = true
		out = append(out, scope)
		if len(out) == 8 {
			break
		}
	}
	return out
}

func firstNonEmptyMemoryScope(scope string) string {
	if scope = strings.TrimSpace(scope); scope != "" {
		return scope
	}
	return "global"
}

func memorySource(record memory.Record) domain.SourceRef {
	sum := sha256.Sum256([]byte(string(record.Kind) + "\x00" + record.Scope + "\x00" + record.Text))
	return domain.SourceRef{
		Kind:         "memory",
		RelativePath: "memory/" + record.ID,
		Digest:       fmt.Sprintf("sha256:%x", sum[:]),
	}
}

func nullablePositiveInt64(value int64) any {
	if value <= 0 {
		return nil
	}
	return value
}

func nullableMemoryString(value string) any {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	return value
}
