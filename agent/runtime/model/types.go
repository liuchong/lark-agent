package model

import (
	"encoding/json"
	"time"
)

type Provider string

const (
	ProviderKimi      Provider = "kimi"
	ProviderOpenAI    Provider = "openai"
	ProviderAnthropic Provider = "anthropic"
)

type Protocol string

const (
	ProtocolOpenAIChat        Protocol = "openai_chat"
	ProtocolOpenAIResponses   Protocol = "openai_responses"
	ProtocolAnthropicMessages Protocol = "anthropic_messages"
)

type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"

	RoleAgent     Role = "agent"
	RoleSemantic  Role = "semantic"
	RoleFinalizer Role = "finalizer"
	RoleCompactor Role = "compactor"
	RoleVision    Role = "vision"
)

type ReasoningMode string

const (
	ReasoningProviderDefault ReasoningMode = "provider_default"
	ReasoningEnabled         ReasoningMode = "enabled"
	ReasoningDisabled        ReasoningMode = "disabled"
)

type StreamMode string

const (
	StreamAuto     StreamMode = "auto"
	StreamDisabled StreamMode = "disabled"
	StreamRequired StreamMode = "required"
)

type ToolChoiceIntent string

const (
	ToolChoiceAuto     ToolChoiceIntent = "auto"
	ToolChoiceNone     ToolChoiceIntent = "none"
	ToolChoiceRequired ToolChoiceIntent = "required"
)

type BlockType string

const (
	BlockText       BlockType = "text"
	BlockImage      BlockType = "image"
	BlockToolCall   BlockType = "tool_call"
	BlockToolResult BlockType = "tool_result"
	BlockThinking   BlockType = "thinking"
)

type FinishReason string

const (
	FinishCompleted FinishReason = "completed"
	FinishToolCalls FinishReason = "tool_calls"
	FinishTruncated FinishReason = "truncated"
	FinishFiltered  FinishReason = "filtered"
	FinishPaused    FinishReason = "paused"
	FinishOther     FinishReason = "other"
)

type RecoveryAction string

const (
	RecoveryNone              RecoveryAction = "none"
	RecoveryRetryStep         RecoveryAction = "retry_step"
	RecoveryRetryWork         RecoveryAction = "retry_work"
	RecoveryChangeInput       RecoveryAction = "change_input"
	RecoveryStopDeterministic RecoveryAction = "stop_deterministic"
)

type Profile struct {
	Name          string
	Provider      Provider
	Protocol      Protocol
	BaseURL       string
	Model         string
	CredentialRef string
	Timeout       time.Duration
	Stream        StreamMode
	Reasoning     ReasoningConfig
	Capabilities  Capabilities
}

type ReasoningConfig struct {
	Mode   ReasoningMode
	Effort string
}

type Capabilities struct {
	ToolUse          bool
	Thinking         bool
	ParallelToolCall bool
	ImageInput       bool
	MaxContextTokens int
	MaxOutputTokens  int
}

type RoleBindings struct {
	Agent     string
	Semantic  string
	Finalizer string
	Compactor string
	Vision    string
}

type Request struct {
	Profile          Profile
	Role             Role
	Messages         []Message
	Tools            []Tool
	ToolChoice       ToolChoiceIntent
	StructuredOutput json.RawMessage
	CacheKey         string
	RunState         RunState
	Budgets          Budgets
}

type Message struct {
	Role       Role
	Blocks     []Block
	Name       string
	ToolCallID string
}

type Block struct {
	Type       BlockType
	Text       string
	ImageURL   string
	ToolCall   *ToolCall
	ToolResult *ToolResult
	Thinking   *ThinkingBlock
}

type Tool struct {
	Name        string
	Description string
	Schema      json.RawMessage
}

type ToolCall struct {
	ID        string
	Name      string
	Arguments json.RawMessage
}

type ToolResult struct {
	ID      string
	Name    string
	Content string
}

type ThinkingBlock struct {
	ID        string
	Type      string
	Content   string
	Signature string
}

type RunState struct {
	OriginalGoal    string
	Phase           string
	CompletedChecks []string
	VerifiedSources []string
	Unknowns        []string
	LastFailedGate  string
	LegalNextSteps  []string
	ModelTurn       int
	MaxModelTurns   int
	StepAttempt     int
}

type Budgets struct {
	MaxAttemptsPerStep int
	FirstByteTimeout   time.Duration
	StreamIdleTimeout  time.Duration
	MaxContextTokens   int
	MaxOutputTokens    int
	MaxContextBytes    int
	OutputReserveBytes int
}

type Turn struct {
	Text           string
	ToolCalls      []ToolCall
	FinishReason   FinishReason
	RequestID      string
	Usage          Usage
	Cache          CacheMetrics
	ThinkingBlocks []ThinkingBlock
	ProviderStatus map[string]string
}

type Usage struct {
	PromptTokens     int
	CompletionTokens int
	TotalTokens      int
	ThinkingTokens   int
	CacheReadTokens  int
	CacheWriteTokens int
}

type CacheMetrics struct {
	Hit         bool
	ReadTokens  int
	WriteTokens int
}

type HTTPRequest struct {
	Method  string
	Path    string
	Headers map[string]string
	Body    map[string]any
}

type Codec interface {
	Encode(Request) (HTTPRequest, error)
	Decode([]byte) (Turn, error)
}
