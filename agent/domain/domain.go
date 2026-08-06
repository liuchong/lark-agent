// Package domain contains the stable business types shared by the Lark agent
// runtime, router, storage, and tools.
package domain

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// Mode controls whether the agent can act autonomously.
type Mode string

const (
	ModeAuto     Mode = "auto"
	ModeApproval Mode = "approval"
	ModePaused   Mode = "paused"
)

// ParseMode validates a user-visible mode string.
func ParseMode(raw string) (Mode, error) {
	switch Mode(raw) {
	case ModeAuto, ModeApproval, ModePaused:
		return Mode(raw), nil
	default:
		return "", fmt.Errorf("unsupported mode %q", raw)
	}
}

// ReplyScope controls which groups permit one configured reply entry point.
type ReplyScope string

const (
	ReplyScopeAllGroups        ReplyScope = "all_groups"
	ReplyScopeConfiguredGroups ReplyScope = "configured_groups"
)

// ParseReplyScope validates a user-visible reply scope.
func ParseReplyScope(raw string) (ReplyScope, error) {
	switch ReplyScope(raw) {
	case ReplyScopeAllGroups, ReplyScopeConfiguredGroups:
		return ReplyScope(raw), nil
	default:
		return "", fmt.Errorf("unsupported reply scope %q", raw)
	}
}

// PrivateReplyScope controls whether inbound human P2P messages may enter the
// delegated owner-reply workflow.
type PrivateReplyScope string

const (
	PrivateReplyScopeAll      PrivateReplyScope = "all_private"
	PrivateReplyScopeDisabled PrivateReplyScope = "disabled"
)

// ParsePrivateReplyScope validates a user-visible private reply scope.
func ParsePrivateReplyScope(raw string) (PrivateReplyScope, error) {
	switch PrivateReplyScope(raw) {
	case PrivateReplyScopeAll, PrivateReplyScopeDisabled:
		return PrivateReplyScope(raw), nil
	default:
		return "", fmt.Errorf("unsupported private reply scope %q", raw)
	}
}

// Sensitivity controls how eagerly non-mention messages are considered.
type Sensitivity string

const (
	SensitivityLow    Sensitivity = "low"
	SensitivityNormal Sensitivity = "normal"
	SensitivityHigh   Sensitivity = "high"
)

// EventSource records where a normalized event came from.
type EventSource string

const (
	SourceRealtime EventSource = "realtime"
	SourcePoll     EventSource = "poll"
	SourceSchedule EventSource = "schedule"
)

// Mention is a compact mention reference inside a message.
type Mention struct {
	Key    string `json:"key,omitempty" yaml:"key,omitempty"`
	OpenID string `json:"open_id" yaml:"open_id"`
	Name   string `json:"name,omitempty" yaml:"name,omitempty"`
}

// NormalizedEvent is the single durable intake shape for real-time and polled
// events. Content is untrusted business data.
type NormalizedEvent struct {
	Source           EventSource  `json:"source" yaml:"source"`
	EventID          string       `json:"event_id,omitempty" yaml:"event_id,omitempty"`
	MessageID        string       `json:"message_id" yaml:"message_id"`
	ChatID           string       `json:"chat_id,omitempty" yaml:"chat_id,omitempty"`
	ChatName         string       `json:"chat_name,omitempty" yaml:"chat_name,omitempty"`
	ChatType         string       `json:"chat_type,omitempty" yaml:"chat_type,omitempty"`
	ChatPartnerID    string       `json:"chat_partner_id,omitempty" yaml:"chat_partner_id,omitempty"`
	RootMessageID    string       `json:"root_id,omitempty" yaml:"root_id,omitempty"`
	ReplyToMessageID string       `json:"reply_to,omitempty" yaml:"reply_to,omitempty"`
	ThreadID         string       `json:"thread_id,omitempty" yaml:"thread_id,omitempty"`
	SenderID         string       `json:"sender_id,omitempty" yaml:"sender_id,omitempty"`
	SenderName       string       `json:"sender_name,omitempty" yaml:"sender_name,omitempty"`
	SenderType       string       `json:"sender_type,omitempty" yaml:"sender_type,omitempty"`
	Content          string       `json:"content,omitempty" yaml:"content,omitempty"`
	ResourceURLs     []string     `json:"resource_urls,omitempty" yaml:"resource_urls,omitempty"`
	Attachments      []Attachment `json:"attachments,omitempty" yaml:"attachments,omitempty"`
	Mentions         []Mention    `json:"mentions,omitempty" yaml:"mentions,omitempty"`
	CreatedAt        time.Time    `json:"created_at,omitempty" yaml:"created_at,omitempty"`
	UpdatedAt        time.Time    `json:"updated_at,omitempty" yaml:"updated_at,omitempty"`
	RawDigest        string       `json:"raw_digest,omitempty" yaml:"raw_digest,omitempty"`
	WorkspaceID      string       `json:"workspace_id,omitempty" yaml:"workspace_id,omitempty"`
	InTestScope      bool         `json:"in_test_scope,omitempty" yaml:"in_test_scope,omitempty"`
	InAssistantScope bool         `json:"in_assistant_scope,omitempty" yaml:"in_assistant_scope,omitempty"`
}

// Attachment describes bounded message evidence. DataURL is ephemeral and is
// deliberately excluded from durable event serialization.
type Attachment struct {
	Type             string `json:"type" yaml:"type"`
	Key              string `json:"key,omitempty" yaml:"key,omitempty"`
	MediaType        string `json:"media_type,omitempty" yaml:"media_type,omitempty"`
	Readable         bool   `json:"readable,omitempty" yaml:"readable,omitempty"`
	UnreadableReason string `json:"unreadable_reason,omitempty" yaml:"unreadable_reason,omitempty"`
	DataURL          string `json:"-" yaml:"-"`
}

// ContextMode describes how same-chat messages were selected around a target.
type ContextMode string

const (
	ContextModeAdjacent   ContextMode = "adjacent"
	ContextModeReplyChain ContextMode = "reply_chain"
	ContextModeThread     ContextMode = "thread"
)

// ContextSelection records relation-aware context provenance for the model.
type ContextSelection struct {
	Mode              ContextMode `json:"mode" yaml:"mode"`
	AnchorMessageID   string      `json:"anchor_message_id,omitempty" yaml:"anchor_message_id,omitempty"`
	RootMessageID     string      `json:"root_id,omitempty" yaml:"root_id,omitempty"`
	ReplyToMessageID  string      `json:"reply_to,omitempty" yaml:"reply_to,omitempty"`
	Truncated         bool        `json:"truncated,omitempty" yaml:"truncated,omitempty"`
	Incomplete        bool        `json:"incomplete,omitempty" yaml:"incomplete,omitempty"`
	MissingMessageIDs []string    `json:"missing_message_ids,omitempty" yaml:"missing_message_ids,omitempty"`
	Reason            string      `json:"reason,omitempty" yaml:"reason,omitempty"`
}

// MentionsUser reports whether the event explicitly mentions the owner.
func (e NormalizedEvent) MentionsUser(openID string) bool {
	if openID == "" {
		return false
	}
	for _, mention := range e.Mentions {
		if mention.OpenID == openID {
			return true
		}
	}
	return false
}

// OnlineSessionStatus is the durable lifecycle state of one daemon process.
type OnlineSessionStatus string

const (
	OnlineSessionStarting OnlineSessionStatus = "starting"
	OnlineSessionReady    OnlineSessionStatus = "ready"
	OnlineSessionStopped  OnlineSessionStatus = "stopped"
)

// OnlineSession identifies one daemon process lifetime.
type OnlineSession struct {
	ID        string              `json:"id" yaml:"id"`
	Status    OnlineSessionStatus `json:"status" yaml:"status"`
	StartedAt time.Time           `json:"started_at" yaml:"started_at"`
	ReadyAt   time.Time           `json:"ready_at,omitempty" yaml:"ready_at,omitempty"`
	EndedAt   time.Time           `json:"ended_at,omitempty" yaml:"ended_at,omitempty"`
	Reason    string              `json:"reason,omitempty" yaml:"reason,omitempty"`
}

type ResourceSubscriptionStatus string

const (
	ResourceSubscriptionPending   ResourceSubscriptionStatus = "pending"
	ResourceSubscriptionActive    ResourceSubscriptionStatus = "active"
	ResourceSubscriptionDegraded  ResourceSubscriptionStatus = "degraded"
	ResourceSubscriptionForbidden ResourceSubscriptionStatus = "forbidden"
	ResourceSubscriptionRemoved   ResourceSubscriptionStatus = "removed"
)

type ResourceSubscription struct {
	ID                   int64                      `json:"id" yaml:"id"`
	OriginalURL          string                     `json:"url" yaml:"url"`
	ResourceType         string                     `json:"resource_type" yaml:"resource_type"`
	FileToken            string                     `json:"file_token,omitempty" yaml:"file_token,omitempty"`
	AppToken             string                     `json:"app_token,omitempty" yaml:"app_token,omitempty"`
	WikiNodeToken        string                     `json:"wiki_node_token,omitempty" yaml:"wiki_node_token,omitempty"`
	TableID              string                     `json:"table_id,omitempty" yaml:"table_id,omitempty"`
	ViewID               string                     `json:"view_id,omitempty" yaml:"view_id,omitempty"`
	MonitorModes         []string                   `json:"monitor_modes,omitempty" yaml:"monitor_modes,omitempty"`
	RemoteSubscriptionID string                     `json:"remote_subscription_id,omitempty" yaml:"remote_subscription_id,omitempty"`
	Status               ResourceSubscriptionStatus `json:"status" yaml:"status"`
	Cursor               string                     `json:"cursor,omitempty" yaml:"cursor,omitempty"`
	LastError            string                     `json:"last_error,omitempty" yaml:"last_error,omitempty"`
	CreatedAt            time.Time                  `json:"created_at" yaml:"created_at"`
	UpdatedAt            time.Time                  `json:"updated_at" yaml:"updated_at"`
}

type ResourceEvidenceSource string

const (
	ResourceEvidenceNotification ResourceEvidenceSource = "notification"
	ResourceEvidenceCommentEvent ResourceEvidenceSource = "comment_event"
	ResourceEvidenceRecordEvent  ResourceEvidenceSource = "record_event"
	ResourceEvidenceReconcile    ResourceEvidenceSource = "reconcile"
)

// ResourceEvidence is the privacy-bounded durable projection of a trusted
// document or Base signal. Complete private threads and arbitrary record fields
// never enter this shape.
type ResourceEvidence struct {
	ID              int64                  `json:"id" yaml:"id"`
	DedupKey        string                 `json:"dedup_key" yaml:"dedup_key"`
	SourceKind      ResourceEvidenceSource `json:"source_kind" yaml:"source_kind"`
	SourceID        string                 `json:"source_id" yaml:"source_id"`
	SubscriptionID  int64                  `json:"subscription_id,omitempty" yaml:"subscription_id,omitempty"`
	ResourceType    string                 `json:"resource_type" yaml:"resource_type"`
	OriginalURL     string                 `json:"url,omitempty" yaml:"url,omitempty"`
	FileToken       string                 `json:"file_token,omitempty" yaml:"file_token,omitempty"`
	AppToken        string                 `json:"app_token,omitempty" yaml:"app_token,omitempty"`
	TableID         string                 `json:"table_id,omitempty" yaml:"table_id,omitempty"`
	ViewID          string                 `json:"view_id,omitempty" yaml:"view_id,omitempty"`
	RecordID        string                 `json:"record_id,omitempty" yaml:"record_id,omitempty"`
	CommentID       string                 `json:"comment_id,omitempty" yaml:"comment_id,omitempty"`
	Title           string                 `json:"title,omitempty" yaml:"title,omitempty"`
	IssueKey        string                 `json:"issue_key,omitempty" yaml:"issue_key,omitempty"`
	StatusFieldID   string                 `json:"status_field_id,omitempty" yaml:"status_field_id,omitempty"`
	StatusFieldName string                 `json:"status_field_name,omitempty" yaml:"status_field_name,omitempty"`
	StatusValue     string                 `json:"status_value,omitempty" yaml:"status_value,omitempty"`
	AssigneeOpenIDs []string               `json:"assignee_open_ids,omitempty" yaml:"assignee_open_ids,omitempty"`
	OwnerMentioned  bool                   `json:"owner_mentioned,omitempty" yaml:"owner_mentioned,omitempty"`
	ContentDigest   string                 `json:"content_digest" yaml:"content_digest"`
	ObservedAt      time.Time              `json:"observed_at" yaml:"observed_at"`
}

type ResourceEvidenceQuery struct {
	Terms    []string `json:"terms,omitempty" yaml:"terms,omitempty"`
	AppToken string   `json:"app_token,omitempty" yaml:"app_token,omitempty"`
	TableID  string   `json:"table_id,omitempty" yaml:"table_id,omitempty"`
	RecordID string   `json:"record_id,omitempty" yaml:"record_id,omitempty"`
	Limit    int      `json:"limit,omitempty" yaml:"limit,omitempty"`
}

// GitHubReferenceKind identifies the trusted external object behind one Lark
// notification.
type GitHubReferenceKind string

const (
	GitHubReferenceWorkflowRun GitHubReferenceKind = "workflow_run"
	GitHubReferencePullRequest GitHubReferenceKind = "pull_request"
)

// GitHubReference is trusted only after the Lark relation and app sender are
// verified outside the model.
type GitHubReference struct {
	SchemaVersion      int                 `json:"schema_version" yaml:"schema_version"`
	Repository         string              `json:"repository" yaml:"repository"`
	Kind               GitHubReferenceKind `json:"kind" yaml:"kind"`
	WorkflowRunID      int64               `json:"workflow_run_id,omitempty" yaml:"workflow_run_id,omitempty"`
	WorkflowRunAttempt int                 `json:"workflow_run_attempt,omitempty" yaml:"workflow_run_attempt,omitempty"`
	PullRequestNumber  int                 `json:"pull_request_number,omitempty" yaml:"pull_request_number,omitempty"`
	HeadSHA            string              `json:"head_sha,omitempty" yaml:"head_sha,omitempty"`
	HTMLURL            string              `json:"html_url,omitempty" yaml:"html_url,omitempty"`
}

var (
	gitHubRepositoryPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)
	gitHubSHAPattern        = regexp.MustCompile(`^[a-fA-F0-9]{40}([a-fA-F0-9]{24})?$`)
)

func (r GitHubReference) Validate() error {
	if r.SchemaVersion != 1 {
		return fmt.Errorf("unsupported github reference schema version %d", r.SchemaVersion)
	}
	if !gitHubRepositoryPattern.MatchString(r.Repository) {
		return fmt.Errorf("invalid github repository %q", r.Repository)
	}
	switch r.Kind {
	case GitHubReferenceWorkflowRun:
		if r.WorkflowRunID <= 0 {
			return fmt.Errorf("github workflow_run_id must be positive")
		}
		if r.WorkflowRunAttempt < 0 {
			return fmt.Errorf("github workflow_run_attempt cannot be negative")
		}
	case GitHubReferencePullRequest:
		if r.PullRequestNumber <= 0 {
			return fmt.Errorf("github pull_request_number must be positive")
		}
	default:
		return fmt.Errorf("unsupported github reference kind %q", r.Kind)
	}
	if r.PullRequestNumber < 0 {
		return fmt.Errorf("github pull_request_number cannot be negative")
	}
	if r.HeadSHA != "" && !gitHubSHAPattern.MatchString(r.HeadSHA) {
		return fmt.Errorf("invalid github head_sha")
	}
	if r.HTMLURL != "" {
		parsed, err := url.Parse(r.HTMLURL)
		if err != nil || (parsed.Scheme != "https" && parsed.Scheme != "http") || parsed.Host == "" {
			return fmt.Errorf("invalid github html_url")
		}
	}
	return nil
}

func (r GitHubReference) ExternalKey() string {
	switch r.Kind {
	case GitHubReferenceWorkflowRun:
		return fmt.Sprintf("%s:workflow_run:%d:%d", r.Repository, r.WorkflowRunID, r.WorkflowRunAttempt)
	case GitHubReferencePullRequest:
		return fmt.Sprintf("%s:pull_request:%d", r.Repository, r.PullRequestNumber)
	default:
		return ""
	}
}

// ExternalReference is one verified Lark-message-to-external-object binding.
type ExternalReference struct {
	ID              int64           `json:"id" yaml:"id"`
	Provider        string          `json:"provider" yaml:"provider"`
	Kind            string          `json:"kind" yaml:"kind"`
	ExternalKey     string          `json:"external_key" yaml:"external_key"`
	LarkMessageID   string          `json:"lark_message_id" yaml:"lark_message_id"`
	ChatID          string          `json:"chat_id" yaml:"chat_id"`
	SenderAppID     string          `json:"sender_app_id" yaml:"sender_app_id"`
	Reference       GitHubReference `json:"reference" yaml:"reference"`
	ReferenceDigest string          `json:"reference_digest" yaml:"reference_digest"`
	VerifiedAt      time.Time       `json:"verified_at" yaml:"verified_at"`
	UpdatedAt       time.Time       `json:"updated_at" yaml:"updated_at"`
}

// IntakeDisposition records whether an observed event entered the work queue.
type IntakeDisposition string

const (
	IntakeAdmitted       IntakeDisposition = "admitted"
	IntakeDuplicate      IntakeDisposition = "duplicate"
	IntakeOfflineBacklog IntakeDisposition = "offline_backlog"
)

// IntakeReceipt is the durable admission result for one observed event.
type IntakeReceipt struct {
	ID             int64             `json:"id" yaml:"id"`
	MessageID      string            `json:"message_id,omitempty" yaml:"message_id,omitempty"`
	EventID        string            `json:"event_id,omitempty" yaml:"event_id,omitempty"`
	Source         EventSource       `json:"source" yaml:"source"`
	SessionID      string            `json:"session_id" yaml:"session_id"`
	EventJSON      string            `json:"event_json" yaml:"event_json"`
	EventCreatedAt time.Time         `json:"event_created_at,omitempty" yaml:"event_created_at,omitempty"`
	ObservedAt     time.Time         `json:"observed_at" yaml:"observed_at"`
	Disposition    IntakeDisposition `json:"disposition" yaml:"disposition"`
	Reason         string            `json:"reason,omitempty" yaml:"reason,omitempty"`
	WorkItemID     int64             `json:"work_item_id,omitempty" yaml:"work_item_id,omitempty"`
}

// WorkItemStatus is the durable state of a unit of agent work.
type WorkItemStatus string

const (
	StatusReceived         WorkItemStatus = "received"
	StatusRouted           WorkItemStatus = "routed"
	StatusWaitingUser      WorkItemStatus = "waiting_user"
	StatusReady            WorkItemStatus = "ready"
	StatusProcessing       WorkItemStatus = "processing"
	StatusAwaitingApproval WorkItemStatus = "awaiting_approval"
	StatusExecuting        WorkItemStatus = "executing"
	StatusCompleted        WorkItemStatus = "completed"
	StatusIgnored          WorkItemStatus = "ignored"
	StatusCancelled        WorkItemStatus = "cancelled"
	StatusRetryWait        WorkItemStatus = "retry_wait"
	StatusDeadLetter       WorkItemStatus = "dead_letter"
	StatusInterrupted      WorkItemStatus = "interrupted"
)

// WorkKind classifies the scheduler lane for a work item.
type WorkKind string

const (
	WorkKindGeneric         WorkKind = "generic"
	WorkKindFastPath        WorkKind = "fast_path"
	WorkKindOwnerControl    WorkKind = "owner_control"
	WorkKindSimpleQuestion  WorkKind = "simple_question"
	WorkKindDirectMention   WorkKind = "direct_mention"
	WorkKindResourceHandoff WorkKind = "resource_handoff"
	WorkKindCodingQuestion  WorkKind = "coding_question"
	WorkKindCodingGoal      WorkKind = "coding_goal"
)

// TaskClass describes the work implied by bounded conversation context.
type TaskClass string

const (
	TaskClassSimple          TaskClass = "simple"
	TaskClassInvestigation   TaskClass = "investigation"
	TaskClassCoding          TaskClass = "coding"
	TaskClassResourceHandoff TaskClass = "resource_handoff"
)

// DelegatedInvestigationStatus tracks one resumable delegated investigation.
type DelegatedInvestigationStatus string

const (
	InvestigationPendingProgress DelegatedInvestigationStatus = "pending_progress"
	InvestigationInvestigating   DelegatedInvestigationStatus = "investigating"
	InvestigationFinalizing      DelegatedInvestigationStatus = "finalizing"
	InvestigationCompleted       DelegatedInvestigationStatus = "completed"
	InvestigationBlocked         DelegatedInvestigationStatus = "blocked"
)

// DelegatedInvestigation is the durable closure contract for staged replies.
type DelegatedInvestigation struct {
	ID               int64                        `json:"id" yaml:"id"`
	WorkItemID       int64                        `json:"work_item_id" yaml:"work_item_id"`
	TaskSummary      string                       `json:"task_summary" yaml:"task_summary"`
	TaskClass        TaskClass                    `json:"task_class" yaml:"task_class"`
	ContextCutoff    time.Time                    `json:"context_cutoff" yaml:"context_cutoff"`
	ContextDigest    string                       `json:"context_digest" yaml:"context_digest"`
	ContextMessages  []NormalizedEvent            `json:"context_messages,omitempty" yaml:"context_messages,omitempty"`
	Status           DelegatedInvestigationStatus `json:"status" yaml:"status"`
	ProgressActionID int64                        `json:"progress_action_id,omitempty" yaml:"progress_action_id,omitempty"`
	FinalActionID    int64                        `json:"final_action_id,omitempty" yaml:"final_action_id,omitempty"`
	LastError        string                       `json:"last_error,omitempty" yaml:"last_error,omitempty"`
	CreatedAt        time.Time                    `json:"created_at" yaml:"created_at"`
	UpdatedAt        time.Time                    `json:"updated_at" yaml:"updated_at"`
}

// ArchivedDelegatedInvestigation preserves one superseded contextual
// generation for operator inspection without making it runnable.
type ArchivedDelegatedInvestigation struct {
	DelegatedInvestigation
	ArchivedReason string    `json:"archived_reason" yaml:"archived_reason"`
	ArchivedAt     time.Time `json:"archived_at" yaml:"archived_at"`
}

const (
	PriorityBackground      = 10
	PriorityCodingQuestion  = 40
	PriorityDirectMention   = 60
	PriorityResourceHandoff = 65
	PrioritySimpleQuestion  = 70
	PriorityOwnerControl    = 95
	PriorityFastPath        = 90
	PriorityPostReply       = 100
)

// SchedulerLane separates latency-sensitive replies from durable background
// goals while preserving priority ordering inside each lane.
type SchedulerLane string

const (
	SchedulerLaneAny         SchedulerLane = "any"
	SchedulerLaneInteractive SchedulerLane = "interactive"
	SchedulerLaneForeground  SchedulerLane = "foreground"
	SchedulerLaneBackground  SchedulerLane = "background"
)

// WorkItem is a durable task produced from a normalized event.
type WorkItem struct {
	ID                  int64             `json:"id" yaml:"id"`
	DedupKey            string            `json:"dedup_key" yaml:"dedup_key"`
	Status              WorkItemStatus    `json:"status" yaml:"status"`
	WorkKind            WorkKind          `json:"work_kind,omitempty" yaml:"work_kind,omitempty"`
	Priority            int               `json:"priority,omitempty" yaml:"priority,omitempty"`
	Generation          int               `json:"generation,omitempty" yaml:"generation,omitempty"`
	DuplicateOf         int64             `json:"duplicate_of,omitempty" yaml:"duplicate_of,omitempty"`
	SessionID           string            `json:"session_id,omitempty" yaml:"session_id,omitempty"`
	Event               NormalizedEvent   `json:"event" yaml:"event"`
	LeaseBy             string            `json:"lease_by,omitempty" yaml:"lease_by,omitempty"`
	LeaseTime           time.Time         `json:"lease_time,omitempty" yaml:"lease_time,omitempty"`
	RetryCount          int               `json:"retry_count,omitempty" yaml:"retry_count,omitempty"`
	NextAttemptAt       time.Time         `json:"next_attempt_at,omitempty" yaml:"next_attempt_at,omitempty"`
	CreatedAt           time.Time         `json:"created_at,omitempty" yaml:"created_at,omitempty"`
	UpdatedAt           time.Time         `json:"updated_at,omitempty" yaml:"updated_at,omitempty"`
	TaskSummary         string            `json:"-" yaml:"-"`
	TaskClass           TaskClass         `json:"-" yaml:"-"`
	ContextCutoff       time.Time         `json:"-" yaml:"-"`
	ContextDigest       string            `json:"-" yaml:"-"`
	ResolvedContext     []NormalizedEvent `json:"-" yaml:"-"`
	InvestigationActive bool              `json:"-" yaml:"-"`
	ResourceEvidenceID  int64             `json:"-" yaml:"-"`
}

// CodingWorkKind distinguishes one-shot answers from durable follow-up goals.
type CodingWorkKind string

const (
	CodingQuestion CodingWorkKind = "coding_question"
	CodingGoalWork CodingWorkKind = "coding_goal"
)

// CodingGoalStatus is the state of durable multi-turn coding follow-up.
type CodingGoalStatus string

const (
	CodingGoalActive    CodingGoalStatus = "active"
	CodingGoalBlocked   CodingGoalStatus = "blocked"
	CodingGoalCompleted CodingGoalStatus = "completed"
	CodingGoalPaused    CodingGoalStatus = "paused"
)

// CodingGoalSpec is the minimal validated input for creating a CodingGoal.
type CodingGoalSpec struct {
	WorkItemID            int64    `json:"work_item_id" yaml:"work_item_id"`
	OriginalMessageID     string   `json:"original_message_id" yaml:"original_message_id"`
	Question              string   `json:"question" yaml:"question"`
	CompletionConditions  []string `json:"completion_conditions" yaml:"completion_conditions"`
	BlockingConditions    []string `json:"blocking_conditions" yaml:"blocking_conditions"`
	MaxInvestigationTurns int      `json:"max_investigation_turns" yaml:"max_investigation_turns"`
}

// CodingGoal records a durable coding follow-up that should outlive one model run.
type CodingGoal struct {
	Kind                   CodingWorkKind   `json:"kind" yaml:"kind"`
	Status                 CodingGoalStatus `json:"status" yaml:"status"`
	WorkItemID             int64            `json:"work_item_id" yaml:"work_item_id"`
	OriginalMessageID      string           `json:"original_message_id" yaml:"original_message_id"`
	Question               string           `json:"question" yaml:"question"`
	CompletionConditions   []string         `json:"completion_conditions" yaml:"completion_conditions"`
	BlockingConditions     []string         `json:"blocking_conditions" yaml:"blocking_conditions"`
	MaxInvestigationTurns  int              `json:"max_investigation_turns" yaml:"max_investigation_turns"`
	UsedInvestigationTurns int              `json:"used_investigation_turns" yaml:"used_investigation_turns"`
	CreatedAt              time.Time        `json:"created_at" yaml:"created_at"`
	UpdatedAt              time.Time        `json:"updated_at" yaml:"updated_at"`
}

// NewCodingGoal validates that durable coding follow-up has explicit boundaries.
func NewCodingGoal(spec CodingGoalSpec) (CodingGoal, error) {
	if spec.WorkItemID == 0 {
		return CodingGoal{}, fmt.Errorf("coding goal work item id is required")
	}
	if spec.OriginalMessageID == "" {
		return CodingGoal{}, fmt.Errorf("coding goal original message id is required")
	}
	if spec.Question == "" {
		return CodingGoal{}, fmt.Errorf("coding goal question is required")
	}
	if len(nonEmptyStrings(spec.CompletionConditions)) == 0 {
		return CodingGoal{}, fmt.Errorf("coding goal completion conditions are required")
	}
	if len(nonEmptyStrings(spec.BlockingConditions)) == 0 {
		return CodingGoal{}, fmt.Errorf("coding goal blocking conditions are required")
	}
	if spec.MaxInvestigationTurns <= 0 {
		spec.MaxInvestigationTurns = 150
	}
	now := time.Now().UTC()
	return CodingGoal{
		Kind:                  CodingGoalWork,
		Status:                CodingGoalActive,
		WorkItemID:            spec.WorkItemID,
		OriginalMessageID:     spec.OriginalMessageID,
		Question:              spec.Question,
		CompletionConditions:  nonEmptyStrings(spec.CompletionConditions),
		BlockingConditions:    nonEmptyStrings(spec.BlockingConditions),
		MaxInvestigationTurns: spec.MaxInvestigationTurns,
		CreatedAt:             now,
		UpdatedAt:             now,
	}, nil
}

func nonEmptyStrings(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			out = append(out, value)
		}
	}
	return out
}

// NewWorkItem creates a received work item with a stable message-based dedupe
// key.
func NewWorkItem(event NormalizedEvent) WorkItem {
	return WorkItem{
		DedupKey:  DedupKey(event),
		Status:    StatusReceived,
		WorkKind:  WorkKindGeneric,
		Priority:  PriorityBackground,
		Event:     event,
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}
}

// DedupKey returns the idempotency key for the event intake layer.
func DedupKey(event NormalizedEvent) string {
	if event.MessageID != "" {
		return "message:" + event.MessageID
	}
	if event.EventID != "" {
		return "event:" + event.EventID
	}
	return "content:" + event.RawDigest
}

// DecisionKind is the router/agent outcome before action execution.
type DecisionKind string

const (
	DecisionIgnore          DecisionKind = "ignore"
	DecisionRecord          DecisionKind = "record"
	DecisionNotify          DecisionKind = "notify"
	DecisionReply           DecisionKind = "reply"
	DecisionRequestApproval DecisionKind = "request_approval"
)

// Relevance records why a work item is relevant.
type Relevance string

const (
	RelevanceNone             Relevance = "none"
	RelevanceDirectMention    Relevance = "direct_mention"
	RelevancePrivateMessage   Relevance = "private_message"
	RelevanceInferred         Relevance = "inferred"
	RelevanceOwnerRequest     Relevance = "owner_request"
	RelevanceAssistantRequest Relevance = "assistant_request"
)

// Risk is a policy classification for proposed side effects.
type Risk string

const (
	RiskLow       Risk = "low"
	RiskMedium    Risk = "medium"
	RiskHigh      Risk = "high"
	RiskForbidden Risk = "forbidden"
)

// EvidenceStatus records whether a reply makes a verified claim or stops at
// an evidence-limited result.
type EvidenceStatus string

const (
	EvidenceVerified     EvidenceStatus = "verified"
	EvidenceInsufficient EvidenceStatus = "insufficient"
)

// ReplyOutcome records whether a sender-facing decision is complete, bounded
// but partial, or requires exact missing input.
type ReplyOutcome string

const (
	ReplyOutcomeComplete      ReplyOutcome = "complete"
	ReplyOutcomePartial       ReplyOutcome = "partial"
	ReplyOutcomeClarification ReplyOutcome = "clarification"
)

// DecisionProgress preserves useful bounded work independently from reply
// wording so quality checks do not depend on fixed phrases.
type DecisionProgress struct {
	CompletedChecks []string `json:"completed_checks,omitempty" yaml:"completed_checks,omitempty"`
	InitialFinding  string   `json:"initial_finding,omitempty" yaml:"initial_finding,omitempty"`
	Unknowns        []string `json:"unknowns,omitempty" yaml:"unknowns,omitempty"`
	NextStep        string   `json:"next_step,omitempty" yaml:"next_step,omitempty"`
}

// Decision is an auditable routing or agent decision.
type Decision struct {
	Kind           DecisionKind         `json:"kind" yaml:"kind"`
	Mode           Mode                 `json:"mode" yaml:"mode"`
	Relevance      Relevance            `json:"relevance" yaml:"relevance"`
	WorkKind       WorkKind             `json:"work_kind,omitempty" yaml:"work_kind,omitempty"`
	Priority       int                  `json:"priority,omitempty" yaml:"priority,omitempty"`
	Confidence     float64              `json:"confidence" yaml:"confidence"`
	Risk           Risk                 `json:"risk" yaml:"risk"`
	EvidenceStatus EvidenceStatus       `json:"evidence_status,omitempty" yaml:"evidence_status,omitempty"`
	ReplyOutcome   ReplyOutcome         `json:"reply_outcome,omitempty" yaml:"reply_outcome,omitempty"`
	Progress       DecisionProgress     `json:"progress,omitempty" yaml:"progress,omitempty"`
	Reason         string               `json:"reason" yaml:"reason"`
	ReplyText      string               `json:"reply_text,omitempty" yaml:"reply_text,omitempty"`
	OwnerAction    string               `json:"owner_action,omitempty" yaml:"owner_action,omitempty"`
	Language       string               `json:"language,omitempty" yaml:"language,omitempty"`
	Sources        []SourceRef          `json:"sources,omitempty" yaml:"sources,omitempty"`
	ControlCommand *OwnerControlCommand `json:"control_command,omitempty" yaml:"control_command,omitempty"`
}

// ReplyCandidateStatus is the durable lifecycle of one validated but not yet
// necessarily sent reply.
type ReplyCandidateStatus string

const (
	ReplyCandidatePending   ReplyCandidateStatus = "pending"
	ReplyCandidateHeld      ReplyCandidateStatus = "held"
	ReplyCandidateConsumed  ReplyCandidateStatus = "consumed"
	ReplyCandidateCancelled ReplyCandidateStatus = "cancelled"
)

// WorkReplyCandidate preserves an already validated decision across a final
// owner-handled semantic recheck.
type WorkReplyCandidate struct {
	WorkItemID int64                `json:"work_item_id" yaml:"work_item_id"`
	Digest     string               `json:"digest" yaml:"digest"`
	Status     ReplyCandidateStatus `json:"status" yaml:"status"`
	HoldReason string               `json:"hold_reason,omitempty" yaml:"hold_reason,omitempty"`
	Decision   Decision             `json:"decision" yaml:"decision"`
	CreatedAt  time.Time            `json:"created_at" yaml:"created_at"`
	UpdatedAt  time.Time            `json:"updated_at" yaml:"updated_at"`
}

// SourceRef identifies a source that may be shown to the model or owner.
type SourceRef struct {
	RelativePath string `json:"relative_path,omitempty" yaml:"relative_path,omitempty"`
	Digest       string `json:"digest,omitempty" yaml:"digest,omitempty"`
	Kind         string `json:"kind,omitempty" yaml:"kind,omitempty"`
}

// WorkspaceScope records the current local context boundary.
type WorkspaceScope struct {
	ConfiguredRoot string    `json:"configured_root" yaml:"configured_root"`
	RealRoot       string    `json:"real_root" yaml:"real_root"`
	Version        string    `json:"version" yaml:"version"`
	CreatedAt      time.Time `json:"created_at" yaml:"created_at"`
}

// ActionStatus is the state of a proposed or executed side effect.
type ActionStatus string

const (
	ActionReady            ActionStatus = "ready"
	ActionAwaitingApproval ActionStatus = "awaiting_approval"
	ActionExecuting        ActionStatus = "executing"
	ActionCompleted        ActionStatus = "completed"
	ActionCancelled        ActionStatus = "cancelled"
	ActionBlocked          ActionStatus = "blocked"
)

// Action describes an auditable side effect.
type Action struct {
	ID           int64        `json:"id,omitempty" yaml:"id,omitempty"`
	Status       ActionStatus `json:"status" yaml:"status"`
	Kind         string       `json:"kind" yaml:"kind"`
	Idempotency  string       `json:"idempotency,omitempty" yaml:"idempotency,omitempty"`
	CancelReason string       `json:"cancel_reason,omitempty" yaml:"cancel_reason,omitempty"`
}

// AgentRunStatus is the durable state of a multi-step model run.
type AgentRunStatus string

const (
	AgentRunRunning   AgentRunStatus = "running"
	AgentRunCompleted AgentRunStatus = "completed"
	AgentRunFailed    AgentRunStatus = "failed"
	AgentRunAbandoned AgentRunStatus = "abandoned"
)

// AgentRun records one bounded multi-step attempt for a work item.
type AgentRun struct {
	ID                string         `json:"id" yaml:"id"`
	WorkItemID        int64          `json:"work_item_id" yaml:"work_item_id"`
	DedupKey          string         `json:"dedup_key" yaml:"dedup_key"`
	Status            AgentRunStatus `json:"status" yaml:"status"`
	Role              string         `json:"role,omitempty" yaml:"role,omitempty"`
	Profile           string         `json:"profile,omitempty" yaml:"profile,omitempty"`
	Provider          string         `json:"provider,omitempty" yaml:"provider,omitempty"`
	Protocol          string         `json:"protocol,omitempty" yaml:"protocol,omitempty"`
	Model             string         `json:"model,omitempty" yaml:"model,omitempty"`
	ModelFingerprint  string         `json:"model_fingerprint,omitempty" yaml:"model_fingerprint,omitempty"`
	ConfigFingerprint string         `json:"config_fingerprint,omitempty" yaml:"config_fingerprint,omitempty"`
	LastError         string         `json:"last_error,omitempty" yaml:"last_error,omitempty"`
	StartedAt         time.Time      `json:"started_at" yaml:"started_at"`
	CompletedAt       time.Time      `json:"completed_at,omitempty" yaml:"completed_at,omitempty"`
}

// AgentStep records one model response or one tool result.
type AgentStep struct {
	RunID            string    `json:"run_id" yaml:"run_id"`
	Sequence         int       `json:"sequence" yaml:"sequence"`
	Kind             string    `json:"kind" yaml:"kind"`
	Phase            string    `json:"phase,omitempty" yaml:"phase,omitempty"`
	Attempt          int       `json:"attempt,omitempty" yaml:"attempt,omitempty"`
	ToolCallID       string    `json:"tool_call_id,omitempty" yaml:"tool_call_id,omitempty"`
	ToolName         string    `json:"tool_name,omitempty" yaml:"tool_name,omitempty"`
	InputJSON        string    `json:"input_json,omitempty" yaml:"input_json,omitempty"`
	OutputJSON       string    `json:"output_json,omitempty" yaml:"output_json,omitempty"`
	RequestID        string    `json:"request_id,omitempty" yaml:"request_id,omitempty"`
	FinishReason     string    `json:"finish_reason,omitempty" yaml:"finish_reason,omitempty"`
	HTTPStatus       int       `json:"http_status,omitempty" yaml:"http_status,omitempty"`
	FailureCategory  string    `json:"failure_category,omitempty" yaml:"failure_category,omitempty"`
	RecoveryAction   string    `json:"recovery_action,omitempty" yaml:"recovery_action,omitempty"`
	PromptTokens     int       `json:"prompt_tokens,omitempty" yaml:"prompt_tokens,omitempty"`
	CompletionTokens int       `json:"completion_tokens,omitempty" yaml:"completion_tokens,omitempty"`
	Error            string    `json:"error,omitempty" yaml:"error,omitempty"`
	CreatedAt        time.Time `json:"created_at" yaml:"created_at"`
}

// ActionAttempt is one durable, idempotent side-effect proposal or execution.
type ActionAttempt struct {
	ID             int64        `json:"id" yaml:"id"`
	WorkItemID     int64        `json:"work_item_id" yaml:"work_item_id"`
	RunID          string       `json:"run_id,omitempty" yaml:"run_id,omitempty"`
	Kind           string       `json:"kind" yaml:"kind"`
	IdempotencyKey string       `json:"idempotency_key" yaml:"idempotency_key"`
	Status         ActionStatus `json:"status" yaml:"status"`
	RequestJSON    string       `json:"request_json,omitempty" yaml:"request_json,omitempty"`
	ResponseJSON   string       `json:"response_json,omitempty" yaml:"response_json,omitempty"`
	Error          string       `json:"error,omitempty" yaml:"error,omitempty"`
	CreatedAt      time.Time    `json:"created_at" yaml:"created_at"`
	UpdatedAt      time.Time    `json:"updated_at" yaml:"updated_at"`
}

// InterruptionStage describes the most precise durable stage observed at a
// process boundary.
type InterruptionStage string

const (
	InterruptionStageQueue           InterruptionStage = "queue"
	InterruptionStageModel           InterruptionStage = "model"
	InterruptionStageTool            InterruptionStage = "tool"
	InterruptionStageActionExecution InterruptionStage = "action_execution"
)

// WorkInterruption snapshots the last known execution evidence before a work
// item is paused across daemon sessions.
type WorkInterruption struct {
	ID            int64             `json:"id" yaml:"id"`
	WorkItemID    int64             `json:"work_item_id" yaml:"work_item_id"`
	RunID         string            `json:"run_id,omitempty" yaml:"run_id,omitempty"`
	SessionID     string            `json:"session_id,omitempty" yaml:"session_id,omitempty"`
	Stage         InterruptionStage `json:"stage" yaml:"stage"`
	LastSequence  int               `json:"last_sequence,omitempty" yaml:"last_sequence,omitempty"`
	LastKind      string            `json:"last_kind,omitempty" yaml:"last_kind,omitempty"`
	LastTool      string            `json:"last_tool,omitempty" yaml:"last_tool,omitempty"`
	ActionKind    string            `json:"action_kind,omitempty" yaml:"action_kind,omitempty"`
	ActionStatus  ActionStatus      `json:"action_status,omitempty" yaml:"action_status,omitempty"`
	Reason        string            `json:"reason" yaml:"reason"`
	InterruptedAt time.Time         `json:"interrupted_at" yaml:"interrupted_at"`
	ResumedAt     time.Time         `json:"resumed_at,omitempty" yaml:"resumed_at,omitempty"`
}

// WorkInspectionQuery selects one durable intake by work or message identity.
type WorkInspectionQuery struct {
	WorkItemID int64  `json:"work_item_id,omitempty" yaml:"work_item_id,omitempty"`
	MessageID  string `json:"message_id,omitempty" yaml:"message_id,omitempty"`
}

// ResumeWorkRequest explicitly moves paused work into the current session.
type ResumeWorkRequest struct {
	WorkItemID    int64  `json:"work_item_id,omitempty" yaml:"work_item_id,omitempty"`
	MessageID     string `json:"message_id,omitempty" yaml:"message_id,omitempty"`
	ForceTerminal bool   `json:"force_terminal,omitempty" yaml:"force_terminal,omitempty"`
}

// CancelWorkRequest selects audited queue work for durable cancellation.
type CancelWorkRequest struct {
	WorkItemIDs     []int64  `json:"work_item_ids,omitempty" yaml:"work_item_ids,omitempty"`
	MessageIDs      []string `json:"message_ids,omitempty" yaml:"message_ids,omitempty"`
	AllInterrupted  bool     `json:"all_interrupted,omitempty" yaml:"all_interrupted,omitempty"`
	KeepWorkItemIDs []int64  `json:"keep_work_item_ids,omitempty" yaml:"keep_work_item_ids,omitempty"`
	Reason          string   `json:"reason" yaml:"reason"`
}

// CancelWorkResult reports the exact durable work closed by an operator.
type CancelWorkResult struct {
	Changed              int     `json:"changed" yaml:"changed"`
	CancelledWorkItemIDs []int64 `json:"cancelled_work_item_ids" yaml:"cancelled_work_item_ids"`
	Reason               string  `json:"reason" yaml:"reason"`
}

// WorkInspectionState gives direct answers for the durable lifecycle facts
// most useful to operators and automation.
type WorkInspectionState struct {
	Observed       bool `json:"observed" yaml:"observed"`
	Admitted       bool `json:"admitted" yaml:"admitted"`
	OfflineBacklog bool `json:"offline_backlog" yaml:"offline_backlog"`
	Interrupted    bool `json:"interrupted" yaml:"interrupted"`
	Replied        bool `json:"replied" yaml:"replied"`
	Completed      bool `json:"completed" yaml:"completed"`
	Uncertain      bool `json:"uncertain" yaml:"uncertain"`
}

// WorkInspection combines the latest receipt, queue, run, step, action, and
// interruption evidence for one message.
type WorkInspection struct {
	Receipt              *IntakeReceipt                   `json:"receipt,omitempty" yaml:"receipt,omitempty"`
	WorkItem             *WorkItem                        `json:"work_item,omitempty" yaml:"work_item,omitempty"`
	LatestRun            *AgentRun                        `json:"latest_run,omitempty" yaml:"latest_run,omitempty"`
	LatestStep           *AgentStep                       `json:"latest_step,omitempty" yaml:"latest_step,omitempty"`
	LatestAction         *ActionAttempt                   `json:"latest_action,omitempty" yaml:"latest_action,omitempty"`
	LatestInterruption   *WorkInterruption                `json:"latest_interruption,omitempty" yaml:"latest_interruption,omitempty"`
	Investigation        *DelegatedInvestigation          `json:"investigation,omitempty" yaml:"investigation,omitempty"`
	InvestigationHistory []ArchivedDelegatedInvestigation `json:"investigation_history,omitempty" yaml:"investigation_history,omitempty"`
	ReplyCandidate       *WorkReplyCandidate              `json:"reply_candidate,omitempty" yaml:"reply_candidate,omitempty"`
	State                WorkInspectionState              `json:"state" yaml:"state"`
}

// OwnerControlName identifies a deterministic command in the owner-private
// control plane.
type OwnerControlName string

const (
	OwnerControlHelp            OwnerControlName = "help"
	OwnerControlStatus          OwnerControlName = "status"
	OwnerControlDoctor          OwnerControlName = "doctor"
	OwnerControlTasks           OwnerControlName = "tasks"
	OwnerControlTask            OwnerControlName = "task"
	OwnerControlTaskRetry       OwnerControlName = "task_retry"
	OwnerControlTaskResume      OwnerControlName = "task_resume"
	OwnerControlTaskCancel      OwnerControlName = "task_cancel"
	OwnerControlTaskAcknowledge OwnerControlName = "task_acknowledge"
	OwnerControlTaskReconcile   OwnerControlName = "task_reconcile"
	OwnerControlApprovals       OwnerControlName = "approvals"
	OwnerControlApproval        OwnerControlName = "approval"
	OwnerControlApprovalApprove OwnerControlName = "approval_approve"
	OwnerControlApprovalReject  OwnerControlName = "approval_reject"
	OwnerControlRecent          OwnerControlName = "recent"
	OwnerControlVersion         OwnerControlName = "version"
	OwnerControlPing            OwnerControlName = "ping"
	OwnerControlMemoryList      OwnerControlName = "memory_list"
	OwnerControlMemoryAdd       OwnerControlName = "memory_add"
	OwnerControlMemoryDelete    OwnerControlName = "memory_delete"
	OwnerControlMemoryFeedback  OwnerControlName = "memory_feedback"
)

type OwnerTaskView string

const (
	OwnerTaskViewAction  OwnerTaskView = "action"
	OwnerTaskViewRunning OwnerTaskView = "running"
	OwnerTaskViewRecent  OwnerTaskView = "recent"
	OwnerTaskViewAll     OwnerTaskView = "all"
)

type OwnerResolutionDisposition string

const (
	OwnerResolutionAcknowledged OwnerResolutionDisposition = "acknowledged"
	OwnerResolutionCompleted    OwnerResolutionDisposition = "completed"
	OwnerResolutionNotCompleted OwnerResolutionDisposition = "not_completed"
	OwnerResolutionUnknown      OwnerResolutionDisposition = "unknown"
)

type OwnerControlCommand struct {
	Name           OwnerControlName           `json:"name" yaml:"name"`
	Topic          string                     `json:"topic,omitempty" yaml:"topic,omitempty"`
	View           OwnerTaskView              `json:"view,omitempty" yaml:"view,omitempty"`
	Page           int                        `json:"page,omitempty" yaml:"page,omitempty"`
	Count          int                        `json:"count,omitempty" yaml:"count,omitempty"`
	WorkItemID     int64                      `json:"work_item_id,omitempty" yaml:"work_item_id,omitempty"`
	ActionID       int64                      `json:"action_id,omitempty" yaml:"action_id,omitempty"`
	Confirm        bool                       `json:"confirm,omitempty" yaml:"confirm,omitempty"`
	Disposition    OwnerResolutionDisposition `json:"disposition,omitempty" yaml:"disposition,omitempty"`
	Reason         string                     `json:"reason,omitempty" yaml:"reason,omitempty"`
	MemoryID       string                     `json:"memory_id,omitempty" yaml:"memory_id,omitempty"`
	MemoryKind     string                     `json:"memory_kind,omitempty" yaml:"memory_kind,omitempty"`
	MemoryScope    string                     `json:"memory_scope,omitempty" yaml:"memory_scope,omitempty"`
	MemoryContent  string                     `json:"memory_content,omitempty" yaml:"memory_content,omitempty"`
	MemoryVerdict  string                     `json:"memory_verdict,omitempty" yaml:"memory_verdict,omitempty"`
	MemoryFeedback string                     `json:"memory_feedback,omitempty" yaml:"memory_feedback,omitempty"`
}

type SemanticControlKind string

const (
	SemanticControlNotCommand SemanticControlKind = "not_command"
	SemanticControlCommand    SemanticControlKind = "command"
	SemanticControlAmbiguous  SemanticControlKind = "ambiguous"
)

type SemanticControlResolution struct {
	Kind          SemanticControlKind  `json:"kind" yaml:"kind"`
	Command       *OwnerControlCommand `json:"command,omitempty" yaml:"command,omitempty"`
	Clarification string               `json:"clarification,omitempty" yaml:"clarification,omitempty"`
}

type OwnerWorkResolution struct {
	ID               int64                      `json:"id" yaml:"id"`
	WorkItemID       int64                      `json:"work_item_id" yaml:"work_item_id"`
	ActionID         int64                      `json:"action_id,omitempty" yaml:"action_id,omitempty"`
	CommandMessageID string                     `json:"command_message_id" yaml:"command_message_id"`
	Disposition      OwnerResolutionDisposition `json:"disposition" yaml:"disposition"`
	Reason           string                     `json:"reason" yaml:"reason"`
	WorkUpdatedAt    time.Time                  `json:"work_updated_at,omitempty" yaml:"work_updated_at,omitempty"`
	ResolvedAt       time.Time                  `json:"resolved_at" yaml:"resolved_at"`
}

type OwnerTaskQuery struct {
	View     OwnerTaskView `json:"view" yaml:"view"`
	Page     int           `json:"page" yaml:"page"`
	PageSize int           `json:"page_size" yaml:"page_size"`
}

type OwnerTaskSummary struct {
	WorkItem           WorkItem                `json:"work_item" yaml:"work_item"`
	State              WorkInspectionState     `json:"state" yaml:"state"`
	LatestRun          *AgentRun               `json:"latest_run,omitempty" yaml:"latest_run,omitempty"`
	LatestStep         *AgentStep              `json:"latest_step,omitempty" yaml:"latest_step,omitempty"`
	LatestAction       *ActionAttempt          `json:"latest_action,omitempty" yaml:"latest_action,omitempty"`
	LatestInterruption *WorkInterruption       `json:"latest_interruption,omitempty" yaml:"latest_interruption,omitempty"`
	Investigation      *DelegatedInvestigation `json:"investigation,omitempty" yaml:"investigation,omitempty"`
	Resolution         *OwnerWorkResolution    `json:"resolution,omitempty" yaml:"resolution,omitempty"`
}

type OwnerTaskPage struct {
	Items    []OwnerTaskSummary `json:"items" yaml:"items"`
	Page     int                `json:"page" yaml:"page"`
	PageSize int                `json:"page_size" yaml:"page_size"`
	Total    int                `json:"total" yaml:"total"`
}

type OwnerApprovalPage struct {
	Items    []ActionAttempt `json:"items" yaml:"items"`
	Page     int             `json:"page" yaml:"page"`
	PageSize int             `json:"page_size" yaml:"page_size"`
	Total    int             `json:"total" yaml:"total"`
}

type OwnerMutationResult struct {
	CommandMessageID string                     `json:"command_message_id" yaml:"command_message_id"`
	Name             OwnerControlName           `json:"name" yaml:"name"`
	WorkItemID       int64                      `json:"work_item_id,omitempty" yaml:"work_item_id,omitempty"`
	ActionID         int64                      `json:"action_id,omitempty" yaml:"action_id,omitempty"`
	Changed          int                        `json:"changed" yaml:"changed"`
	Disposition      OwnerResolutionDisposition `json:"disposition,omitempty" yaml:"disposition,omitempty"`
	Reason           string                     `json:"reason,omitempty" yaml:"reason,omitempty"`
	MemoryID         string                     `json:"memory_id,omitempty" yaml:"memory_id,omitempty"`
	Replayed         bool                       `json:"replayed" yaml:"replayed"`
}

// QueueSummary is a compact operational view of the durable queue.
type QueueSummary struct {
	LaneCounts      map[string]int     `json:"lane_counts" yaml:"lane_counts"`
	StatusCounts    map[string]int     `json:"status_counts" yaml:"status_counts"`
	StaleProcessing int                `json:"stale_processing" yaml:"stale_processing"`
	FastPathHits    int                `json:"fast_path_hits" yaml:"fast_path_hits"`
	Recent          []RecentWorkMetric `json:"recent,omitempty" yaml:"recent,omitempty"`
}

// RecentWorkMetric exposes bounded per-message scheduler and model cost.
type RecentWorkMetric struct {
	WorkItemID int64          `json:"work_item_id" yaml:"work_item_id"`
	MessageID  string         `json:"message_id" yaml:"message_id"`
	Status     WorkItemStatus `json:"status" yaml:"status"`
	WorkKind   WorkKind       `json:"work_kind" yaml:"work_kind"`
	DurationMS int64          `json:"duration_ms" yaml:"duration_ms"`
	ModelTurns int            `json:"model_turns" yaml:"model_turns"`
	ToolCalls  int            `json:"tool_calls" yaml:"tool_calls"`
	FastPath   bool           `json:"fast_path" yaml:"fast_path"`
}
