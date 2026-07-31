package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/liuchong/lark-agent/agent/domain"
	"github.com/liuchong/lark-agent/agent/memory"
	errs "github.com/liuchong/lark-agent/internal/apperr"
)

const ownerTaskPageSize = 10

// ListOwnerTasks returns a bounded task view for the owner control plane.
func (s *Store) ListOwnerTasks(
	ctx context.Context,
	query domain.OwnerTaskQuery,
) (domain.OwnerTaskPage, error) {
	if query.Page <= 0 {
		query.Page = 1
	}
	if query.PageSize <= 0 {
		query.PageSize = ownerTaskPageSize
	}
	if query.PageSize > 20 {
		return domain.OwnerTaskPage{}, errs.NewValidationError(
			errs.SubtypeInvalidArgument,
			"owner task page size must be at most 20",
		)
	}
	if query.View == "" {
		query.View = domain.OwnerTaskViewAction
	}
	clause, args, err := ownerTaskWhere(query.View)
	if err != nil {
		return domain.OwnerTaskPage{}, err
	}
	var total int
	if err := s.db.QueryRowContext(
		ctx,
		`SELECT COUNT(*) FROM work_items `+clause,
		args...,
	).Scan(&total); err != nil {
		return domain.OwnerTaskPage{}, errs.NewInternalError(
			errs.SubtypeStorage,
			"count owner task view",
		).WithCause(err)
	}
	pageArgs := append(append([]any{}, args...), query.PageSize, (query.Page-1)*query.PageSize)
	rows, err := s.db.QueryContext(
		ctx,
		workItemSelect+" "+clause+` ORDER BY updated_at DESC, id DESC LIMIT ? OFFSET ?`,
		pageArgs...,
	)
	if err != nil {
		return domain.OwnerTaskPage{}, errs.NewInternalError(
			errs.SubtypeStorage,
			"list owner task view",
		).WithCause(err)
	}
	defer rows.Close() //nolint:errcheck
	var items []domain.WorkItem
	for rows.Next() {
		item, err := scanWorkItem(rows)
		if err != nil {
			return domain.OwnerTaskPage{}, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return domain.OwnerTaskPage{}, errs.NewInternalError(
			errs.SubtypeStorage,
			"iterate owner task view",
		).WithCause(err)
	}
	out := domain.OwnerTaskPage{
		Page: query.Page, PageSize: query.PageSize, Total: total,
	}
	for _, item := range items {
		inspection, err := s.InspectWork(ctx, domain.WorkInspectionQuery{
			WorkItemID: item.ID,
		})
		if err != nil {
			return domain.OwnerTaskPage{}, err
		}
		resolution, found, err := s.latestOwnerWorkResolution(ctx, item.ID)
		if err != nil {
			return domain.OwnerTaskPage{}, err
		}
		summary := domain.OwnerTaskSummary{
			WorkItem:           item,
			State:              inspection.State,
			LatestRun:          inspection.LatestRun,
			LatestStep:         inspection.LatestStep,
			LatestAction:       inspection.LatestAction,
			LatestInterruption: inspection.LatestInterruption,
			Investigation:      inspection.Investigation,
		}
		if found {
			summary.Resolution = &resolution
		}
		out.Items = append(out.Items, summary)
	}
	return out, nil
}

func ownerTaskWhere(view domain.OwnerTaskView) (string, []any, error) {
	switch view {
	case domain.OwnerTaskViewAction:
		return `WHERE status IN (?, ?, ?)
			AND NOT EXISTS (
				SELECT 1 FROM owner_work_resolutions r
				WHERE r.work_item_id = work_items.id
				  AND r.work_updated_at = work_items.updated_at
				  AND r.id = (
					SELECT r2.id FROM owner_work_resolutions r2
					WHERE r2.work_item_id = work_items.id
					ORDER BY r2.resolved_at DESC, r2.id DESC LIMIT 1
				  )
				  AND r.disposition IN (?, ?, ?)
			)`, []any{
				domain.StatusAwaitingApproval,
				domain.StatusInterrupted,
				domain.StatusDeadLetter,
				domain.OwnerResolutionAcknowledged,
				domain.OwnerResolutionCompleted,
				domain.OwnerResolutionUnknown,
			}, nil
	case domain.OwnerTaskViewRunning:
		return `WHERE status IN (?, ?, ?, ?, ?, ?, ?, ?)`, []any{
			domain.StatusReceived,
			domain.StatusRouted,
			domain.StatusWaitingUser,
			domain.StatusReady,
			domain.StatusProcessing,
			domain.StatusAwaitingApproval,
			domain.StatusExecuting,
			domain.StatusRetryWait,
		}, nil
	case domain.OwnerTaskViewRecent, domain.OwnerTaskViewAll:
		return "", nil, nil
	default:
		return "", nil, errs.NewValidationError(
			errs.SubtypeInvalidArgument,
			"unknown owner task view %q",
			view,
		)
	}
}

// ListPendingOwnerApprovals returns only actions that still await an owner
// decision.
func (s *Store) ListPendingOwnerApprovals(
	ctx context.Context,
	page, pageSize int,
) (domain.OwnerApprovalPage, error) {
	if page <= 0 {
		page = 1
	}
	if pageSize <= 0 {
		pageSize = ownerTaskPageSize
	}
	if pageSize > 20 {
		return domain.OwnerApprovalPage{}, errs.NewValidationError(
			errs.SubtypeInvalidArgument,
			"owner approval page size must be at most 20",
		)
	}
	var total int
	if err := s.db.QueryRowContext(
		ctx,
		`SELECT COUNT(*) FROM action_attempts WHERE status = ?`,
		domain.ActionAwaitingApproval,
	).Scan(&total); err != nil {
		return domain.OwnerApprovalPage{}, errs.NewInternalError(
			errs.SubtypeStorage,
			"count pending owner approvals",
		).WithCause(err)
	}
	rows, err := s.db.QueryContext(
		ctx,
		`SELECT id, work_item_id, COALESCE(run_id, ''), kind, idempotency_key, status,
		        COALESCE(request_json, ''), COALESCE(response_json, ''), COALESCE(error, ''),
		        created_at, updated_at
		 FROM action_attempts WHERE status = ?
		 ORDER BY updated_at DESC, id DESC LIMIT ? OFFSET ?`,
		domain.ActionAwaitingApproval,
		pageSize,
		(page-1)*pageSize,
	)
	if err != nil {
		return domain.OwnerApprovalPage{}, errs.NewInternalError(
			errs.SubtypeStorage,
			"list pending owner approvals",
		).WithCause(err)
	}
	defer rows.Close() //nolint:errcheck
	out := domain.OwnerApprovalPage{
		Page: page, PageSize: pageSize, Total: total,
	}
	for rows.Next() {
		action, err := scanActionAttempt(rows)
		if err != nil {
			return domain.OwnerApprovalPage{}, err
		}
		out.Items = append(out.Items, action)
	}
	if err := rows.Err(); err != nil {
		return domain.OwnerApprovalPage{}, errs.NewInternalError(
			errs.SubtypeStorage,
			"iterate pending owner approvals",
		).WithCause(err)
	}
	return out, nil
}

func (s *Store) latestOwnerWorkResolution(
	ctx context.Context,
	workItemID int64,
) (domain.OwnerWorkResolution, bool, error) {
	var resolution domain.OwnerWorkResolution
	var disposition, resolvedAt string
	var workUpdatedAt sql.NullString
	err := s.db.QueryRowContext(
		ctx,
		`SELECT id, work_item_id, COALESCE(action_id, 0), command_message_id,
		        disposition, reason, work_updated_at, resolved_at
		 FROM owner_work_resolutions
		 WHERE work_item_id = ?
		 ORDER BY resolved_at DESC, id DESC LIMIT 1`,
		workItemID,
	).Scan(
		&resolution.ID,
		&resolution.WorkItemID,
		&resolution.ActionID,
		&resolution.CommandMessageID,
		&disposition,
		&resolution.Reason,
		&workUpdatedAt,
		&resolvedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.OwnerWorkResolution{}, false, nil
	}
	if err != nil {
		return domain.OwnerWorkResolution{}, false, errs.NewInternalError(
			errs.SubtypeStorage,
			"read owner work resolution",
		).WithCause(err)
	}
	resolution.Disposition = domain.OwnerResolutionDisposition(disposition)
	if workUpdatedAt.Valid {
		resolution.WorkUpdatedAt, _ = time.Parse(time.RFC3339Nano, workUpdatedAt.String)
	}
	resolution.ResolvedAt, _ = time.Parse(time.RFC3339Nano, resolvedAt)
	return resolution, true, nil
}

// ExecuteOwnerMutation atomically journals and applies one exact owner command.
// A duplicate command message returns the stored result without repeating the
// state transition.
func (s *Store) ExecuteOwnerMutation(
	ctx context.Context,
	commandMessageID string,
	command domain.OwnerControlCommand,
) (domain.OwnerMutationResult, error) {
	commandMessageID = strings.TrimSpace(commandMessageID)
	if commandMessageID == "" {
		return domain.OwnerMutationResult{}, errs.NewValidationError(
			errs.SubtypeInvalidArgument,
			"owner mutation requires a command message id",
		)
	}
	if !isOwnerMutation(command.Name) {
		return domain.OwnerMutationResult{}, errs.NewValidationError(
			errs.SubtypeInvalidArgument,
			"command %s is not a mutation",
			command.Name,
		)
	}
	commandJSON, err := json.Marshal(command)
	if err != nil {
		return domain.OwnerMutationResult{}, errs.NewInternalError(
			errs.SubtypeUnknown,
			"encode owner control command",
		).WithCause(err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.OwnerMutationResult{}, errs.NewInternalError(
			errs.SubtypeStorage,
			"begin owner control mutation",
		).WithCause(err)
	}
	defer tx.Rollback() //nolint:errcheck

	now := time.Now().UTC().Format(time.RFC3339Nano)
	insertResult, err := tx.ExecContext(
		ctx,
		`INSERT INTO owner_control_commands(
			message_id, command_json, status, created_at, updated_at
		 ) VALUES (?, ?, ?, ?, ?)
		 ON CONFLICT(message_id) DO NOTHING`,
		commandMessageID,
		string(commandJSON),
		domain.ActionExecuting,
		now,
		now,
	)
	if err != nil {
		return domain.OwnerMutationResult{}, errs.NewInternalError(
			errs.SubtypeStorage,
			"journal owner control command",
		).WithCause(err)
	}
	inserted, err := insertResult.RowsAffected()
	if err != nil {
		return domain.OwnerMutationResult{}, errs.NewInternalError(
			errs.SubtypeStorage,
			"read owner control journal result",
		).WithCause(err)
	}
	if inserted == 0 {
		var status, resultJSON string
		if err := tx.QueryRowContext(
			ctx,
			`SELECT status, COALESCE(result_json, '')
			 FROM owner_control_commands WHERE message_id = ?`,
			commandMessageID,
		).Scan(&status, &resultJSON); err != nil {
			return domain.OwnerMutationResult{}, errs.NewInternalError(
				errs.SubtypeStorage,
				"read owner control journal",
			).WithCause(err)
		}
		if status != string(domain.ActionCompleted) {
			return domain.OwnerMutationResult{}, errs.NewInternalError(
				errs.SubtypeStorage,
				"owner control command has non-terminal journal status %s",
				status,
			)
		}
		var result domain.OwnerMutationResult
		if err := json.Unmarshal([]byte(resultJSON), &result); err != nil {
			return domain.OwnerMutationResult{}, errs.NewInternalError(
				errs.SubtypeStorage,
				"decode completed owner control result",
			).WithCause(err)
		}
		result.Replayed = true
		return result, nil
	}
	result := domain.OwnerMutationResult{
		CommandMessageID: commandMessageID,
		Name:             command.Name,
		WorkItemID:       command.WorkItemID,
		ActionID:         command.ActionID,
		Reason:           command.Reason,
	}
	if err := s.executeOwnerMutationTx(ctx, tx, commandMessageID, command, &result, now); err != nil {
		return domain.OwnerMutationResult{}, err
	}
	encodedResult, err := json.Marshal(result)
	if err != nil {
		return domain.OwnerMutationResult{}, errs.NewInternalError(
			errs.SubtypeUnknown,
			"encode owner control result",
		).WithCause(err)
	}
	if _, err := tx.ExecContext(
		ctx,
		`UPDATE owner_control_commands
		 SET status = ?, result_json = ?, error = NULL, updated_at = ?
		 WHERE message_id = ?`,
		domain.ActionCompleted,
		string(encodedResult),
		now,
		commandMessageID,
	); err != nil {
		return domain.OwnerMutationResult{}, errs.NewInternalError(
			errs.SubtypeStorage,
			"complete owner control journal",
		).WithCause(err)
	}
	if err := tx.Commit(); err != nil {
		return domain.OwnerMutationResult{}, errs.NewInternalError(
			errs.SubtypeStorage,
			"commit owner control mutation",
		).WithCause(err)
	}
	return result, nil
}

func isOwnerMutation(name domain.OwnerControlName) bool {
	switch name {
	case domain.OwnerControlTaskRetry,
		domain.OwnerControlTaskResume,
		domain.OwnerControlTaskCancel,
		domain.OwnerControlTaskAcknowledge,
		domain.OwnerControlTaskReconcile,
		domain.OwnerControlApprovalApprove,
		domain.OwnerControlApprovalReject,
		domain.OwnerControlMemoryAdd,
		domain.OwnerControlMemoryDelete,
		domain.OwnerControlMemoryFeedback:
		return true
	default:
		return false
	}
}

func (s *Store) executeOwnerMutationTx(
	ctx context.Context,
	tx *sql.Tx,
	commandMessageID string,
	command domain.OwnerControlCommand,
	result *domain.OwnerMutationResult,
	now string,
) error {
	switch command.Name {
	case domain.OwnerControlTaskRetry:
		changed, err := s.retryOwnerWorkTx(ctx, tx, command.WorkItemID, now)
		result.Changed = changed
		return err
	case domain.OwnerControlTaskResume:
		changed, err := s.resumeOwnerWorkTx(
			ctx,
			tx,
			command.WorkItemID,
			command.Confirm,
			now,
		)
		result.Changed = changed
		return err
	case domain.OwnerControlTaskCancel:
		changed, err := s.cancelOwnerWorkTx(
			ctx,
			tx,
			commandMessageID,
			command.WorkItemID,
			command.Reason,
			now,
		)
		result.Changed = changed
		return err
	case domain.OwnerControlTaskAcknowledge:
		actionID, err := s.resolveOwnerWorkTx(
			ctx,
			tx,
			commandMessageID,
			command.WorkItemID,
			domain.OwnerResolutionAcknowledged,
			command.Reason,
			now,
		)
		result.ActionID = actionID
		result.Changed = 1
		result.Disposition = domain.OwnerResolutionAcknowledged
		return err
	case domain.OwnerControlTaskReconcile:
		actionID, err := s.resolveOwnerWorkTx(
			ctx,
			tx,
			commandMessageID,
			command.WorkItemID,
			command.Disposition,
			command.Reason,
			now,
		)
		result.ActionID = actionID
		result.Changed = 1
		result.Disposition = command.Disposition
		return err
	case domain.OwnerControlApprovalApprove:
		workID, err := s.decideOwnerApprovalTx(ctx, tx, command.ActionID, true, "", now)
		result.WorkItemID = workID
		result.Changed = 1
		return err
	case domain.OwnerControlApprovalReject:
		workID, err := s.decideOwnerApprovalTx(
			ctx,
			tx,
			command.ActionID,
			false,
			command.Reason,
			now,
		)
		result.WorkItemID = workID
		result.Changed = 1
		return err
	case domain.OwnerControlMemoryAdd:
		record := memory.Record{
			Kind:       memory.Kind(command.MemoryKind),
			Scope:      command.MemoryScope,
			Status:     memory.StatusConfirmed,
			Text:       command.MemoryContent,
			Confidence: 1,
		}
		if err := validateMemoryRecord(record); err != nil {
			return err
		}
		id, err := randomID()
		if err != nil {
			return err
		}
		scope := firstNonEmptyMemoryScope(record.Scope)
		if _, err := tx.ExecContext(ctx, `INSERT INTO memory_entries(
			id, kind, scope, content, status, source_message_id,
			confidence, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			id,
			record.Kind,
			scope,
			strings.TrimSpace(record.Text),
			record.Status,
			commandMessageID,
			record.Confidence,
			now,
			now,
		); err != nil {
			return errs.NewInternalError(errs.SubtypeStorage, "insert owner memory").WithCause(err)
		}
		result.MemoryID = id
		result.Changed = 1
		return nil
	case domain.OwnerControlMemoryDelete:
		if !command.Confirm {
			return errs.NewValidationError(errs.SubtypeFailedPrecondition, "memory deletion requires confirm")
		}
		update, err := tx.ExecContext(ctx, `UPDATE memory_entries
			SET deleted_at = ?, updated_at = ?
			WHERE id = ? AND deleted_at IS NULL`, now, now, command.MemoryID)
		if err != nil {
			return errs.NewInternalError(errs.SubtypeStorage, "delete owner memory").WithCause(err)
		}
		changed, err := update.RowsAffected()
		if err != nil {
			return errs.NewInternalError(errs.SubtypeStorage, "read owner memory deletion result").WithCause(err)
		}
		if changed != 1 {
			return errs.NewValidationError(errs.SubtypeInvalidArgument, "memory entry not found: %s", command.MemoryID)
		}
		result.MemoryID = command.MemoryID
		result.Changed = int(changed)
		return nil
	case domain.OwnerControlMemoryFeedback:
		feedback := memory.Feedback{
			MemoryEntryID:   command.MemoryID,
			Verdict:         memory.FeedbackVerdict(command.MemoryVerdict),
			Note:            command.MemoryFeedback,
			SourceMessageID: commandMessageID,
		}
		if !validMemoryFeedbackVerdict(feedback.Verdict) {
			return errs.NewValidationError(errs.SubtypeInvalidArgument, "invalid memory feedback verdict: %s", feedback.Verdict)
		}
		if len(feedback.Note) > maxMemoryNoteBytes || containsCredentialLikeContent(feedback.Note) {
			return errs.NewValidationError(errs.SubtypeInvalidArgument, "invalid memory feedback note")
		}
		var deletedAt sql.NullString
		if err := tx.QueryRowContext(
			ctx,
			`SELECT deleted_at FROM memory_entries WHERE id = ?`,
			feedback.MemoryEntryID,
		).Scan(&deletedAt); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return errs.NewValidationError(errs.SubtypeInvalidArgument, "memory entry not found: %s", feedback.MemoryEntryID)
			}
			return errs.NewInternalError(errs.SubtypeStorage, "locate owner memory").WithCause(err)
		}
		if deletedAt.Valid && feedback.Verdict != memory.FeedbackConfirm {
			return errs.NewValidationError(errs.SubtypeFailedPrecondition, "memory entry is deleted: %s", feedback.MemoryEntryID)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO memory_feedback(
			memory_entry_id, verdict, note, source_message_id, created_at
		) VALUES (?, ?, ?, ?, ?)`,
			feedback.MemoryEntryID,
			feedback.Verdict,
			strings.TrimSpace(feedback.Note),
			commandMessageID,
			now,
		); err != nil {
			return errs.NewInternalError(errs.SubtypeStorage, "insert owner memory feedback").WithCause(err)
		}
		var updateErr error
		switch feedback.Verdict {
		case memory.FeedbackConfirm:
			_, updateErr = tx.ExecContext(ctx, `UPDATE memory_entries
				SET status = 'confirmed', deleted_at = NULL, updated_at = ?
				WHERE id = ?`, now, feedback.MemoryEntryID)
		case memory.FeedbackReject:
			_, updateErr = tx.ExecContext(ctx, `UPDATE memory_entries
				SET deleted_at = ?, updated_at = ?
				WHERE id = ?`, now, now, feedback.MemoryEntryID)
		default:
			_, updateErr = tx.ExecContext(ctx, `UPDATE memory_entries
				SET updated_at = ? WHERE id = ?`, now, feedback.MemoryEntryID)
		}
		if updateErr != nil {
			return errs.NewInternalError(errs.SubtypeStorage, "apply owner memory feedback").WithCause(updateErr)
		}
		result.MemoryID = feedback.MemoryEntryID
		result.Changed = 1
		result.Reason = string(feedback.Verdict)
		return nil
	default:
		return errs.NewValidationError(
			errs.SubtypeInvalidArgument,
			"unsupported owner mutation %s",
			command.Name,
		)
	}
}

func (s *Store) retryOwnerWorkTx(
	ctx context.Context,
	tx *sql.Tx,
	workItemID int64,
	now string,
) (int, error) {
	res, err := tx.ExecContext(
		ctx,
		`UPDATE work_items
		 SET status = ?, decision_json = NULL, lease_by = NULL, lease_time = NULL,
		     retry_count = 0, next_attempt_at = NULL, updated_at = ?
		 WHERE id = ? AND status = ? AND session_id = ?
		   AND NOT EXISTS (
			SELECT 1 FROM action_attempts a
			WHERE a.work_item_id = work_items.id AND a.status IN (?, ?)
		   )
		   AND NOT EXISTS (
			SELECT 1 FROM work_interruptions i
			WHERE i.work_item_id = work_items.id AND i.resumed_at IS NULL
			  AND i.action_status = ?
		   )`,
		domain.StatusReceived,
		now,
		workItemID,
		domain.StatusRetryWait,
		s.session.ID,
		domain.ActionExecuting,
		domain.ActionBlocked,
		domain.ActionExecuting,
	)
	if err != nil {
		return 0, errs.NewInternalError(
			errs.SubtypeStorage,
			"retry owner-selected work",
		).WithCause(err)
	}
	changed, _ := res.RowsAffected()
	if changed != 1 {
		return 0, errs.NewValidationError(
			errs.SubtypeFailedPrecondition,
			"work item %d is not safely retryable in the current session",
			workItemID,
		)
	}
	return 1, nil
}

func (s *Store) resumeOwnerWorkTx(
	ctx context.Context,
	tx *sql.Tx,
	workItemID int64,
	confirm bool,
	now string,
) (int, error) {
	if s.session.ID == "" || s.session.Status == domain.OnlineSessionStopped {
		return 0, errs.NewValidationError(
			errs.SubtypeFailedPrecondition,
			"work resume requires an active daemon session",
		)
	}
	var status string
	if err := tx.QueryRowContext(
		ctx,
		`SELECT status FROM work_items WHERE id = ?`,
		workItemID,
	).Scan(&status); errors.Is(err, sql.ErrNoRows) {
		return 0, errs.NewValidationError(
			errs.SubtypeInvalidArgument,
			"work item %d was not found",
			workItemID,
		)
	} else if err != nil {
		return 0, errs.NewInternalError(
			errs.SubtypeStorage,
			"read owner-selected work for resume",
		).WithCause(err)
	}
	var uncertain int
	if err := tx.QueryRowContext(
		ctx,
		`SELECT COUNT(*) FROM work_interruptions
		 WHERE work_item_id = ? AND resumed_at IS NULL AND action_status = ?`,
		workItemID,
		domain.ActionExecuting,
	).Scan(&uncertain); err != nil {
		return 0, errs.NewInternalError(
			errs.SubtypeStorage,
			"check owner resume uncertainty",
		).WithCause(err)
	}
	if uncertain > 0 {
		return 0, errs.NewValidationError(
			errs.SubtypeFailedPrecondition,
			"work item %d has an unresolved external result; reconcile it first",
			workItemID,
		)
	}
	itemStatus := domain.WorkItemStatus(status)
	if isTerminalStatus(itemStatus) && !confirm {
		return 0, errs.NewValidationError(
			errs.SubtypeFailedPrecondition,
			"terminal work requires confirm",
		)
	}
	if itemStatus != domain.StatusInterrupted && !isTerminalStatus(itemStatus) {
		return 0, errs.NewValidationError(
			errs.SubtypeFailedPrecondition,
			"work item %d is already active in status %s",
			workItemID,
			status,
		)
	}
	if _, err := tx.ExecContext(
		ctx,
		`UPDATE work_items
		 SET status = ?, session_id = ?, decision_json = NULL, lease_by = NULL,
		     lease_time = NULL, retry_count = 0, next_attempt_at = NULL, updated_at = ?
		 WHERE id = ?`,
		domain.StatusReceived,
		s.session.ID,
		now,
		workItemID,
	); err != nil {
		return 0, errs.NewInternalError(
			errs.SubtypeStorage,
			"resume owner-selected work",
		).WithCause(err)
	}
	if _, err := tx.ExecContext(
		ctx,
		`UPDATE work_interruptions SET resumed_at = ?
		 WHERE work_item_id = ? AND resumed_at IS NULL`,
		now,
		workItemID,
	); err != nil {
		return 0, errs.NewInternalError(
			errs.SubtypeStorage,
			"close owner-selected interruption",
		).WithCause(err)
	}
	return 1, nil
}

func (s *Store) cancelOwnerWorkTx(
	ctx context.Context,
	tx *sql.Tx,
	commandMessageID string,
	workItemID int64,
	reason string,
	now string,
) (int, error) {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return 0, errs.NewValidationError(
			errs.SubtypeInvalidArgument,
			"work cancellation requires a reason",
		)
	}
	var status string
	if err := tx.QueryRowContext(
		ctx,
		`SELECT status FROM work_items WHERE id = ?`,
		workItemID,
	).Scan(&status); errors.Is(err, sql.ErrNoRows) {
		return 0, errs.NewValidationError(
			errs.SubtypeInvalidArgument,
			"work item %d was not found",
			workItemID,
		)
	} else if err != nil {
		return 0, errs.NewInternalError(
			errs.SubtypeStorage,
			"read owner-selected work for cancellation",
		).WithCause(err)
	}
	if !isCancellationSafeStatus(domain.WorkItemStatus(status)) {
		return 0, errs.NewValidationError(
			errs.SubtypeFailedPrecondition,
			"work item %d is not safely cancellable from status %s",
			workItemID,
			status,
		)
	}
	var unsafe int
	if err := tx.QueryRowContext(
		ctx,
		`SELECT
			(SELECT COUNT(*) FROM action_attempts
			 WHERE work_item_id = ? AND status = ?)
			+
			(SELECT COUNT(*) FROM work_interruptions
			 WHERE work_item_id = ? AND resumed_at IS NULL AND action_status = ?)`,
		workItemID,
		domain.ActionExecuting,
		workItemID,
		domain.ActionExecuting,
	).Scan(&unsafe); err != nil {
		return 0, errs.NewInternalError(
			errs.SubtypeStorage,
			"check owner cancellation safety",
		).WithCause(err)
	}
	if unsafe > 0 {
		return 0, errs.NewValidationError(
			errs.SubtypeFailedPrecondition,
			"work item %d has an unresolved external result; reconcile it first",
			workItemID,
		)
	}
	if _, err := tx.ExecContext(
		ctx,
		`UPDATE action_attempts SET status = ?, error = ?, updated_at = ?
		 WHERE work_item_id = ? AND status IN (?, ?)`,
		domain.ActionCancelled,
		"owner cancelled: "+reason,
		now,
		workItemID,
		domain.ActionAwaitingApproval,
		domain.ActionReady,
	); err != nil {
		return 0, errs.NewInternalError(
			errs.SubtypeStorage,
			"cancel owner-selected pending actions",
		).WithCause(err)
	}
	if _, err := tx.ExecContext(
		ctx,
		`UPDATE work_items SET status = ?, lease_by = NULL, lease_time = NULL,
		        next_attempt_at = NULL, updated_at = ? WHERE id = ?`,
		domain.StatusCancelled,
		now,
		workItemID,
	); err != nil {
		return 0, errs.NewInternalError(
			errs.SubtypeStorage,
			"cancel owner-selected work",
		).WithCause(err)
	}
	if _, err := tx.ExecContext(
		ctx,
		`UPDATE coding_goals SET status = ?, updated_at = ? WHERE work_item_id = ?`,
		domain.CodingGoalBlocked,
		now,
		workItemID,
	); err != nil {
		return 0, errs.NewInternalError(
			errs.SubtypeStorage,
			"block cancelled owner work goal",
		).WithCause(err)
	}
	if _, err := tx.ExecContext(
		ctx,
		`UPDATE work_interruptions SET resumed_at = ?
		 WHERE work_item_id = ? AND resumed_at IS NULL`,
		now,
		workItemID,
	); err != nil {
		return 0, errs.NewInternalError(
			errs.SubtypeStorage,
			"close cancelled owner work interruption",
		).WithCause(err)
	}
	requestJSON, _ := json.Marshal(map[string]string{"reason": reason})
	if _, err := tx.ExecContext(
		ctx,
		`INSERT INTO action_attempts(
			work_item_id, kind, idempotency_key, status, request_json, created_at, updated_at
		 ) VALUES (?, 'operator_cancel', ?, ?, ?, ?, ?)`,
		workItemID,
		"owner_control_cancel:"+commandMessageID,
		domain.ActionCompleted,
		string(requestJSON),
		now,
		now,
	); err != nil {
		return 0, errs.NewInternalError(
			errs.SubtypeStorage,
			"record owner cancellation audit",
		).WithCause(err)
	}
	return 1, nil
}

func (s *Store) resolveOwnerWorkTx(
	ctx context.Context,
	tx *sql.Tx,
	commandMessageID string,
	workItemID int64,
	disposition domain.OwnerResolutionDisposition,
	reason string,
	now string,
) (int64, error) {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return 0, errs.NewValidationError(
			errs.SubtypeInvalidArgument,
			"owner resolution requires a reason",
		)
	}
	var status string
	if err := tx.QueryRowContext(
		ctx,
		`SELECT status FROM work_items WHERE id = ?`,
		workItemID,
	).Scan(&status); errors.Is(err, sql.ErrNoRows) {
		return 0, errs.NewValidationError(
			errs.SubtypeInvalidArgument,
			"work item %d was not found",
			workItemID,
		)
	} else if err != nil {
		return 0, errs.NewInternalError(
			errs.SubtypeStorage,
			"read owner-selected work for resolution",
		).WithCause(err)
	}
	var actionID int64
	var actionStatus string
	actionErr := tx.QueryRowContext(
		ctx,
		`SELECT id, status FROM action_attempts
		 WHERE work_item_id = ? ORDER BY updated_at DESC, id DESC LIMIT 1`,
		workItemID,
	).Scan(&actionID, &actionStatus)
	if actionErr != nil && !errors.Is(actionErr, sql.ErrNoRows) {
		return 0, errs.NewInternalError(
			errs.SubtypeStorage,
			"read owner-selected action for resolution",
		).WithCause(actionErr)
	}
	switch disposition {
	case domain.OwnerResolutionAcknowledged:
		if !isTerminalStatus(domain.WorkItemStatus(status)) &&
			domain.WorkItemStatus(status) != domain.StatusInterrupted {
			return 0, errs.NewValidationError(
				errs.SubtypeFailedPrecondition,
				"only interrupted or terminal work can be acknowledged",
			)
		}
		if domain.WorkItemStatus(status) == domain.StatusInterrupted {
			if _, err := tx.ExecContext(
				ctx,
				`UPDATE work_items SET status = ?, lease_by = NULL, lease_time = NULL,
				        next_attempt_at = NULL, updated_at = ? WHERE id = ?`,
				domain.StatusCancelled,
				now,
				workItemID,
			); err != nil {
				return 0, errs.NewInternalError(
					errs.SubtypeStorage,
					"close acknowledged interrupted work",
				).WithCause(err)
			}
		}
		if _, err := tx.ExecContext(
			ctx,
			`UPDATE action_attempts SET status = ?, error = ?, updated_at = ?
			 WHERE work_item_id = ? AND status IN (?, ?)`,
			domain.ActionCancelled,
			"owner acknowledged and closed: "+reason,
			now,
			workItemID,
			domain.ActionAwaitingApproval,
			domain.ActionReady,
		); err != nil {
			return 0, errs.NewInternalError(
				errs.SubtypeStorage,
				"close acknowledged pending actions",
			).WithCause(err)
		}
		if _, err := tx.ExecContext(
			ctx,
			`UPDATE work_interruptions SET resumed_at = ?
			 WHERE work_item_id = ? AND resumed_at IS NULL`,
			now,
			workItemID,
		); err != nil {
			return 0, errs.NewInternalError(
				errs.SubtypeStorage,
				"close acknowledged interruption",
			).WithCause(err)
		}
	case domain.OwnerResolutionCompleted,
		domain.OwnerResolutionNotCompleted,
		domain.OwnerResolutionUnknown:
		var uncertain int
		if err := tx.QueryRowContext(
			ctx,
			`SELECT COUNT(*) FROM work_interruptions
			 WHERE work_item_id = ? AND resumed_at IS NULL AND action_status = ?`,
			workItemID,
			domain.ActionExecuting,
		).Scan(&uncertain); err != nil {
			return 0, errs.NewInternalError(
				errs.SubtypeStorage,
				"check owner reconciliation evidence",
			).WithCause(err)
		}
		if uncertain == 0 || errors.Is(actionErr, sql.ErrNoRows) ||
			domain.ActionStatus(actionStatus) != domain.ActionExecuting {
			return 0, errs.NewValidationError(
				errs.SubtypeFailedPrecondition,
				"work item %d has no unresolved external result to reconcile",
				workItemID,
			)
		}
		nextActionStatus := domain.ActionBlocked
		nextWorkStatus := domain.StatusDeadLetter
		switch disposition {
		case domain.OwnerResolutionCompleted:
			nextActionStatus = domain.ActionCompleted
			nextWorkStatus = domain.StatusCompleted
		case domain.OwnerResolutionNotCompleted:
			nextActionStatus = domain.ActionCancelled
			nextWorkStatus = domain.StatusInterrupted
		}
		if _, err := tx.ExecContext(
			ctx,
			`UPDATE action_attempts SET status = ?, error = ?, updated_at = ? WHERE id = ?`,
			nextActionStatus,
			"owner reconciliation: "+reason,
			now,
			actionID,
		); err != nil {
			return 0, errs.NewInternalError(
				errs.SubtypeStorage,
				"apply owner action reconciliation",
			).WithCause(err)
		}
		if _, err := tx.ExecContext(
			ctx,
			`UPDATE work_items SET status = ?, lease_by = NULL, lease_time = NULL,
			        next_attempt_at = NULL, updated_at = ? WHERE id = ?`,
			nextWorkStatus,
			now,
			workItemID,
		); err != nil {
			return 0, errs.NewInternalError(
				errs.SubtypeStorage,
				"apply owner work reconciliation",
			).WithCause(err)
		}
		if _, err := tx.ExecContext(
			ctx,
			`UPDATE work_interruptions SET resumed_at = ?
			 WHERE work_item_id = ? AND resumed_at IS NULL`,
			now,
			workItemID,
		); err != nil {
			return 0, errs.NewInternalError(
				errs.SubtypeStorage,
				"close reconciled interruption",
			).WithCause(err)
		}
	default:
		return 0, errs.NewValidationError(
			errs.SubtypeInvalidArgument,
			"unknown owner resolution %s",
			disposition,
		)
	}
	if errors.Is(actionErr, sql.ErrNoRows) {
		actionID = 0
	}
	actionValue := any(nil)
	if actionID > 0 {
		actionValue = actionID
	}
	var workUpdatedAt string
	if err := tx.QueryRowContext(
		ctx,
		`SELECT updated_at FROM work_items WHERE id = ?`,
		workItemID,
	).Scan(&workUpdatedAt); err != nil {
		return 0, errs.NewInternalError(
			errs.SubtypeStorage,
			"read resolved owner work epoch",
		).WithCause(err)
	}
	if _, err := tx.ExecContext(
		ctx,
		`INSERT INTO owner_work_resolutions(
			work_item_id, action_id, command_message_id, disposition, reason,
			work_updated_at, resolved_at
		 ) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		workItemID,
		actionValue,
		commandMessageID,
		disposition,
		reason,
		workUpdatedAt,
		now,
	); err != nil {
		return 0, errs.NewInternalError(
			errs.SubtypeStorage,
			"record owner work resolution",
		).WithCause(err)
	}
	return actionID, nil
}

func (s *Store) decideOwnerApprovalTx(
	ctx context.Context,
	tx *sql.Tx,
	actionID int64,
	approve bool,
	reason string,
	now string,
) (int64, error) {
	actionStatus := domain.ActionReady
	if !approve {
		actionStatus = domain.ActionCancelled
	}
	var workItemID int64
	if err := tx.QueryRowContext(
		ctx,
		`UPDATE action_attempts
		 SET status = ?, error = CASE WHEN ? = '' THEN error ELSE ? END, updated_at = ?
		 WHERE id = ? AND status = ?
		 RETURNING work_item_id`,
		actionStatus,
		reason,
		reason,
		now,
		actionID,
		domain.ActionAwaitingApproval,
	).Scan(&workItemID); errors.Is(err, sql.ErrNoRows) {
		return 0, errs.NewValidationError(
			errs.SubtypeFailedPrecondition,
			"action %d is not awaiting approval",
			actionID,
		)
	} else if err != nil {
		return 0, errs.NewInternalError(
			errs.SubtypeStorage,
			"apply owner approval decision",
		).WithCause(err)
	}
	if approve {
		var sessionID, sessionStatus string
		sessionErr := tx.QueryRowContext(
			ctx,
			`SELECT id, status FROM online_sessions
			 WHERE status IN (?, ?)
			 ORDER BY started_at DESC, id DESC LIMIT 1`,
			domain.OnlineSessionStarting,
			domain.OnlineSessionReady,
		).Scan(&sessionID, &sessionStatus)
		switch {
		case sessionErr == nil &&
			domain.OnlineSessionStatus(sessionStatus) == domain.OnlineSessionReady:
			if _, err := tx.ExecContext(
				ctx,
				`UPDATE work_items
				 SET status = ?, session_id = ?, lease_by = NULL, lease_time = NULL,
				     next_attempt_at = NULL, updated_at = ? WHERE id = ?`,
				domain.StatusReceived,
				sessionID,
				now,
				workItemID,
			); err != nil {
				return 0, errs.NewInternalError(
					errs.SubtypeStorage,
					"assign owner-approved work",
				).WithCause(err)
			}
		case errors.Is(sessionErr, sql.ErrNoRows),
			sessionErr == nil &&
				domain.OnlineSessionStatus(sessionStatus) == domain.OnlineSessionStarting:
			if _, err := tx.ExecContext(
				ctx,
				`UPDATE work_items
				 SET status = ?, lease_by = NULL, lease_time = NULL,
				     next_attempt_at = NULL, updated_at = ? WHERE id = ?`,
				domain.StatusInterrupted,
				now,
				workItemID,
			); err != nil {
				return 0, errs.NewInternalError(
					errs.SubtypeStorage,
					"pause owner-approved work without ready session",
				).WithCause(err)
			}
		default:
			return 0, errs.NewInternalError(
				errs.SubtypeStorage,
				"locate session for owner approval",
			).WithCause(sessionErr)
		}
	} else {
		if strings.TrimSpace(reason) == "" {
			return 0, errs.NewValidationError(
				errs.SubtypeInvalidArgument,
				"approval rejection requires a reason",
			)
		}
		if _, err := tx.ExecContext(
			ctx,
			`UPDATE work_items SET status = ?, lease_by = NULL, lease_time = NULL,
			        next_attempt_at = NULL, updated_at = ? WHERE id = ?`,
			domain.StatusCancelled,
			now,
			workItemID,
		); err != nil {
			return 0, errs.NewInternalError(
				errs.SubtypeStorage,
				"cancel owner-rejected work",
			).WithCause(err)
		}
	}
	goalStatus := domain.CodingGoalActive
	if !approve {
		goalStatus = domain.CodingGoalBlocked
	}
	if _, err := tx.ExecContext(
		ctx,
		`UPDATE coding_goals SET status = ?, updated_at = ? WHERE work_item_id = ?`,
		goalStatus,
		now,
		workItemID,
	); err != nil {
		return 0, errs.NewInternalError(
			errs.SubtypeStorage,
			"update owner-approved coding goal",
		).WithCause(err)
	}
	if approve {
		if _, err := tx.ExecContext(
			ctx,
			`UPDATE work_interruptions SET resumed_at = ?
			 WHERE work_item_id = ? AND resumed_at IS NULL`,
			now,
			workItemID,
		); err != nil {
			return 0, errs.NewInternalError(
				errs.SubtypeStorage,
				"close owner-approved interruption",
			).WithCause(err)
		}
	}
	return workItemID, nil
}
