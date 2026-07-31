// Package storage persists the agent inbox, work queue, and audit state.
package storage

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	"github.com/liuchong/lark-agent/agent/domain"
	"github.com/liuchong/lark-agent/agent/replymatch"
	"github.com/liuchong/lark-agent/internal/apperr"
	vfs "github.com/liuchong/lark-agent/internal/fsx"
)

// Store is a SQLite-backed durable queue.
type Store struct {
	db              *sql.DB
	maxRetries      int
	duplicateWindow time.Duration
	maxActiveGoals  int
	session         domain.OnlineSession
	ownsSession     bool
}

// Open opens the state database for one daemon runtime, creates a new online
// session, and fences unfinished work from every older session.
func Open(path string) (*Store, error) {
	return open(path, true)
}

// OpenInspection opens the state database without creating, stopping, or
// recovering an online session. Operator commands use it so inspecting the
// queue cannot interrupt a running daemon.
func OpenInspection(path string) (*Store, error) {
	return open(path, false)
}

func open(path string, createSession bool) (*Store, error) {
	if err := vfs.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, errs.NewInternalError(errs.SubtypeFileIO, "create state directory: %s", filepath.Dir(path)).WithCause(err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, errs.NewInternalError(errs.SubtypeStorage, "open agent state database: %s", path).WithCause(err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	store := &Store{db: db, maxRetries: 20, duplicateWindow: 2 * time.Minute, maxActiveGoals: 3}
	if err := store.migrate(); err != nil {
		_ = db.Close()
		return nil, err
	}
	if !createSession {
		if err := store.loadActiveSession(context.Background()); err != nil {
			_ = db.Close()
			return nil, err
		}
		return store, nil
	}
	session, err := store.createOnlineSession(context.Background())
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	store.session = session
	store.ownsSession = true
	if _, err := store.RecoverInterruptedWork(context.Background(), "daemon session restarted"); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

// ConfigureRecovery sets the bounded poison-item retry limit.
func (s *Store) ConfigureRecovery(maxRetries int) {
	if maxRetries > 0 {
		s.maxRetries = maxRetries
	}
}

// ConfigureScheduler applies scheduler behavior that must be enforced during
// intake, before a work item can be claimed.
func (s *Store) ConfigureScheduler(duplicateWindow time.Duration, maxActiveGoals int) {
	if duplicateWindow > 0 {
		s.duplicateWindow = duplicateWindow
	}
	if maxActiveGoals > 0 {
		s.maxActiveGoals = maxActiveGoals
	}
}

// Close closes the database.
func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	if s.ownsSession && s.session.ID != "" && s.session.Status != domain.OnlineSessionStopped {
		if _, err := s.StopCurrentSession(context.Background(), "store closed"); err != nil {
			return err
		}
	}
	return s.db.Close()
}

func (s *Store) migrate() error {
	for _, pragma := range []string{
		`PRAGMA busy_timeout=5000`,
		`PRAGMA journal_mode=WAL`,
		`PRAGMA foreign_keys=ON`,
	} {
		if _, err := s.db.Exec(pragma); err != nil {
			return errs.NewInternalError(errs.SubtypeStorage, "configure agent state database").WithCause(err)
		}
	}
	if _, err := s.db.Exec(`CREATE TABLE IF NOT EXISTS schema_version (version INTEGER NOT NULL)`); err != nil {
		return errs.NewInternalError(errs.SubtypeStorage, "create agent schema version table").WithCause(err)
	}
	var version int
	err := s.db.QueryRow(`SELECT version FROM schema_version LIMIT 1`).Scan(&version)
	if errors.Is(err, sql.ErrNoRows) {
		version = 0
	} else if err != nil {
		return errs.NewInternalError(errs.SubtypeStorage, "read agent schema version").WithCause(err)
	}
	migrations := []struct {
		version    int
		statements []string
	}{
		{version: 1, statements: []string{
			`CREATE TABLE IF NOT EXISTS work_items (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			dedup_key TEXT NOT NULL UNIQUE,
			status TEXT NOT NULL,
			event_json TEXT NOT NULL,
			decision_json TEXT,
			lease_by TEXT,
			lease_time TEXT,
			retry_count INTEGER NOT NULL DEFAULT 0,
			next_attempt_at TEXT,
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL
		)`,
			`CREATE INDEX IF NOT EXISTS idx_work_items_status ON work_items(status, updated_at)`,
			`CREATE TABLE IF NOT EXISTS checkpoints (
			id TEXT PRIMARY KEY,
			data BLOB NOT NULL,
			updated_at TEXT NOT NULL
		)`,
			`CREATE TABLE IF NOT EXISTS poll_cursors (
			scope TEXT PRIMARY KEY,
			cursor_time TEXT NOT NULL,
			updated_at TEXT NOT NULL
		)`,
		}},
		{version: 2, statements: []string{
			`CREATE TABLE IF NOT EXISTS agent_runs (
				id TEXT PRIMARY KEY,
				work_item_id INTEGER NOT NULL REFERENCES work_items(id),
				dedup_key TEXT NOT NULL,
				status TEXT NOT NULL,
				model_fingerprint TEXT,
				config_fingerprint TEXT,
				last_error TEXT,
				started_at TEXT NOT NULL,
				completed_at TEXT
			)`,
			`CREATE INDEX IF NOT EXISTS idx_agent_runs_work_item ON agent_runs(work_item_id, started_at)`,
			`CREATE INDEX IF NOT EXISTS idx_agent_runs_status ON agent_runs(status, started_at)`,
			`CREATE TABLE IF NOT EXISTS agent_steps (
				id INTEGER PRIMARY KEY AUTOINCREMENT,
				run_id TEXT NOT NULL REFERENCES agent_runs(id) ON DELETE CASCADE,
				sequence INTEGER NOT NULL,
				kind TEXT NOT NULL,
				tool_call_id TEXT,
				tool_name TEXT,
				input_json TEXT,
				output_json TEXT,
				request_id TEXT,
				prompt_tokens INTEGER NOT NULL DEFAULT 0,
				completion_tokens INTEGER NOT NULL DEFAULT 0,
				error TEXT,
				created_at TEXT NOT NULL,
				UNIQUE(run_id, sequence)
			)`,
			`CREATE TABLE IF NOT EXISTS tool_calls (
				id INTEGER PRIMARY KEY AUTOINCREMENT,
				run_id TEXT NOT NULL REFERENCES agent_runs(id) ON DELETE CASCADE,
				call_id TEXT NOT NULL,
				tool_name TEXT NOT NULL,
				arguments_json TEXT,
				result_json TEXT,
				status TEXT NOT NULL,
				error TEXT,
				created_at TEXT NOT NULL,
				UNIQUE(run_id, call_id)
			)`,
			`CREATE TABLE IF NOT EXISTS action_attempts (
				id INTEGER PRIMARY KEY AUTOINCREMENT,
				work_item_id INTEGER NOT NULL REFERENCES work_items(id),
				run_id TEXT REFERENCES agent_runs(id),
				kind TEXT NOT NULL,
				idempotency_key TEXT NOT NULL UNIQUE,
				status TEXT NOT NULL,
				request_json TEXT,
				response_json TEXT,
				error TEXT,
				created_at TEXT NOT NULL,
				updated_at TEXT NOT NULL
			)`,
			`CREATE TABLE IF NOT EXISTS dead_letters (
				work_item_id INTEGER PRIMARY KEY REFERENCES work_items(id),
				reason TEXT NOT NULL,
				metadata_json TEXT,
				created_at TEXT NOT NULL
			)`,
		}},
		{version: 3, statements: []string{
			`ALTER TABLE work_items ADD COLUMN work_kind TEXT NOT NULL DEFAULT 'generic'`,
			`ALTER TABLE work_items ADD COLUMN priority INTEGER NOT NULL DEFAULT 10`,
			`ALTER TABLE work_items ADD COLUMN duplicate_of INTEGER REFERENCES work_items(id)`,
			`ALTER TABLE work_items ADD COLUMN lease_timeout_seconds INTEGER NOT NULL DEFAULT 300`,
			`CREATE INDEX IF NOT EXISTS idx_work_items_claim_priority ON work_items(status, priority DESC, id)`,
			`CREATE INDEX IF NOT EXISTS idx_work_items_duplicate_of ON work_items(duplicate_of)`,
		}},
		{version: 4, statements: []string{
			`CREATE TABLE IF NOT EXISTS coding_goals (
				id INTEGER PRIMARY KEY AUTOINCREMENT,
				work_item_id INTEGER NOT NULL UNIQUE REFERENCES work_items(id),
				original_message_id TEXT NOT NULL,
				question TEXT NOT NULL,
				status TEXT NOT NULL,
				completion_conditions_json TEXT NOT NULL,
				blocking_conditions_json TEXT NOT NULL,
				max_investigation_turns INTEGER NOT NULL,
				used_investigation_turns INTEGER NOT NULL DEFAULT 0,
				created_at TEXT NOT NULL,
				updated_at TEXT NOT NULL
			)`,
			`CREATE INDEX IF NOT EXISTS idx_coding_goals_status ON coding_goals(status, updated_at)`,
		}},
		{version: 5, statements: []string{
			`CREATE TABLE IF NOT EXISTS online_sessions (
				id TEXT PRIMARY KEY,
				status TEXT NOT NULL,
				started_at TEXT NOT NULL,
				ready_at TEXT,
				ended_at TEXT,
				reason TEXT
			)`,
			`ALTER TABLE work_items ADD COLUMN session_id TEXT REFERENCES online_sessions(id)`,
			`CREATE INDEX IF NOT EXISTS idx_work_items_session_status ON work_items(session_id, status, priority DESC, id)`,
			`CREATE TABLE IF NOT EXISTS intake_receipts (
				id INTEGER PRIMARY KEY AUTOINCREMENT,
				message_id TEXT NOT NULL DEFAULT '',
				event_id TEXT NOT NULL DEFAULT '',
				source TEXT NOT NULL,
				session_id TEXT NOT NULL REFERENCES online_sessions(id),
				event_json TEXT NOT NULL,
				event_created_at TEXT,
				observed_at TEXT NOT NULL,
				disposition TEXT NOT NULL,
				reason TEXT,
				work_item_id INTEGER REFERENCES work_items(id)
			)`,
			`CREATE UNIQUE INDEX IF NOT EXISTS idx_intake_receipts_message_id
			 ON intake_receipts(message_id) WHERE message_id <> ''`,
			`CREATE UNIQUE INDEX IF NOT EXISTS idx_intake_receipts_event_id
			 ON intake_receipts(event_id) WHERE event_id <> ''`,
			`CREATE INDEX IF NOT EXISTS idx_intake_receipts_work_item ON intake_receipts(work_item_id)`,
			`CREATE TABLE IF NOT EXISTS work_interruptions (
				id INTEGER PRIMARY KEY AUTOINCREMENT,
				work_item_id INTEGER NOT NULL REFERENCES work_items(id),
				run_id TEXT,
				session_id TEXT,
				stage TEXT NOT NULL,
				last_sequence INTEGER NOT NULL DEFAULT 0,
				last_kind TEXT,
				last_tool TEXT,
				action_kind TEXT,
				action_status TEXT,
				reason TEXT NOT NULL,
				interrupted_at TEXT NOT NULL,
				resumed_at TEXT
			)`,
			`CREATE INDEX IF NOT EXISTS idx_work_interruptions_work_item
			 ON work_interruptions(work_item_id, interrupted_at DESC)`,
		}},
		{version: 6, statements: []string{
			`DROP INDEX IF EXISTS idx_intake_receipts_message_id`,
			`DROP INDEX IF EXISTS idx_intake_receipts_event_id`,
			`CREATE INDEX IF NOT EXISTS idx_intake_receipts_message_id
			 ON intake_receipts(message_id, observed_at DESC) WHERE message_id <> ''`,
			`CREATE INDEX IF NOT EXISTS idx_intake_receipts_event_id
			 ON intake_receipts(event_id, observed_at DESC) WHERE event_id <> ''`,
		}},
		{version: 7, statements: []string{
			`CREATE TABLE IF NOT EXISTS lifecycle_actions (
				id INTEGER PRIMARY KEY AUTOINCREMENT,
				idempotency_key TEXT NOT NULL UNIQUE,
				status TEXT NOT NULL,
				request_text TEXT NOT NULL,
				error TEXT,
				created_at TEXT NOT NULL,
				updated_at TEXT NOT NULL
			)`,
			`CREATE INDEX IF NOT EXISTS idx_lifecycle_actions_status
			 ON lifecycle_actions(status, updated_at)`,
		}},
		{version: 8, statements: []string{
			`CREATE TABLE IF NOT EXISTS resource_subscriptions (
				id INTEGER PRIMARY KEY AUTOINCREMENT,
				original_url TEXT NOT NULL UNIQUE,
				resource_type TEXT NOT NULL,
				file_token TEXT,
				app_token TEXT,
				wiki_node_token TEXT,
				table_id TEXT,
				view_id TEXT,
				monitor_modes_json TEXT NOT NULL,
				remote_subscription_id TEXT,
				status TEXT NOT NULL,
				cursor TEXT,
				last_error TEXT,
				created_at TEXT NOT NULL,
				updated_at TEXT NOT NULL
			)`,
			`CREATE INDEX IF NOT EXISTS idx_resource_subscriptions_status
			 ON resource_subscriptions(status, updated_at)`,
			`CREATE INDEX IF NOT EXISTS idx_resource_subscriptions_resource
			 ON resource_subscriptions(resource_type, file_token, app_token, table_id)`,
		}},
		{version: 9, statements: []string{
			`CREATE TABLE IF NOT EXISTS external_references (
				id INTEGER PRIMARY KEY AUTOINCREMENT,
				provider TEXT NOT NULL,
				kind TEXT NOT NULL,
				external_key TEXT NOT NULL,
				lark_message_id TEXT NOT NULL,
				chat_id TEXT NOT NULL,
				sender_app_id TEXT NOT NULL,
				reference_json TEXT NOT NULL,
				reference_digest TEXT NOT NULL,
				verified_at TEXT NOT NULL,
				updated_at TEXT NOT NULL,
				UNIQUE(provider, lark_message_id)
			)`,
			`CREATE INDEX IF NOT EXISTS idx_external_references_key
			 ON external_references(provider, external_key)`,
		}},
		{version: 10, statements: []string{
			`CREATE TABLE IF NOT EXISTS owner_reply_resolutions (
				id INTEGER PRIMARY KEY AUTOINCREMENT,
				work_item_id INTEGER NOT NULL REFERENCES work_items(id),
				target_message_id TEXT NOT NULL,
				result TEXT NOT NULL,
				matched_owner_message_ids_json TEXT NOT NULL,
				confidence REAL NOT NULL,
				reason TEXT NOT NULL,
				context_cutoff TEXT NOT NULL,
				evaluated_at TEXT NOT NULL
			)`,
			`CREATE INDEX IF NOT EXISTS idx_owner_reply_resolutions_work_item
			 ON owner_reply_resolutions(work_item_id, evaluated_at DESC, id DESC)`,
		}},
		{version: 11, statements: []string{
			`CREATE TABLE IF NOT EXISTS owner_control_commands (
				message_id TEXT PRIMARY KEY,
				command_json TEXT NOT NULL,
				status TEXT NOT NULL,
				result_json TEXT,
				error TEXT,
				created_at TEXT NOT NULL,
				updated_at TEXT NOT NULL
			)`,
			`CREATE INDEX IF NOT EXISTS idx_owner_control_commands_status
			 ON owner_control_commands(status, updated_at)`,
			`CREATE TABLE IF NOT EXISTS owner_work_resolutions (
				id INTEGER PRIMARY KEY AUTOINCREMENT,
				work_item_id INTEGER NOT NULL REFERENCES work_items(id),
				action_id INTEGER REFERENCES action_attempts(id),
				command_message_id TEXT NOT NULL UNIQUE,
				disposition TEXT NOT NULL,
				reason TEXT NOT NULL,
				resolved_at TEXT NOT NULL
			)`,
			`CREATE INDEX IF NOT EXISTS idx_owner_work_resolutions_work_item
			 ON owner_work_resolutions(work_item_id, resolved_at DESC, id DESC)`,
			`CREATE INDEX IF NOT EXISTS idx_action_attempts_pending
			 ON action_attempts(status, updated_at DESC, id DESC)`,
		}},
		{version: 12, statements: []string{
			`ALTER TABLE owner_work_resolutions ADD COLUMN work_updated_at TEXT`,
			`CREATE INDEX IF NOT EXISTS idx_owner_work_resolutions_work_epoch
			 ON owner_work_resolutions(work_item_id, work_updated_at, id DESC)`,
		}},
		{version: 13, statements: []string{
			`ALTER TABLE owner_reply_resolutions ADD COLUMN task_summary TEXT NOT NULL DEFAULT ''`,
			`ALTER TABLE owner_reply_resolutions ADD COLUMN task_class TEXT NOT NULL DEFAULT ''`,
			`ALTER TABLE owner_reply_resolutions ADD COLUMN classification_confidence REAL NOT NULL DEFAULT 0`,
			`ALTER TABLE owner_reply_resolutions ADD COLUMN requires_progress INTEGER NOT NULL DEFAULT 0`,
			`CREATE TABLE IF NOT EXISTS delegated_investigations (
				id INTEGER PRIMARY KEY AUTOINCREMENT,
				work_item_id INTEGER NOT NULL UNIQUE REFERENCES work_items(id),
				task_summary TEXT NOT NULL,
				task_class TEXT NOT NULL,
				context_cutoff TEXT NOT NULL,
				context_digest TEXT NOT NULL,
				status TEXT NOT NULL,
				progress_action_id INTEGER REFERENCES action_attempts(id),
				final_action_id INTEGER REFERENCES action_attempts(id),
				last_error TEXT,
				created_at TEXT NOT NULL,
				updated_at TEXT NOT NULL
			)`,
			`CREATE INDEX IF NOT EXISTS idx_delegated_investigations_status
			 ON delegated_investigations(status, updated_at)`,
		}},
		{version: 14, statements: []string{
			`ALTER TABLE delegated_investigations
			 ADD COLUMN context_messages_json TEXT NOT NULL DEFAULT '[]'`,
		}},
		{version: 15, statements: []string{
			`CREATE TABLE IF NOT EXISTS memory_entries (
				id TEXT PRIMARY KEY,
				kind TEXT NOT NULL CHECK (
					kind IN ('fact', 'preference', 'project', 'response_feedback')
				),
				scope TEXT NOT NULL,
				content TEXT NOT NULL,
				status TEXT NOT NULL CHECK (status IN ('candidate', 'confirmed')),
				source_work_item_id INTEGER REFERENCES work_items(id),
				source_message_id TEXT,
				confidence REAL NOT NULL CHECK (confidence >= 0 AND confidence <= 1),
				created_at TEXT NOT NULL,
				updated_at TEXT NOT NULL,
				deleted_at TEXT
			)`,
			`CREATE TABLE IF NOT EXISTS memory_feedback (
				id INTEGER PRIMARY KEY AUTOINCREMENT,
				memory_entry_id TEXT NOT NULL REFERENCES memory_entries(id),
				verdict TEXT NOT NULL CHECK (
					verdict IN ('confirm', 'reject', 'helpful', 'unhelpful')
				),
				note TEXT NOT NULL DEFAULT '',
				source_message_id TEXT,
				created_at TEXT NOT NULL
			)`,
			`CREATE INDEX IF NOT EXISTS idx_memory_entries_retrieval
			 ON memory_entries(status, scope, deleted_at, updated_at DESC)`,
			`CREATE INDEX IF NOT EXISTS idx_memory_entries_source
			 ON memory_entries(source_work_item_id, source_message_id)`,
			`CREATE INDEX IF NOT EXISTS idx_memory_feedback_entry
			 ON memory_feedback(memory_entry_id, created_at DESC)`,
			`CREATE UNIQUE INDEX IF NOT EXISTS idx_memory_feedback_message
			 ON memory_feedback(memory_entry_id, source_message_id)
			 WHERE source_message_id IS NOT NULL AND source_message_id <> ''`,
		}},
	}
	for _, migration := range migrations {
		if version >= migration.version {
			continue
		}
		tx, err := s.db.Begin()
		if err != nil {
			return errs.NewInternalError(errs.SubtypeStorage, "begin agent schema migration %d", migration.version).WithCause(err)
		}
		for _, stmt := range migration.statements {
			if _, err := tx.Exec(stmt); err != nil {
				_ = tx.Rollback()
				return errs.NewInternalError(errs.SubtypeStorage, "apply agent schema migration %d", migration.version).WithCause(err)
			}
		}
		if version == 0 {
			_, err = tx.Exec(`INSERT INTO schema_version(version) VALUES (?)`, migration.version)
		} else {
			_, err = tx.Exec(`UPDATE schema_version SET version = ?`, migration.version)
		}
		if err != nil {
			_ = tx.Rollback()
			return errs.NewInternalError(errs.SubtypeStorage, "record agent schema migration %d", migration.version).WithCause(err)
		}
		if err := tx.Commit(); err != nil {
			return errs.NewInternalError(errs.SubtypeStorage, "commit agent schema migration %d", migration.version).WithCause(err)
		}
		version = migration.version
	}
	return nil
}

// UpsertExternalReference persists an identical verified reference
// idempotently and rejects any attempt to reuse the Lark message for a
// different external object.
func (s *Store) UpsertExternalReference(
	ctx context.Context,
	ref domain.ExternalReference,
) (domain.ExternalReference, error) {
	if strings.TrimSpace(ref.Provider) == "" ||
		strings.TrimSpace(ref.Kind) == "" ||
		strings.TrimSpace(ref.ExternalKey) == "" ||
		strings.TrimSpace(ref.LarkMessageID) == "" ||
		strings.TrimSpace(ref.ChatID) == "" ||
		strings.TrimSpace(ref.SenderAppID) == "" {
		return domain.ExternalReference{}, errs.NewValidationError(
			errs.SubtypeInvalidArgument,
			"external reference identity fields are required",
		)
	}
	if err := ref.Reference.Validate(); err != nil {
		return domain.ExternalReference{}, errs.NewValidationError(
			errs.SubtypeInvalidArgument,
			"invalid external reference",
		).WithCause(err)
	}
	referenceJSON, err := json.Marshal(ref.Reference)
	if err != nil {
		return domain.ExternalReference{}, errs.NewInternalError(
			errs.SubtypeUnknown,
			"encode external reference",
		).WithCause(err)
	}
	sum := sha256.Sum256(referenceJSON)
	digest := fmt.Sprintf("sha256:%x", sum[:])
	now := time.Now().UTC()
	if ref.VerifiedAt.IsZero() {
		ref.VerifiedAt = now
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.ExternalReference{}, errs.NewInternalError(
			errs.SubtypeStorage,
			"begin external reference transaction",
		).WithCause(err)
	}
	defer tx.Rollback() //nolint:errcheck
	existing, found, err := queryExternalReference(
		tx.QueryRowContext(ctx, externalReferenceSelect+` WHERE provider = ? AND lark_message_id = ?`,
			ref.Provider, ref.LarkMessageID),
	)
	if err != nil {
		return domain.ExternalReference{}, err
	}
	if found {
		if existing.ReferenceDigest != digest ||
			existing.ExternalKey != ref.ExternalKey ||
			existing.ChatID != ref.ChatID ||
			existing.SenderAppID != ref.SenderAppID {
			return domain.ExternalReference{}, errs.NewValidationError(
				errs.SubtypeFailedPrecondition,
				"conflicting external reference for Lark message %s",
				ref.LarkMessageID,
			)
		}
		return existing, nil
	}
	result, err := tx.ExecContext(ctx, `INSERT INTO external_references(
			provider, kind, external_key, lark_message_id, chat_id, sender_app_id,
			reference_json, reference_digest, verified_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		ref.Provider,
		ref.Kind,
		ref.ExternalKey,
		ref.LarkMessageID,
		ref.ChatID,
		ref.SenderAppID,
		string(referenceJSON),
		digest,
		ref.VerifiedAt.UTC().Format(time.RFC3339Nano),
		now.Format(time.RFC3339Nano),
	)
	if err != nil {
		return domain.ExternalReference{}, errs.NewInternalError(
			errs.SubtypeStorage,
			"insert external reference",
		).WithCause(err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		return domain.ExternalReference{}, errs.NewInternalError(
			errs.SubtypeStorage,
			"read external reference id",
		).WithCause(err)
	}
	if err := tx.Commit(); err != nil {
		return domain.ExternalReference{}, errs.NewInternalError(
			errs.SubtypeStorage,
			"commit external reference",
		).WithCause(err)
	}
	ref.ID = id
	ref.ReferenceDigest = digest
	ref.UpdatedAt = now
	return ref, nil
}

const externalReferenceSelect = `SELECT id, provider, kind, external_key,
	lark_message_id, chat_id, sender_app_id, reference_json, reference_digest,
	verified_at, updated_at FROM external_references`

// GetExternalReference reads one verified reference without changing work
// admission or replay state.
func (s *Store) GetExternalReference(
	ctx context.Context,
	provider string,
	larkMessageID string,
) (domain.ExternalReference, bool, error) {
	return queryExternalReference(s.db.QueryRowContext(
		ctx,
		externalReferenceSelect+` WHERE provider = ? AND lark_message_id = ?`,
		provider,
		larkMessageID,
	))
}

type externalReferenceRowScanner interface {
	Scan(...any) error
}

func queryExternalReference(row externalReferenceRowScanner) (domain.ExternalReference, bool, error) {
	var ref domain.ExternalReference
	var referenceJSON, verifiedAt, updatedAt string
	err := row.Scan(
		&ref.ID,
		&ref.Provider,
		&ref.Kind,
		&ref.ExternalKey,
		&ref.LarkMessageID,
		&ref.ChatID,
		&ref.SenderAppID,
		&referenceJSON,
		&ref.ReferenceDigest,
		&verifiedAt,
		&updatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.ExternalReference{}, false, nil
	}
	if err != nil {
		return domain.ExternalReference{}, false, errs.NewInternalError(
			errs.SubtypeStorage,
			"read external reference",
		).WithCause(err)
	}
	if err := json.Unmarshal([]byte(referenceJSON), &ref.Reference); err != nil {
		return domain.ExternalReference{}, false, errs.NewInternalError(
			errs.SubtypeInvalidResponse,
			"decode stored external reference",
		).WithCause(err)
	}
	ref.VerifiedAt, err = time.Parse(time.RFC3339Nano, verifiedAt)
	if err != nil {
		return domain.ExternalReference{}, false, errs.NewInternalError(
			errs.SubtypeInvalidResponse,
			"parse external reference verified_at",
		).WithCause(err)
	}
	ref.UpdatedAt, err = time.Parse(time.RFC3339Nano, updatedAt)
	if err != nil {
		return domain.ExternalReference{}, false, errs.NewInternalError(
			errs.SubtypeInvalidResponse,
			"parse external reference updated_at",
		).WithCause(err)
	}
	return ref, true, nil
}

// GetPollCursor returns the stored polling cursor for a scope.
func (s *Store) GetPollCursor(scope string) (time.Time, bool, error) {
	var raw string
	err := s.db.QueryRowContext(context.Background(), `SELECT cursor_time FROM poll_cursors WHERE scope = ?`, scope).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return time.Time{}, false, nil
	}
	if err != nil {
		return time.Time{}, false, errs.NewInternalError(errs.SubtypeStorage, "read poll cursor").WithCause(err)
	}
	parsed, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		return time.Time{}, false, errs.NewInternalError(errs.SubtypeStorage, "parse poll cursor").WithCause(err)
	}
	return parsed, true, nil
}

// SetPollCursor stores the latest polling cursor for a scope.
func (s *Store) SetPollCursor(scope string, cursor time.Time) error {
	_, err := s.db.ExecContext(context.Background(),
		`INSERT INTO poll_cursors(scope, cursor_time, updated_at) VALUES (?, ?, ?)
		 ON CONFLICT(scope) DO UPDATE SET cursor_time = excluded.cursor_time, updated_at = excluded.updated_at`,
		scope, cursor.UTC().Format(time.RFC3339Nano), time.Now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		return errs.NewInternalError(errs.SubtypeStorage, "write poll cursor").WithCause(err)
	}
	return nil
}

func (s *Store) UpsertResourceSubscription(ctx context.Context, sub domain.ResourceSubscription) (domain.ResourceSubscription, error) {
	if strings.TrimSpace(sub.OriginalURL) == "" {
		return domain.ResourceSubscription{}, errs.NewValidationError(errs.SubtypeInvalidArgument, "resource subscription URL is required").
			WithParam("url")
	}
	if sub.Status == "" {
		sub.Status = domain.ResourceSubscriptionPending
	}
	now := time.Now().UTC()
	modes, err := json.Marshal(sub.MonitorModes)
	if err != nil {
		return domain.ResourceSubscription{}, errs.NewValidationError(errs.SubtypeInvalidArgument, "encode resource monitor modes").WithCause(err)
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO resource_subscriptions (
			original_url, resource_type, file_token, app_token, wiki_node_token, table_id, view_id,
			monitor_modes_json, remote_subscription_id, status, cursor, last_error, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(original_url) DO UPDATE SET
			resource_type=excluded.resource_type,
			file_token=excluded.file_token,
			app_token=excluded.app_token,
			wiki_node_token=excluded.wiki_node_token,
			table_id=excluded.table_id,
			view_id=excluded.view_id,
			monitor_modes_json=excluded.monitor_modes_json,
			remote_subscription_id=excluded.remote_subscription_id,
			status=excluded.status,
			cursor=excluded.cursor,
			last_error=excluded.last_error,
			updated_at=excluded.updated_at`,
		sub.OriginalURL, sub.ResourceType, nullString(sub.FileToken), nullString(sub.AppToken),
		nullString(sub.WikiNodeToken), nullString(sub.TableID), nullString(sub.ViewID),
		string(modes), nullString(sub.RemoteSubscriptionID), sub.Status, nullString(sub.Cursor),
		nullString(sub.LastError), now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano))
	if err != nil {
		return domain.ResourceSubscription{}, errs.NewInternalError(errs.SubtypeStorage, "upsert resource subscription").WithCause(err)
	}
	return s.GetResourceSubscription(ctx, sub.OriginalURL)
}

func (s *Store) GetResourceSubscription(ctx context.Context, originalURL string) (domain.ResourceSubscription, error) {
	row := s.db.QueryRowContext(ctx, `SELECT id, original_url, resource_type, file_token, app_token,
		wiki_node_token, table_id, view_id, monitor_modes_json, remote_subscription_id, status,
		cursor, last_error, created_at, updated_at
		FROM resource_subscriptions WHERE original_url = ?`, originalURL)
	return scanResourceSubscription(row)
}

func (s *Store) ListResourceSubscriptions(ctx context.Context) ([]domain.ResourceSubscription, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, original_url, resource_type, file_token, app_token,
		wiki_node_token, table_id, view_id, monitor_modes_json, remote_subscription_id, status,
		cursor, last_error, created_at, updated_at
		FROM resource_subscriptions WHERE status <> ? ORDER BY id`, domain.ResourceSubscriptionRemoved)
	if err != nil {
		return nil, errs.NewInternalError(errs.SubtypeStorage, "list resource subscriptions").WithCause(err)
	}
	defer rows.Close() //nolint:errcheck // read error is handled through rows.Err below
	var out []domain.ResourceSubscription
	for rows.Next() {
		sub, err := scanResourceSubscription(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, sub)
	}
	if err := rows.Err(); err != nil {
		return nil, errs.NewInternalError(errs.SubtypeStorage, "read resource subscriptions").WithCause(err)
	}
	return out, nil
}

func (s *Store) RemoveResourceSubscription(ctx context.Context, originalURL string) (domain.ResourceSubscription, error) {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	result, err := s.db.ExecContext(ctx, `UPDATE resource_subscriptions
		SET status = ?, updated_at = ? WHERE original_url = ?`,
		domain.ResourceSubscriptionRemoved, now, originalURL)
	if err != nil {
		return domain.ResourceSubscription{}, errs.NewInternalError(errs.SubtypeStorage, "remove resource subscription").WithCause(err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return domain.ResourceSubscription{}, errs.NewInternalError(errs.SubtypeStorage, "count removed resource subscription").WithCause(err)
	}
	if affected == 0 {
		return domain.ResourceSubscription{}, errs.NewValidationError(errs.SubtypeFailedPrecondition, "resource subscription not found").WithParam("url")
	}
	return s.GetResourceSubscription(ctx, originalURL)
}

// StartAgentRun creates a durable multi-step run for an already-enqueued event.
func (s *Store) StartAgentRun(ctx context.Context, event domain.NormalizedEvent, modelFingerprint, configFingerprint string) (domain.AgentRun, error) {
	dedupKey := domain.DedupKey(event)
	var workItemID int64
	if err := s.db.QueryRowContext(ctx, `SELECT id FROM work_items WHERE dedup_key = ?`, dedupKey).Scan(&workItemID); err != nil {
		return domain.AgentRun{}, errs.NewInternalError(errs.SubtypeStorage, "locate work item for agent run").WithCause(err)
	}
	runID, err := randomID()
	if err != nil {
		return domain.AgentRun{}, err
	}
	now := time.Now().UTC()
	run := domain.AgentRun{
		ID:                runID,
		WorkItemID:        workItemID,
		DedupKey:          dedupKey,
		Status:            domain.AgentRunRunning,
		ModelFingerprint:  modelFingerprint,
		ConfigFingerprint: configFingerprint,
		StartedAt:         now,
	}
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO agent_runs(id, work_item_id, dedup_key, status, model_fingerprint, config_fingerprint, started_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		run.ID, run.WorkItemID, run.DedupKey, run.Status, run.ModelFingerprint, run.ConfigFingerprint, now.Format(time.RFC3339Nano))
	if err != nil {
		return domain.AgentRun{}, errs.NewInternalError(errs.SubtypeStorage, "start agent run").WithCause(err)
	}
	return run, nil
}

// AppendAgentStep persists one model response or tool result.
func (s *Store) AppendAgentStep(ctx context.Context, step domain.AgentStep) error {
	if step.CreatedAt.IsZero() {
		step.CreatedAt = time.Now().UTC()
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return errs.NewInternalError(errs.SubtypeStorage, "begin agent step transaction").WithCause(err)
	}
	defer tx.Rollback() //nolint:errcheck // commit path below closes the transaction
	_, err = tx.ExecContext(ctx,
		`INSERT INTO agent_steps(
			run_id, sequence, kind, tool_call_id, tool_name, input_json, output_json,
			request_id, prompt_tokens, completion_tokens, error, created_at
		 ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		step.RunID, step.Sequence, step.Kind, step.ToolCallID, step.ToolName,
		step.InputJSON, step.OutputJSON, step.RequestID, step.PromptTokens,
		step.CompletionTokens, step.Error, step.CreatedAt.Format(time.RFC3339Nano))
	if err != nil {
		return errs.NewInternalError(errs.SubtypeStorage, "append agent step").WithCause(err)
	}
	if step.Kind == "tool" {
		status := "completed"
		if step.Error != "" {
			status = "failed"
		}
		_, err = tx.ExecContext(ctx,
			`INSERT INTO tool_calls(run_id, call_id, tool_name, arguments_json, result_json, status, error, created_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?)
			 ON CONFLICT(run_id, call_id) DO UPDATE SET
				result_json = excluded.result_json, status = excluded.status, error = excluded.error`,
			step.RunID, step.ToolCallID, step.ToolName, step.InputJSON, step.OutputJSON,
			status, step.Error, step.CreatedAt.Format(time.RFC3339Nano))
		if err != nil {
			return errs.NewInternalError(errs.SubtypeStorage, "record agent tool call").WithCause(err)
		}
	}
	if err := tx.Commit(); err != nil {
		return errs.NewInternalError(errs.SubtypeStorage, "commit agent step").WithCause(err)
	}
	return nil
}

// FinishAgentRun marks a run completed or failed.
func (s *Store) FinishAgentRun(ctx context.Context, runID string, status domain.AgentRunStatus, runErr string) error {
	if status != domain.AgentRunCompleted && status != domain.AgentRunFailed && status != domain.AgentRunAbandoned {
		return errs.NewValidationError(errs.SubtypeInvalidArgument, "invalid terminal agent run status: %s", status)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return errs.NewInternalError(errs.SubtypeStorage, "begin finish agent run").WithCause(err)
	}
	defer tx.Rollback() //nolint:errcheck
	var workItemID int64
	var modelTurns int
	if err := tx.QueryRowContext(ctx,
		`SELECT ar.work_item_id,
		        (SELECT COUNT(*) FROM agent_steps ast WHERE ast.run_id = ar.id AND ast.kind = 'model')
		 FROM agent_runs ar WHERE ar.id = ? AND ar.status = ?`,
		runID, domain.AgentRunRunning).Scan(&workItemID, &modelTurns); err != nil {
		return errs.NewInternalError(errs.SubtypeStorage, "read running agent run budget").WithCause(err)
	}
	result, err := tx.ExecContext(ctx,
		`UPDATE agent_runs SET status = ?, last_error = ?, completed_at = ? WHERE id = ? AND status = ?`,
		status, runErr, time.Now().UTC().Format(time.RFC3339Nano), runID, domain.AgentRunRunning)
	if err != nil {
		return errs.NewInternalError(errs.SubtypeStorage, "finish agent run").WithCause(err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return errs.NewInternalError(errs.SubtypeStorage, "read finished agent run result").WithCause(err)
	}
	if affected != 1 {
		return errs.NewInternalError(errs.SubtypeStorage, "agent run is not running: %s", runID)
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE coding_goals
		 SET used_investigation_turns = used_investigation_turns + ?, updated_at = ?
		 WHERE work_item_id = ?`,
		modelTurns, time.Now().UTC().Format(time.RFC3339Nano), workItemID); err != nil {
		return errs.NewInternalError(errs.SubtypeStorage, "accumulate coding goal turns").WithCause(err)
	}
	if err := tx.Commit(); err != nil {
		return errs.NewInternalError(errs.SubtypeStorage, "commit finished agent run").WithCause(err)
	}
	return nil
}

// RequeueAbandonedRuns recovers work whose process stopped mid-loop.
func (s *Store) RequeueAbandonedRuns(maxAge time.Duration) (int64, error) {
	if err := s.RequeueExpiredLeases(maxAge); err != nil {
		return 0, err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return 0, errs.NewInternalError(errs.SubtypeStorage, "begin abandoned run recovery").WithCause(err)
	}
	defer tx.Rollback() //nolint:errcheck
	rows, err := tx.Query(
		`SELECT ar.id, ar.work_item_id
		 FROM agent_runs ar
		 JOIN work_items wi ON wi.id = ar.work_item_id
		 WHERE ar.status = ? AND wi.status <> ?`,
		domain.AgentRunRunning, domain.StatusProcessing)
	if err != nil {
		return 0, errs.NewInternalError(errs.SubtypeStorage, "list abandoned agent runs").WithCause(err)
	}
	var runIDs []string
	var workItemIDs []int64
	for rows.Next() {
		var runID string
		var workItemID int64
		if err := rows.Scan(&runID, &workItemID); err != nil {
			_ = rows.Close()
			return 0, errs.NewInternalError(errs.SubtypeStorage, "scan abandoned agent run").WithCause(err)
		}
		runIDs = append(runIDs, runID)
		workItemIDs = append(workItemIDs, workItemID)
	}
	if err := rows.Close(); err != nil {
		return 0, errs.NewInternalError(errs.SubtypeStorage, "close abandoned run rows").WithCause(err)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	for i, runID := range runIDs {
		var modelTurns int
		if err := tx.QueryRow(
			`SELECT COUNT(*) FROM agent_steps WHERE run_id = ? AND kind = 'model'`,
			runID,
		).Scan(&modelTurns); err != nil {
			return 0, errs.NewInternalError(errs.SubtypeStorage, "count abandoned run turns").WithCause(err)
		}
		if _, err := tx.Exec(
			`UPDATE coding_goals
			 SET used_investigation_turns = used_investigation_turns + ?, updated_at = ?
			 WHERE work_item_id = ?`,
			modelTurns, now, workItemIDs[i]); err != nil {
			return 0, errs.NewInternalError(errs.SubtypeStorage, "accumulate abandoned goal turns").WithCause(err)
		}
		if _, err := tx.Exec(
			`UPDATE agent_runs SET status = ?, last_error = ?, completed_at = ? WHERE id = ?`,
			domain.AgentRunAbandoned, "process stopped before terminal decision", now, runID); err != nil {
			return 0, errs.NewInternalError(errs.SubtypeStorage, "abandon interrupted agent run").WithCause(err)
		}
		if _, err := tx.Exec(
			`UPDATE action_attempts SET status = ?, error = ?, updated_at = ?
			 WHERE work_item_id = ? AND status = ?`,
			domain.ActionBlocked, "execution result is uncertain after process interruption", now,
			workItemIDs[i], domain.ActionExecuting); err != nil {
			return 0, errs.NewInternalError(errs.SubtypeStorage, "mark interrupted actions uncertain").WithCause(err)
		}
		if _, err := tx.Exec(
			`UPDATE work_items SET status = ?, lease_by = NULL, lease_time = NULL, updated_at = ? WHERE id = ? AND status = ?`,
			domain.StatusReceived, now, workItemIDs[i], domain.StatusProcessing); err != nil {
			return 0, errs.NewInternalError(errs.SubtypeStorage, "requeue interrupted work item").WithCause(err)
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, errs.NewInternalError(errs.SubtypeStorage, "commit abandoned run recovery").WithCause(err)
	}
	return int64(len(runIDs)), nil
}

// RequeueChangedRuntimeFailures immediately re-evaluates failed work after a
// model or agent configuration upgrade.
func (s *Store) RequeueChangedRuntimeFailures(modelFingerprint, configFingerprint string) (int64, error) {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	result, err := s.db.Exec(
		`UPDATE work_items
		 SET status = ?, decision_json = NULL, lease_by = NULL, lease_time = NULL,
		     retry_count = 0, next_attempt_at = NULL, updated_at = ?
		 WHERE status IN (?, ?)
		   AND EXISTS (
			SELECT 1 FROM agent_runs r
			WHERE r.work_item_id = work_items.id
			  AND r.status IN (?, ?)
			  AND r.id = (
				SELECT r2.id FROM agent_runs r2
				WHERE r2.work_item_id = work_items.id
				ORDER BY r2.started_at DESC, r2.id DESC LIMIT 1
			  )
			  AND (COALESCE(r.model_fingerprint, '') <> ? OR COALESCE(r.config_fingerprint, '') <> ?)
		   )`,
		domain.StatusReceived, now, domain.StatusRetryWait, domain.StatusDeadLetter,
		domain.AgentRunFailed, domain.AgentRunAbandoned, modelFingerprint, configFingerprint)
	if err != nil {
		return 0, errs.NewInternalError(errs.SubtypeStorage, "requeue failures after runtime upgrade").WithCause(err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return 0, errs.NewInternalError(errs.SubtypeStorage, "read runtime upgrade requeue result").WithCause(err)
	}
	if changed > 0 {
		if _, err := s.db.Exec(
			`DELETE FROM dead_letters WHERE work_item_id IN (
				SELECT id FROM work_items WHERE status = ? AND updated_at = ?
			)`, domain.StatusReceived, now); err != nil {
			return 0, errs.NewInternalError(errs.SubtypeStorage, "clear upgraded dead letters").WithCause(err)
		}
	}
	return changed, nil
}

// RequeueChangedRuntimeDirectMentions re-evaluates direct owner mentions that
// an older operating contract ignored or only notified without sending a reply.
func (s *Store) RequeueChangedRuntimeDirectMentions(ownerOpenID, modelFingerprint, configFingerprint string) (int64, error) {
	rows, err := s.db.Query(
		`SELECT w.id, w.status, w.event_json, COALESCE(w.decision_json, '')
		 FROM work_items w
		 JOIN agent_runs r ON r.id = (
			SELECT latest.id FROM agent_runs latest
			WHERE latest.work_item_id = w.id
			ORDER BY latest.started_at DESC, latest.id DESC LIMIT 1
		 )
		 WHERE w.status IN (?, ?)
		   AND r.status = ?
		   AND (COALESCE(r.model_fingerprint, '') <> ? OR COALESCE(r.config_fingerprint, '') <> ?)
		   AND NOT EXISTS (
			SELECT 1 FROM action_attempts a
			WHERE a.work_item_id = w.id AND a.kind = 'reply' AND a.status = ?
		   )`,
		domain.StatusIgnored, domain.StatusCompleted, domain.AgentRunCompleted,
		modelFingerprint, configFingerprint, domain.ActionCompleted)
	if err != nil {
		return 0, errs.NewInternalError(errs.SubtypeStorage, "list outdated direct mentions").WithCause(err)
	}
	var ids []int64
	for rows.Next() {
		var id int64
		var status domain.WorkItemStatus
		var eventJSON string
		var decisionJSON string
		if err := rows.Scan(&id, &status, &eventJSON, &decisionJSON); err != nil {
			_ = rows.Close()
			return 0, errs.NewInternalError(errs.SubtypeStorage, "scan outdated direct mention").WithCause(err)
		}
		var event domain.NormalizedEvent
		if err := json.Unmarshal([]byte(eventJSON), &event); err != nil {
			_ = rows.Close()
			return 0, errs.NewInternalError(errs.SubtypeStorage, "decode outdated direct mention").WithCause(err)
		}
		if !event.MentionsUser(ownerOpenID) {
			continue
		}
		if status == domain.StatusCompleted {
			var decision domain.Decision
			if err := json.Unmarshal([]byte(decisionJSON), &decision); err != nil {
				_ = rows.Close()
				return 0, errs.NewInternalError(errs.SubtypeStorage, "decode outdated direct mention decision").WithCause(err)
			}
			if decision.Kind != domain.DecisionNotify {
				continue
			}
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return 0, errs.NewInternalError(errs.SubtypeStorage, "iterate outdated direct mentions").WithCause(err)
	}
	if err := rows.Close(); err != nil {
		return 0, errs.NewInternalError(errs.SubtypeStorage, "close outdated direct mention rows").WithCause(err)
	}
	if len(ids) == 0 {
		return 0, nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return 0, errs.NewInternalError(errs.SubtypeStorage, "begin direct mention recovery").WithCause(err)
	}
	defer tx.Rollback() //nolint:errcheck
	now := time.Now().UTC().Format(time.RFC3339Nano)
	var changed int64
	for _, id := range ids {
		result, err := tx.Exec(
			`UPDATE work_items SET status = ?, decision_json = NULL, lease_by = NULL,
			        lease_time = NULL, retry_count = 0, next_attempt_at = NULL, updated_at = ?
			 WHERE id = ? AND status IN (?, ?)`,
			domain.StatusReceived, now, id, domain.StatusIgnored, domain.StatusCompleted)
		if err != nil {
			return 0, errs.NewInternalError(errs.SubtypeStorage, "requeue outdated direct mention").WithCause(err)
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return 0, errs.NewInternalError(errs.SubtypeStorage, "read direct mention recovery result").WithCause(err)
		}
		changed += affected
	}
	if err := tx.Commit(); err != nil {
		return 0, errs.NewInternalError(errs.SubtypeStorage, "commit direct mention recovery").WithCause(err)
	}
	return changed, nil
}

// RequeueLowRiskDirectMentionApprovals replays low-risk direct mentions that
// older reply policy incorrectly held for approval because the generic
// confidence floor was too high for acknowledgement replies.
func (s *Store) RequeueLowRiskDirectMentionApprovals(ownerOpenID string) (int64, error) {
	rows, err := s.db.Query(
		`SELECT w.id, w.event_json, COALESCE(w.decision_json, ''), a.status, COALESCE(a.error, '')
		 FROM work_items w
		 JOIN action_attempts a ON a.work_item_id = w.id
		 WHERE a.kind = 'reply'
		   AND a.status IN (?, ?)
		   AND NOT EXISTS (
			SELECT 1 FROM action_attempts completed
			WHERE completed.work_item_id = w.id
			  AND completed.kind = 'reply'
			  AND completed.status = ?
		   )`,
		domain.ActionAwaitingApproval, domain.ActionCancelled, domain.ActionCompleted)
	if err != nil {
		return 0, errs.NewInternalError(errs.SubtypeStorage, "list low-risk direct mention approvals").WithCause(err)
	}
	var ids []int64
	for rows.Next() {
		var id int64
		var eventJSON string
		var decisionJSON string
		var actionStatus string
		var actionError string
		if err := rows.Scan(&id, &eventJSON, &decisionJSON, &actionStatus, &actionError); err != nil {
			_ = rows.Close()
			return 0, errs.NewInternalError(errs.SubtypeStorage, "scan low-risk direct mention approval").WithCause(err)
		}
		var event domain.NormalizedEvent
		if err := json.Unmarshal([]byte(eventJSON), &event); err != nil {
			_ = rows.Close()
			return 0, errs.NewInternalError(errs.SubtypeStorage, "decode low-risk direct mention approval event").WithCause(err)
		}
		if !event.MentionsUser(ownerOpenID) {
			continue
		}
		switch actionStatus {
		case string(domain.ActionAwaitingApproval):
			var decision domain.Decision
			if err := json.Unmarshal([]byte(decisionJSON), &decision); err != nil {
				_ = rows.Close()
				return 0, errs.NewInternalError(errs.SubtypeStorage, "decode low-risk direct mention approval decision").WithCause(err)
			}
			if decision.Risk != domain.RiskLow || strings.TrimSpace(decision.ReplyText) == "" {
				continue
			}
		case string(domain.ActionCancelled):
			if actionError != lowRiskDirectMentionApprovalRecoveryError {
				continue
			}
		default:
			continue
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return 0, errs.NewInternalError(errs.SubtypeStorage, "iterate low-risk direct mention approvals").WithCause(err)
	}
	if err := rows.Close(); err != nil {
		return 0, errs.NewInternalError(errs.SubtypeStorage, "close low-risk direct mention approval rows").WithCause(err)
	}
	if len(ids) == 0 {
		return 0, nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return 0, errs.NewInternalError(errs.SubtypeStorage, "begin low-risk direct mention approval recovery").WithCause(err)
	}
	defer tx.Rollback() //nolint:errcheck
	now := time.Now().UTC().Format(time.RFC3339Nano)
	var changed int64
	for _, id := range ids {
		result, err := tx.Exec(
			`UPDATE action_attempts
			 SET status = ?, error = NULL, updated_at = ?
			 WHERE work_item_id = ?
			   AND kind = 'reply'
			   AND status IN (?, ?)
			   AND NOT EXISTS (
				SELECT 1 FROM action_attempts completed
				WHERE completed.work_item_id = action_attempts.work_item_id
				  AND completed.kind = 'reply'
				  AND completed.status = ?
			   )`,
			domain.ActionReady, now, id, domain.ActionAwaitingApproval, domain.ActionCancelled, domain.ActionCompleted)
		if err != nil {
			return 0, errs.NewInternalError(errs.SubtypeStorage, "ready low-risk direct mention approval").WithCause(err)
		}
		actionAffected, err := result.RowsAffected()
		if err != nil {
			return 0, errs.NewInternalError(errs.SubtypeStorage, "read low-risk direct mention approval ready result").WithCause(err)
		}
		if actionAffected == 0 {
			continue
		}
		result, err = tx.Exec(
			`UPDATE work_items SET status = ?, decision_json = NULL, lease_by = NULL,
			        lease_time = NULL, retry_count = 0, next_attempt_at = NULL, updated_at = ?
			 WHERE id = ?`,
			domain.StatusReceived, now, id)
		if err != nil {
			return 0, errs.NewInternalError(errs.SubtypeStorage, "requeue low-risk direct mention approval").WithCause(err)
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return 0, errs.NewInternalError(errs.SubtypeStorage, "read low-risk direct mention approval recovery result").WithCause(err)
		}
		changed += affected
	}
	if err := tx.Commit(); err != nil {
		return 0, errs.NewInternalError(errs.SubtypeStorage, "commit low-risk direct mention approval recovery").WithCause(err)
	}
	return changed, nil
}

const lowRiskDirectMentionApprovalRecoveryError = "requeued after low-risk direct mention reply policy upgrade"

// RequeueLegacyCompletedMentions recovers direct mentions that an older runtime
// marked completed without creating a model run or audited external action.
func (s *Store) RequeueLegacyCompletedMentions(ownerOpenID string) (int64, error) {
	rows, err := s.db.Query(
		`SELECT w.id, w.event_json
		 FROM work_items w
		 WHERE w.status = ?
		   AND NOT EXISTS (SELECT 1 FROM agent_runs r WHERE r.work_item_id = w.id)
		   AND NOT EXISTS (
			SELECT 1 FROM action_attempts a
			WHERE a.work_item_id = w.id AND a.status = ?
		   )`,
		domain.StatusCompleted, domain.ActionCompleted)
	if err != nil {
		return 0, errs.NewInternalError(errs.SubtypeStorage, "list legacy completed mentions").WithCause(err)
	}
	var ids []int64
	for rows.Next() {
		var id int64
		var eventJSON string
		if err := rows.Scan(&id, &eventJSON); err != nil {
			_ = rows.Close()
			return 0, errs.NewInternalError(errs.SubtypeStorage, "scan legacy completed mention").WithCause(err)
		}
		var event domain.NormalizedEvent
		if err := json.Unmarshal([]byte(eventJSON), &event); err != nil {
			_ = rows.Close()
			return 0, errs.NewInternalError(errs.SubtypeStorage, "decode legacy completed mention").WithCause(err)
		}
		if event.MentionsUser(ownerOpenID) {
			ids = append(ids, id)
		}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return 0, errs.NewInternalError(errs.SubtypeStorage, "iterate legacy completed mentions").WithCause(err)
	}
	if err := rows.Close(); err != nil {
		return 0, errs.NewInternalError(errs.SubtypeStorage, "close legacy completed mention rows").WithCause(err)
	}
	if len(ids) == 0 {
		return 0, nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return 0, errs.NewInternalError(errs.SubtypeStorage, "begin legacy mention recovery").WithCause(err)
	}
	defer tx.Rollback() //nolint:errcheck
	now := time.Now().UTC().Format(time.RFC3339Nano)
	var changed int64
	for _, id := range ids {
		result, err := tx.Exec(
			`UPDATE work_items SET status = ?, decision_json = NULL, lease_by = NULL,
			        lease_time = NULL, retry_count = 0, next_attempt_at = NULL, updated_at = ?
			 WHERE id = ? AND status = ?`,
			domain.StatusReceived, now, id, domain.StatusCompleted)
		if err != nil {
			return 0, errs.NewInternalError(errs.SubtypeStorage, "requeue legacy completed mention").WithCause(err)
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return 0, errs.NewInternalError(errs.SubtypeStorage, "read legacy mention recovery result").WithCause(err)
		}
		changed += affected
	}
	if err := tx.Commit(); err != nil {
		return 0, errs.NewInternalError(errs.SubtypeStorage, "commit legacy mention recovery").WithCause(err)
	}
	return changed, nil
}

// ListAgentRuns returns durable runs for diagnostics and tests.
func (s *Store) ListAgentRuns() ([]domain.AgentRun, error) {
	rows, err := s.db.Query(
		`SELECT id, work_item_id, dedup_key, status, COALESCE(model_fingerprint, ''),
		        COALESCE(config_fingerprint, ''), COALESCE(last_error, ''), started_at, completed_at
		 FROM agent_runs ORDER BY started_at, id`)
	if err != nil {
		return nil, errs.NewInternalError(errs.SubtypeStorage, "list agent runs").WithCause(err)
	}
	defer func() {
		_ = rows.Close()
	}()
	var runs []domain.AgentRun
	for rows.Next() {
		var run domain.AgentRun
		var status string
		var started string
		var completed sql.NullString
		if err := rows.Scan(&run.ID, &run.WorkItemID, &run.DedupKey, &status, &run.ModelFingerprint,
			&run.ConfigFingerprint, &run.LastError, &started, &completed); err != nil {
			return nil, errs.NewInternalError(errs.SubtypeStorage, "scan agent run").WithCause(err)
		}
		run.Status = domain.AgentRunStatus(status)
		run.StartedAt, _ = time.Parse(time.RFC3339Nano, started)
		if completed.Valid {
			run.CompletedAt, _ = time.Parse(time.RFC3339Nano, completed.String)
		}
		runs = append(runs, run)
	}
	return runs, rows.Err()
}

// ListAgentSteps returns one run's durable trajectory.
func (s *Store) ListAgentSteps(runID string) ([]domain.AgentStep, error) {
	rows, err := s.db.Query(
		`SELECT run_id, sequence, kind, tool_call_id, tool_name, input_json, output_json,
		        request_id, prompt_tokens, completion_tokens, error, created_at
		 FROM agent_steps WHERE run_id = ? ORDER BY sequence`, runID)
	if err != nil {
		return nil, errs.NewInternalError(errs.SubtypeStorage, "list agent steps").WithCause(err)
	}
	defer func() {
		_ = rows.Close()
	}()
	var steps []domain.AgentStep
	for rows.Next() {
		var step domain.AgentStep
		var created string
		if err := rows.Scan(&step.RunID, &step.Sequence, &step.Kind, &step.ToolCallID, &step.ToolName,
			&step.InputJSON, &step.OutputJSON, &step.RequestID, &step.PromptTokens,
			&step.CompletionTokens, &step.Error, &created); err != nil {
			return nil, errs.NewInternalError(errs.SubtypeStorage, "scan agent step").WithCause(err)
		}
		step.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
		steps = append(steps, step)
	}
	return steps, rows.Err()
}

// ExportAgentRunTranscript returns a JSONL replay transcript for one agent run.
func (s *Store) ExportAgentRunTranscript(runID string) (string, error) {
	run, err := s.getAgentRun(runID)
	if err != nil {
		return "", err
	}
	steps, err := s.ListAgentSteps(runID)
	if err != nil {
		return "", err
	}
	var out strings.Builder
	if err := writeTranscriptLine(&out, "run", run); err != nil {
		return "", err
	}
	for _, step := range steps {
		if err := writeTranscriptLine(&out, "step", step); err != nil {
			return "", err
		}
	}
	return out.String(), nil
}

func (s *Store) getAgentRun(runID string) (domain.AgentRun, error) {
	row := s.db.QueryRow(
		`SELECT id, work_item_id, dedup_key, status, COALESCE(model_fingerprint, ''),
		        COALESCE(config_fingerprint, ''), COALESCE(last_error, ''), started_at, completed_at
		 FROM agent_runs WHERE id = ?`, runID)
	var run domain.AgentRun
	var status string
	var started string
	var completed sql.NullString
	if err := row.Scan(&run.ID, &run.WorkItemID, &run.DedupKey, &status, &run.ModelFingerprint,
		&run.ConfigFingerprint, &run.LastError, &started, &completed); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return domain.AgentRun{}, errs.NewValidationError(errs.SubtypeInvalidArgument, "agent run not found: %s", runID)
		}
		return domain.AgentRun{}, errs.NewInternalError(errs.SubtypeStorage, "read agent run").WithCause(err)
	}
	run.Status = domain.AgentRunStatus(status)
	run.StartedAt, _ = time.Parse(time.RFC3339Nano, started)
	if completed.Valid {
		run.CompletedAt, _ = time.Parse(time.RFC3339Nano, completed.String)
	}
	return run, nil
}

func writeTranscriptLine(out *strings.Builder, kind string, value any) error {
	data, err := json.Marshal(map[string]any{
		"kind": kind,
		"data": value,
	})
	if err != nil {
		return errs.NewInternalError(errs.SubtypeUnknown, "encode transcript line").WithCause(err)
	}
	out.Write(data)
	out.WriteByte('\n')
	return nil
}

func randomID() (string, error) {
	data := make([]byte, 16)
	if _, err := rand.Read(data); err != nil {
		return "", errs.NewInternalError(errs.SubtypeUnknown, "generate agent run id").WithCause(err)
	}
	return fmt.Sprintf("%x", data), nil
}

// RequestShellApproval creates or returns an awaiting exact-command action.
func (s *Store) RequestShellApproval(ctx context.Context, dedupKey, command, cwd string) (int64, error) {
	var workItemID int64
	if err := s.db.QueryRowContext(ctx, `SELECT id FROM work_items WHERE dedup_key = ?`, dedupKey).Scan(&workItemID); err != nil {
		return 0, errs.NewInternalError(errs.SubtypeStorage, "locate shell approval work item").WithCause(err)
	}
	request, err := json.Marshal(map[string]string{"command": command, "cwd": cwd})
	if err != nil {
		return 0, errs.NewInternalError(errs.SubtypeUnknown, "encode shell approval request").WithCause(err)
	}
	key := shellActionKey(dedupKey, command, cwd)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := s.db.ExecContext(ctx,
		`INSERT OR IGNORE INTO action_attempts(
			work_item_id, kind, idempotency_key, status, request_json, created_at, updated_at
		 ) VALUES (?, 'shell', ?, ?, ?, ?, ?)`,
		workItemID, key, domain.ActionAwaitingApproval, string(request), now, now); err != nil {
		return 0, errs.NewInternalError(errs.SubtypeStorage, "request shell approval").WithCause(err)
	}
	var actionID int64
	if err := s.db.QueryRowContext(ctx,
		`SELECT id FROM action_attempts WHERE idempotency_key = ?`, key).Scan(&actionID); err != nil {
		return 0, errs.NewInternalError(errs.SubtypeStorage, "read shell approval request").WithCause(err)
	}
	return actionID, nil
}

// ConsumeShellApproval atomically consumes an approved exact-command action.
func (s *Store) ConsumeShellApproval(ctx context.Context, dedupKey, command, cwd string) (int64, bool, error) {
	key := shellActionKey(dedupKey, command, cwd)
	var actionID int64
	err := s.db.QueryRowContext(ctx,
		`SELECT id FROM action_attempts WHERE idempotency_key = ? AND status = ?`,
		key, domain.ActionReady).Scan(&actionID)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, errs.NewInternalError(errs.SubtypeStorage, "read approved shell action").WithCause(err)
	}
	result, err := s.db.ExecContext(ctx,
		`UPDATE action_attempts SET status = ?, updated_at = ? WHERE id = ? AND status = ?`,
		domain.ActionExecuting, time.Now().UTC().Format(time.RFC3339Nano), actionID, domain.ActionReady)
	if err != nil {
		return 0, false, errs.NewInternalError(errs.SubtypeStorage, "consume approved shell action").WithCause(err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return 0, false, errs.NewInternalError(errs.SubtypeStorage, "read consumed shell action result").WithCause(err)
	}
	return actionID, affected == 1, nil
}

// BeginShellAction starts an audited exact command, reuses a completed result,
// and blocks a command whose prior execution ended without a result.
func (s *Store) BeginShellAction(ctx context.Context, dedupKey, command, cwd string) (int64, string, bool, error) {
	var workItemID int64
	if err := s.db.QueryRowContext(ctx, `SELECT id FROM work_items WHERE dedup_key = ?`, dedupKey).Scan(&workItemID); err != nil {
		return 0, "", false, errs.NewInternalError(errs.SubtypeStorage, "locate shell action work item").WithCause(err)
	}
	key := shellActionKey(dedupKey, command, cwd)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, "", false, errs.NewInternalError(errs.SubtypeStorage, "begin shell action audit").WithCause(err)
	}
	defer tx.Rollback() //nolint:errcheck
	var actionID int64
	var status string
	var response string
	err = tx.QueryRowContext(ctx,
		`SELECT id, status, COALESCE(response_json, '') FROM action_attempts WHERE idempotency_key = ?`,
		key).Scan(&actionID, &status, &response)
	if errors.Is(err, sql.ErrNoRows) {
		request, marshalErr := json.Marshal(map[string]string{"command": command, "cwd": cwd})
		if marshalErr != nil {
			return 0, "", false, errs.NewInternalError(errs.SubtypeUnknown, "encode shell action request").WithCause(marshalErr)
		}
		now := time.Now().UTC().Format(time.RFC3339Nano)
		result, insertErr := tx.ExecContext(ctx,
			`INSERT INTO action_attempts(
				work_item_id, kind, idempotency_key, status, request_json, created_at, updated_at
			 ) VALUES (?, 'shell', ?, ?, ?, ?, ?)`,
			workItemID, key, domain.ActionExecuting, string(request), now, now)
		if insertErr != nil {
			return 0, "", false, errs.NewInternalError(errs.SubtypeStorage, "start shell action audit").WithCause(insertErr)
		}
		actionID, err = result.LastInsertId()
		if err != nil {
			return 0, "", false, errs.NewInternalError(errs.SubtypeStorage, "read shell action id").WithCause(err)
		}
		if err := tx.Commit(); err != nil {
			return 0, "", false, errs.NewInternalError(errs.SubtypeStorage, "commit shell action audit").WithCause(err)
		}
		return actionID, "", false, nil
	}
	if err != nil {
		return 0, "", false, errs.NewInternalError(errs.SubtypeStorage, "read shell action audit").WithCause(err)
	}
	if err := tx.Commit(); err != nil {
		return 0, "", false, errs.NewInternalError(errs.SubtypeStorage, "commit existing shell action audit").WithCause(err)
	}
	switch domain.ActionStatus(status) {
	case domain.ActionCompleted:
		return actionID, response, false, nil
	case domain.ActionExecuting, domain.ActionBlocked:
		return actionID, "", true, nil
	default:
		return actionID, "", true, nil
	}
}

// CompleteShellApproval records the exact approved command result.
func (s *Store) CompleteShellApproval(ctx context.Context, actionID int64, responseJSON, errorText string) error {
	status := domain.ActionCompleted
	if errorText != "" {
		status = domain.ActionBlocked
	}
	result, err := s.db.ExecContext(ctx,
		`UPDATE action_attempts SET status = ?, response_json = ?, error = ?, updated_at = ?
		 WHERE id = ? AND status = ?`,
		status, responseJSON, errorText, time.Now().UTC().Format(time.RFC3339Nano), actionID, domain.ActionExecuting)
	if err != nil {
		return errs.NewInternalError(errs.SubtypeStorage, "complete approved shell action").WithCause(err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return errs.NewInternalError(errs.SubtypeStorage, "read completed shell action result").WithCause(err)
	}
	if affected != 1 {
		return errs.NewValidationError(errs.SubtypeFailedPrecondition, "action %d is not executing", actionID)
	}
	return nil
}

// BeginReplyAction starts or resumes one idempotent external reply.
func (s *Store) BeginReplyAction(
	ctx context.Context,
	dedupKey string,
	text string,
) (int64, string, string, bool, error) {
	var workItemID int64
	if err := s.db.QueryRowContext(
		ctx, `SELECT id FROM work_items WHERE dedup_key = ?`, dedupKey,
	).Scan(&workItemID); err != nil {
		return 0, "", "", false, errs.NewInternalError(errs.SubtypeStorage, "locate reply action work item").WithCause(err)
	}
	key := dedupKey + ":reply"
	requestJSON, err := json.Marshal(map[string]string{"text": text})
	if err != nil {
		return 0, "", "", false, errs.NewInternalError(errs.SubtypeUnknown, "encode reply action request").WithCause(err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, "", "", false, errs.NewInternalError(errs.SubtypeStorage, "begin reply action audit").WithCause(err)
	}
	defer tx.Rollback() //nolint:errcheck
	var actionID int64
	var status domain.ActionStatus
	var responseJSON string
	err = tx.QueryRowContext(ctx,
		`SELECT id, status, COALESCE(response_json, '') FROM action_attempts WHERE idempotency_key = ?`,
		key,
	).Scan(&actionID, &status, &responseJSON)
	if errors.Is(err, sql.ErrNoRows) {
		now := time.Now().UTC().Format(time.RFC3339Nano)
		result, insertErr := tx.ExecContext(ctx,
			`INSERT INTO action_attempts(
				work_item_id, kind, idempotency_key, status, request_json, created_at, updated_at
			 ) VALUES (?, 'reply', ?, ?, ?, ?, ?)`,
			workItemID, key, domain.ActionExecuting, string(requestJSON), now, now)
		if insertErr != nil {
			return 0, "", "", false, errs.NewInternalError(errs.SubtypeStorage, "start reply action audit").WithCause(insertErr)
		}
		actionID, err = result.LastInsertId()
		if err != nil {
			return 0, "", "", false, errs.NewInternalError(errs.SubtypeStorage, "read reply action id").WithCause(err)
		}
		if err := tx.Commit(); err != nil {
			return 0, "", "", false, errs.NewInternalError(errs.SubtypeStorage, "commit reply action audit").WithCause(err)
		}
		return actionID, key, "", false, nil
	}
	if err != nil {
		return 0, "", "", false, errs.NewInternalError(errs.SubtypeStorage, "read reply action audit").WithCause(err)
	}
	if status == domain.ActionCompleted {
		var response map[string]string
		if responseJSON != "" {
			if err := json.Unmarshal([]byte(responseJSON), &response); err != nil {
				return 0, "", "", false, errs.NewInternalError(errs.SubtypeStorage, "decode reply action response").WithCause(err)
			}
		}
		if err := tx.Commit(); err != nil {
			return 0, "", "", false, errs.NewInternalError(errs.SubtypeStorage, "commit completed reply action audit").WithCause(err)
		}
		return actionID, key, response["message_id"], true, nil
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE action_attempts SET status = ?, request_json = ?, error = NULL, updated_at = ? WHERE id = ?`,
		domain.ActionExecuting, string(requestJSON), time.Now().UTC().Format(time.RFC3339Nano), actionID,
	); err != nil {
		return 0, "", "", false, errs.NewInternalError(errs.SubtypeStorage, "resume reply action audit").WithCause(err)
	}
	if err := tx.Commit(); err != nil {
		return 0, "", "", false, errs.NewInternalError(errs.SubtypeStorage, "commit resumed reply action audit").WithCause(err)
	}
	return actionID, key, "", false, nil
}

// CompleteReplyAction records the external reply ID or exact send error.
func (s *Store) CompleteReplyAction(ctx context.Context, actionID int64, messageID, errorText string) error {
	responseJSON, err := json.Marshal(map[string]string{"message_id": messageID})
	if err != nil {
		return errs.NewInternalError(errs.SubtypeUnknown, "encode reply action response").WithCause(err)
	}
	return s.CompleteShellApproval(ctx, actionID, string(responseJSON), errorText)
}

// BeginOwnerActivity starts or resumes the transient working-reaction audit.
func (s *Store) BeginOwnerActivity(
	ctx context.Context,
	dedupKey string,
	messageID string,
) (int64, string, bool, error) {
	var workItemID int64
	if err := s.db.QueryRowContext(
		ctx, `SELECT id FROM work_items WHERE dedup_key = ?`, dedupKey,
	).Scan(&workItemID); err != nil {
		return 0, "", false, errs.NewInternalError(errs.SubtypeStorage, "locate owner activity work item").WithCause(err)
	}
	key := dedupKey + ":typing-reaction"
	requestJSON, err := json.Marshal(map[string]string{"message_id": messageID, "emoji_type": "Typing"})
	if err != nil {
		return 0, "", false, errs.NewInternalError(errs.SubtypeUnknown, "encode owner activity request").WithCause(err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, "", false, errs.NewInternalError(errs.SubtypeStorage, "begin owner activity audit").WithCause(err)
	}
	defer tx.Rollback() //nolint:errcheck
	var actionID int64
	var status domain.ActionStatus
	var responseJSON string
	err = tx.QueryRowContext(ctx,
		`SELECT id, status, COALESCE(response_json, '') FROM action_attempts WHERE idempotency_key = ?`,
		key,
	).Scan(&actionID, &status, &responseJSON)
	if errors.Is(err, sql.ErrNoRows) {
		now := time.Now().UTC().Format(time.RFC3339Nano)
		result, insertErr := tx.ExecContext(ctx,
			`INSERT INTO action_attempts(
				work_item_id, kind, idempotency_key, status, request_json, created_at, updated_at
			 ) VALUES (?, 'owner_activity', ?, ?, ?, ?, ?)`,
			workItemID, key, domain.ActionExecuting, string(requestJSON), now, now)
		if insertErr != nil {
			return 0, "", false, errs.NewInternalError(errs.SubtypeStorage, "start owner activity audit").WithCause(insertErr)
		}
		actionID, err = result.LastInsertId()
		if err != nil {
			return 0, "", false, errs.NewInternalError(errs.SubtypeStorage, "read owner activity id").WithCause(err)
		}
		if err := tx.Commit(); err != nil {
			return 0, "", false, errs.NewInternalError(errs.SubtypeStorage, "commit owner activity audit").WithCause(err)
		}
		return actionID, "", false, nil
	}
	if err != nil {
		return 0, "", false, errs.NewInternalError(errs.SubtypeStorage, "read owner activity audit").WithCause(err)
	}
	var response map[string]string
	if responseJSON != "" {
		if err := json.Unmarshal([]byte(responseJSON), &response); err != nil {
			return 0, "", false, errs.NewInternalError(errs.SubtypeStorage, "decode owner activity response").WithCause(err)
		}
	}
	if status == domain.ActionCompleted {
		if err := tx.Commit(); err != nil {
			return 0, "", false, errs.NewInternalError(errs.SubtypeStorage, "commit completed owner activity audit").WithCause(err)
		}
		return actionID, response["reaction_id"], true, nil
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE action_attempts SET status = ?, error = NULL, updated_at = ? WHERE id = ?`,
		domain.ActionExecuting, time.Now().UTC().Format(time.RFC3339Nano), actionID,
	); err != nil {
		return 0, "", false, errs.NewInternalError(errs.SubtypeStorage, "resume owner activity audit").WithCause(err)
	}
	if err := tx.Commit(); err != nil {
		return 0, "", false, errs.NewInternalError(errs.SubtypeStorage, "commit resumed owner activity audit").WithCause(err)
	}
	return actionID, response["reaction_id"], false, nil
}

// RecordOwnerActivityReaction persists the exact reaction that cleanup must remove.
func (s *Store) RecordOwnerActivityReaction(ctx context.Context, actionID int64, reactionID string) error {
	responseJSON, err := json.Marshal(map[string]string{"reaction_id": reactionID})
	if err != nil {
		return errs.NewInternalError(errs.SubtypeUnknown, "encode owner activity response").WithCause(err)
	}
	result, err := s.db.ExecContext(ctx,
		`UPDATE action_attempts SET response_json = ?, updated_at = ? WHERE id = ? AND status = ?`,
		string(responseJSON), time.Now().UTC().Format(time.RFC3339Nano), actionID, domain.ActionExecuting,
	)
	if err != nil {
		return errs.NewInternalError(errs.SubtypeStorage, "record owner activity reaction").WithCause(err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return errs.NewInternalError(errs.SubtypeStorage, "read owner activity reaction result").WithCause(err)
	}
	if affected != 1 {
		return errs.NewValidationError(errs.SubtypeFailedPrecondition, "owner activity %d is not executing", actionID)
	}
	return nil
}

// CompleteOwnerActivity records successful cleanup or its exact error.
func (s *Store) CompleteOwnerActivity(
	ctx context.Context,
	actionID int64,
	reactionID string,
	errorText string,
) error {
	responseJSON, err := json.Marshal(map[string]string{"reaction_id": reactionID})
	if err != nil {
		return errs.NewInternalError(errs.SubtypeUnknown, "encode owner activity completion").WithCause(err)
	}
	return s.CompleteShellApproval(ctx, actionID, string(responseJSON), errorText)
}

// ClaimOwnerActivityCleanup claims one failed reaction deletion for an
// independent retry that does not reprocess the work item.
func (s *Store) ClaimOwnerActivityCleanup(
	ctx context.Context,
	before time.Time,
) (int64, string, string, bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, "", "", false, errs.NewInternalError(errs.SubtypeStorage, "begin owner activity cleanup claim").WithCause(err)
	}
	defer tx.Rollback() //nolint:errcheck
	var actionID int64
	var requestJSON, responseJSON string
	err = tx.QueryRowContext(ctx,
		`SELECT a.id, COALESCE(a.request_json, ''), COALESCE(a.response_json, '')
		 FROM action_attempts a
		 JOIN work_items w ON w.id = a.work_item_id
		 WHERE a.kind = 'owner_activity'
		   AND a.updated_at <= ?
		   AND (
			a.status = ?
			OR (
				a.status = ?
				AND w.status IN (?, ?, ?, ?, ?)
			)
		   )
		 ORDER BY a.id LIMIT 1`,
		before.UTC().Format(time.RFC3339Nano),
		domain.ActionBlocked,
		domain.ActionExecuting,
		domain.StatusCompleted,
		domain.StatusIgnored,
		domain.StatusAwaitingApproval,
		domain.StatusDeadLetter,
		domain.StatusCancelled,
	).Scan(&actionID, &requestJSON, &responseJSON)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, "", "", false, nil
	}
	if err != nil {
		return 0, "", "", false, errs.NewInternalError(errs.SubtypeStorage, "read owner activity cleanup").WithCause(err)
	}
	var request, response map[string]string
	if err := json.Unmarshal([]byte(requestJSON), &request); err != nil {
		return 0, "", "", false, errs.NewInternalError(errs.SubtypeStorage, "decode owner activity cleanup request").WithCause(err)
	}
	if err := json.Unmarshal([]byte(responseJSON), &response); err != nil {
		return 0, "", "", false, errs.NewInternalError(errs.SubtypeStorage, "decode owner activity cleanup response").WithCause(err)
	}
	if request["message_id"] == "" || response["reaction_id"] == "" {
		return 0, "", "", false, errs.NewInternalError(errs.SubtypeInvalidResponse, "owner activity cleanup is missing message_id or reaction_id")
	}
	result, err := tx.ExecContext(ctx,
		`UPDATE action_attempts SET status = ?, error = NULL, updated_at = ?
		 WHERE id = ? AND status IN (?, ?)`,
		domain.ActionExecuting, time.Now().UTC().Format(time.RFC3339Nano), actionID,
		domain.ActionBlocked, domain.ActionExecuting,
	)
	if err != nil {
		return 0, "", "", false, errs.NewInternalError(errs.SubtypeStorage, "claim owner activity cleanup").WithCause(err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return 0, "", "", false, errs.NewInternalError(errs.SubtypeStorage, "read owner activity cleanup claim result").WithCause(err)
	}
	if affected != 1 {
		return 0, "", "", false, nil
	}
	if err := tx.Commit(); err != nil {
		return 0, "", "", false, errs.NewInternalError(errs.SubtypeStorage, "commit owner activity cleanup claim").WithCause(err)
	}
	return actionID, request["message_id"], response["reaction_id"], true, nil
}

// RequestReplyApproval creates or returns an exact draft-reply approval.
func (s *Store) RequestReplyApproval(
	ctx context.Context,
	dedupKey, text, reason, ownerAction string,
	relevance domain.Relevance,
) (int64, error) {
	var workItemID int64
	if err := s.db.QueryRowContext(ctx, `SELECT id FROM work_items WHERE dedup_key = ?`, dedupKey).Scan(&workItemID); err != nil {
		return 0, errs.NewInternalError(errs.SubtypeStorage, "locate reply approval work item").WithCause(err)
	}
	request, err := json.Marshal(map[string]string{
		"text":         text,
		"reason":       reason,
		"owner_action": ownerAction,
		"relevance":    string(relevance),
	})
	if err != nil {
		return 0, errs.NewInternalError(errs.SubtypeUnknown, "encode reply approval request").WithCause(err)
	}
	key := replyActionKey(dedupKey, text, reason, ownerAction, relevance)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := s.db.ExecContext(ctx,
		`INSERT OR IGNORE INTO action_attempts(
			work_item_id, kind, idempotency_key, status, request_json, created_at, updated_at
		 ) VALUES (?, 'reply', ?, ?, ?, ?, ?)`,
		workItemID, key, domain.ActionAwaitingApproval, string(request), now, now); err != nil {
		return 0, errs.NewInternalError(errs.SubtypeStorage, "request reply approval").WithCause(err)
	}
	var actionID int64
	if err := s.db.QueryRowContext(ctx, `SELECT id FROM action_attempts WHERE idempotency_key = ?`, key).Scan(&actionID); err != nil {
		return 0, errs.NewInternalError(errs.SubtypeStorage, "read reply approval request").WithCause(err)
	}
	return actionID, nil
}

// ConsumeReplyApproval atomically consumes an approved exact draft reply.
func (s *Store) ConsumeReplyApproval(
	ctx context.Context,
	dedupKey, text, reason, ownerAction string,
	relevance domain.Relevance,
) (int64, bool, error) {
	key := replyActionKey(dedupKey, text, reason, ownerAction, relevance)
	var actionID int64
	err := s.db.QueryRowContext(ctx,
		`SELECT id FROM action_attempts WHERE idempotency_key = ? AND status = ?`,
		key, domain.ActionReady).Scan(&actionID)
	if errors.Is(err, sql.ErrNoRows) {
		key = legacyReplyActionKey(dedupKey, text, reason, ownerAction)
		err = s.db.QueryRowContext(ctx,
			`SELECT id FROM action_attempts WHERE idempotency_key = ? AND status = ?`,
			key, domain.ActionReady).Scan(&actionID)
		if errors.Is(err, sql.ErrNoRows) {
			return 0, false, nil
		}
		if err != nil {
			return 0, false, errs.NewInternalError(errs.SubtypeStorage, "read approved reply action").WithCause(err)
		}
		legacyRelevance, found, legacyErr := s.replyApprovalDecisionRelevance(ctx, dedupKey)
		if legacyErr != nil {
			return 0, false, legacyErr
		}
		if !found || legacyRelevance != relevance {
			return 0, false, nil
		}
	}
	if err != nil {
		return 0, false, errs.NewInternalError(errs.SubtypeStorage, "read approved reply action").WithCause(err)
	}
	result, err := s.db.ExecContext(ctx,
		`UPDATE action_attempts SET status = ?, updated_at = ? WHERE id = ? AND status = ?`,
		domain.ActionExecuting, time.Now().UTC().Format(time.RFC3339Nano), actionID, domain.ActionReady)
	if err != nil {
		return 0, false, errs.NewInternalError(errs.SubtypeStorage, "consume approved reply action").WithCause(err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return 0, false, errs.NewInternalError(errs.SubtypeStorage, "read consumed reply action result").WithCause(err)
	}
	return actionID, affected == 1, nil
}

// CompleteReplyApproval records the exact approved reply result.
func (s *Store) CompleteReplyApproval(ctx context.Context, actionID int64, messageID, errorText string) error {
	response, _ := json.Marshal(map[string]string{"message_id": messageID})
	return s.CompleteShellApproval(ctx, actionID, string(response), errorText)
}

// BeginPostReplyNotification starts or resumes the durable owner notification.
// New delegated replies complete this notice before the sender-facing send;
// rows created by older versions may still represent a post-reply notice.
func (s *Store) BeginPostReplyNotification(
	ctx context.Context,
	dedupKey string,
	decision domain.Decision,
) (int64, string, bool, error) {
	var workItemID int64
	if err := s.db.QueryRowContext(ctx, `SELECT id FROM work_items WHERE dedup_key = ?`, dedupKey).Scan(&workItemID); err != nil {
		return 0, "", false, errs.NewInternalError(errs.SubtypeStorage, "locate post-reply notification work item").WithCause(err)
	}
	request, err := json.Marshal(decision)
	if err != nil {
		return 0, "", false, errs.NewInternalError(errs.SubtypeUnknown, "encode post-reply notification").WithCause(err)
	}
	key := postReplyNotificationActionKey(dedupKey, request)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, "", false, errs.NewInternalError(errs.SubtypeStorage, "begin post-reply notification audit").WithCause(err)
	}
	defer tx.Rollback() //nolint:errcheck
	var actionID int64
	var status domain.ActionStatus
	err = tx.QueryRowContext(ctx,
		`SELECT id, status FROM action_attempts WHERE idempotency_key = ?`,
		key).Scan(&actionID, &status)
	if errors.Is(err, sql.ErrNoRows) {
		now := time.Now().UTC().Format(time.RFC3339Nano)
		result, insertErr := tx.ExecContext(ctx,
			`INSERT INTO action_attempts(
				work_item_id, kind, idempotency_key, status, request_json, created_at, updated_at
			 ) VALUES (?, 'owner_notification', ?, ?, ?, ?, ?)`,
			workItemID, key, domain.ActionExecuting, string(request), now, now)
		if insertErr != nil {
			return 0, "", false, errs.NewInternalError(errs.SubtypeStorage, "start post-reply notification audit").WithCause(insertErr)
		}
		actionID, err = result.LastInsertId()
		if err != nil {
			return 0, "", false, errs.NewInternalError(errs.SubtypeStorage, "read post-reply notification id").WithCause(err)
		}
		if err := tx.Commit(); err != nil {
			return 0, "", false, errs.NewInternalError(errs.SubtypeStorage, "commit post-reply notification audit").WithCause(err)
		}
		return actionID, key, false, nil
	}
	if err != nil {
		return 0, "", false, errs.NewInternalError(errs.SubtypeStorage, "read post-reply notification audit").WithCause(err)
	}
	if status == domain.ActionCompleted {
		if err := tx.Commit(); err != nil {
			return 0, "", false, errs.NewInternalError(errs.SubtypeStorage, "commit completed post-reply notification audit").WithCause(err)
		}
		return actionID, key, true, nil
	}
	result, err := tx.ExecContext(ctx,
		`UPDATE action_attempts SET status = ?, error = NULL, updated_at = ? WHERE id = ?`,
		domain.ActionExecuting, time.Now().UTC().Format(time.RFC3339Nano), actionID)
	if err != nil {
		return 0, "", false, errs.NewInternalError(errs.SubtypeStorage, "resume post-reply notification audit").WithCause(err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return 0, "", false, errs.NewInternalError(errs.SubtypeStorage, "read resumed post-reply notification result").WithCause(err)
	}
	if affected != 1 {
		return 0, "", false, errs.NewValidationError(errs.SubtypeFailedPrecondition, "post-reply notification action %d was not found", actionID)
	}
	if err := tx.Commit(); err != nil {
		return 0, "", false, errs.NewInternalError(errs.SubtypeStorage, "commit resumed post-reply notification audit").WithCause(err)
	}
	return actionID, key, false, nil
}

// ReadyPostReplyNotification returns an owner notification that can finish the
// work item without rerunning the model or sender-facing reply. A completed
// reply action is required so a pre-reply notice cannot accidentally skip the
// sender-facing send after restart.
func (s *Store) ReadyPostReplyNotification(workItemID int64) (int64, string, domain.Decision, bool, error) {
	var actionID int64
	var key string
	var requestJSON string
	err := s.db.QueryRow(
		`SELECT id, idempotency_key, COALESCE(request_json, '')
		 FROM action_attempts
		 WHERE work_item_id = ? AND kind = 'owner_notification' AND status IN (?, ?, ?)
		   AND EXISTS(
				SELECT 1 FROM action_attempts reply
				WHERE reply.work_item_id = action_attempts.work_item_id
				  AND reply.kind = 'reply' AND reply.status = ?
		   )
		 ORDER BY id LIMIT 1`,
		workItemID,
		domain.ActionExecuting,
		domain.ActionBlocked,
		domain.ActionCompleted,
		domain.ActionCompleted,
	).Scan(&actionID, &key, &requestJSON)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, "", domain.Decision{}, false, nil
	}
	if err != nil {
		return 0, "", domain.Decision{}, false, errs.NewInternalError(
			errs.SubtypeStorage, "read pending post-reply notification",
		).WithCause(err)
	}
	var decision domain.Decision
	if err := json.Unmarshal([]byte(requestJSON), &decision); err != nil {
		return 0, "", domain.Decision{}, false, errs.NewInternalError(
			errs.SubtypeStorage, "decode pending post-reply notification",
		).WithCause(err)
	}
	return actionID, key, decision, true, nil
}

// CompletePostReplyNotification records a delivered or failed owner notice.
func (s *Store) CompletePostReplyNotification(ctx context.Context, actionID int64, errorText string) error {
	return s.CompleteShellApproval(ctx, actionID, `{}`, errorText)
}

// ListActionAttempts lists approval and action audit records.
func (s *Store) ListActionAttempts() ([]domain.ActionAttempt, error) {
	rows, err := s.db.Query(
		`SELECT id, work_item_id, COALESCE(run_id, ''), kind, idempotency_key, status,
		        COALESCE(request_json, ''), COALESCE(response_json, ''), COALESCE(error, ''),
		        created_at, updated_at
		 FROM action_attempts ORDER BY id`)
	if err != nil {
		return nil, errs.NewInternalError(errs.SubtypeStorage, "list action attempts").WithCause(err)
	}
	defer func() {
		_ = rows.Close()
	}()
	var actions []domain.ActionAttempt
	for rows.Next() {
		action, err := scanActionAttempt(rows)
		if err != nil {
			return nil, err
		}
		actions = append(actions, action)
	}
	return actions, rows.Err()
}

// GetActionAttempt returns one approval or action audit record.
func (s *Store) GetActionAttempt(id int64) (domain.ActionAttempt, error) {
	row := s.db.QueryRow(
		`SELECT id, work_item_id, COALESCE(run_id, ''), kind, idempotency_key, status,
		        COALESCE(request_json, ''), COALESCE(response_json, ''), COALESCE(error, ''),
		        created_at, updated_at
		 FROM action_attempts WHERE id = ?`, id)
	return scanActionAttempt(row)
}

// DecideAction approves or rejects one pending action and requeues its work.
func (s *Store) DecideAction(id int64, approve bool) error {
	tx, err := s.db.Begin()
	if err != nil {
		return errs.NewInternalError(errs.SubtypeStorage, "begin approval decision").WithCause(err)
	}
	defer tx.Rollback() //nolint:errcheck
	actionStatus := domain.ActionReady
	if !approve {
		actionStatus = domain.ActionCancelled
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	var workItemID int64
	if err := tx.QueryRow(
		`UPDATE action_attempts
		 SET status = ?, updated_at = ?
		 WHERE id = ? AND status = ?
		 RETURNING work_item_id`,
		actionStatus, now, id, domain.ActionAwaitingApproval,
	).Scan(&workItemID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return errs.NewValidationError(errs.SubtypeFailedPrecondition, "action %d is not awaiting approval", id)
		}
		return errs.NewInternalError(errs.SubtypeStorage, "update approval decision").WithCause(err)
	}
	if approve {
		var sessionID, sessionStatus string
		sessionErr := tx.QueryRow(
			`SELECT id, status FROM online_sessions
			 WHERE status IN (?, ?)
			 ORDER BY started_at DESC, id DESC LIMIT 1`,
			domain.OnlineSessionStarting,
			domain.OnlineSessionReady,
		).Scan(&sessionID, &sessionStatus)
		switch {
		case sessionErr == nil && domain.OnlineSessionStatus(sessionStatus) == domain.OnlineSessionReady:
			if _, err := tx.Exec(
				`UPDATE work_items
				 SET status = ?, session_id = ?, lease_by = NULL, lease_time = NULL,
				     next_attempt_at = NULL, updated_at = ?
				 WHERE id = ?`,
				domain.StatusReceived, sessionID, now, workItemID,
			); err != nil {
				return errs.NewInternalError(errs.SubtypeStorage, "assign approved work to ready session").WithCause(err)
			}
			if _, err := tx.Exec(
				`UPDATE work_interruptions SET resumed_at = ?
				 WHERE work_item_id = ? AND resumed_at IS NULL`,
				now, workItemID,
			); err != nil {
				return errs.NewInternalError(errs.SubtypeStorage, "close approved work interruption").WithCause(err)
			}
		case errors.Is(sessionErr, sql.ErrNoRows),
			sessionErr == nil && domain.OnlineSessionStatus(sessionStatus) == domain.OnlineSessionStarting:
			if _, err := tx.Exec(
				`UPDATE work_items
				 SET status = ?, lease_by = NULL, lease_time = NULL,
				     next_attempt_at = NULL, updated_at = ?
				 WHERE id = ?`,
				domain.StatusInterrupted, now, workItemID,
			); err != nil {
				return errs.NewInternalError(errs.SubtypeStorage, "pause approved work without ready session").WithCause(err)
			}
		default:
			return errs.NewInternalError(errs.SubtypeStorage, "locate ready session for approval").WithCause(sessionErr)
		}
	} else {
		if _, err := tx.Exec(
			`UPDATE work_items SET status = ?, lease_by = NULL, lease_time = NULL,
			        next_attempt_at = NULL, updated_at = ? WHERE id = ?`,
			domain.StatusCancelled, now, workItemID,
		); err != nil {
			return errs.NewInternalError(errs.SubtypeStorage, "cancel rejected work item").WithCause(err)
		}
	}
	goalStatus := domain.CodingGoalActive
	if !approve {
		goalStatus = domain.CodingGoalBlocked
	}
	if _, err := tx.Exec(
		`UPDATE coding_goals SET status = ?, updated_at = ? WHERE work_item_id = ?`,
		goalStatus, now, workItemID); err != nil {
		return errs.NewInternalError(errs.SubtypeStorage, "update approved coding goal").WithCause(err)
	}
	if err := tx.Commit(); err != nil {
		return errs.NewInternalError(errs.SubtypeStorage, "commit approval decision").WithCause(err)
	}
	return nil
}

// ReadyApprovedReply returns the exact persisted draft that should resume
// without another nondeterministic model call.
func (s *Store) ReadyApprovedReply(workItemID int64) (domain.Decision, bool, error) {
	var requestJSON string
	var decisionJSON string
	err := s.db.QueryRow(
		`SELECT a.request_json, COALESCE(w.decision_json, '')
		 FROM action_attempts a
		 JOIN work_items w ON w.id = a.work_item_id
		 WHERE a.work_item_id = ? AND a.kind = 'reply' AND a.status = ?
		 ORDER BY a.id LIMIT 1`,
		workItemID, domain.ActionReady).Scan(&requestJSON, &decisionJSON)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Decision{}, false, nil
	}
	if err != nil {
		return domain.Decision{}, false, errs.NewInternalError(errs.SubtypeStorage, "read approved reply draft").WithCause(err)
	}
	var request struct {
		Text        string `json:"text"`
		Reason      string `json:"reason"`
		OwnerAction string `json:"owner_action"`
		Relevance   string `json:"relevance"`
	}
	if err := json.Unmarshal([]byte(requestJSON), &request); err != nil {
		return domain.Decision{}, false, errs.NewInternalError(errs.SubtypeStorage, "decode approved reply draft").WithCause(err)
	}
	if strings.TrimSpace(request.Text) == "" {
		return domain.Decision{}, false, errs.NewInternalError(errs.SubtypeStorage, "approved reply draft is empty")
	}
	relevance := domain.Relevance(request.Relevance)
	if relevance == "" {
		var persisted domain.Decision
		if strings.TrimSpace(decisionJSON) == "" {
			return domain.Decision{}, false, errs.NewInternalError(
				errs.SubtypeInvalidResponse,
				"legacy approved reply is missing durable decision relevance",
			)
		}
		if err := json.Unmarshal([]byte(decisionJSON), &persisted); err != nil {
			return domain.Decision{}, false, errs.NewInternalError(
				errs.SubtypeStorage,
				"decode legacy approved reply decision",
			).WithCause(err)
		}
		relevance = persisted.Relevance
	}
	if !validReplyApprovalRelevance(relevance) {
		return domain.Decision{}, false, errs.NewInternalError(
			errs.SubtypeInvalidResponse,
			"approved reply has invalid relevance %q",
			relevance,
		)
	}
	return domain.Decision{
		Kind:        domain.DecisionReply,
		Mode:        domain.ModeApproval,
		Relevance:   relevance,
		Confidence:  1,
		Risk:        domain.RiskLow,
		Reason:      request.Reason,
		ReplyText:   request.Text,
		OwnerAction: request.OwnerAction,
	}, true, nil
}

func shellActionKey(dedupKey, command, cwd string) string {
	sum := sha256.Sum256([]byte(dedupKey + "\x00" + command + "\x00" + cwd))
	return fmt.Sprintf("shell:%x", sum[:])
}

func replyActionKey(
	dedupKey, text, reason, ownerAction string,
	relevance domain.Relevance,
) string {
	sum := sha256.Sum256([]byte(
		dedupKey + "\x00" + text + "\x00" + reason + "\x00" + ownerAction +
			"\x00" + string(relevance),
	))
	return fmt.Sprintf("reply:%x", sum[:])
}

func legacyReplyActionKey(dedupKey, text, reason, ownerAction string) string {
	sum := sha256.Sum256([]byte(
		dedupKey + "\x00" + text + "\x00" + reason + "\x00" + ownerAction,
	))
	return fmt.Sprintf("reply:%x", sum[:])
}

func (s *Store) replyApprovalDecisionRelevance(
	ctx context.Context,
	dedupKey string,
) (domain.Relevance, bool, error) {
	var decisionJSON string
	err := s.db.QueryRowContext(
		ctx,
		`SELECT COALESCE(decision_json, '') FROM work_items WHERE dedup_key = ?`,
		dedupKey,
	).Scan(&decisionJSON)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, errs.NewInternalError(
			errs.SubtypeStorage,
			"read reply approval work decision",
		).WithCause(err)
	}
	if strings.TrimSpace(decisionJSON) == "" {
		return "", false, nil
	}
	var decision domain.Decision
	if err := json.Unmarshal([]byte(decisionJSON), &decision); err != nil {
		return "", false, errs.NewInternalError(
			errs.SubtypeStorage,
			"decode reply approval work decision",
		).WithCause(err)
	}
	if !validReplyApprovalRelevance(decision.Relevance) {
		return "", false, errs.NewInternalError(
			errs.SubtypeInvalidResponse,
			"reply approval work decision has invalid relevance %q",
			decision.Relevance,
		)
	}
	return decision.Relevance, true, nil
}

func validReplyApprovalRelevance(relevance domain.Relevance) bool {
	switch relevance {
	case domain.RelevanceDirectMention,
		domain.RelevanceInferred,
		domain.RelevanceOwnerRequest,
		domain.RelevanceAssistantRequest:
		return true
	default:
		return false
	}
}

func postReplyNotificationActionKey(dedupKey string, decisionJSON []byte) string {
	sum := sha256.Sum256(append([]byte(dedupKey+"\x00"), decisionJSON...))
	return fmt.Sprintf("on:%x", sum[:23])
}

func scanActionAttempt(row rowScanner) (domain.ActionAttempt, error) {
	var action domain.ActionAttempt
	var status string
	var created, updated string
	if err := row.Scan(&action.ID, &action.WorkItemID, &action.RunID, &action.Kind,
		&action.IdempotencyKey, &status, &action.RequestJSON, &action.ResponseJSON,
		&action.Error, &created, &updated); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return domain.ActionAttempt{}, errs.NewValidationError(errs.SubtypeFailedPrecondition, "action was not found")
		}
		return domain.ActionAttempt{}, errs.NewInternalError(errs.SubtypeStorage, "scan action attempt").WithCause(err)
	}
	action.Status = domain.ActionStatus(status)
	action.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
	action.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updated)
	return action, nil
}

// EnqueueEvent writes an event if its dedupe key has not been seen.
func (s *Store) EnqueueEvent(event domain.NormalizedEvent) (bool, error) {
	return s.EnqueueWorkItem(domain.NewWorkItem(event))
}

// EnqueueWorkItem persists a deterministically classified item so scheduler
// priority is available before the first claim.
func (s *Store) EnqueueWorkItem(item domain.WorkItem) (bool, error) {
	if item.DedupKey == "" {
		item.DedupKey = domain.DedupKey(item.Event)
	}
	if item.Status == "" {
		item.Status = domain.StatusReceived
	}
	if item.WorkKind == "" {
		item.WorkKind = domain.WorkKindGeneric
	}
	if item.Priority == 0 {
		item.Priority = domain.PriorityBackground
	}
	data, err := json.Marshal(item.Event)
	if err != nil {
		return false, errs.NewInternalError(errs.SubtypeUnknown, "marshal normalized event").WithCause(err)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	duplicateOf, duplicateFound, err := s.findEquivalentWorkItem(item.Event)
	if err != nil {
		return false, err
	}
	if duplicateFound {
		res, err := s.db.ExecContext(context.Background(),
			`INSERT OR IGNORE INTO work_items(
				dedup_key, status, work_kind, priority, duplicate_of, session_id,
				event_json, created_at, updated_at
			 ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			item.DedupKey, domain.StatusIgnored, domain.WorkKindGeneric, domain.PriorityBackground,
			duplicateOf, s.session.ID, string(data), now, now)
		if err != nil {
			return false, errs.NewInternalError(errs.SubtypeStorage, "enqueue duplicate work item").WithCause(err)
		}
		affected, err := res.RowsAffected()
		if err != nil {
			return false, errs.NewInternalError(errs.SubtypeStorage, "read duplicate enqueue result").WithCause(err)
		}
		if affected == 0 {
			if err := s.hydrateDuplicateEvent(item); err != nil {
				return false, err
			}
		}
		return affected == 1, nil
	}
	res, err := s.db.ExecContext(context.Background(),
		`INSERT OR IGNORE INTO work_items(
			dedup_key, status, work_kind, priority, session_id, event_json,
			created_at, updated_at
		 ) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		item.DedupKey, item.Status, item.WorkKind, item.Priority, s.session.ID,
		string(data), now, now)
	if err != nil {
		return false, errs.NewInternalError(errs.SubtypeStorage, "enqueue work item").WithCause(err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return false, errs.NewInternalError(errs.SubtypeStorage, "read enqueue result").WithCause(err)
	}
	if affected == 0 {
		if err := s.hydrateDuplicateEvent(item); err != nil {
			return false, err
		}
	}
	return affected == 1, nil
}

func (s *Store) findEquivalentWorkItem(event domain.NormalizedEvent) (int64, bool, error) {
	key := normalizedRequestContent(event)
	if key == "" || event.SenderID == "" {
		return 0, false, nil
	}
	rows, err := s.db.QueryContext(context.Background(),
		`SELECT id, event_json, created_at FROM work_items
		 WHERE duplicate_of IS NULL
		 ORDER BY id DESC
		 LIMIT 50`)
	if err != nil {
		return 0, false, errs.NewInternalError(errs.SubtypeStorage, "list recent work for duplicate detection").WithCause(err)
	}
	defer func() {
		_ = rows.Close()
	}()
	for rows.Next() {
		var id int64
		var eventJSON, receivedAtRaw string
		if err := rows.Scan(&id, &eventJSON, &receivedAtRaw); err != nil {
			return 0, false, errs.NewInternalError(errs.SubtypeStorage, "scan duplicate candidate").WithCause(err)
		}
		var candidate domain.NormalizedEvent
		if err := json.Unmarshal([]byte(eventJSON), &candidate); err != nil {
			return 0, false, errs.NewInternalError(errs.SubtypeStorage, "decode duplicate candidate").WithCause(err)
		}
		if candidate.SenderID != event.SenderID ||
			normalizedRequestContent(candidate) != key ||
			!crossEntryDuplicate(candidate, event) {
			continue
		}
		candidateTime := candidate.CreatedAt
		if candidateTime.IsZero() {
			candidateTime, _ = time.Parse(time.RFC3339Nano, receivedAtRaw)
		}
		eventTime := event.CreatedAt
		if eventTime.IsZero() {
			eventTime = time.Now().UTC()
		}
		diff := eventTime.Sub(candidateTime)
		if diff < 0 {
			diff = -diff
		}
		if diff > s.duplicateWindow {
			continue
		}
		return id, true, nil
	}
	if err := rows.Err(); err != nil {
		return 0, false, errs.NewInternalError(errs.SubtypeStorage, "iterate duplicate candidates").WithCause(err)
	}
	return 0, false, nil
}

func crossEntryDuplicate(left, right domain.NormalizedEvent) bool {
	leftPrivate := isPrivateChatType(left.ChatType)
	rightPrivate := isPrivateChatType(right.ChatType)
	return left.ChatID != right.ChatID && leftPrivate != rightPrivate
}

func isPrivateChatType(chatType string) bool {
	switch strings.ToLower(strings.TrimSpace(chatType)) {
	case "p2p", "private":
		return true
	default:
		return false
	}
}

func normalizedRequestContent(event domain.NormalizedEvent) string {
	content := strings.TrimSpace(event.Content)
	for _, mention := range event.Mentions {
		if mention.Key != "" {
			content = strings.ReplaceAll(content, mention.Key, "")
		}
		if mention.Name != "" {
			content = strings.ReplaceAll(content, "@"+mention.Name, "")
		}
	}
	fields := strings.Fields(content)
	for len(fields) > 0 && strings.HasPrefix(fields[0], "@") {
		fields = fields[1:]
	}
	return strings.ToLower(strings.Join(fields, " "))
}

func (s *Store) hydrateDuplicateEvent(item domain.WorkItem) error {
	if strings.TrimSpace(item.Event.Content) == "" {
		return nil
	}
	var id int64
	var eventJSON string
	err := s.db.QueryRowContext(context.Background(),
		`SELECT id, event_json FROM work_items WHERE dedup_key = ?`,
		item.DedupKey).Scan(&id, &eventJSON)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return errs.NewInternalError(errs.SubtypeStorage, "read duplicate work item").WithCause(err)
	}
	var existing domain.NormalizedEvent
	if err := json.Unmarshal([]byte(eventJSON), &existing); err != nil {
		return errs.NewInternalError(errs.SubtypeStorage, "decode duplicate work item event").WithCause(err)
	}
	if strings.TrimSpace(existing.Content) != "" {
		return nil
	}
	data, err := json.Marshal(item.Event)
	if err != nil {
		return errs.NewInternalError(errs.SubtypeUnknown, "marshal hydrated duplicate event").WithCause(err)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err = s.db.ExecContext(context.Background(),
		`UPDATE work_items SET event_json = ?, updated_at = ? WHERE id = ?`,
		string(data), now, id)
	if err != nil {
		return errs.NewInternalError(errs.SubtypeStorage, "hydrate duplicate work item").WithCause(err)
	}
	return nil
}

// ClaimNext leases the next available work item.
func (s *Store) ClaimNext(worker string) (domain.WorkItem, bool, error) {
	return s.ClaimNextForLane(worker, domain.SchedulerLaneAny)
}

// ClaimNextForLane leases the highest-priority eligible item for one worker
// lane. Foreground workers never claim CodingGoal work; background workers only
// claim CodingGoal work.
func (s *Store) ClaimNextForLane(worker string, lane domain.SchedulerLane) (domain.WorkItem, bool, error) {
	nonce, err := randomID()
	if err != nil {
		return domain.WorkItem{}, false, err
	}
	leaseToken := worker + ":" + nonce
	tx, err := s.db.BeginTx(context.Background(), nil)
	if err != nil {
		return domain.WorkItem{}, false, errs.NewInternalError(errs.SubtypeStorage, "begin claim transaction").WithCause(err)
	}
	defer tx.Rollback() //nolint:errcheck // commit path below closes the transaction

	now := time.Now().UTC()
	nowRaw := now.Format(time.RFC3339Nano)
	laneClause := ""
	args := []any{
		domain.StatusReceived,
		domain.StatusReady,
		domain.StatusRetryWait,
		domain.StatusWaitingUser,
		nowRaw,
	}
	switch lane {
	case domain.SchedulerLaneInteractive:
		laneClause = ` AND work_kind IN (?, ?, ?)`
		args = append(
			args,
			domain.WorkKindFastPath,
			domain.WorkKindOwnerControl,
			domain.WorkKindSimpleQuestion,
		)
	case domain.SchedulerLaneForeground:
		laneClause = ` AND work_kind <> ?`
		args = append(args, domain.WorkKindCodingGoal)
	case domain.SchedulerLaneBackground:
		laneClause = ` AND work_kind = ?`
		args = append(args, domain.WorkKindCodingGoal)
	}
	query := workItemSelect + `
		 WHERE status IN (?, ?, ?, ?)
		   AND (next_attempt_at IS NULL OR next_attempt_at = '' OR next_attempt_at <= ?)` + laneClause + `
		 ORDER BY priority DESC, id
		 LIMIT 1`
	row := tx.QueryRowContext(context.Background(),
		query, args...)
	item, err := scanWorkItem(row)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.WorkItem{}, false, nil
	}
	if err != nil {
		return domain.WorkItem{}, false, err
	}
	res, err := tx.ExecContext(context.Background(),
		`UPDATE work_items SET status = ?, lease_by = ?, lease_time = ?, updated_at = ?
		 WHERE id = ? AND status IN (?, ?, ?, ?)`,
		domain.StatusProcessing, leaseToken, nowRaw, nowRaw, item.ID,
		domain.StatusReceived, domain.StatusReady, domain.StatusRetryWait, domain.StatusWaitingUser)
	if err != nil {
		return domain.WorkItem{}, false, errs.NewInternalError(errs.SubtypeStorage, "lease work item").WithCause(err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return domain.WorkItem{}, false, errs.NewInternalError(errs.SubtypeStorage, "read lease result").WithCause(err)
	}
	if affected == 0 {
		return domain.WorkItem{}, false, nil
	}
	if err := tx.Commit(); err != nil {
		return domain.WorkItem{}, false, errs.NewInternalError(errs.SubtypeStorage, "commit claim transaction").WithCause(err)
	}
	item.Status = domain.StatusProcessing
	item.LeaseBy = leaseToken
	item.LeaseTime = now
	if err := s.hydrateDelegatedInvestigationWorkItem(&item); err != nil {
		_ = s.MarkRetryClaim(item.ID, item.LeaseBy, err.Error(), time.Second)
		return domain.WorkItem{}, false, err
	}
	return item, true, nil
}

func (s *Store) hydrateDelegatedInvestigationWorkItem(
	item *domain.WorkItem,
) error {
	investigation, ok, err := s.GetDelegatedInvestigation(item.ID)
	if err != nil || !ok {
		return err
	}
	switch investigation.Status {
	case domain.InvestigationPendingProgress,
		domain.InvestigationInvestigating,
		domain.InvestigationFinalizing:
	default:
		return nil
	}
	item.TaskSummary = investigation.TaskSummary
	item.TaskClass = investigation.TaskClass
	item.ContextCutoff = investigation.ContextCutoff
	item.ContextDigest = investigation.ContextDigest
	item.ResolvedContext = append(
		[]domain.NormalizedEvent(nil),
		investigation.ContextMessages...,
	)
	item.InvestigationActive = true
	return nil
}

// RequeueExpiredLeases moves expired processing items back to received.
func (s *Store) RequeueExpiredLeases(maxAge time.Duration) error {
	now := time.Now().UTC()
	rows, err := s.db.QueryContext(context.Background(),
		`SELECT id, COALESCE(lease_by, ''), COALESCE(lease_time, ''), lease_timeout_seconds
		 FROM work_items WHERE status = ?`,
		domain.StatusProcessing)
	if err != nil {
		return errs.NewInternalError(errs.SubtypeStorage, "list processing leases").WithCause(err)
	}
	type expiredLease struct {
		id, timeoutSeconds int64
		token, rawTime     string
	}
	var expired []expiredLease
	for rows.Next() {
		var id int64
		var token, rawLease string
		var timeoutSeconds int64
		if err := rows.Scan(&id, &token, &rawLease, &timeoutSeconds); err != nil {
			_ = rows.Close()
			return errs.NewInternalError(errs.SubtypeStorage, "scan processing lease").WithCause(err)
		}
		if maxAge < 0 {
			expired = append(expired, expiredLease{id: id, token: token, rawTime: rawLease, timeoutSeconds: timeoutSeconds})
			continue
		}
		leaseAge := maxAge
		if timeoutSeconds > 0 {
			leaseAge = time.Duration(timeoutSeconds) * time.Second
		}
		if rawLease == "" {
			expired = append(expired, expiredLease{id: id, token: token, rawTime: rawLease, timeoutSeconds: timeoutSeconds})
			continue
		}
		leaseTime, err := time.Parse(time.RFC3339Nano, rawLease)
		if err != nil || leaseTime.Add(leaseAge).Before(now) {
			expired = append(expired, expiredLease{id: id, token: token, rawTime: rawLease, timeoutSeconds: timeoutSeconds})
		}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return errs.NewInternalError(errs.SubtypeStorage, "iterate processing leases").WithCause(err)
	}
	if err := rows.Close(); err != nil {
		return errs.NewInternalError(errs.SubtypeStorage, "close processing lease rows").WithCause(err)
	}
	if len(expired) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(context.Background(), nil)
	if err != nil {
		return errs.NewInternalError(errs.SubtypeStorage, "begin expired lease recovery").WithCause(err)
	}
	defer tx.Rollback() //nolint:errcheck
	nowRaw := now.Format(time.RFC3339Nano)
	for _, lease := range expired {
		if _, err := tx.ExecContext(context.Background(),
			`UPDATE work_items SET status = ?, lease_by = NULL, lease_time = NULL, updated_at = ?
			 WHERE id = ? AND status = ? AND COALESCE(lease_by, '') = ? AND COALESCE(lease_time, '') = ?`,
			domain.StatusReceived, nowRaw, lease.id, domain.StatusProcessing, lease.token, lease.rawTime); err != nil {
			return errs.NewInternalError(errs.SubtypeStorage, "requeue expired lease").WithCause(err)
		}
	}
	if err := tx.Commit(); err != nil {
		return errs.NewInternalError(errs.SubtypeStorage, "commit expired lease recovery").WithCause(err)
	}
	return nil
}

// UpdateWorkItemScheduling stores the scheduler lane, priority, and lease bound.
func (s *Store) UpdateWorkItemScheduling(id int64, kind domain.WorkKind, priority int, lease time.Duration) error {
	return s.updateWorkItemScheduling(id, "", kind, priority, lease)
}

// UpdateWorkItemSchedulingClaim updates scheduling only while the exact claim
// token still owns the processing item.
func (s *Store) UpdateWorkItemSchedulingClaim(
	id int64,
	leaseToken string,
	kind domain.WorkKind,
	priority int,
	lease time.Duration,
) error {
	return s.updateWorkItemScheduling(id, leaseToken, kind, priority, lease)
}

func (s *Store) updateWorkItemScheduling(
	id int64,
	leaseToken string,
	kind domain.WorkKind,
	priority int,
	lease time.Duration,
) error {
	if kind == "" {
		kind = domain.WorkKindGeneric
	}
	if priority == 0 {
		priority = domain.PriorityBackground
	}
	if lease <= 0 {
		lease = 5 * time.Minute
	}
	query := `UPDATE work_items
		 SET work_kind = ?, priority = ?, lease_timeout_seconds = ?, updated_at = ?
		 WHERE id = ?`
	args := []any{kind, priority, int64(lease.Seconds()), time.Now().UTC().Format(time.RFC3339Nano), id}
	if leaseToken != "" {
		query += ` AND status = ? AND lease_by = ?`
		args = append(args, domain.StatusProcessing, leaseToken)
	}
	result, err := s.db.ExecContext(context.Background(), query, args...)
	if err != nil {
		return errs.NewInternalError(errs.SubtypeStorage, "update work item scheduling").WithCause(err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return errs.NewInternalError(errs.SubtypeStorage, "read scheduling update result").WithCause(err)
	}
	if affected == 0 {
		return errs.NewValidationError(errs.SubtypeFailedPrecondition, "work item %d was not found", id)
	}
	return nil
}

// ValidateLease verifies that an external side effect is still owned by the
// exact claim token.
func (s *Store) ValidateLease(id int64, leaseToken string) error {
	var count int
	if err := s.db.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM work_items WHERE id = ? AND status = ? AND lease_by = ?`,
		id, domain.StatusProcessing, leaseToken).Scan(&count); err != nil {
		return errs.NewInternalError(errs.SubtypeStorage, "validate work item lease").WithCause(err)
	}
	if count != 1 {
		return errs.NewValidationError(errs.SubtypeFailedPrecondition, "work item lease was lost")
	}
	return nil
}

// RefreshLease records that the current fenced worker is still making progress.
func (s *Store) RefreshLease(id int64, worker string) error {
	result, err := s.db.ExecContext(context.Background(),
		`UPDATE work_items SET lease_time = ?, updated_at = ?
		 WHERE id = ? AND status = ? AND lease_by = ?`,
		time.Now().UTC().Format(time.RFC3339Nano), time.Now().UTC().Format(time.RFC3339Nano),
		id, domain.StatusProcessing, worker)
	if err != nil {
		return errs.NewInternalError(errs.SubtypeStorage, "refresh work item lease").WithCause(err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return errs.NewInternalError(errs.SubtypeStorage, "read lease refresh result").WithCause(err)
	}
	if affected == 0 {
		return errs.NewValidationError(errs.SubtypeFailedPrecondition, "processing work item %d was not found", id)
	}
	return nil
}

// QueueSummary returns local scheduler health without calling Lark or a model.
func (s *Store) QueueSummary() (domain.QueueSummary, error) {
	summary := domain.QueueSummary{
		LaneCounts:   map[string]int{},
		StatusCounts: map[string]int{},
	}
	rows, err := s.db.QueryContext(context.Background(),
		`SELECT status, work_kind, COUNT(*) FROM work_items GROUP BY status, work_kind`)
	if err != nil {
		return summary, errs.NewInternalError(errs.SubtypeStorage, "summarize queue lanes").WithCause(err)
	}
	for rows.Next() {
		var status, kind string
		var count int
		if err := rows.Scan(&status, &kind, &count); err != nil {
			_ = rows.Close()
			return summary, errs.NewInternalError(errs.SubtypeStorage, "scan queue summary").WithCause(err)
		}
		summary.StatusCounts[status] += count
		summary.LaneCounts[kind] += count
		if kind == string(domain.WorkKindFastPath) && status == string(domain.StatusCompleted) {
			summary.FastPathHits += count
		}
	}
	if err := rows.Close(); err != nil {
		return summary, errs.NewInternalError(errs.SubtypeStorage, "close queue summary rows").WithCause(err)
	}
	leaseRows, err := s.db.QueryContext(context.Background(),
		`SELECT COALESCE(lease_time, ''), lease_timeout_seconds
		 FROM work_items WHERE status = ?`,
		domain.StatusProcessing)
	if err != nil {
		return summary, errs.NewInternalError(errs.SubtypeStorage, "summarize processing leases").WithCause(err)
	}
	now := time.Now().UTC()
	for leaseRows.Next() {
		var raw string
		var seconds int64
		if err := leaseRows.Scan(&raw, &seconds); err != nil {
			_ = leaseRows.Close()
			return summary, errs.NewInternalError(errs.SubtypeStorage, "scan summary lease").WithCause(err)
		}
		leaseTime, parseErr := time.Parse(time.RFC3339Nano, raw)
		timeout := time.Duration(seconds) * time.Second
		if raw == "" || parseErr != nil || timeout <= 0 || leaseTime.Add(timeout).Before(now) {
			summary.StaleProcessing++
		}
	}
	if err := leaseRows.Close(); err != nil {
		return summary, errs.NewInternalError(errs.SubtypeStorage, "close summary leases").WithCause(err)
	}
	recentRows, err := s.db.QueryContext(context.Background(),
		`SELECT wi.id, wi.event_json, wi.status, wi.work_kind, wi.created_at, wi.updated_at,
		        (SELECT COUNT(*) FROM agent_steps ast
		         JOIN agent_runs ar ON ar.id = ast.run_id
		         WHERE ar.work_item_id = wi.id AND ast.kind = 'model'),
		        (SELECT COUNT(*) FROM tool_calls tc
		         JOIN agent_runs ar ON ar.id = tc.run_id
		         WHERE ar.work_item_id = wi.id)
		 FROM work_items wi
		 ORDER BY wi.updated_at DESC, wi.id DESC
		 LIMIT 10`)
	if err != nil {
		return summary, errs.NewInternalError(errs.SubtypeStorage, "list recent work metrics").WithCause(err)
	}
	for recentRows.Next() {
		var metric domain.RecentWorkMetric
		var eventJSON, status, kind, createdRaw, updatedRaw string
		if err := recentRows.Scan(
			&metric.WorkItemID, &eventJSON, &status, &kind, &createdRaw, &updatedRaw,
			&metric.ModelTurns, &metric.ToolCalls,
		); err != nil {
			_ = recentRows.Close()
			return summary, errs.NewInternalError(errs.SubtypeStorage, "scan recent work metric").WithCause(err)
		}
		var event domain.NormalizedEvent
		_ = json.Unmarshal([]byte(eventJSON), &event)
		created, _ := time.Parse(time.RFC3339Nano, createdRaw)
		updated, _ := time.Parse(time.RFC3339Nano, updatedRaw)
		metric.MessageID = event.MessageID
		metric.Status = domain.WorkItemStatus(status)
		metric.WorkKind = domain.WorkKind(kind)
		metric.DurationMS = updated.Sub(created).Milliseconds()
		metric.FastPath = metric.WorkKind == domain.WorkKindFastPath
		summary.Recent = append(summary.Recent, metric)
	}
	if err := recentRows.Close(); err != nil {
		return summary, errs.NewInternalError(errs.SubtypeStorage, "close recent work metrics").WithCause(err)
	}
	return summary, nil
}

// SaveCodingGoal persists a durable background coding follow-up.
func (s *Store) SaveCodingGoal(goal domain.CodingGoal) error {
	return s.saveCodingGoal(goal, "")
}

// SaveCodingGoalClaim persists a goal only while the exact background claim is
// still valid.
func (s *Store) SaveCodingGoalClaim(goal domain.CodingGoal, leaseToken string) error {
	return s.saveCodingGoal(goal, leaseToken)
}

func (s *Store) saveCodingGoal(goal domain.CodingGoal, leaseToken string) error {
	tx, err := s.db.BeginTx(context.Background(), nil)
	if err != nil {
		return errs.NewInternalError(errs.SubtypeStorage, "begin save coding goal").WithCause(err)
	}
	defer tx.Rollback() //nolint:errcheck
	// Acquire SQLite's single-writer lock before checking capacity so concurrent
	// background workers cannot both pass the same MaxActive observation.
	if _, err := tx.ExecContext(context.Background(),
		`UPDATE coding_goals SET updated_at = updated_at WHERE id = (SELECT id FROM coding_goals LIMIT 1)`); err != nil {
		return errs.NewInternalError(errs.SubtypeStorage, "lock coding goals for capacity check").WithCause(err)
	}
	if leaseToken != "" {
		var owned int
		if err := tx.QueryRowContext(context.Background(),
			`SELECT COUNT(*) FROM work_items WHERE id = ? AND status = ? AND lease_by = ?`,
			goal.WorkItemID, domain.StatusProcessing, leaseToken).Scan(&owned); err != nil {
			return errs.NewInternalError(errs.SubtypeStorage, "validate coding goal claim").WithCause(err)
		}
		if owned != 1 {
			return errs.NewValidationError(errs.SubtypeFailedPrecondition, "coding goal lease was lost")
		}
	}
	if goal.Status == domain.CodingGoalActive {
		var active int
		if err := tx.QueryRowContext(context.Background(),
			`SELECT COUNT(*) FROM coding_goals WHERE status = ? AND work_item_id <> ?`,
			domain.CodingGoalActive, goal.WorkItemID).Scan(&active); err != nil {
			return errs.NewInternalError(errs.SubtypeStorage, "count active coding goals").WithCause(err)
		}
		if active >= s.maxActiveGoals {
			return errs.NewValidationError(
				errs.SubtypeFailedPrecondition,
				"active coding goal limit reached: %d",
				s.maxActiveGoals,
			)
		}
	}
	completion, err := json.Marshal(goal.CompletionConditions)
	if err != nil {
		return errs.NewInternalError(errs.SubtypeUnknown, "marshal coding goal completion conditions").WithCause(err)
	}
	blocking, err := json.Marshal(goal.BlockingConditions)
	if err != nil {
		return errs.NewInternalError(errs.SubtypeUnknown, "marshal coding goal blocking conditions").WithCause(err)
	}
	if goal.CreatedAt.IsZero() {
		goal.CreatedAt = time.Now().UTC()
	}
	if goal.UpdatedAt.IsZero() {
		goal.UpdatedAt = goal.CreatedAt
	}
	_, err = tx.ExecContext(context.Background(),
		`INSERT INTO coding_goals(
			work_item_id, original_message_id, question, status,
			completion_conditions_json, blocking_conditions_json,
			max_investigation_turns, used_investigation_turns, created_at, updated_at
		 ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(work_item_id) DO UPDATE SET
			question = excluded.question,
			status = excluded.status,
			completion_conditions_json = excluded.completion_conditions_json,
			blocking_conditions_json = excluded.blocking_conditions_json,
			max_investigation_turns = excluded.max_investigation_turns,
			used_investigation_turns = coding_goals.used_investigation_turns,
			updated_at = excluded.updated_at`,
		goal.WorkItemID, goal.OriginalMessageID, goal.Question, goal.Status,
		string(completion), string(blocking), goal.MaxInvestigationTurns,
		goal.UsedInvestigationTurns, goal.CreatedAt.Format(time.RFC3339Nano),
		goal.UpdatedAt.Format(time.RFC3339Nano))
	if err != nil {
		return errs.NewInternalError(errs.SubtypeStorage, "save coding goal").WithCause(err)
	}
	if _, err := tx.ExecContext(context.Background(),
		`UPDATE work_items SET work_kind = ?, priority = ?, lease_timeout_seconds = ?, updated_at = ?
		 WHERE id = ?`,
		domain.WorkKindCodingGoal, domain.PriorityBackground, int64((2 * time.Hour).Seconds()),
		time.Now().UTC().Format(time.RFC3339Nano), goal.WorkItemID); err != nil {
		return errs.NewInternalError(errs.SubtypeStorage, "schedule coding goal").WithCause(err)
	}
	if err := tx.Commit(); err != nil {
		return errs.NewInternalError(errs.SubtypeStorage, "commit coding goal").WithCause(err)
	}
	return nil
}

// CodingGoalBudget returns durable model-turn usage across retries.
func (s *Store) CodingGoalBudget(workItemID int64) (int, int, error) {
	var used, maxTurns int
	if err := s.db.QueryRowContext(context.Background(),
		`SELECT used_investigation_turns, max_investigation_turns
		 FROM coding_goals WHERE work_item_id = ?`,
		workItemID).Scan(&used, &maxTurns); err != nil {
		return 0, 0, errs.NewInternalError(errs.SubtypeStorage, "read coding goal budget").WithCause(err)
	}
	return used, maxTurns, nil
}

// ListCodingGoals returns persisted coding goals for diagnostics.
func (s *Store) ListCodingGoals(status domain.CodingGoalStatus) ([]domain.CodingGoal, error) {
	query := `SELECT work_item_id, original_message_id, question, status,
			completion_conditions_json, blocking_conditions_json,
			max_investigation_turns, used_investigation_turns, created_at, updated_at
		 FROM coding_goals`
	var args []any
	if status != "" {
		query += ` WHERE status = ?`
		args = append(args, status)
	}
	query += ` ORDER BY updated_at DESC, id DESC`
	rows, err := s.db.QueryContext(context.Background(), query, args...)
	if err != nil {
		return nil, errs.NewInternalError(errs.SubtypeStorage, "list coding goals").WithCause(err)
	}
	defer func() {
		_ = rows.Close()
	}()
	var goals []domain.CodingGoal
	for rows.Next() {
		var goal domain.CodingGoal
		var statusRaw, completionRaw, blockingRaw, createdAt, updatedAt string
		if err := rows.Scan(
			&goal.WorkItemID, &goal.OriginalMessageID, &goal.Question, &statusRaw,
			&completionRaw, &blockingRaw, &goal.MaxInvestigationTurns,
			&goal.UsedInvestigationTurns, &createdAt, &updatedAt,
		); err != nil {
			return nil, errs.NewInternalError(errs.SubtypeStorage, "scan coding goal").WithCause(err)
		}
		goal.Kind = domain.CodingGoalWork
		goal.Status = domain.CodingGoalStatus(statusRaw)
		if err := json.Unmarshal([]byte(completionRaw), &goal.CompletionConditions); err != nil {
			return nil, errs.NewInternalError(errs.SubtypeStorage, "decode coding goal completion conditions").WithCause(err)
		}
		if err := json.Unmarshal([]byte(blockingRaw), &goal.BlockingConditions); err != nil {
			return nil, errs.NewInternalError(errs.SubtypeStorage, "decode coding goal blocking conditions").WithCause(err)
		}
		goal.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
		goal.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updatedAt)
		goals = append(goals, goal)
	}
	if err := rows.Err(); err != nil {
		return nil, errs.NewInternalError(errs.SubtypeStorage, "iterate coding goals").WithCause(err)
	}
	return goals, nil
}

// ListWorkItems returns all work items for diagnostics and tests.
func (s *Store) ListWorkItems() ([]domain.WorkItem, error) {
	rows, err := s.db.QueryContext(context.Background(),
		workItemSelect+` ORDER BY id`)
	if err != nil {
		return nil, errs.NewInternalError(errs.SubtypeStorage, "list work items").WithCause(err)
	}
	defer func() {
		_ = rows.Close()
	}()
	var out []domain.WorkItem
	for rows.Next() {
		item, err := scanWorkItem(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	if err := rows.Err(); err != nil {
		return nil, errs.NewInternalError(errs.SubtypeStorage, "iterate work items").WithCause(err)
	}
	return out, nil
}

// ListPendingDelegatedWork returns only active direct/private delegated targets
// from one exact chat for multi-target semantic matching.
func (s *Store) ListPendingDelegatedWork(chatID string) ([]domain.WorkItem, error) {
	if strings.TrimSpace(chatID) == "" {
		return nil, errs.NewValidationError(
			errs.SubtypeInvalidArgument,
			"chat_id is required",
		).WithParam("chat_id")
	}
	rows, err := s.db.QueryContext(
		context.Background(),
		workItemSelect+` WHERE status IN (?, ?) AND work_kind = ? ORDER BY id`,
		domain.StatusWaitingUser,
		domain.StatusProcessing,
		domain.WorkKindDirectMention,
	)
	if err != nil {
		return nil, errs.NewInternalError(
			errs.SubtypeStorage,
			"list pending delegated work",
		).WithCause(err)
	}
	defer func() {
		_ = rows.Close()
	}()
	var out []domain.WorkItem
	for rows.Next() {
		item, scanErr := scanWorkItem(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		if item.Event.ChatID == chatID {
			out = append(out, item)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, errs.NewInternalError(
			errs.SubtypeStorage,
			"iterate pending delegated work",
		).WithCause(err)
	}
	return out, nil
}

// RecordOwnerReplyResolution appends one immutable semantic-resolution audit.
func (s *Store) RecordOwnerReplyResolution(
	workItemID int64,
	resolution replymatch.Resolution,
) error {
	matchedJSON, err := json.Marshal(resolution.MatchedOwnerMessageIDs)
	if err != nil {
		return errs.NewInternalError(
			errs.SubtypeStorage,
			"encode matched owner messages",
		).WithCause(err)
	}
	if resolution.ContextCutoff.IsZero() {
		return errs.NewValidationError(
			errs.SubtypeInvalidArgument,
			"semantic resolution context cutoff is required",
		).WithParam("context_cutoff")
	}
	_, err = s.db.ExecContext(
		context.Background(),
		`INSERT INTO owner_reply_resolutions(
			work_item_id, target_message_id, result,
			matched_owner_message_ids_json, confidence, reason,
			task_summary, task_class, classification_confidence,
			requires_progress, context_cutoff, evaluated_at
		 ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		workItemID,
		resolution.TargetMessageID,
		resolution.Result,
		string(matchedJSON),
		resolution.Confidence,
		resolution.Reason,
		resolution.TaskSummary,
		resolution.TaskClass,
		resolution.ClassificationConfidence,
		resolution.RequiresProgress,
		resolution.ContextCutoff.UTC().Format(time.RFC3339Nano),
		time.Now().UTC().Format(time.RFC3339Nano),
	)
	if err != nil {
		return errs.NewInternalError(
			errs.SubtypeStorage,
			"record semantic owner-reply resolution",
		).WithCause(err)
	}
	return nil
}

// ListOwnerReplyResolutions returns immutable semantic audits oldest first.
func (s *Store) ListOwnerReplyResolutions(
	workItemID int64,
) ([]replymatch.Resolution, error) {
	rows, err := s.db.QueryContext(
		context.Background(),
		`SELECT target_message_id, result, matched_owner_message_ids_json,
		        confidence, reason, task_summary, task_class,
		        classification_confidence, requires_progress, context_cutoff
		 FROM owner_reply_resolutions
		 WHERE work_item_id = ?
		 ORDER BY evaluated_at, id`,
		workItemID,
	)
	if err != nil {
		return nil, errs.NewInternalError(
			errs.SubtypeStorage,
			"list semantic owner-reply resolutions",
		).WithCause(err)
	}
	defer func() {
		_ = rows.Close()
	}()
	var out []replymatch.Resolution
	for rows.Next() {
		var resolution replymatch.Resolution
		var resultRaw, matchedRaw, taskClassRaw, cutoffRaw string
		if err := rows.Scan(
			&resolution.TargetMessageID,
			&resultRaw,
			&matchedRaw,
			&resolution.Confidence,
			&resolution.Reason,
			&resolution.TaskSummary,
			&taskClassRaw,
			&resolution.ClassificationConfidence,
			&resolution.RequiresProgress,
			&cutoffRaw,
		); err != nil {
			return nil, errs.NewInternalError(
				errs.SubtypeStorage,
				"scan semantic owner-reply resolution",
			).WithCause(err)
		}
		resolution.Result = replymatch.Result(resultRaw)
		resolution.TaskClass = domain.TaskClass(taskClassRaw)
		if err := json.Unmarshal(
			[]byte(matchedRaw),
			&resolution.MatchedOwnerMessageIDs,
		); err != nil {
			return nil, errs.NewInternalError(
				errs.SubtypeStorage,
				"decode matched owner messages",
			).WithCause(err)
		}
		resolution.ContextCutoff, _ = time.Parse(time.RFC3339Nano, cutoffRaw)
		out = append(out, resolution)
	}
	if err := rows.Err(); err != nil {
		return nil, errs.NewInternalError(
			errs.SubtypeStorage,
			"iterate semantic owner-reply resolutions",
		).WithCause(err)
	}
	return out, nil
}

// BeginDelegatedInvestigation creates one idempotent resumable investigation.
func (s *Store) BeginDelegatedInvestigation(
	plan domain.DelegatedInvestigation,
) (domain.DelegatedInvestigation, bool, error) {
	if plan.WorkItemID <= 0 {
		return domain.DelegatedInvestigation{}, false, errs.NewValidationError(
			errs.SubtypeInvalidArgument,
			"delegated investigation requires work_item_id",
		).WithParam("work_item_id")
	}
	if strings.TrimSpace(plan.TaskSummary) == "" {
		return domain.DelegatedInvestigation{}, false, errs.NewValidationError(
			errs.SubtypeInvalidArgument,
			"delegated investigation requires task_summary",
		).WithParam("task_summary")
	}
	switch plan.TaskClass {
	case domain.TaskClassInvestigation, domain.TaskClassCoding:
	default:
		return domain.DelegatedInvestigation{}, false, errs.NewValidationError(
			errs.SubtypeInvalidArgument,
			"delegated investigation requires investigation or coding task_class",
		).WithParam("task_class")
	}
	if plan.ContextCutoff.IsZero() || strings.TrimSpace(plan.ContextDigest) == "" {
		return domain.DelegatedInvestigation{}, false, errs.NewValidationError(
			errs.SubtypeInvalidArgument,
			"delegated investigation requires context cutoff and digest",
		)
	}
	if plan.Status == "" {
		plan.Status = domain.InvestigationPendingProgress
	}
	contextJSON, err := json.Marshal(plan.ContextMessages)
	if err != nil {
		return domain.DelegatedInvestigation{}, false, errs.NewInternalError(
			errs.SubtypeStorage,
			"encode delegated investigation context",
		).WithCause(err)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	result, err := s.db.ExecContext(
		context.Background(),
		`INSERT OR IGNORE INTO delegated_investigations(
			work_item_id, task_summary, task_class, context_cutoff,
			context_digest, status, context_messages_json, created_at, updated_at
		 ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		plan.WorkItemID,
		strings.TrimSpace(plan.TaskSummary),
		plan.TaskClass,
		plan.ContextCutoff.UTC().Format(time.RFC3339Nano),
		strings.TrimSpace(plan.ContextDigest),
		plan.Status,
		string(contextJSON),
		now,
		now,
	)
	if err != nil {
		return domain.DelegatedInvestigation{}, false, errs.NewInternalError(
			errs.SubtypeStorage,
			"begin delegated investigation",
		).WithCause(err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return domain.DelegatedInvestigation{}, false, errs.NewInternalError(
			errs.SubtypeStorage,
			"read delegated investigation insert result",
		).WithCause(err)
	}
	investigation, ok, err := s.GetDelegatedInvestigation(plan.WorkItemID)
	if err == nil && ok &&
		(investigation.TaskSummary != strings.TrimSpace(plan.TaskSummary) ||
			investigation.TaskClass != plan.TaskClass ||
			!investigation.ContextCutoff.Equal(plan.ContextCutoff.UTC()) ||
			investigation.ContextDigest != strings.TrimSpace(plan.ContextDigest)) {
		return domain.DelegatedInvestigation{}, false, errs.NewInternalError(
			errs.SubtypeFailedPrecondition,
			"delegated investigation context changed after it was persisted",
		)
	}
	return investigation, affected == 1 && ok, err
}

// BeginInvestigationMessageAction creates or reads one staged-message action.
// Existing executing actions are returned without being replayed because their
// external result is uncertain.
func (s *Store) BeginInvestigationMessageAction(
	ctx context.Context,
	workItemID int64,
	stage string,
	text string,
) (domain.Action, bool, string, error) {
	if workItemID <= 0 || strings.TrimSpace(text) == "" {
		return domain.Action{}, false, "", errs.NewValidationError(
			errs.SubtypeInvalidArgument,
			"investigation message action requires work item and text",
		)
	}
	var suffix, kind string
	switch stage {
	case "owner_notice":
		suffix = "investigation-owner-notice"
		kind = "investigation_owner_notice"
	case "progress":
		suffix = "investigation-progress"
		kind = "investigation_progress"
	default:
		return domain.Action{}, false, "", errs.NewValidationError(
			errs.SubtypeInvalidArgument,
			"unsupported investigation message stage %q",
			stage,
		).WithParam("stage")
	}
	var dedupKey string
	if err := s.db.QueryRowContext(
		ctx,
		`SELECT dedup_key FROM work_items WHERE id = ?`,
		workItemID,
	).Scan(&dedupKey); err != nil {
		return domain.Action{}, false, "", errs.NewInternalError(
			errs.SubtypeStorage,
			"locate investigation work item",
		).WithCause(err)
	}
	key := dedupKey + ":" + suffix
	requestJSON, err := json.Marshal(map[string]string{"text": text})
	if err != nil {
		return domain.Action{}, false, "", errs.NewInternalError(
			errs.SubtypeUnknown,
			"encode investigation message action",
		).WithCause(err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.Action{}, false, "", errs.NewInternalError(
			errs.SubtypeStorage,
			"begin investigation message action",
		).WithCause(err)
	}
	defer tx.Rollback() //nolint:errcheck
	action := domain.Action{Kind: kind, Idempotency: key}
	var responseJSON string
	err = tx.QueryRowContext(
		ctx,
		`SELECT id, status, COALESCE(response_json, '')
		 FROM action_attempts WHERE idempotency_key = ?`,
		key,
	).Scan(&action.ID, &action.Status, &responseJSON)
	if errors.Is(err, sql.ErrNoRows) {
		now := time.Now().UTC().Format(time.RFC3339Nano)
		result, insertErr := tx.ExecContext(
			ctx,
			`INSERT INTO action_attempts(
				work_item_id, kind, idempotency_key, status, request_json,
				created_at, updated_at
			 ) VALUES (?, ?, ?, ?, ?, ?, ?)`,
			workItemID,
			kind,
			key,
			domain.ActionExecuting,
			string(requestJSON),
			now,
			now,
		)
		if insertErr != nil {
			return domain.Action{}, false, "", errs.NewInternalError(
				errs.SubtypeStorage,
				"insert investigation message action",
			).WithCause(insertErr)
		}
		action.ID, err = result.LastInsertId()
		if err != nil {
			return domain.Action{}, false, "", errs.NewInternalError(
				errs.SubtypeStorage,
				"read investigation message action id",
			).WithCause(err)
		}
		action.Status = domain.ActionExecuting
		if stage == "progress" {
			if _, err := tx.ExecContext(
				ctx,
				`UPDATE delegated_investigations
				 SET progress_action_id = ?, updated_at = ?
				 WHERE work_item_id = ?`,
				action.ID,
				now,
				workItemID,
			); err != nil {
				return domain.Action{}, false, "", errs.NewInternalError(
					errs.SubtypeStorage,
					"link investigation progress action",
				).WithCause(err)
			}
		}
		if err := tx.Commit(); err != nil {
			return domain.Action{}, false, "", errs.NewInternalError(
				errs.SubtypeStorage,
				"commit investigation message action",
			).WithCause(err)
		}
		return action, true, "", nil
	}
	if err != nil {
		return domain.Action{}, false, "", errs.NewInternalError(
			errs.SubtypeStorage,
			"read investigation message action",
		).WithCause(err)
	}
	var response map[string]string
	if responseJSON != "" {
		if err := json.Unmarshal([]byte(responseJSON), &response); err != nil {
			return domain.Action{}, false, "", errs.NewInternalError(
				errs.SubtypeStorage,
				"decode investigation message action response",
			).WithCause(err)
		}
	}
	if action.Status == domain.ActionBlocked {
		if _, err := tx.ExecContext(
			ctx,
			`UPDATE action_attempts
			 SET status = ?, request_json = ?, error = NULL, updated_at = ?
			 WHERE id = ?`,
			domain.ActionExecuting,
			string(requestJSON),
			time.Now().UTC().Format(time.RFC3339Nano),
			action.ID,
		); err != nil {
			return domain.Action{}, false, "", errs.NewInternalError(
				errs.SubtypeStorage,
				"resume known-failed investigation message action",
			).WithCause(err)
		}
		action.Status = domain.ActionExecuting
		if err := tx.Commit(); err != nil {
			return domain.Action{}, false, "", errs.NewInternalError(
				errs.SubtypeStorage,
				"commit resumed investigation message action",
			).WithCause(err)
		}
		return action, true, "", nil
	}
	if err := tx.Commit(); err != nil {
		return domain.Action{}, false, "", errs.NewInternalError(
			errs.SubtypeStorage,
			"commit existing investigation message action",
		).WithCause(err)
	}
	return action, false, response["message_id"], nil
}

// CompleteInvestigationMessageAction records a staged message result.
func (s *Store) CompleteInvestigationMessageAction(
	ctx context.Context,
	actionID int64,
	messageID string,
	actionErr string,
) error {
	return s.CompleteReplyAction(ctx, actionID, messageID, actionErr)
}

// GetDelegatedInvestigation returns one investigation by work item.
func (s *Store) GetDelegatedInvestigation(
	workItemID int64,
) (domain.DelegatedInvestigation, bool, error) {
	var investigation domain.DelegatedInvestigation
	var taskClass, status, cutoffRaw, contextRaw, createdRaw, updatedRaw string
	var progressActionID, finalActionID sql.NullInt64
	var lastError sql.NullString
	err := s.db.QueryRowContext(
		context.Background(),
		`SELECT id, work_item_id, task_summary, task_class, context_cutoff,
		        context_digest, status, progress_action_id, final_action_id,
		        last_error, context_messages_json, created_at, updated_at
		 FROM delegated_investigations WHERE work_item_id = ?`,
		workItemID,
	).Scan(
		&investigation.ID,
		&investigation.WorkItemID,
		&investigation.TaskSummary,
		&taskClass,
		&cutoffRaw,
		&investigation.ContextDigest,
		&status,
		&progressActionID,
		&finalActionID,
		&lastError,
		&contextRaw,
		&createdRaw,
		&updatedRaw,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.DelegatedInvestigation{}, false, nil
	}
	if err != nil {
		return domain.DelegatedInvestigation{}, false, errs.NewInternalError(
			errs.SubtypeStorage,
			"get delegated investigation",
		).WithCause(err)
	}
	investigation.TaskClass = domain.TaskClass(taskClass)
	investigation.Status = domain.DelegatedInvestigationStatus(status)
	investigation.ContextCutoff, _ = time.Parse(time.RFC3339Nano, cutoffRaw)
	investigation.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdRaw)
	investigation.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updatedRaw)
	if progressActionID.Valid {
		investigation.ProgressActionID = progressActionID.Int64
	}
	if finalActionID.Valid {
		investigation.FinalActionID = finalActionID.Int64
	}
	if lastError.Valid {
		investigation.LastError = lastError.String
	}
	if err := json.Unmarshal(
		[]byte(contextRaw),
		&investigation.ContextMessages,
	); err != nil {
		return domain.DelegatedInvestigation{}, false, errs.NewInternalError(
			errs.SubtypeStorage,
			"decode delegated investigation context",
		).WithCause(err)
	}
	markExpiredInvestigationImages(investigation.ContextMessages)
	return investigation, true, nil
}

func markExpiredInvestigationImages(messages []domain.NormalizedEvent) {
	for messageIndex := range messages {
		for attachmentIndex := range messages[messageIndex].Attachments {
			attachment := &messages[messageIndex].Attachments[attachmentIndex]
			if attachment.Type != "image" || attachment.DataURL != "" {
				continue
			}
			if attachment.Readable {
				attachment.Readable = false
				attachment.UnreadableReason = "image_bytes_not_persisted"
			}
		}
	}
}

// TransitionDelegatedInvestigation applies one checked state transition.
func (s *Store) TransitionDelegatedInvestigation(
	workItemID int64,
	from domain.DelegatedInvestigationStatus,
	to domain.DelegatedInvestigationStatus,
	lastError string,
) error {
	if !validInvestigationTransition(from, to) {
		return errs.NewValidationError(
			errs.SubtypeInvalidArgument,
			"invalid delegated investigation transition %s -> %s",
			from,
			to,
		)
	}
	result, err := s.db.ExecContext(
		context.Background(),
		`UPDATE delegated_investigations
		 SET status = ?, last_error = ?, updated_at = ?
		 WHERE work_item_id = ? AND status = ?`,
		to,
		strings.TrimSpace(lastError),
		time.Now().UTC().Format(time.RFC3339Nano),
		workItemID,
		from,
	)
	if err != nil {
		return errs.NewInternalError(
			errs.SubtypeStorage,
			"transition delegated investigation",
		).WithCause(err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return errs.NewInternalError(
			errs.SubtypeStorage,
			"read delegated investigation transition result",
		).WithCause(err)
	}
	if affected != 1 {
		return errs.NewInternalError(
			errs.SubtypeFailedPrecondition,
			"delegated investigation state is no longer %s",
			from,
		)
	}
	return nil
}

// MarkDelegatedInvestigationFinalizing idempotently enters finalization.
func (s *Store) MarkDelegatedInvestigationFinalizing(workItemID int64) error {
	investigation, ok, err := s.GetDelegatedInvestigation(workItemID)
	if err != nil || !ok {
		return err
	}
	switch investigation.Status {
	case domain.InvestigationFinalizing, domain.InvestigationCompleted:
		return nil
	case domain.InvestigationInvestigating:
		return s.TransitionDelegatedInvestigation(
			workItemID,
			domain.InvestigationInvestigating,
			domain.InvestigationFinalizing,
			"",
		)
	default:
		return errs.NewInternalError(
			errs.SubtypeFailedPrecondition,
			"delegated investigation cannot finalize from %s",
			investigation.Status,
		)
	}
}

// CompleteDelegatedInvestigation links a completed final reply when present
// and terminalizes the durable investigation before the work item is closed.
func (s *Store) CompleteDelegatedInvestigation(workItemID int64) error {
	investigation, ok, err := s.GetDelegatedInvestigation(workItemID)
	if err != nil || !ok {
		return err
	}
	if investigation.Status == domain.InvestigationCompleted {
		return nil
	}
	if investigation.Status != domain.InvestigationFinalizing {
		return errs.NewInternalError(
			errs.SubtypeFailedPrecondition,
			"delegated investigation cannot complete from %s",
			investigation.Status,
		)
	}
	var finalActionID sql.NullInt64
	err = s.db.QueryRowContext(
		context.Background(),
		`SELECT a.id
		 FROM action_attempts a
		 JOIN work_items w ON w.id = a.work_item_id
		 WHERE w.id = ?
		   AND a.idempotency_key = w.dedup_key || ':reply'
		   AND a.status = ?
		 ORDER BY a.id DESC
		 LIMIT 1`,
		workItemID,
		domain.ActionCompleted,
	).Scan(&finalActionID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return errs.NewInternalError(
			errs.SubtypeStorage,
			"locate delegated investigation final action",
		).WithCause(err)
	}
	result, err := s.db.ExecContext(
		context.Background(),
		`UPDATE delegated_investigations
		 SET status = ?, final_action_id = ?, last_error = '', updated_at = ?
		 WHERE work_item_id = ? AND status = ?`,
		domain.InvestigationCompleted,
		finalActionID,
		time.Now().UTC().Format(time.RFC3339Nano),
		workItemID,
		domain.InvestigationFinalizing,
	)
	if err != nil {
		return errs.NewInternalError(
			errs.SubtypeStorage,
			"complete delegated investigation",
		).WithCause(err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return errs.NewInternalError(
			errs.SubtypeStorage,
			"read delegated investigation completion result",
		).WithCause(err)
	}
	if affected != 1 {
		return errs.NewInternalError(
			errs.SubtypeFailedPrecondition,
			"delegated investigation finalization changed concurrently",
		)
	}
	return nil
}

func validInvestigationTransition(
	from domain.DelegatedInvestigationStatus,
	to domain.DelegatedInvestigationStatus,
) bool {
	switch from {
	case domain.InvestigationPendingProgress:
		return to == domain.InvestigationInvestigating ||
			to == domain.InvestigationBlocked
	case domain.InvestigationInvestigating:
		return to == domain.InvestigationFinalizing ||
			to == domain.InvestigationBlocked
	case domain.InvestigationFinalizing:
		return to == domain.InvestigationCompleted ||
			to == domain.InvestigationBlocked
	default:
		return false
	}
}

// Complete marks a work item completed, ignored, or cancelled according to its
// decision and persists the decision snapshot.
func (s *Store) Complete(id int64, decision domain.Decision) error {
	return s.completeClaim(id, "", decision)
}

// CompleteClaim atomically completes work and its CodingGoal only if the exact
// lease token still owns the item.
func (s *Store) CompleteClaim(id int64, leaseToken string, decision domain.Decision) error {
	return s.completeClaim(id, leaseToken, decision)
}

func (s *Store) completeClaim(id int64, leaseToken string, decision domain.Decision) error {
	status := domain.StatusCompleted
	switch decision.Kind {
	case domain.DecisionIgnore:
		status = domain.StatusIgnored
	case domain.DecisionRequestApproval:
		status = domain.StatusAwaitingApproval
	}
	if decision.WorkKind == "" {
		decision.WorkKind = domain.WorkKindGeneric
	}
	if decision.Priority == 0 {
		decision.Priority = domain.PriorityBackground
	}
	data, err := json.Marshal(decision)
	if err != nil {
		return errs.NewInternalError(errs.SubtypeUnknown, "marshal decision").WithCause(err)
	}
	tx, err := s.db.BeginTx(context.Background(), nil)
	if err != nil {
		return errs.NewInternalError(errs.SubtypeStorage, "begin complete work item").WithCause(err)
	}
	defer tx.Rollback() //nolint:errcheck
	query := `UPDATE work_items
		 SET status = ?, decision_json = ?, work_kind = ?, priority = ?,
		     lease_by = NULL, lease_time = NULL, updated_at = ?
		 WHERE id = ?`
	args := []any{
		status, string(data), decision.WorkKind, decision.Priority,
		time.Now().UTC().Format(time.RFC3339Nano), id,
	}
	if leaseToken != "" {
		query += ` AND status = ? AND lease_by = ?`
		args = append(args, domain.StatusProcessing, leaseToken)
	}
	res, err := tx.ExecContext(context.Background(), query, args...)
	if err != nil {
		return errs.NewInternalError(errs.SubtypeStorage, "complete work item").WithCause(err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return errs.NewInternalError(errs.SubtypeStorage, "read complete result").WithCause(err)
	}
	if affected == 0 {
		return errs.NewValidationError(errs.SubtypeFailedPrecondition, "work item %d was not found", id)
	}
	goalStatus := domain.CodingGoalCompleted
	switch status {
	case domain.StatusIgnored:
		goalStatus = domain.CodingGoalBlocked
	case domain.StatusAwaitingApproval:
		goalStatus = domain.CodingGoalPaused
	default:
		if strings.Contains(decision.Reason, "coding_goal_turn_budget_exhausted") {
			goalStatus = domain.CodingGoalBlocked
		}
	}
	if _, err := tx.ExecContext(context.Background(),
		`UPDATE coding_goals SET status = ?, updated_at = ? WHERE work_item_id = ?`,
		goalStatus, time.Now().UTC().Format(time.RFC3339Nano), id); err != nil {
		return errs.NewInternalError(errs.SubtypeStorage, "finish coding goal").WithCause(err)
	}
	if err := tx.Commit(); err != nil {
		return errs.NewInternalError(errs.SubtypeStorage, "commit complete work item").WithCause(err)
	}
	return nil
}

// MarkRetry releases a failed work item for a later retry.
func (s *Store) MarkRetry(id int64, reason string) error {
	return s.MarkRetryAfter(id, reason, 0)
}

// MarkRetryClaim releases work only if the exact lease token still owns it.
func (s *Store) MarkRetryClaim(id int64, leaseToken, reason string, minimumDelay time.Duration) error {
	return s.markRetryAfter(id, leaseToken, reason, minimumDelay)
}

// MarkDeadLetter records a deterministic permanent failure without retrying.
func (s *Store) MarkDeadLetter(id int64, reason string) error {
	return s.markDeadLetter(id, "", reason)
}

// MarkDeadLetterClaim records a permanent failure only for the exact lease.
func (s *Store) MarkDeadLetterClaim(id int64, leaseToken, reason string) error {
	if strings.TrimSpace(leaseToken) == "" {
		return errs.NewValidationError(
			errs.SubtypeInvalidArgument,
			"dead-letter lease token is required",
		).WithParam("lease_token")
	}
	return s.markDeadLetter(id, leaseToken, reason)
}

func (s *Store) markDeadLetter(id int64, leaseToken, reason string) error {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return errs.NewValidationError(
			errs.SubtypeInvalidArgument,
			"dead-letter reason is required",
		).WithParam("reason")
	}
	data, err := json.Marshal(domain.Decision{
		Kind:   domain.DecisionRecord,
		Reason: reason,
	})
	if err != nil {
		return errs.NewInternalError(
			errs.SubtypeUnknown,
			"marshal dead-letter decision",
		).WithCause(err)
	}
	tx, err := s.db.BeginTx(context.Background(), nil)
	if err != nil {
		return errs.NewInternalError(
			errs.SubtypeStorage,
			"begin permanent dead-letter transition",
		).WithCause(err)
	}
	defer tx.Rollback() //nolint:errcheck
	query := `SELECT retry_count FROM work_items WHERE id = ?`
	args := []any{id}
	if leaseToken != "" {
		query += ` AND status = ? AND lease_by = ?`
		args = append(args, domain.StatusProcessing, leaseToken)
	}
	var retryCount int
	if err := tx.QueryRow(query, args...).Scan(&retryCount); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return errs.NewValidationError(
				errs.SubtypeFailedPrecondition,
				"work item lease was lost",
			)
		}
		return errs.NewInternalError(
			errs.SubtypeStorage,
			"read permanent dead-letter work",
		).WithCause(err)
	}
	retryCount++
	now := time.Now().UTC().Format(time.RFC3339Nano)
	update := `UPDATE work_items
		SET status = ?, decision_json = ?, lease_by = NULL, lease_time = NULL,
		    retry_count = ?, next_attempt_at = NULL, updated_at = ?
		WHERE id = ?`
	updateArgs := []any{
		domain.StatusDeadLetter,
		string(data),
		retryCount,
		now,
		id,
	}
	if leaseToken != "" {
		update += ` AND status = ? AND lease_by = ?`
		updateArgs = append(updateArgs, domain.StatusProcessing, leaseToken)
	}
	result, err := tx.Exec(update, updateArgs...)
	if err != nil {
		return errs.NewInternalError(
			errs.SubtypeStorage,
			"move permanent failure to dead letter",
		).WithCause(err)
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return errs.NewValidationError(
			errs.SubtypeFailedPrecondition,
			"work item lease was lost",
		)
	}
	metadata, err := json.Marshal(map[string]any{
		"retry_count": retryCount,
		"permanent":   true,
	})
	if err != nil {
		return errs.NewInternalError(
			errs.SubtypeUnknown,
			"marshal permanent dead-letter metadata",
		).WithCause(err)
	}
	if _, err := tx.Exec(
		`INSERT INTO dead_letters(work_item_id, reason, metadata_json, created_at)
		 VALUES (?, ?, ?, ?)
		 ON CONFLICT(work_item_id) DO UPDATE SET reason = excluded.reason,
		 metadata_json = excluded.metadata_json, created_at = excluded.created_at`,
		id,
		reason,
		string(metadata),
		now,
	); err != nil {
		return errs.NewInternalError(
			errs.SubtypeStorage,
			"record permanent dead-letter reason",
		).WithCause(err)
	}
	if _, err := tx.Exec(
		`UPDATE coding_goals SET status = ?, updated_at = ? WHERE work_item_id = ?`,
		domain.CodingGoalBlocked,
		now,
		id,
	); err != nil {
		return errs.NewInternalError(
			errs.SubtypeStorage,
			"block permanent dead-letter coding goal",
		).WithCause(err)
	}
	if err := tx.Commit(); err != nil {
		return errs.NewInternalError(
			errs.SubtypeStorage,
			"commit permanent dead-letter transition",
		).WithCause(err)
	}
	return nil
}

// DeferWaitingUserClaim releases an exact processing lease back to the
// semantic owner-reply waiting state.
func (s *Store) DeferWaitingUserClaim(
	id int64,
	leaseToken string,
	reason string,
	delay time.Duration,
) error {
	if delay <= 0 {
		return errs.NewValidationError(
			errs.SubtypeInvalidArgument,
			"delegated reply retry delay must be positive",
		).WithParam("delay")
	}
	now := time.Now().UTC()
	decisionJSON, err := json.Marshal(domain.Decision{
		Kind:   domain.DecisionRecord,
		Reason: reason,
	})
	if err != nil {
		return errs.NewInternalError(
			errs.SubtypeStorage,
			"encode delegated reply defer reason",
		).WithCause(err)
	}
	tx, err := s.db.BeginTx(context.Background(), nil)
	if err != nil {
		return errs.NewInternalError(
			errs.SubtypeStorage,
			"begin delegated reply defer",
		).WithCause(err)
	}
	defer tx.Rollback() //nolint:errcheck
	var retryCount int
	if err := tx.QueryRow(
		`SELECT retry_count
		 FROM work_items
		 WHERE id = ? AND status = ? AND lease_by = ?`,
		id,
		domain.StatusProcessing,
		leaseToken,
	).Scan(&retryCount); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return errs.NewValidationError(
				errs.SubtypeFailedPrecondition,
				"delegated reply lease is no longer current",
			)
		}
		return errs.NewInternalError(
			errs.SubtypeStorage,
			"read delegated reply retry count",
		).WithCause(err)
	}
	retryCount++
	nowRaw := now.Format(time.RFC3339Nano)
	if s.maxRetries > 0 && retryCount >= s.maxRetries {
		result, err := tx.Exec(
			`UPDATE work_items
			 SET status = ?, decision_json = ?, lease_by = NULL, lease_time = NULL,
			     retry_count = ?, next_attempt_at = NULL, updated_at = ?
			 WHERE id = ? AND status = ? AND lease_by = ?`,
			domain.StatusDeadLetter,
			string(decisionJSON),
			retryCount,
			nowRaw,
			id,
			domain.StatusProcessing,
			leaseToken,
		)
		if err != nil {
			return errs.NewInternalError(
				errs.SubtypeStorage,
				"move deferred delegated reply to dead letter",
			).WithCause(err)
		}
		if affected, _ := result.RowsAffected(); affected != 1 {
			return errs.NewValidationError(
				errs.SubtypeFailedPrecondition,
				"delegated reply lease is no longer current",
			)
		}
		metadata, _ := json.Marshal(map[string]any{
			"retry_count": retryCount,
			"source":      "semantic_waiting_user",
		})
		if _, err := tx.Exec(
			`INSERT INTO dead_letters(work_item_id, reason, metadata_json, created_at)
			 VALUES (?, ?, ?, ?)
			 ON CONFLICT(work_item_id) DO UPDATE SET reason = excluded.reason,
				metadata_json = excluded.metadata_json, created_at = excluded.created_at`,
			id,
			reason,
			string(metadata),
			nowRaw,
		); err != nil {
			return errs.NewInternalError(
				errs.SubtypeStorage,
				"record delegated reply dead-letter reason",
			).WithCause(err)
		}
		if _, err := tx.Exec(
			`UPDATE coding_goals SET status = ?, updated_at = ? WHERE work_item_id = ?`,
			domain.CodingGoalBlocked,
			nowRaw,
			id,
		); err != nil {
			return errs.NewInternalError(
				errs.SubtypeStorage,
				"block deferred delegated coding goal",
			).WithCause(err)
		}
		if err := requireOwnerResolutionNotificationTx(
			tx,
			id,
			reason,
			nowRaw,
		); err != nil {
			return err
		}
		if err := tx.Commit(); err != nil {
			return errs.NewInternalError(
				errs.SubtypeStorage,
				"commit delegated reply dead-letter transition",
			).WithCause(err)
		}
		return nil
	}
	result, err := tx.Exec(
		`UPDATE work_items
		 SET status = ?, decision_json = ?, lease_by = NULL, lease_time = NULL,
		     retry_count = ?, next_attempt_at = ?, updated_at = ?
		 WHERE id = ? AND status = ? AND lease_by = ?`,
		domain.StatusWaitingUser,
		string(decisionJSON),
		retryCount,
		now.Add(delay).Format(time.RFC3339Nano),
		nowRaw,
		id,
		domain.StatusProcessing,
		leaseToken,
	)
	if err != nil {
		return errs.NewInternalError(
			errs.SubtypeStorage,
			"defer delegated reply",
		).WithCause(err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return errs.NewInternalError(
			errs.SubtypeStorage,
			"read delegated reply defer result",
		).WithCause(err)
	}
	if affected != 1 {
		return errs.NewValidationError(
			errs.SubtypeFailedPrecondition,
			"delegated reply lease is no longer current",
		)
	}
	if err := tx.Commit(); err != nil {
		return errs.NewInternalError(
			errs.SubtypeStorage,
			"commit delegated reply defer",
		).WithCause(err)
	}
	return nil
}

// MarkRetryAfter honors a provider retry window while preserving the bounded
// exponential retry ceiling.
func (s *Store) MarkRetryAfter(id int64, reason string, minimumDelay time.Duration) error {
	return s.markRetryAfter(id, "", reason, minimumDelay)
}

func (s *Store) markRetryAfter(id int64, leaseToken, reason string, minimumDelay time.Duration) error {
	decision := domain.Decision{Kind: domain.DecisionRecord, Reason: reason}
	data, err := json.Marshal(decision)
	if err != nil {
		return errs.NewInternalError(errs.SubtypeUnknown, "marshal retry decision").WithCause(err)
	}
	tx, err := s.db.BeginTx(context.Background(), nil)
	if err != nil {
		return errs.NewInternalError(errs.SubtypeStorage, "begin work item retry").WithCause(err)
	}
	defer tx.Rollback() //nolint:errcheck
	query := `SELECT retry_count FROM work_items WHERE id = ?`
	args := []any{id}
	if leaseToken != "" {
		query += ` AND status = ? AND lease_by = ?`
		args = append(args, domain.StatusProcessing, leaseToken)
	}
	var retryCount int
	if err := tx.QueryRowContext(context.Background(), query, args...).Scan(&retryCount); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return errs.NewValidationError(errs.SubtypeFailedPrecondition, "work item %d was not found", id)
		}
		return errs.NewInternalError(errs.SubtypeStorage, "read work item retry count").WithCause(err)
	}
	retryCount++
	now := time.Now().UTC()
	if s.maxRetries > 0 && retryCount >= s.maxRetries {
		nowRaw := now.Format(time.RFC3339Nano)
		update := `UPDATE work_items SET status = ?, decision_json = ?, lease_by = NULL, lease_time = NULL,
			        retry_count = ?, next_attempt_at = NULL, updated_at = ? WHERE id = ?`
		updateArgs := []any{domain.StatusDeadLetter, string(data), retryCount, nowRaw, id}
		if leaseToken != "" {
			update += ` AND status = ? AND lease_by = ?`
			updateArgs = append(updateArgs, domain.StatusProcessing, leaseToken)
		}
		result, err := tx.Exec(update, updateArgs...)
		if err != nil {
			return errs.NewInternalError(errs.SubtypeStorage, "move work item to dead letter").WithCause(err)
		}
		if affected, _ := result.RowsAffected(); affected != 1 {
			return errs.NewValidationError(errs.SubtypeFailedPrecondition, "work item lease was lost")
		}
		metadata, _ := json.Marshal(map[string]any{"retry_count": retryCount})
		if _, err := tx.Exec(
			`INSERT INTO dead_letters(work_item_id, reason, metadata_json, created_at)
			 VALUES (?, ?, ?, ?)
			 ON CONFLICT(work_item_id) DO UPDATE SET reason = excluded.reason,
				metadata_json = excluded.metadata_json, created_at = excluded.created_at`,
			id, reason, string(metadata), nowRaw); err != nil {
			return errs.NewInternalError(errs.SubtypeStorage, "record dead-letter reason").WithCause(err)
		}
		if _, err := tx.Exec(
			`UPDATE coding_goals SET status = ?, updated_at = ? WHERE work_item_id = ?`,
			domain.CodingGoalBlocked, nowRaw, id); err != nil {
			return errs.NewInternalError(errs.SubtypeStorage, "block dead-letter coding goal").WithCause(err)
		}
		if err := tx.Commit(); err != nil {
			return errs.NewInternalError(errs.SubtypeStorage, "commit dead-letter transition").WithCause(err)
		}
		return nil
	}
	delay := retryDelay(retryCount)
	if minimumDelay > delay {
		delay = minimumDelay
	}
	if delay > 15*time.Minute {
		delay = 15 * time.Minute
	}
	nextAttempt := now.Add(delay).Format(time.RFC3339Nano)
	update := `UPDATE work_items SET status = ?, decision_json = ?, lease_by = NULL, lease_time = NULL,
		 retry_count = ?, next_attempt_at = ?, updated_at = ? WHERE id = ?`
	updateArgs := []any{domain.StatusRetryWait, string(data), retryCount, nextAttempt, now.Format(time.RFC3339Nano), id}
	if leaseToken != "" {
		update += ` AND status = ? AND lease_by = ?`
		updateArgs = append(updateArgs, domain.StatusProcessing, leaseToken)
	}
	res, err := tx.ExecContext(context.Background(), update, updateArgs...)
	if err != nil {
		return errs.NewInternalError(errs.SubtypeStorage, "mark work item retry").WithCause(err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return errs.NewInternalError(errs.SubtypeStorage, "read retry result").WithCause(err)
	}
	if affected == 0 {
		return errs.NewValidationError(errs.SubtypeFailedPrecondition, "work item %d was not found", id)
	}
	if err := tx.Commit(); err != nil {
		return errs.NewInternalError(errs.SubtypeStorage, "commit work item retry").WithCause(err)
	}
	return nil
}

// RetryWorkItems accelerates ordinary retry-wait work in the active session.
// It never resumes prior-session, interrupted, processing, terminal, or
// uncertain-action work; those states require inspection and explicit resume.
// Passing no ids retries all eligible work in the active session.
func (s *Store) RetryWorkItems(ids []int64) (int64, error) {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if len(ids) == 0 {
		res, err := s.db.ExecContext(context.Background(),
			`UPDATE work_items
			 SET status = ?, decision_json = NULL, lease_by = NULL, lease_time = NULL, retry_count = 0, next_attempt_at = NULL, updated_at = ?
			 WHERE status = ? AND session_id = ?
			   AND NOT EXISTS (
				SELECT 1 FROM action_attempts a
				WHERE a.work_item_id = work_items.id AND a.status IN (?, ?)
			   )`,
			domain.StatusReceived, now, domain.StatusRetryWait, s.session.ID,
			domain.ActionExecuting, domain.ActionBlocked)
		if err != nil {
			return 0, errs.NewInternalError(errs.SubtypeStorage, "retry queued work items").WithCause(err)
		}
		changed, err := res.RowsAffected()
		if err != nil {
			return 0, errs.NewInternalError(errs.SubtypeStorage, "read retry queued work result").WithCause(err)
		}
		return changed, nil
	}
	tx, err := s.db.BeginTx(context.Background(), nil)
	if err != nil {
		return 0, errs.NewInternalError(errs.SubtypeStorage, "begin retry transaction").WithCause(err)
	}
	defer tx.Rollback() //nolint:errcheck // commit path below closes the transaction
	var changed int64
	for _, id := range ids {
		res, err := tx.ExecContext(context.Background(),
			`UPDATE work_items
			 SET status = ?, decision_json = NULL, lease_by = NULL, lease_time = NULL, retry_count = 0, next_attempt_at = NULL, updated_at = ?
			 WHERE id = ? AND status = ? AND session_id = ?
			   AND NOT EXISTS (
				SELECT 1 FROM action_attempts a
				WHERE a.work_item_id = work_items.id AND a.status IN (?, ?)
			   )`,
			domain.StatusReceived, now, id, domain.StatusRetryWait, s.session.ID,
			domain.ActionExecuting, domain.ActionBlocked)
		if err != nil {
			return 0, errs.NewInternalError(errs.SubtypeStorage, "retry queued work item").WithCause(err)
		}
		affected, err := res.RowsAffected()
		if err != nil {
			return 0, errs.NewInternalError(errs.SubtypeStorage, "read retry queued work item result").WithCause(err)
		}
		if affected == 0 {
			return 0, errs.NewValidationError(
				errs.SubtypeFailedPrecondition,
				"work item %d is not eligible for queue retry",
				id,
			).WithHint(
				"Use queue inspect, then queue resume for prior-session, interrupted, or terminal work.",
			)
		}
		changed += affected
	}
	if err := tx.Commit(); err != nil {
		return 0, errs.NewInternalError(errs.SubtypeStorage, "commit retry transaction").WithCause(err)
	}
	return changed, nil
}

func retryDelay(retryCount int) time.Duration {
	if retryCount < 1 {
		retryCount = 1
	}
	shift := retryCount - 1
	if shift > 6 {
		shift = 6
	}
	delay := time.Duration(1<<shift) * 15 * time.Second
	if delay > 15*time.Minute {
		return 15 * time.Minute
	}
	return delay
}

// Get returns a persisted Eino checkpoint.
func (s *Store) Get(ctx context.Context, checkPointID string) ([]byte, bool, error) {
	var data []byte
	err := s.db.QueryRowContext(ctx, `SELECT data FROM checkpoints WHERE id = ?`, checkPointID).Scan(&data)
	if err == sql.ErrNoRows {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, errs.NewInternalError(errs.SubtypeStorage, "read checkpoint").WithCause(err)
	}
	return data, true, nil
}

// Set stores an Eino checkpoint.
func (s *Store) Set(ctx context.Context, checkPointID string, checkPoint []byte) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO checkpoints(id, data, updated_at) VALUES (?, ?, ?)
		 ON CONFLICT(id) DO UPDATE SET data = excluded.data, updated_at = excluded.updated_at`,
		checkPointID, checkPoint, time.Now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		return errs.NewInternalError(errs.SubtypeStorage, "write checkpoint").WithCause(err)
	}
	return nil
}

// Delete removes an Eino checkpoint.
func (s *Store) Delete(ctx context.Context, checkPointID string) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM checkpoints WHERE id = ?`, checkPointID); err != nil {
		return errs.NewInternalError(errs.SubtypeStorage, "delete checkpoint").WithCause(err)
	}
	return nil
}

type rowScanner interface {
	Scan(dest ...any) error
}

const workItemSelect = `SELECT id, dedup_key, status, work_kind, priority,
		duplicate_of, session_id, event_json, lease_by, lease_time, retry_count,
		next_attempt_at, created_at, updated_at
	FROM work_items`

func scanWorkItem(row rowScanner) (domain.WorkItem, error) {
	var item domain.WorkItem
	var status, workKind, eventJSON, createdAt, updatedAt string
	var sessionID, leaseBy, leaseTime, nextAttemptAt sql.NullString
	var duplicateOf sql.NullInt64
	if err := row.Scan(&item.ID, &item.DedupKey, &status, &workKind,
		&item.Priority, &duplicateOf, &sessionID, &eventJSON, &leaseBy,
		&leaseTime, &item.RetryCount, &nextAttemptAt, &createdAt, &updatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return domain.WorkItem{}, err
		}
		return domain.WorkItem{}, errs.NewInternalError(errs.SubtypeStorage, "scan work item").WithCause(err)
	}
	item.Status = domain.WorkItemStatus(status)
	item.WorkKind = domain.WorkKind(workKind)
	if item.WorkKind == "" {
		item.WorkKind = domain.WorkKindGeneric
	}
	if duplicateOf.Valid {
		item.DuplicateOf = duplicateOf.Int64
	}
	if sessionID.Valid {
		item.SessionID = sessionID.String
	}
	if err := json.Unmarshal([]byte(eventJSON), &item.Event); err != nil {
		return domain.WorkItem{}, errs.NewInternalError(errs.SubtypeStorage, "decode work item event").WithCause(err)
	}
	if leaseBy.Valid {
		item.LeaseBy = leaseBy.String
	}
	if leaseTime.Valid && leaseTime.String != "" {
		item.LeaseTime, _ = time.Parse(time.RFC3339Nano, leaseTime.String)
	}
	if nextAttemptAt.Valid && nextAttemptAt.String != "" {
		item.NextAttemptAt, _ = time.Parse(time.RFC3339Nano, nextAttemptAt.String)
	}
	item.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
	item.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updatedAt)
	return item, nil
}

func scanResourceSubscription(row rowScanner) (domain.ResourceSubscription, error) {
	var sub domain.ResourceSubscription
	var fileToken, appToken, wikiNodeToken, tableID, viewID sql.NullString
	var remoteID, cursor, lastError sql.NullString
	var modesJSON, status, createdAt, updatedAt string
	if err := row.Scan(&sub.ID, &sub.OriginalURL, &sub.ResourceType, &fileToken, &appToken,
		&wikiNodeToken, &tableID, &viewID, &modesJSON, &remoteID, &status, &cursor,
		&lastError, &createdAt, &updatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return domain.ResourceSubscription{}, err
		}
		return domain.ResourceSubscription{}, errs.NewInternalError(errs.SubtypeStorage, "scan resource subscription").WithCause(err)
	}
	sub.FileToken = nullableString(fileToken)
	sub.AppToken = nullableString(appToken)
	sub.WikiNodeToken = nullableString(wikiNodeToken)
	sub.TableID = nullableString(tableID)
	sub.ViewID = nullableString(viewID)
	sub.RemoteSubscriptionID = nullableString(remoteID)
	sub.Cursor = nullableString(cursor)
	sub.LastError = nullableString(lastError)
	sub.Status = domain.ResourceSubscriptionStatus(status)
	if modesJSON != "" {
		if err := json.Unmarshal([]byte(modesJSON), &sub.MonitorModes); err != nil {
			return domain.ResourceSubscription{}, errs.NewInternalError(errs.SubtypeStorage, "decode resource subscription monitor modes").WithCause(err)
		}
	}
	sub.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
	sub.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updatedAt)
	return sub, nil
}

func nullableString(value sql.NullString) string {
	if !value.Valid {
		return ""
	}
	return value.String
}
