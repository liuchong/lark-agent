package resource

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/liuchong/lark-agent/agent/domain"
	errs "github.com/liuchong/lark-agent/internal/apperr"
	servicelark "github.com/liuchong/lark-agent/internal/lark"
)

type Client interface {
	ResolveResource(context.Context, servicelark.ResourceRef) (servicelark.ResourceRef, error)
	EnsureCommentSubscription(context.Context, servicelark.ResourceRef) (servicelark.RemoteSubscription, error)
	ListBaseFields(context.Context, string, string) ([]servicelark.BaseField, error)
	GetBaseRecord(context.Context, string, string, string) (servicelark.BaseRecord, error)
	ListBaseRecords(context.Context, string, string, string) ([]servicelark.BaseRecord, error)
	GetComment(context.Context, string, string, string) (servicelark.ResourceComment, error)
}

type Store interface {
	ListResourceSubscriptions(context.Context) ([]domain.ResourceSubscription, error)
	UpsertResourceSubscription(context.Context, domain.ResourceSubscription) (domain.ResourceSubscription, error)
	RecordResourceEvidence(context.Context, domain.ResourceEvidence) (domain.ResourceEvidence, bool, error)
	FindResourceEvidence(context.Context, domain.ResourceEvidenceQuery) ([]domain.ResourceEvidence, error)
	RecordWorkIntake(context.Context, domain.WorkItem) (domain.IntakeReceipt, error)
}

type Config struct {
	OwnerOpenID        string
	StatusFieldNames   []string
	AssigneeFieldNames []string
	Now                func() time.Time
}

type Monitor struct {
	client             Client
	store              Store
	ownerOpenID        string
	statusFieldNames   []string
	assigneeFieldNames []string
	now                func() time.Time
}

type SyncResult struct {
	Seen     int `json:"seen"`
	Active   int `json:"active"`
	Degraded int `json:"degraded"`
}

type ReconcileResult struct {
	Subscriptions int `json:"subscriptions"`
	Records       int `json:"records"`
	Changed       int `json:"changed"`
	WorkCreated   int `json:"work_created"`
}

type SignalResult struct {
	MatchedSubscriptions int `json:"matched_subscriptions"`
	Evidence             int `json:"evidence"`
	WorkCreated          int `json:"work_created"`
}

var issueKeyPattern = regexp.MustCompile(`(?i)\b(?:BUG|ISSUE)-[0-9]+\b`)

func NewMonitor(client Client, store Store, cfg Config) *Monitor {
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	statusNames := cfg.StatusFieldNames
	if len(statusNames) == 0 {
		statusNames = []string{"状态", "处理状态", "Bug 状态", "Status"}
	}
	assigneeNames := cfg.AssigneeFieldNames
	if len(assigneeNames) == 0 {
		assigneeNames = []string{"经办人", "处理人", "负责人", "开发负责人", "Assignee", "Owner"}
	}
	return &Monitor{
		client: client, store: store, ownerOpenID: strings.TrimSpace(cfg.OwnerOpenID),
		statusFieldNames: statusNames, assigneeFieldNames: assigneeNames, now: now,
	}
}

func (m *Monitor) IngestResourceNotification(
	ctx context.Context,
	event domain.NormalizedEvent,
) (bool, error) {
	if m == nil || m.client == nil || m.store == nil {
		return false, errs.NewConfigError(errs.SubtypeNotConfigured, "resource monitor is not configured")
	}
	if !applicationSender(event.SenderType) {
		return false, nil
	}
	urls := uniqueNonEmpty(event.ResourceURLs)
	if len(urls) == 0 {
		return false, nil
	}
	accepted := false
	for _, rawURL := range urls {
		ref, err := servicelark.ParseResourceURL(rawURL)
		if err != nil {
			return accepted, err
		}
		resolved, err := m.client.ResolveResource(ctx, ref)
		if err != nil {
			return accepted, err
		}
		evidence, err := m.evidenceFromResolvedResource(ctx, resolved, domain.ResourceEvidenceNotification)
		if err != nil {
			return accepted, err
		}
		evidence.SourceID = event.MessageID
		evidence.DedupKey = "notification:" + event.MessageID + ":" + resourceIdentity(resolved)
		evidence.OriginalURL = rawURL
		evidence.ObservedAt = event.CreatedAt
		if evidence.ObservedAt.IsZero() {
			evidence.ObservedAt = m.now().UTC()
		}
		if evidence.IssueKey == "" {
			evidence.IssueKey = issueKey(event.Content)
		}
		evidence.OwnerMentioned = evidence.OwnerMentioned ||
			event.MentionsUser(m.ownerOpenID) ||
			notificationMentionsOwner(event.Content)
		evidence.ContentDigest = digestEvidence(evidence)
		stored, inserted, err := m.store.RecordResourceEvidence(ctx, evidence)
		if err != nil {
			return accepted, err
		}
		accepted = true
		if inserted && stored.OwnerMentioned {
			if _, err := m.createResourceWork(ctx, stored, event.Content); err != nil {
				return accepted, err
			}
		}
	}
	return accepted, nil
}

func (m *Monitor) SyncSubscriptions(ctx context.Context) (SyncResult, error) {
	if m == nil || m.client == nil || m.store == nil {
		return SyncResult{}, errs.NewConfigError(errs.SubtypeNotConfigured, "resource monitor is not configured")
	}
	subs, err := m.store.ListResourceSubscriptions(ctx)
	if err != nil {
		return SyncResult{}, err
	}
	result := SyncResult{Seen: len(subs)}
	for _, sub := range subs {
		if sub.Status == domain.ResourceSubscriptionRemoved {
			continue
		}
		ref := subscriptionRef(sub)
		resolved, resolveErr := m.client.ResolveResource(ctx, ref)
		if resolveErr == nil {
			var remote servicelark.RemoteSubscription
			remote, resolveErr = m.client.EnsureCommentSubscription(ctx, resolved)
			if resolveErr == nil && !remote.Active {
				resolveErr = errs.NewPermissionError(
					errs.SubtypeFailedPrecondition,
					"Lark file subscription is not active",
				)
			}
			if resolveErr == nil {
				sub.ResourceType = string(resolved.ResourceType)
				sub.FileToken = resolved.FileToken
				sub.AppToken = resolved.AppToken
				sub.TableID = resolved.TableID
				sub.ViewID = resolved.ViewID
				sub.MonitorModes = MonitorModesForResource(resolved.ResourceType)
				sub.RemoteSubscriptionID = remote.ID
				sub.Status = domain.ResourceSubscriptionActive
				sub.LastError = ""
			}
		}
		if resolveErr != nil {
			sub.Status = subscriptionFailureStatus(resolveErr)
			sub.LastError = resolveErr.Error()
			result.Degraded++
		} else {
			result.Active++
		}
		if _, err := m.store.UpsertResourceSubscription(ctx, sub); err != nil {
			return result, err
		}
	}
	return result, nil
}

func MonitorModesForResource(resourceType servicelark.ResourceType) []string {
	if resourceType == servicelark.ResourceTypeBase {
		return []string{"base_record", "base_field", "cloud_docs_notice"}
	}
	return []string{"document_comment", "cloud_docs_notice"}
}

func (m *Monitor) Reconcile(ctx context.Context) (ReconcileResult, error) {
	if m == nil || m.client == nil || m.store == nil {
		return ReconcileResult{}, errs.NewConfigError(errs.SubtypeNotConfigured, "resource monitor is not configured")
	}
	subs, err := m.store.ListResourceSubscriptions(ctx)
	if err != nil {
		return ReconcileResult{}, err
	}
	var result ReconcileResult
	for _, sub := range subs {
		if (sub.Status != domain.ResourceSubscriptionActive &&
			sub.Status != domain.ResourceSubscriptionDegraded) ||
			sub.AppToken == "" || sub.TableID == "" {
			continue
		}
		result.Subscriptions++
		fields, err := m.client.ListBaseFields(ctx, sub.AppToken, sub.TableID)
		if err != nil {
			return result, m.degradeSubscription(ctx, sub, err)
		}
		records, err := m.client.ListBaseRecords(ctx, sub.AppToken, sub.TableID, sub.ViewID)
		if err != nil {
			return result, m.degradeSubscription(ctx, sub, err)
		}
		cold := strings.TrimSpace(sub.Cursor) == ""
		for _, record := range records {
			result.Records++
			evidence := m.projectRecord(sub, fields, record, domain.ResourceEvidenceReconcile)
			existing, err := m.store.FindResourceEvidence(ctx, domain.ResourceEvidenceQuery{
				AppToken: sub.AppToken, TableID: sub.TableID, RecordID: record.ID, Limit: 1,
			})
			if err != nil {
				return result, err
			}
			if len(existing) > 0 && existing[0].ContentDigest == evidence.ContentDigest {
				continue
			}
			stored, inserted, err := m.store.RecordResourceEvidence(ctx, evidence)
			if err != nil {
				return result, err
			}
			if !inserted {
				continue
			}
			result.Changed++
			if !cold && stored.OwnerMentioned {
				created, err := m.createResourceWork(ctx, stored, "subscribed Base record changed")
				if err != nil {
					return result, err
				}
				if created {
					result.WorkCreated++
				}
			}
		}
		sub.Cursor = m.now().UTC().Format(time.RFC3339Nano)
		sub.Status = domain.ResourceSubscriptionActive
		sub.LastError = ""
		if _, err := m.store.UpsertResourceSubscription(ctx, sub); err != nil {
			return result, err
		}
	}
	return result, nil
}

func (m *Monitor) HandleResourceSignal(
	ctx context.Context,
	signal servicelark.ResourceSignal,
) (SignalResult, error) {
	if m == nil || m.store == nil {
		return SignalResult{}, errs.NewConfigError(errs.SubtypeNotConfigured, "resource monitor is not configured")
	}
	if signal.EventID == "" || signal.FileToken == "" {
		return SignalResult{}, errs.NewValidationError(
			errs.SubtypeInvalidArgument,
			"resource signal event_id and file_token are required",
		)
	}
	subs, err := m.store.ListResourceSubscriptions(ctx)
	if err != nil {
		return SignalResult{}, err
	}
	if signal.Kind == servicelark.ResourceSignalFieldChange {
		reconciled, err := m.Reconcile(ctx)
		return SignalResult{
			MatchedSubscriptions: reconciled.Subscriptions,
			Evidence:             reconciled.Changed,
			WorkCreated:          reconciled.WorkCreated,
		}, err
	}
	var result SignalResult
	for _, sub := range subs {
		if sub.Status != domain.ResourceSubscriptionActive ||
			sub.FileToken != signal.FileToken ||
			(signal.TableID != "" && sub.TableID != "" && sub.TableID != signal.TableID) {
			continue
		}
		result.MatchedSubscriptions++
		switch signal.Kind {
		case servicelark.ResourceSignalComment:
			comment, err := m.client.GetComment(
				ctx, signal.FileToken, signal.FileType, signal.CommentID,
			)
			if err != nil {
				return result, m.degradeSubscription(ctx, sub, err)
			}
			title := strings.TrimSpace(strings.Join(
				[]string{comment.Quote, comment.Text},
				" ",
			))
			evidence := domain.ResourceEvidence{
				DedupKey:   "comment_event:" + signal.EventID,
				SourceKind: domain.ResourceEvidenceCommentEvent,
				SourceID:   signal.EventID, SubscriptionID: sub.ID,
				ResourceType: sub.ResourceType, OriginalURL: sub.OriginalURL,
				FileToken: sub.FileToken, AppToken: sub.AppToken, TableID: sub.TableID,
				ViewID: sub.ViewID, CommentID: signal.CommentID,
				Title: title, IssueKey: issueKey(title),
				OwnerMentioned: signal.Mentioned && signal.ToOpenID == m.ownerOpenID,
				ObservedAt:     signal.ObservedAt,
			}
			if evidence.ObservedAt.IsZero() {
				evidence.ObservedAt = m.now().UTC()
			}
			evidence.ContentDigest = digestEvidence(evidence)
			stored, inserted, err := m.store.RecordResourceEvidence(ctx, evidence)
			if err != nil {
				return result, err
			}
			if inserted {
				result.Evidence++
			}
			if inserted && stored.OwnerMentioned {
				created, err := m.createResourceWork(ctx, stored, "subscribed Base comment mentioned the owner")
				if err != nil {
					return result, err
				}
				if created {
					result.WorkCreated++
				}
			}
		case servicelark.ResourceSignalRecordChange:
			if m.client == nil || sub.AppToken == "" || sub.TableID == "" {
				continue
			}
			fields, err := m.client.ListBaseFields(ctx, sub.AppToken, sub.TableID)
			if err != nil {
				return result, m.degradeSubscription(ctx, sub, err)
			}
			for _, recordID := range uniqueNonEmpty(signal.RecordIDs) {
				record, err := m.client.GetBaseRecord(ctx, sub.AppToken, sub.TableID, recordID)
				if err != nil {
					return result, m.degradeSubscription(ctx, sub, err)
				}
				evidence := m.projectRecord(sub, fields, record, domain.ResourceEvidenceRecordEvent)
				evidence.SourceID = signal.EventID
				evidence.DedupKey = "record_event:" + signal.EventID + ":" + recordID
				evidence.ObservedAt = signal.ObservedAt
				if evidence.ObservedAt.IsZero() {
					evidence.ObservedAt = m.now().UTC()
				}
				evidence.ContentDigest = digestEvidence(evidence)
				stored, inserted, err := m.store.RecordResourceEvidence(ctx, evidence)
				if err != nil {
					return result, err
				}
				if !inserted {
					continue
				}
				result.Evidence++
				if stored.OwnerMentioned {
					created, err := m.createResourceWork(ctx, stored, "subscribed Base record changed")
					if err != nil {
						return result, err
					}
					if created {
						result.WorkCreated++
					}
				}
			}
		default:
			return result, errs.NewValidationError(
				errs.SubtypeInvalidArgument,
				"unsupported resource signal kind %q",
				signal.Kind,
			)
		}
	}
	return result, nil
}

func (m *Monitor) evidenceFromResolvedResource(
	ctx context.Context,
	ref servicelark.ResourceRef,
	source domain.ResourceEvidenceSource,
) (domain.ResourceEvidence, error) {
	evidence := domain.ResourceEvidence{
		SourceKind: source, ResourceType: string(ref.ResourceType), OriginalURL: ref.OriginalURL,
		FileToken: ref.FileToken, AppToken: ref.AppToken, TableID: ref.TableID,
		ViewID: ref.ViewID, RecordID: ref.RecordID,
	}
	if ref.ResourceType != servicelark.ResourceTypeBase || ref.TableID == "" || ref.RecordID == "" {
		evidence.Title = strings.TrimSpace(ref.OriginalURL)
		return evidence, nil
	}
	fields, err := m.client.ListBaseFields(ctx, ref.AppToken, ref.TableID)
	if err != nil {
		return domain.ResourceEvidence{}, err
	}
	record, err := m.client.GetBaseRecord(ctx, ref.AppToken, ref.TableID, ref.RecordID)
	if err != nil {
		return domain.ResourceEvidence{}, err
	}
	sub := domain.ResourceSubscription{
		ResourceType: string(ref.ResourceType), OriginalURL: ref.OriginalURL,
		FileToken: ref.FileToken, AppToken: ref.AppToken, TableID: ref.TableID, ViewID: ref.ViewID,
	}
	return m.projectRecord(sub, fields, record, source), nil
}

func (m *Monitor) projectRecord(
	sub domain.ResourceSubscription,
	fields []servicelark.BaseField,
	record servicelark.BaseRecord,
	source domain.ResourceEvidenceSource,
) domain.ResourceEvidence {
	titleField := firstPrimaryField(fields)
	statusField := selectField(fields, m.statusFieldNames, 3)
	assigneeField := selectField(fields, m.assigneeFieldNames, 11)
	title := fieldString(record.Fields[titleField.Name])
	status := fieldString(record.Fields[statusField.Name])
	assignees := fieldUserIDs(record.Fields[assigneeField.Name])
	sort.Strings(assignees)
	evidence := domain.ResourceEvidence{
		DedupKey:        fmt.Sprintf("record:%d:%s:%s", sub.ID, record.ID, digestRecord(record)),
		SourceKind:      source,
		SourceID:        record.ID,
		SubscriptionID:  sub.ID,
		ResourceType:    "base",
		OriginalURL:     sub.OriginalURL,
		FileToken:       sub.FileToken,
		AppToken:        sub.AppToken,
		TableID:         sub.TableID,
		ViewID:          sub.ViewID,
		RecordID:        record.ID,
		Title:           title,
		IssueKey:        issueKey(title),
		StatusFieldID:   statusField.ID,
		StatusFieldName: statusField.Name,
		StatusValue:     status,
		AssigneeOpenIDs: assignees,
		OwnerMentioned:  containsString(assignees, m.ownerOpenID),
		ObservedAt:      m.now().UTC(),
	}
	evidence.ContentDigest = digestEvidence(evidence)
	return evidence
}

func (m *Monitor) createResourceWork(
	ctx context.Context,
	evidence domain.ResourceEvidence,
	contextText string,
) (bool, error) {
	messageID := fmt.Sprintf("resource:%d", evidence.ID)
	content := strings.TrimSpace(strings.Join([]string{
		"Resource handoff",
		"Issue: " + evidence.IssueKey,
		"Title: " + evidence.Title,
		"Current status: " + evidence.StatusValue,
		"Resource: " + evidence.OriginalURL,
		"Notification: " + strings.TrimSpace(contextText),
	}, "\n"))
	event := domain.NormalizedEvent{
		Source: domain.SourceSchedule, EventID: messageID, MessageID: messageID,
		SenderType: "resource", Content: content,
		ResourceURLs: []string{evidence.OriginalURL},
		Mentions:     []domain.Mention{{OpenID: m.ownerOpenID}},
		CreatedAt:    evidence.ObservedAt, RawDigest: evidence.ContentDigest,
	}
	item := domain.NewWorkItem(event)
	item.Status = domain.StatusReady
	item.WorkKind = domain.WorkKindResourceHandoff
	item.Priority = domain.PriorityResourceHandoff
	item.ResourceEvidenceID = evidence.ID
	receipt, err := m.store.RecordWorkIntake(ctx, item)
	if err != nil {
		return false, err
	}
	return receipt.Disposition == domain.IntakeAdmitted, nil
}

func (m *Monitor) degradeSubscription(
	ctx context.Context,
	sub domain.ResourceSubscription,
	cause error,
) error {
	sub.Status = subscriptionFailureStatus(cause)
	sub.LastError = cause.Error()
	if _, err := m.store.UpsertResourceSubscription(ctx, sub); err != nil {
		return err
	}
	return cause
}

func subscriptionFailureStatus(err error) domain.ResourceSubscriptionStatus {
	if problem, ok := errs.ProblemOf(err); ok && problem.Category == errs.CategoryAuthorization {
		return domain.ResourceSubscriptionForbidden
	}
	return domain.ResourceSubscriptionDegraded
}

func subscriptionRef(sub domain.ResourceSubscription) servicelark.ResourceRef {
	ref, err := servicelark.ParseResourceURL(sub.OriginalURL)
	if err == nil {
		return ref
	}
	return servicelark.ResourceRef{
		OriginalURL: sub.OriginalURL, ResourceType: servicelark.ResourceType(sub.ResourceType),
		FileToken: sub.FileToken, AppToken: sub.AppToken, WikiNodeToken: sub.WikiNodeToken,
		TableID: sub.TableID, ViewID: sub.ViewID,
	}
}

func firstPrimaryField(fields []servicelark.BaseField) servicelark.BaseField {
	for _, field := range fields {
		if field.Primary {
			return field
		}
	}
	for _, field := range fields {
		if field.Type == 1 {
			return field
		}
	}
	return servicelark.BaseField{}
}

func selectField(
	fields []servicelark.BaseField,
	preferredNames []string,
	fieldType int,
) servicelark.BaseField {
	for _, name := range preferredNames {
		for _, field := range fields {
			if field.Type == fieldType && strings.EqualFold(strings.TrimSpace(field.Name), strings.TrimSpace(name)) {
				return field
			}
		}
	}
	for _, field := range fields {
		if field.Type == fieldType {
			return field
		}
	}
	return servicelark.BaseField{}
}

func fieldString(value any) string {
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed)
	case map[string]any:
		for _, key := range []string{"name", "text", "value"} {
			if text, ok := typed[key].(string); ok && strings.TrimSpace(text) != "" {
				return strings.TrimSpace(text)
			}
		}
	case []any:
		var values []string
		for _, item := range typed {
			if value := fieldString(item); value != "" {
				values = append(values, value)
			}
		}
		return strings.Join(values, ", ")
	}
	return ""
}

func fieldUserIDs(value any) []string {
	var ids []string
	var walk func(any)
	walk = func(current any) {
		switch typed := current.(type) {
		case map[string]any:
			for _, key := range []string{"id", "open_id", "openId"} {
				if id, ok := typed[key].(string); ok && strings.TrimSpace(id) != "" {
					ids = append(ids, strings.TrimSpace(id))
					return
				}
			}
			for _, child := range typed {
				walk(child)
			}
		case []any:
			for _, child := range typed {
				walk(child)
			}
		}
	}
	walk(value)
	return uniqueNonEmpty(ids)
}

func issueKey(text string) string {
	return strings.ToUpper(issueKeyPattern.FindString(text))
}

func applicationSender(senderType string) bool {
	switch strings.ToLower(strings.TrimSpace(senderType)) {
	case "app", "bot":
		return true
	default:
		return false
	}
}

func notificationMentionsOwner(content string) bool {
	lower := strings.ToLower(content)
	for _, marker := range []string{"提到了你", "提及了你", "mentioned you", "mentions you", "@你"} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

func digestRecord(record servicelark.BaseRecord) string {
	data, _ := json.Marshal(record)
	sum := sha256.Sum256(data)
	return fmt.Sprintf("sha256:%x", sum[:])
}

func digestEvidence(evidence domain.ResourceEvidence) string {
	copy := evidence
	copy.ID = 0
	copy.DedupKey = ""
	copy.SourceKind = ""
	copy.SourceID = ""
	copy.ContentDigest = ""
	copy.ObservedAt = time.Time{}
	data, _ := json.Marshal(copy)
	sum := sha256.Sum256(data)
	return fmt.Sprintf("sha256:%x", sum[:])
}

func resourceIdentity(ref servicelark.ResourceRef) string {
	return strings.Join([]string{ref.AppToken, ref.TableID, ref.RecordID, ref.FileToken}, ":")
}

func uniqueNonEmpty(values []string) []string {
	seen := make(map[string]bool, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		if value = strings.TrimSpace(value); value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	return out
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target && target != "" {
			return true
		}
	}
	return false
}
