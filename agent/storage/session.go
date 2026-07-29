package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/liuchong/lark-agent/agent/domain"
	errs "github.com/liuchong/lark-agent/internal/apperr"
)

func (s *Store) loadActiveSession(ctx context.Context) error {
	var id string
	err := s.db.QueryRowContext(ctx,
		`SELECT id FROM online_sessions
		 WHERE status IN (?, ?)
		 ORDER BY started_at DESC, id DESC LIMIT 1`,
		domain.OnlineSessionStarting, domain.OnlineSessionReady).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return errs.NewInternalError(
			errs.SubtypeStorage,
			"locate active online session",
		).WithCause(err)
	}
	session, err := s.GetOnlineSession(ctx, id)
	if err != nil {
		return err
	}
	s.session = session
	return nil
}

func (s *Store) createOnlineSession(ctx context.Context) (domain.OnlineSession, error) {
	id, err := randomID()
	if err != nil {
		return domain.OnlineSession{}, err
	}
	session := domain.OnlineSession{
		ID:        "session-" + id,
		Status:    domain.OnlineSessionStarting,
		StartedAt: time.Now().UTC(),
	}
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO online_sessions(id, status, started_at) VALUES (?, ?, ?)`,
		session.ID, session.Status, session.StartedAt.Format(time.RFC3339Nano)); err != nil {
		return domain.OnlineSession{}, errs.NewInternalError(
			errs.SubtypeStorage,
			"create online session",
		).WithCause(err)
	}
	return session, nil
}

func (s *Store) CurrentSession() domain.OnlineSession {
	if s == nil {
		return domain.OnlineSession{}
	}
	return s.session
}

func (s *Store) MarkCurrentSessionReady(ctx context.Context) (domain.OnlineSession, error) {
	if s.session.ID == "" {
		return domain.OnlineSession{}, errs.NewValidationError(
			errs.SubtypeFailedPrecondition,
			"online session is not initialized",
		)
	}
	now := time.Now().UTC()
	result, err := s.db.ExecContext(ctx,
		`UPDATE online_sessions SET status = ?, ready_at = ?
		 WHERE id = ? AND status = ?`,
		domain.OnlineSessionReady, now.Format(time.RFC3339Nano),
		s.session.ID, domain.OnlineSessionStarting)
	if err != nil {
		return domain.OnlineSession{}, errs.NewInternalError(
			errs.SubtypeStorage,
			"mark online session ready",
		).WithCause(err)
	}
	affected, _ := result.RowsAffected()
	if affected != 1 {
		return domain.OnlineSession{}, errs.NewValidationError(
			errs.SubtypeFailedPrecondition,
			"online session cannot transition to ready",
		)
	}
	s.session.Status = domain.OnlineSessionReady
	s.session.ReadyAt = now
	return s.session, nil
}

func (s *Store) StopCurrentSession(
	ctx context.Context,
	reason string,
) (domain.OnlineSession, error) {
	if s.session.ID == "" {
		return domain.OnlineSession{}, errs.NewValidationError(
			errs.SubtypeFailedPrecondition,
			"online session is not initialized",
		)
	}
	if s.session.Status == domain.OnlineSessionStopped {
		return s.session, nil
	}
	now := time.Now().UTC()
	if _, err := s.db.ExecContext(ctx,
		`UPDATE online_sessions SET status = ?, ended_at = ?, reason = ? WHERE id = ?`,
		domain.OnlineSessionStopped, now.Format(time.RFC3339Nano), reason, s.session.ID); err != nil {
		return domain.OnlineSession{}, errs.NewInternalError(
			errs.SubtypeStorage,
			"stop online session",
		).WithCause(err)
	}
	s.session.Status = domain.OnlineSessionStopped
	s.session.EndedAt = now
	s.session.Reason = reason
	return s.session, nil
}

func (s *Store) GetOnlineSession(
	ctx context.Context,
	id string,
) (domain.OnlineSession, error) {
	var session domain.OnlineSession
	var status, started string
	var ready, ended, reason sql.NullString
	err := s.db.QueryRowContext(ctx,
		`SELECT id, status, started_at, ready_at, ended_at, reason
		 FROM online_sessions WHERE id = ?`, id).
		Scan(&session.ID, &status, &started, &ready, &ended, &reason)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.OnlineSession{}, errs.NewValidationError(
			errs.SubtypeInvalidArgument,
			"online session was not found",
		).WithParam("session_id")
	}
	if err != nil {
		return domain.OnlineSession{}, errs.NewInternalError(
			errs.SubtypeStorage,
			"read online session",
		).WithCause(err)
	}
	session.Status = domain.OnlineSessionStatus(status)
	session.StartedAt, _ = time.Parse(time.RFC3339Nano, started)
	if ready.Valid {
		session.ReadyAt, _ = time.Parse(time.RFC3339Nano, ready.String)
	}
	if ended.Valid {
		session.EndedAt, _ = time.Parse(time.RFC3339Nano, ended.String)
	}
	if reason.Valid {
		session.Reason = reason.String
	}
	return session, nil
}

func (s *Store) RecordIntake(
	ctx context.Context,
	event domain.NormalizedEvent,
) (domain.IntakeReceipt, error) {
	return s.recordIntake(ctx, domain.NewWorkItem(event), false)
}

// RecordWorkIntake atomically records one receipt and its classified
// scheduling metadata before the item becomes claimable.
func (s *Store) RecordWorkIntake(
	ctx context.Context,
	item domain.WorkItem,
) (domain.IntakeReceipt, error) {
	item = normalizeIntakeWorkItem(item)
	return s.recordIntake(ctx, item, false)
}

// RecordBackfillWorkIntake admits one explicitly backfilled work item even when
// its event predates the current online session.
func (s *Store) RecordBackfillWorkIntake(
	ctx context.Context,
	item domain.WorkItem,
) (domain.IntakeReceipt, error) {
	item = normalizeIntakeWorkItem(item)
	return s.recordIntake(ctx, item, true)
}

func normalizeIntakeWorkItem(item domain.WorkItem) domain.WorkItem {
	if item.DedupKey == "" {
		item.DedupKey = domain.DedupKey(item.Event)
	}
	if item.Status == "" {
		item.Status = domain.StatusReceived
	}
	if item.WorkKind == "" {
		item.WorkKind = domain.WorkKindGeneric
	}
	return item
}

func (s *Store) recordIntake(
	ctx context.Context,
	item domain.WorkItem,
	admitHistorical bool,
) (domain.IntakeReceipt, error) {
	event := item.Event
	if event.MessageID == "" && event.EventID == "" {
		return domain.IntakeReceipt{}, errs.NewValidationError(
			errs.SubtypeInvalidArgument,
			"intake event requires message_id or event_id",
		)
	}
	if receipt, ok, err := s.findIntakeReceipt(ctx, event.MessageID, event.EventID); err != nil {
		return domain.IntakeReceipt{}, err
	} else if ok {
		if admitHistorical &&
			receipt.WorkItemID == 0 {
			return s.admitBackfillReceipt(ctx, receipt, item)
		}
		return s.recordDuplicateIntake(ctx, item, receipt)
	}
	eventJSON, err := json.Marshal(event)
	if err != nil {
		return domain.IntakeReceipt{}, errs.NewInternalError(
			errs.SubtypeUnknown,
			"encode intake event",
		).WithCause(err)
	}
	now := time.Now().UTC()
	receipt := domain.IntakeReceipt{
		MessageID:      event.MessageID,
		EventID:        event.EventID,
		Source:         event.Source,
		SessionID:      s.session.ID,
		EventJSON:      string(eventJSON),
		EventCreatedAt: event.CreatedAt,
		ObservedAt:     now,
		Disposition:    domain.IntakeAdmitted,
	}
	// Public CLI projections can expose timestamps at whole-second precision.
	// Compare against the session floor at that same precision so a message
	// created during the startup second is not misclassified as old backlog.
	sessionFloor := s.session.StartedAt.Truncate(time.Second)
	if !admitHistorical && (event.CreatedAt.IsZero() || event.CreatedAt.Before(sessionFloor)) {
		receipt.Disposition = domain.IntakeOfflineBacklog
		receipt.Reason = "event predates the current online session or lacks trusted creation time"
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.IntakeReceipt{}, errs.NewInternalError(
			errs.SubtypeStorage,
			"begin intake receipt",
		).WithCause(err)
	}
	defer tx.Rollback() //nolint:errcheck
	if receipt.Disposition == domain.IntakeAdmitted {
		result, insertErr := tx.ExecContext(ctx,
			`INSERT OR IGNORE INTO work_items(
				dedup_key, status, work_kind, priority, session_id, event_json,
				next_attempt_at, created_at, updated_at
			 ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			item.DedupKey, item.Status, item.WorkKind, item.Priority, s.session.ID,
			string(eventJSON), nullableTime(item.NextAttemptAt),
			now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano))
		if insertErr != nil {
			return domain.IntakeReceipt{}, errs.NewInternalError(
				errs.SubtypeStorage,
				"admit intake work",
			).WithCause(insertErr)
		}
		if affected, _ := result.RowsAffected(); affected == 1 {
			receipt.WorkItemID, err = lastInsertID(result)
			if err != nil {
				return domain.IntakeReceipt{}, err
			}
		} else {
			if err := tx.QueryRowContext(ctx,
				`SELECT id FROM work_items WHERE dedup_key = ?`, item.DedupKey).
				Scan(&receipt.WorkItemID); err != nil {
				return domain.IntakeReceipt{}, errs.NewInternalError(
					errs.SubtypeStorage,
					"locate duplicate intake work",
				).WithCause(err)
			}
			receipt.Disposition = domain.IntakeDuplicate
			receipt.Reason = "work item already exists"
		}
	}
	createdAt := nullableTime(receipt.EventCreatedAt)
	result, err := tx.ExecContext(ctx,
		`INSERT INTO intake_receipts(
			message_id, event_id, source, session_id, event_json, event_created_at,
			observed_at, disposition, reason, work_item_id
		 ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		receipt.MessageID, receipt.EventID, receipt.Source, receipt.SessionID,
		receipt.EventJSON, createdAt, receipt.ObservedAt.Format(time.RFC3339Nano),
		receipt.Disposition, receipt.Reason, nullableID(receipt.WorkItemID))
	if err != nil {
		if existing, ok, findErr := s.findIntakeReceipt(ctx, event.MessageID, event.EventID); findErr == nil && ok {
			return existing, nil
		}
		return domain.IntakeReceipt{}, errs.NewInternalError(
			errs.SubtypeStorage,
			"record intake receipt",
		).WithCause(err)
	}
	receipt.ID, err = lastInsertID(result)
	if err != nil {
		return domain.IntakeReceipt{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.IntakeReceipt{}, errs.NewInternalError(
			errs.SubtypeStorage,
			"commit intake receipt",
		).WithCause(err)
	}
	return receipt, nil
}

func (s *Store) admitBackfillReceipt(
	ctx context.Context,
	receipt domain.IntakeReceipt,
	item domain.WorkItem,
) (domain.IntakeReceipt, error) {
	eventJSON, err := json.Marshal(item.Event)
	if err != nil {
		return domain.IntakeReceipt{}, errs.NewInternalError(
			errs.SubtypeUnknown,
			"encode backfill intake event",
		).WithCause(err)
	}
	now := time.Now().UTC()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.IntakeReceipt{}, errs.NewInternalError(
			errs.SubtypeStorage,
			"begin backfill intake",
		).WithCause(err)
	}
	defer tx.Rollback() //nolint:errcheck
	result, err := tx.ExecContext(ctx,
		`INSERT OR IGNORE INTO work_items(
			dedup_key, status, work_kind, priority, session_id, event_json,
			next_attempt_at, created_at, updated_at
		 ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		item.DedupKey, item.Status, item.WorkKind, item.Priority, s.session.ID,
		string(eventJSON), nullableTime(item.NextAttemptAt),
		now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano))
	if err != nil {
		return domain.IntakeReceipt{}, errs.NewInternalError(
			errs.SubtypeStorage,
			"admit backfilled intake work",
		).WithCause(err)
	}
	var workID int64
	if affected, _ := result.RowsAffected(); affected == 1 {
		workID, err = lastInsertID(result)
		if err != nil {
			return domain.IntakeReceipt{}, err
		}
	} else if err := tx.QueryRowContext(ctx,
		`SELECT id FROM work_items WHERE dedup_key = ?`, item.DedupKey).
		Scan(&workID); err != nil {
		return domain.IntakeReceipt{}, errs.NewInternalError(
			errs.SubtypeStorage,
			"locate backfilled intake work",
		).WithCause(err)
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE intake_receipts
		    SET disposition = ?, reason = ?, work_item_id = ?, session_id = ?,
		        event_json = ?, observed_at = ?
		  WHERE id = ?`,
		domain.IntakeAdmitted, "explicitly backfilled by owner", workID,
		s.session.ID, string(eventJSON), now.Format(time.RFC3339Nano), receipt.ID); err != nil {
		return domain.IntakeReceipt{}, errs.NewInternalError(
			errs.SubtypeStorage,
			"link backfilled intake receipt",
		).WithCause(err)
	}
	if err := tx.Commit(); err != nil {
		return domain.IntakeReceipt{}, errs.NewInternalError(
			errs.SubtypeStorage,
			"commit backfill intake",
		).WithCause(err)
	}
	receipt.Disposition = domain.IntakeAdmitted
	receipt.Reason = "explicitly backfilled by owner"
	receipt.WorkItemID = workID
	receipt.SessionID = s.session.ID
	receipt.EventJSON = string(eventJSON)
	receipt.ObservedAt = now
	return receipt, nil
}

func (s *Store) recordDuplicateIntake(
	ctx context.Context,
	item domain.WorkItem,
	existing domain.IntakeReceipt,
) (domain.IntakeReceipt, error) {
	event := item.Event
	eventJSON, err := json.Marshal(event)
	if err != nil {
		return domain.IntakeReceipt{}, errs.NewInternalError(
			errs.SubtypeUnknown,
			"encode duplicate intake event",
		).WithCause(err)
	}
	receipt := domain.IntakeReceipt{
		MessageID:      event.MessageID,
		EventID:        event.EventID,
		Source:         event.Source,
		SessionID:      s.session.ID,
		EventJSON:      string(eventJSON),
		EventCreatedAt: event.CreatedAt,
		ObservedAt:     time.Now().UTC(),
		Disposition:    domain.IntakeDuplicate,
		Reason:         "message or event was already observed",
		WorkItemID:     existing.WorkItemID,
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.IntakeReceipt{}, errs.NewInternalError(
			errs.SubtypeStorage,
			"begin duplicate intake",
		).WithCause(err)
	}
	defer tx.Rollback() //nolint:errcheck
	result, err := tx.ExecContext(ctx,
		`INSERT INTO intake_receipts(
			message_id, event_id, source, session_id, event_json, event_created_at,
			observed_at, disposition, reason, work_item_id
		 ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		receipt.MessageID, receipt.EventID, receipt.Source, receipt.SessionID,
		receipt.EventJSON, nullableTime(receipt.EventCreatedAt),
		receipt.ObservedAt.Format(time.RFC3339Nano), receipt.Disposition,
		receipt.Reason, nullableID(receipt.WorkItemID))
	if err != nil {
		return domain.IntakeReceipt{}, errs.NewInternalError(
			errs.SubtypeStorage,
			"record duplicate intake receipt",
		).WithCause(err)
	}
	receipt.ID, err = lastInsertID(result)
	if err != nil {
		return domain.IntakeReceipt{}, err
	}
	if existing.WorkItemID != 0 {
		var statusRaw, existingJSON string
		if err := tx.QueryRowContext(
			ctx,
			`SELECT status, event_json FROM work_items WHERE id = ?`,
			existing.WorkItemID,
		).Scan(&statusRaw, &existingJSON); err != nil {
			return domain.IntakeReceipt{}, errs.NewInternalError(
				errs.SubtypeStorage,
				"read duplicate waiting work",
			).WithCause(err)
		}
		var existingEvent domain.NormalizedEvent
		if err := json.Unmarshal([]byte(existingJSON), &existingEvent); err != nil {
			return domain.IntakeReceipt{}, errs.NewInternalError(
				errs.SubtypeStorage,
				"decode duplicate waiting work",
			).WithCause(err)
		}
		newerEdit := !event.UpdatedAt.IsZero() &&
			(existingEvent.UpdatedAt.IsZero() || event.UpdatedAt.After(existingEvent.UpdatedAt))
		hydratesEmpty := strings.TrimSpace(existingEvent.Content) == "" &&
			strings.TrimSpace(event.Content) != ""
		if domain.WorkItemStatus(statusRaw) == domain.StatusWaitingUser &&
			(newerEdit || hydratesEmpty) {
			if _, err := tx.ExecContext(
				ctx,
				`UPDATE work_items
				 SET event_json = ?, next_attempt_at = ?, updated_at = ?
				 WHERE id = ? AND status = ?`,
				string(eventJSON),
				nullableTime(item.NextAttemptAt),
				receipt.ObservedAt.Format(time.RFC3339Nano),
				existing.WorkItemID,
				domain.StatusWaitingUser,
			); err != nil {
				return domain.IntakeReceipt{}, errs.NewInternalError(
					errs.SubtypeStorage,
					"update edited waiting work",
				).WithCause(err)
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return domain.IntakeReceipt{}, errs.NewInternalError(
			errs.SubtypeStorage,
			"commit duplicate intake",
		).WithCause(err)
	}
	return receipt, nil
}

func (s *Store) findIntakeReceipt(
	ctx context.Context,
	messageID, eventID string,
) (domain.IntakeReceipt, bool, error) {
	query := `SELECT id, message_id, event_id, source, session_id, event_json,
	                 event_created_at, observed_at, disposition, reason, work_item_id
	          FROM intake_receipts WHERE `
	var value string
	switch {
	case messageID != "":
		query += `message_id = ?`
		value = messageID
	case eventID != "":
		query += `event_id = ?`
		value = eventID
	default:
		return domain.IntakeReceipt{}, false, nil
	}
	query += ` ORDER BY observed_at DESC, id DESC LIMIT 1`
	receipt, err := scanIntakeReceipt(s.db.QueryRowContext(ctx, query, value))
	if errors.Is(err, sql.ErrNoRows) {
		return domain.IntakeReceipt{}, false, nil
	}
	if err != nil {
		return domain.IntakeReceipt{}, false, err
	}
	return receipt, true, nil
}

func (s *Store) RecoverInterruptedWork(ctx context.Context, reason string) (int, error) {
	return s.interruptSessionWork(ctx, reason, false)
}

// PauseCurrentSessionWork snapshots all unfinished work during an intentional
// shutdown so the offline notice and the next startup observe the same state.
func (s *Store) PauseCurrentSessionWork(ctx context.Context, reason string) (int, error) {
	return s.interruptSessionWork(ctx, reason, true)
}

func (s *Store) interruptSessionWork(
	ctx context.Context,
	reason string,
	currentSession bool,
) (int, error) {
	if !currentSession {
		now := time.Now().UTC().Format(time.RFC3339Nano)
		if _, err := s.db.ExecContext(
			ctx,
			`UPDATE work_items
			 SET session_id = ?, decision_json = NULL, lease_by = NULL,
			     lease_time = NULL, updated_at = ?
			 WHERE status = ?
			   AND COALESCE(session_id, '') <> ?
			   AND NOT EXISTS (
				SELECT 1 FROM agent_runs
				WHERE agent_runs.work_item_id = work_items.id
			   )
			   AND NOT EXISTS (
				SELECT 1 FROM action_attempts
				WHERE action_attempts.work_item_id = work_items.id
			   )`,
			s.session.ID,
			now,
			domain.StatusWaitingUser,
			s.session.ID,
		); err != nil {
			return 0, errs.NewInternalError(
				errs.SubtypeStorage,
				"readmit pristine waiting work",
			).WithCause(err)
		}
	}
	query := `SELECT id, COALESCE(session_id, '') FROM work_items
	 WHERE status IN (?, ?, ?, ?, ?, ?, ?, ?)
	   AND COALESCE(session_id, '') <> ?
	   AND NOT (
		status = ?
		AND NOT EXISTS (
			SELECT 1 FROM agent_runs
			WHERE agent_runs.work_item_id = work_items.id
		)
		AND NOT EXISTS (
			SELECT 1 FROM action_attempts
			WHERE action_attempts.work_item_id = work_items.id
		)
	   )`
	if currentSession {
		query = `SELECT id, COALESCE(session_id, '') FROM work_items
		 WHERE status IN (?, ?, ?, ?, ?, ?, ?, ?)
		   AND COALESCE(session_id, '') = ?
		   AND NOT (
			status = ?
			AND NOT EXISTS (
				SELECT 1 FROM agent_runs
				WHERE agent_runs.work_item_id = work_items.id
			)
			AND NOT EXISTS (
				SELECT 1 FROM action_attempts
				WHERE action_attempts.work_item_id = work_items.id
			)
		   )`
	}
	rows, err := s.db.QueryContext(ctx, query,
		domain.StatusReceived, domain.StatusRouted, domain.StatusWaitingUser,
		domain.StatusReady, domain.StatusProcessing, domain.StatusAwaitingApproval,
		domain.StatusExecuting, domain.StatusRetryWait, s.session.ID,
		domain.StatusWaitingUser)
	if err != nil {
		return 0, errs.NewInternalError(
			errs.SubtypeStorage,
			"list work interrupted by prior sessions",
		).WithCause(err)
	}
	type priorWork struct {
		id        int64
		sessionID string
	}
	var work []priorWork
	for rows.Next() {
		var item priorWork
		if err := rows.Scan(&item.id, &item.sessionID); err != nil {
			_ = rows.Close()
			return 0, errs.NewInternalError(
				errs.SubtypeStorage,
				"scan work interrupted by prior sessions",
			).WithCause(err)
		}
		work = append(work, item)
	}
	if err := rows.Close(); err != nil {
		return 0, errs.NewInternalError(
			errs.SubtypeStorage,
			"close interrupted work list",
		).WithCause(err)
	}
	if len(work) == 0 {
		return 0, nil
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, errs.NewInternalError(
			errs.SubtypeStorage,
			"begin interrupted work recovery",
		).WithCause(err)
	}
	defer tx.Rollback() //nolint:errcheck
	now := time.Now().UTC()
	for _, item := range work {
		snapshot, err := interruptionSnapshot(ctx, tx, item.id, item.sessionID, reason, now)
		if err != nil {
			return 0, err
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE agent_runs SET status = ?, last_error = ?, completed_at = ?
			 WHERE work_item_id = ? AND status = ?`,
			domain.AgentRunAbandoned, reason, now.Format(time.RFC3339Nano),
			item.id, domain.AgentRunRunning); err != nil {
			return 0, errs.NewInternalError(
				errs.SubtypeStorage,
				"abandon prior-session agent run",
			).WithCause(err)
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE action_attempts SET status = ?, error = ?, updated_at = ?
			 WHERE work_item_id = ? AND status = ?`,
			domain.ActionBlocked, "result uncertain after process interruption",
			now.Format(time.RFC3339Nano), item.id, domain.ActionExecuting); err != nil {
			return 0, errs.NewInternalError(
				errs.SubtypeStorage,
				"block uncertain prior-session action",
			).WithCause(err)
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE work_items SET status = ?, lease_by = NULL, lease_time = NULL,
			        next_attempt_at = NULL, updated_at = ? WHERE id = ?`,
			domain.StatusInterrupted, now.Format(time.RFC3339Nano), item.id); err != nil {
			return 0, errs.NewInternalError(
				errs.SubtypeStorage,
				"pause prior-session work",
			).WithCause(err)
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO work_interruptions(
				work_item_id, run_id, session_id, stage, last_sequence, last_kind,
				last_tool, action_kind, action_status, reason, interrupted_at
			 ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			snapshot.WorkItemID, nullString(snapshot.RunID), nullString(snapshot.SessionID),
			snapshot.Stage, snapshot.LastSequence, nullString(snapshot.LastKind),
			nullString(snapshot.LastTool), nullString(snapshot.ActionKind),
			nullString(string(snapshot.ActionStatus)), snapshot.Reason,
			snapshot.InterruptedAt.Format(time.RFC3339Nano)); err != nil {
			return 0, errs.NewInternalError(
				errs.SubtypeStorage,
				"record interruption snapshot",
			).WithCause(err)
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, errs.NewInternalError(
			errs.SubtypeStorage,
			"commit interrupted work recovery",
		).WithCause(err)
	}
	return len(work), nil
}

// BeginLifecycleNotification persists intent before an owner-visible lifecycle
// message is sent. An executing record is uncertain after a crash and is never
// automatically sent again.
func (s *Store) BeginLifecycleNotification(
	ctx context.Context,
	idempotencyKey, text string,
) (int64, domain.ActionStatus, bool, error) {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	result, err := s.db.ExecContext(ctx,
		`INSERT OR IGNORE INTO lifecycle_actions(
			idempotency_key, status, request_text, created_at, updated_at
		 ) VALUES (?, ?, ?, ?, ?)`,
		idempotencyKey, domain.ActionExecuting, text, now, now)
	if err != nil {
		return 0, "", false, errs.NewInternalError(
			errs.SubtypeStorage,
			"begin lifecycle notification",
		).WithCause(err)
	}
	if affected, _ := result.RowsAffected(); affected == 1 {
		id, err := lastInsertID(result)
		return id, domain.ActionExecuting, true, err
	}
	var id int64
	var status string
	if err := s.db.QueryRowContext(ctx,
		`SELECT id, status FROM lifecycle_actions WHERE idempotency_key = ?`,
		idempotencyKey).Scan(&id, &status); err != nil {
		return 0, "", false, errs.NewInternalError(
			errs.SubtypeStorage,
			"read lifecycle notification",
		).WithCause(err)
	}
	current := domain.ActionStatus(status)
	if current != domain.ActionBlocked {
		return id, current, false, nil
	}
	if _, err := s.db.ExecContext(ctx,
		`UPDATE lifecycle_actions
		 SET status = ?, request_text = ?, error = NULL, updated_at = ?
		 WHERE id = ? AND status = ?`,
		domain.ActionExecuting, text, now, id, domain.ActionBlocked); err != nil {
		return 0, "", false, errs.NewInternalError(
			errs.SubtypeStorage,
			"retry lifecycle notification",
		).WithCause(err)
	}
	return id, domain.ActionExecuting, true, nil
}

func (s *Store) CompleteLifecycleNotification(
	ctx context.Context,
	actionID int64,
	sendErr string,
) error {
	status := domain.ActionCompleted
	if sendErr != "" {
		status = domain.ActionBlocked
	}
	result, err := s.db.ExecContext(ctx,
		`UPDATE lifecycle_actions SET status = ?, error = ?, updated_at = ?
		 WHERE id = ? AND status = ?`,
		status, sendErr, time.Now().UTC().Format(time.RFC3339Nano),
		actionID, domain.ActionExecuting)
	if err != nil {
		return errs.NewInternalError(
			errs.SubtypeStorage,
			"complete lifecycle notification",
		).WithCause(err)
	}
	affected, _ := result.RowsAffected()
	if affected != 1 {
		return errs.NewValidationError(
			errs.SubtypeFailedPrecondition,
			"lifecycle notification is not executing",
		)
	}
	return nil
}

// RecoverySummary returns unresolved cross-session work and external actions.
func (s *Store) RecoverySummary(ctx context.Context) (int, int, error) {
	var interrupted int
	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM work_items WHERE status = ?`,
		domain.StatusInterrupted).Scan(&interrupted); err != nil {
		return 0, 0, errs.NewInternalError(
			errs.SubtypeStorage,
			"count interrupted work",
		).WithCause(err)
	}
	var uncertainWork, uncertainLifecycle int
	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(DISTINCT work_item_id) FROM work_interruptions
		 WHERE action_status = ? AND resumed_at IS NULL`,
		domain.ActionExecuting).Scan(&uncertainWork); err != nil {
		return 0, 0, errs.NewInternalError(
			errs.SubtypeStorage,
			"count uncertain work actions",
		).WithCause(err)
	}
	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM lifecycle_actions WHERE status = ?`,
		domain.ActionExecuting).Scan(&uncertainLifecycle); err != nil {
		return 0, 0, errs.NewInternalError(
			errs.SubtypeStorage,
			"count uncertain lifecycle actions",
		).WithCause(err)
	}
	return interrupted, uncertainWork + uncertainLifecycle, nil
}

func interruptionSnapshot(
	ctx context.Context,
	tx *sql.Tx,
	workItemID int64,
	sessionID, reason string,
	now time.Time,
) (domain.WorkInterruption, error) {
	snapshot := domain.WorkInterruption{
		WorkItemID:    workItemID,
		SessionID:     sessionID,
		Stage:         domain.InterruptionStageQueue,
		Reason:        reason,
		InterruptedAt: now,
	}
	var runStatus string
	err := tx.QueryRowContext(ctx,
		`SELECT id, status FROM agent_runs
		 WHERE work_item_id = ? ORDER BY started_at DESC, id DESC LIMIT 1`,
		workItemID).Scan(&snapshot.RunID, &runStatus)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return snapshot, errs.NewInternalError(
			errs.SubtypeStorage,
			"read latest run for interruption",
		).WithCause(err)
	}
	if err == nil {
		snapshot.Stage = domain.InterruptionStageModel
		var sequence sql.NullInt64
		var kind, tool sql.NullString
		stepErr := tx.QueryRowContext(ctx,
			`SELECT sequence, kind, tool_name FROM agent_steps
			 WHERE run_id = ? ORDER BY sequence DESC LIMIT 1`,
			snapshot.RunID).Scan(&sequence, &kind, &tool)
		if stepErr != nil && !errors.Is(stepErr, sql.ErrNoRows) {
			return snapshot, errs.NewInternalError(
				errs.SubtypeStorage,
				"read latest step for interruption",
			).WithCause(stepErr)
		}
		if stepErr == nil {
			snapshot.LastSequence = int(sequence.Int64)
			snapshot.LastKind = kind.String
			snapshot.LastTool = tool.String
			if snapshot.LastTool != "" || snapshot.LastKind == "tool" {
				snapshot.Stage = domain.InterruptionStageTool
			}
		}
	}
	var actionStatus string
	var actionKind string
	actionErr := tx.QueryRowContext(ctx,
		`SELECT kind, status FROM action_attempts
		 WHERE work_item_id = ? ORDER BY id DESC LIMIT 1`,
		workItemID).Scan(&actionKind, &actionStatus)
	if actionErr != nil && !errors.Is(actionErr, sql.ErrNoRows) {
		return snapshot, errs.NewInternalError(
			errs.SubtypeStorage,
			"read latest action for interruption",
		).WithCause(actionErr)
	}
	if actionErr == nil {
		snapshot.ActionKind = actionKind
		snapshot.ActionStatus = domain.ActionStatus(actionStatus)
		if snapshot.ActionStatus == domain.ActionExecuting {
			snapshot.Stage = domain.InterruptionStageActionExecution
		}
	}
	return snapshot, nil
}

func (s *Store) InspectWork(
	ctx context.Context,
	query domain.WorkInspectionQuery,
) (domain.WorkInspection, error) {
	if (query.WorkItemID == 0) == (query.MessageID == "") {
		return domain.WorkInspection{}, errs.NewValidationError(
			errs.SubtypeInvalidArgument,
			"exactly one of work_item_id or message_id is required",
		)
	}
	var inspection domain.WorkInspection
	if query.MessageID != "" {
		if receipt, ok, err := s.findIntakeReceipt(ctx, query.MessageID, ""); err != nil {
			return inspection, err
		} else if ok {
			inspection.Receipt = &receipt
			query.WorkItemID = receipt.WorkItemID
		}
	}
	if query.WorkItemID == 0 {
		inspection.State.Observed = inspection.Receipt != nil
		inspection.State.OfflineBacklog = inspection.Receipt != nil &&
			inspection.Receipt.Disposition == domain.IntakeOfflineBacklog
		return inspection, nil
	}
	item, err := s.getWorkItem(ctx, query.WorkItemID)
	if err != nil {
		return inspection, err
	}
	inspection.WorkItem = &item
	if inspection.Receipt == nil {
		if receipt, ok, receiptErr := s.findReceiptByWork(ctx, item.ID); receiptErr != nil {
			return inspection, receiptErr
		} else if ok {
			inspection.Receipt = &receipt
		}
	}
	inspection.LatestRun, inspection.LatestStep, err = s.latestRunAndStep(ctx, item.ID)
	if err != nil {
		return inspection, err
	}
	inspection.LatestAction, err = s.latestAction(ctx, item.ID)
	if err != nil {
		return inspection, err
	}
	inspection.LatestInterruption, err = s.latestInterruption(ctx, item.ID)
	if err != nil {
		return inspection, err
	}
	inspection.State.Observed = inspection.Receipt != nil || inspection.WorkItem != nil
	inspection.State.Admitted = inspection.WorkItem != nil
	inspection.State.OfflineBacklog = inspection.Receipt != nil &&
		inspection.Receipt.Disposition == domain.IntakeOfflineBacklog
	inspection.State.Interrupted = item.Status == domain.StatusInterrupted
	inspection.State.Completed = isTerminalStatus(item.Status)
	inspection.State.Replied = inspection.LatestAction != nil &&
		inspection.LatestAction.Kind == "reply" &&
		inspection.LatestAction.Status == domain.ActionCompleted
	inspection.State.Uncertain = inspection.LatestInterruption != nil &&
		inspection.LatestInterruption.ActionStatus == domain.ActionExecuting
	return inspection, nil
}

func (s *Store) ResumeWork(
	ctx context.Context,
	request domain.ResumeWorkRequest,
) (domain.WorkInspection, error) {
	if s.session.ID == "" || s.session.Status == domain.OnlineSessionStopped {
		return domain.WorkInspection{}, errs.NewValidationError(
			errs.SubtypeFailedPrecondition,
			"queue resume requires an active daemon session",
		).WithHint("Start the daemon, wait until it is ready, then run queue resume again.")
	}
	inspection, err := s.InspectWork(ctx, domain.WorkInspectionQuery{
		WorkItemID: request.WorkItemID,
		MessageID:  request.MessageID,
	})
	if err != nil {
		return domain.WorkInspection{}, err
	}
	now := time.Now().UTC()
	if inspection.WorkItem == nil {
		if inspection.Receipt == nil ||
			inspection.Receipt.Disposition != domain.IntakeOfflineBacklog {
			return domain.WorkInspection{}, errs.NewValidationError(
				errs.SubtypeFailedPrecondition,
				"work is not resumable",
			)
		}
		var event domain.NormalizedEvent
		if err := json.Unmarshal([]byte(inspection.Receipt.EventJSON), &event); err != nil {
			return domain.WorkInspection{}, errs.NewInternalError(
				errs.SubtypeStorage,
				"decode offline backlog event",
			).WithCause(err)
		}
		item := domain.NewWorkItem(event)
		eventJSON, _ := json.Marshal(event)
		result, err := s.db.ExecContext(ctx,
			`INSERT INTO work_items(
				dedup_key, status, work_kind, priority, session_id, event_json,
				created_at, updated_at
			 ) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			item.DedupKey, domain.StatusReceived, item.WorkKind, item.Priority,
			s.session.ID, string(eventJSON), now.Format(time.RFC3339Nano),
			now.Format(time.RFC3339Nano))
		if err != nil {
			return domain.WorkInspection{}, errs.NewInternalError(
				errs.SubtypeStorage,
				"resume offline backlog",
			).WithCause(err)
		}
		workID, err := lastInsertID(result)
		if err != nil {
			return domain.WorkInspection{}, err
		}
		if _, err := s.db.ExecContext(ctx,
			`UPDATE intake_receipts SET disposition = ?, reason = ?, work_item_id = ?
			 WHERE id = ?`,
			domain.IntakeAdmitted, "explicitly resumed by owner", workID,
			inspection.Receipt.ID); err != nil {
			return domain.WorkInspection{}, errs.NewInternalError(
				errs.SubtypeStorage,
				"link resumed offline backlog receipt",
			).WithCause(err)
		}
		return s.InspectWork(ctx, domain.WorkInspectionQuery{WorkItemID: workID})
	}
	item := inspection.WorkItem
	if isTerminalStatus(item.Status) && !request.ForceTerminal {
		return domain.WorkInspection{}, errs.NewValidationError(
			errs.SubtypeFailedPrecondition,
			"terminal work requires --force-terminal",
		)
	}
	if item.Status != domain.StatusInterrupted && !isTerminalStatus(item.Status) {
		return domain.WorkInspection{}, errs.NewValidationError(
			errs.SubtypeFailedPrecondition,
			"work is already active in a queue",
		)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.WorkInspection{}, errs.NewInternalError(
			errs.SubtypeStorage,
			"begin explicit work resume",
		).WithCause(err)
	}
	defer tx.Rollback() //nolint:errcheck
	if _, err := tx.ExecContext(ctx,
		`UPDATE work_items SET status = ?, session_id = ?, decision_json = NULL,
		        lease_by = NULL, lease_time = NULL, retry_count = 0,
		        next_attempt_at = NULL, updated_at = ? WHERE id = ?`,
		domain.StatusReceived, s.session.ID, now.Format(time.RFC3339Nano), item.ID); err != nil {
		return domain.WorkInspection{}, errs.NewInternalError(
			errs.SubtypeStorage,
			"resume interrupted work",
		).WithCause(err)
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE work_interruptions SET resumed_at = ?
		 WHERE id = (
			SELECT id FROM work_interruptions
			WHERE work_item_id = ? AND resumed_at IS NULL
			ORDER BY interrupted_at DESC, id DESC LIMIT 1
		 )`,
		now.Format(time.RFC3339Nano), item.ID); err != nil {
		return domain.WorkInspection{}, errs.NewInternalError(
			errs.SubtypeStorage,
			"record explicit work resume",
		).WithCause(err)
	}
	if err := tx.Commit(); err != nil {
		return domain.WorkInspection{}, errs.NewInternalError(
			errs.SubtypeStorage,
			"commit explicit work resume",
		).WithCause(err)
	}
	return s.InspectWork(ctx, domain.WorkInspectionQuery{WorkItemID: item.ID})
}

func (s *Store) getWorkItem(ctx context.Context, id int64) (domain.WorkItem, error) {
	item, err := scanWorkItem(s.db.QueryRowContext(ctx,
		workItemSelect+` WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return domain.WorkItem{}, errs.NewValidationError(
			errs.SubtypeInvalidArgument,
			"work item was not found",
		).WithParam("work_item_id")
	}
	return item, err
}

func (s *Store) findReceiptByWork(
	ctx context.Context,
	workItemID int64,
) (domain.IntakeReceipt, bool, error) {
	receipt, err := scanIntakeReceipt(s.db.QueryRowContext(ctx,
		`SELECT id, message_id, event_id, source, session_id, event_json,
		        event_created_at, observed_at, disposition, reason, work_item_id
		 FROM intake_receipts WHERE work_item_id = ? ORDER BY id DESC LIMIT 1`,
		workItemID))
	if errors.Is(err, sql.ErrNoRows) {
		return domain.IntakeReceipt{}, false, nil
	}
	return receipt, err == nil, err
}

func (s *Store) latestRunAndStep(
	ctx context.Context,
	workItemID int64,
) (*domain.AgentRun, *domain.AgentStep, error) {
	var run domain.AgentRun
	var status, started string
	var completed sql.NullString
	err := s.db.QueryRowContext(ctx,
		`SELECT id, work_item_id, dedup_key, status, COALESCE(model_fingerprint, ''),
		        COALESCE(config_fingerprint, ''), COALESCE(last_error, ''),
		        started_at, completed_at
		 FROM agent_runs WHERE work_item_id = ?
		 ORDER BY started_at DESC, id DESC LIMIT 1`, workItemID).
		Scan(&run.ID, &run.WorkItemID, &run.DedupKey, &status,
			&run.ModelFingerprint, &run.ConfigFingerprint, &run.LastError,
			&started, &completed)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, errs.NewInternalError(
			errs.SubtypeStorage,
			"read latest agent run",
		).WithCause(err)
	}
	run.Status = domain.AgentRunStatus(status)
	run.StartedAt, _ = time.Parse(time.RFC3339Nano, started)
	if completed.Valid {
		run.CompletedAt, _ = time.Parse(time.RFC3339Nano, completed.String)
	}
	var step domain.AgentStep
	var created string
	stepErr := s.db.QueryRowContext(ctx,
		`SELECT run_id, sequence, kind, tool_call_id, tool_name, input_json,
		        output_json, request_id, prompt_tokens, completion_tokens,
		        error, created_at
		 FROM agent_steps WHERE run_id = ? ORDER BY sequence DESC LIMIT 1`,
		run.ID).Scan(&step.RunID, &step.Sequence, &step.Kind, &step.ToolCallID,
		&step.ToolName, &step.InputJSON, &step.OutputJSON, &step.RequestID,
		&step.PromptTokens, &step.CompletionTokens, &step.Error, &created)
	if errors.Is(stepErr, sql.ErrNoRows) {
		return &run, nil, nil
	}
	if stepErr != nil {
		return nil, nil, errs.NewInternalError(
			errs.SubtypeStorage,
			"read latest agent step",
		).WithCause(stepErr)
	}
	step.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
	return &run, &step, nil
}

func (s *Store) latestAction(
	ctx context.Context,
	workItemID int64,
) (*domain.ActionAttempt, error) {
	action, err := scanActionAttempt(s.db.QueryRowContext(ctx,
		`SELECT id, work_item_id, COALESCE(run_id, ''), kind, idempotency_key,
		        status, COALESCE(request_json, ''), COALESCE(response_json, ''),
		        COALESCE(error, ''), created_at, updated_at
		 FROM action_attempts WHERE work_item_id = ? ORDER BY id DESC LIMIT 1`,
		workItemID))
	if problem, ok := errs.ProblemOf(err); ok &&
		problem.Subtype == errs.SubtypeFailedPrecondition {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &action, nil
}

func (s *Store) latestInterruption(
	ctx context.Context,
	workItemID int64,
) (*domain.WorkInterruption, error) {
	var snapshot domain.WorkInterruption
	var stage, interrupted string
	var runID, sessionID, lastKind, lastTool, actionKind, actionStatus, resumed sql.NullString
	err := s.db.QueryRowContext(ctx,
		`SELECT id, work_item_id, run_id, session_id, stage, last_sequence,
		        last_kind, last_tool, action_kind, action_status, reason,
		        interrupted_at, resumed_at
		 FROM work_interruptions WHERE work_item_id = ?
		 ORDER BY interrupted_at DESC, id DESC LIMIT 1`, workItemID).
		Scan(&snapshot.ID, &snapshot.WorkItemID, &runID, &sessionID, &stage,
			&snapshot.LastSequence, &lastKind, &lastTool, &actionKind,
			&actionStatus, &snapshot.Reason, &interrupted, &resumed)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, errs.NewInternalError(
			errs.SubtypeStorage,
			"read latest interruption",
		).WithCause(err)
	}
	snapshot.RunID = runID.String
	snapshot.SessionID = sessionID.String
	snapshot.Stage = domain.InterruptionStage(stage)
	snapshot.LastKind = lastKind.String
	snapshot.LastTool = lastTool.String
	snapshot.ActionKind = actionKind.String
	snapshot.ActionStatus = domain.ActionStatus(actionStatus.String)
	snapshot.InterruptedAt, _ = time.Parse(time.RFC3339Nano, interrupted)
	if resumed.Valid {
		snapshot.ResumedAt, _ = time.Parse(time.RFC3339Nano, resumed.String)
	}
	return &snapshot, nil
}

func scanIntakeReceipt(row rowScanner) (domain.IntakeReceipt, error) {
	var receipt domain.IntakeReceipt
	var source, observed, disposition string
	var created, reason sql.NullString
	var workItemID sql.NullInt64
	err := row.Scan(&receipt.ID, &receipt.MessageID, &receipt.EventID, &source,
		&receipt.SessionID, &receipt.EventJSON, &created, &observed, &disposition,
		&reason, &workItemID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return domain.IntakeReceipt{}, err
		}
		return domain.IntakeReceipt{}, errs.NewInternalError(
			errs.SubtypeStorage,
			"scan intake receipt",
		).WithCause(err)
	}
	receipt.Source = domain.EventSource(source)
	receipt.Disposition = domain.IntakeDisposition(disposition)
	receipt.ObservedAt, _ = time.Parse(time.RFC3339Nano, observed)
	if created.Valid {
		receipt.EventCreatedAt, _ = time.Parse(time.RFC3339Nano, created.String)
	}
	receipt.Reason = reason.String
	if workItemID.Valid {
		receipt.WorkItemID = workItemID.Int64
	}
	return receipt, nil
}

func isTerminalStatus(status domain.WorkItemStatus) bool {
	switch status {
	case domain.StatusCompleted, domain.StatusIgnored, domain.StatusCancelled,
		domain.StatusDeadLetter:
		return true
	default:
		return false
	}
}

func lastInsertID(result sql.Result) (int64, error) {
	id, err := result.LastInsertId()
	if err != nil {
		return 0, errs.NewInternalError(
			errs.SubtypeStorage,
			"read inserted row identity",
		).WithCause(err)
	}
	return id, nil
}

func nullableTime(value time.Time) any {
	if value.IsZero() {
		return nil
	}
	return value.UTC().Format(time.RFC3339Nano)
}

func nullableID(value int64) any {
	if value == 0 {
		return nil
	}
	return value
}

func nullString(value string) any {
	if value == "" {
		return nil
	}
	return value
}
