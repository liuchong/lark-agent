// Package domain contains the stable business types shared by the Lark agent
// runtime, router, storage, and tools.
package domain

import (
	"fmt"
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
	Source           EventSource `json:"source" yaml:"source"`
	EventID          string      `json:"event_id,omitempty" yaml:"event_id,omitempty"`
	MessageID        string      `json:"message_id" yaml:"message_id"`
	ChatID           string      `json:"chat_id,omitempty" yaml:"chat_id,omitempty"`
	ChatName         string      `json:"chat_name,omitempty" yaml:"chat_name,omitempty"`
	ChatType         string      `json:"chat_type,omitempty" yaml:"chat_type,omitempty"`
	ChatPartnerID    string      `json:"chat_partner_id,omitempty" yaml:"chat_partner_id,omitempty"`
	RootMessageID    string      `json:"root_id,omitempty" yaml:"root_id,omitempty"`
	ReplyToMessageID string      `json:"reply_to,omitempty" yaml:"reply_to,omitempty"`
	ThreadID         string      `json:"thread_id,omitempty" yaml:"thread_id,omitempty"`
	SenderID         string      `json:"sender_id,omitempty" yaml:"sender_id,omitempty"`
	SenderType       string      `json:"sender_type,omitempty" yaml:"sender_type,omitempty"`
	Content          string      `json:"content,omitempty" yaml:"content,omitempty"`
	Mentions         []Mention   `json:"mentions,omitempty" yaml:"mentions,omitempty"`
	CreatedAt        time.Time   `json:"created_at,omitempty" yaml:"created_at,omitempty"`
	RawDigest        string      `json:"raw_digest,omitempty" yaml:"raw_digest,omitempty"`
	WorkspaceID      string      `json:"workspace_id,omitempty" yaml:"workspace_id,omitempty"`
	InTestScope      bool        `json:"in_test_scope,omitempty" yaml:"in_test_scope,omitempty"`
	InAssistantScope bool        `json:"in_assistant_scope,omitempty" yaml:"in_assistant_scope,omitempty"`
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
	WorkKindGeneric        WorkKind = "generic"
	WorkKindFastPath       WorkKind = "fast_path"
	WorkKindSimpleQuestion WorkKind = "simple_question"
	WorkKindDirectMention  WorkKind = "direct_mention"
	WorkKindCodingQuestion WorkKind = "coding_question"
	WorkKindCodingGoal     WorkKind = "coding_goal"
)

const (
	PriorityBackground     = 10
	PriorityCodingQuestion = 40
	PriorityDirectMention  = 60
	PrioritySimpleQuestion = 70
	PriorityFastPath       = 90
	PriorityPostReply      = 100
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
	ID            int64           `json:"id" yaml:"id"`
	DedupKey      string          `json:"dedup_key" yaml:"dedup_key"`
	Status        WorkItemStatus  `json:"status" yaml:"status"`
	WorkKind      WorkKind        `json:"work_kind,omitempty" yaml:"work_kind,omitempty"`
	Priority      int             `json:"priority,omitempty" yaml:"priority,omitempty"`
	DuplicateOf   int64           `json:"duplicate_of,omitempty" yaml:"duplicate_of,omitempty"`
	SessionID     string          `json:"session_id,omitempty" yaml:"session_id,omitempty"`
	Event         NormalizedEvent `json:"event" yaml:"event"`
	LeaseBy       string          `json:"lease_by,omitempty" yaml:"lease_by,omitempty"`
	LeaseTime     time.Time       `json:"lease_time,omitempty" yaml:"lease_time,omitempty"`
	RetryCount    int             `json:"retry_count,omitempty" yaml:"retry_count,omitempty"`
	NextAttemptAt time.Time       `json:"next_attempt_at,omitempty" yaml:"next_attempt_at,omitempty"`
	CreatedAt     time.Time       `json:"created_at,omitempty" yaml:"created_at,omitempty"`
	UpdatedAt     time.Time       `json:"updated_at,omitempty" yaml:"updated_at,omitempty"`
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

// Decision is an auditable routing or agent decision.
type Decision struct {
	Kind           DecisionKind   `json:"kind" yaml:"kind"`
	Mode           Mode           `json:"mode" yaml:"mode"`
	Relevance      Relevance      `json:"relevance" yaml:"relevance"`
	WorkKind       WorkKind       `json:"work_kind,omitempty" yaml:"work_kind,omitempty"`
	Priority       int            `json:"priority,omitempty" yaml:"priority,omitempty"`
	Confidence     float64        `json:"confidence" yaml:"confidence"`
	Risk           Risk           `json:"risk" yaml:"risk"`
	EvidenceStatus EvidenceStatus `json:"evidence_status,omitempty" yaml:"evidence_status,omitempty"`
	Reason         string         `json:"reason" yaml:"reason"`
	ReplyText      string         `json:"reply_text,omitempty" yaml:"reply_text,omitempty"`
	OwnerAction    string         `json:"owner_action,omitempty" yaml:"owner_action,omitempty"`
	Sources        []SourceRef    `json:"sources,omitempty" yaml:"sources,omitempty"`
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
	ToolCallID       string    `json:"tool_call_id,omitempty" yaml:"tool_call_id,omitempty"`
	ToolName         string    `json:"tool_name,omitempty" yaml:"tool_name,omitempty"`
	InputJSON        string    `json:"input_json,omitempty" yaml:"input_json,omitempty"`
	OutputJSON       string    `json:"output_json,omitempty" yaml:"output_json,omitempty"`
	RequestID        string    `json:"request_id,omitempty" yaml:"request_id,omitempty"`
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
	Receipt            *IntakeReceipt      `json:"receipt,omitempty" yaml:"receipt,omitempty"`
	WorkItem           *WorkItem           `json:"work_item,omitempty" yaml:"work_item,omitempty"`
	LatestRun          *AgentRun           `json:"latest_run,omitempty" yaml:"latest_run,omitempty"`
	LatestStep         *AgentStep          `json:"latest_step,omitempty" yaml:"latest_step,omitempty"`
	LatestAction       *ActionAttempt      `json:"latest_action,omitempty" yaml:"latest_action,omitempty"`
	LatestInterruption *WorkInterruption   `json:"latest_interruption,omitempty" yaml:"latest_interruption,omitempty"`
	State              WorkInspectionState `json:"state" yaml:"state"`
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
