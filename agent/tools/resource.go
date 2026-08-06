package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/cloudwego/eino/schema"

	"github.com/liuchong/lark-agent/agent/domain"
	errs "github.com/liuchong/lark-agent/internal/apperr"
)

type ResourceEvidenceStore interface {
	GetResourceEvidenceForWork(context.Context, string) (domain.ResourceEvidence, error)
	FindResourceEvidence(context.Context, domain.ResourceEvidenceQuery) ([]domain.ResourceEvidence, error)
}

type ResourceEvidenceResolver interface {
	ResolveResourceEvidence(context.Context, string, string) (domain.ResourceEvidence, error)
}

type ResourceActionStore interface {
	RequestResourceAction(context.Context, string, string, string) (int64, error)
	ConsumeResourceAction(context.Context, string, string, string) (int64, bool, error)
	BeginResourceAction(context.Context, string, string, string) (int64, string, bool, error)
	CompleteResourceAction(context.Context, int64, string, string) error
}

type ResourceMutationClient interface {
	ListBaseFields(context.Context, string, string) ([]ResourceField, error)
	CompareAndUpdateBaseField(context.Context, ResourceFieldUpdate) (any, error)
	ReplyToComment(context.Context, string, string, string, string) (any, error)
}

type ResourceField struct {
	Name    string
	Type    int
	Options []string
}

type ResourceFieldUpdate struct {
	AppToken  string
	TableID   string
	RecordID  string
	FieldName string
	Before    any
	After     any
}

type ResourceToolOptions struct {
	Mode     domain.Mode
	Evidence ResourceEvidenceStore
	Resolver ResourceEvidenceResolver
	Actions  ResourceActionStore
	Client   ResourceMutationClient
}

type baseStatusUpdateArgs struct {
	AppToken      string `json:"app_token"`
	TableID       string `json:"table_id"`
	RecordID      string `json:"record_id"`
	FieldName     string `json:"field_name"`
	ExpectedValue string `json:"expected_value"`
	DesiredValue  string `json:"desired_value"`
}

type resourceCommentReplyArgs struct {
	FileToken string `json:"file_token"`
	FileType  string `json:"file_type"`
	CommentID string `json:"comment_id"`
	Text      string `json:"text"`
}

func ResourceDefinitions(options ResourceToolOptions) []Definition {
	return []Definition{
		resourceEvidenceDefinition(options),
		inspectBaseSchemaDefinition(options.Client),
		updateBaseStatusDefinition(options),
		replyResourceCommentDefinition(options),
	}
}

func inspectBaseSchemaDefinition(client ResourceMutationClient) Definition {
	return Definition{
		ResourceHandoffOnly: true,
		NonOwnerReadOnly:    true,
		Info: toolInfo(
			"inspect_base_schema",
			"Read current Base field names, types, and allowed status options before proposing a status transition.",
			map[string]*schema.ParameterInfo{
				"app_token": {Type: schema.String, Required: true},
				"table_id":  {Type: schema.String, Required: true},
			},
		),
		Execute: func(ctx context.Context, raw json.RawMessage) (Execution, error) {
			if client == nil {
				return Execution{}, errs.NewConfigError(errs.SubtypeNotConfigured, "resource client is not configured")
			}
			var args struct {
				AppToken string `json:"app_token"`
				TableID  string `json:"table_id"`
			}
			if err := decodeArgs(raw, &args); err != nil {
				return Execution{}, err
			}
			fields, err := client.ListBaseFields(ctx, args.AppToken, args.TableID)
			return jsonExecution(fields, nil, err)
		},
	}
}

func resourceEvidenceDefinition(options ResourceToolOptions) Definition {
	return Definition{
		ResourceHandoffOnly: true,
		NonOwnerReadOnly:    true,
		Info: toolInfo(
			"get_resource_evidence",
			"Read trusted evidence linked to this handoff. For a conversational record share with no linked evidence, pass the exact resource_url from the bounded conversation to resolve, persist, and link the current record through the typed Lark API.",
			map[string]*schema.ParameterInfo{
				"resource_url": {Type: schema.String},
				"terms":        {Type: schema.Array, ElemInfo: &schema.ParameterInfo{Type: schema.String}},
				"limit":        {Type: schema.Integer},
			},
		),
		Execute: func(ctx context.Context, raw json.RawMessage) (Execution, error) {
			store := options.Evidence
			if store == nil {
				return Execution{}, errs.NewConfigError(errs.SubtypeNotConfigured, "resource evidence store is not configured")
			}
			var args struct {
				ResourceURL string   `json:"resource_url"`
				Terms       []string `json:"terms"`
				Limit       int      `json:"limit"`
			}
			if err := decodeArgs(raw, &args); err != nil {
				return Execution{}, err
			}
			dedupKey := workItemDedup(ctx)
			if dedupKey == "" {
				return Execution{}, errs.NewInternalError(errs.SubtypeFailedPrecondition, "resource work identity is missing")
			}
			current, err := store.GetResourceEvidenceForWork(ctx, dedupKey)
			hasCurrent := err == nil
			resourceURL := strings.TrimSpace(args.ResourceURL)
			if resourceURL != "" {
				scope, ok := invocationScope(ctx)
				if !ok || !containsExactResourceURL(scope.ResourceURLs, resourceURL) {
					return Execution{}, errs.NewPermissionError(
						errs.SubtypeMissingScope,
						"resource_url is not present in the bounded conversation",
					)
				}
				if hasCurrent {
					if strings.TrimSpace(current.OriginalURL) != resourceURL {
						return Execution{}, errs.NewValidationError(
							errs.SubtypeFailedPrecondition,
							"work item already has different authoritative resource evidence",
						)
					}
				} else {
					if options.Resolver == nil {
						return Execution{}, errs.NewConfigError(
							errs.SubtypeNotConfigured,
							"conversational resource resolver is not configured",
						)
					}
					current, err = options.Resolver.ResolveResourceEvidence(
						ctx,
						dedupKey,
						resourceURL,
					)
					if err != nil {
						return Execution{}, err
					}
					hasCurrent = true
				}
			}
			if !hasCurrent && len(args.Terms) == 0 {
				return Execution{}, errs.NewValidationError(
					errs.SubtypeFailedPrecondition,
					"this conversational resource handoff has no directly linked evidence; pass an exact resource_url from the bounded conversation or provide exact issue title or key terms",
				)
			}
			var related []domain.ResourceEvidence
			if len(args.Terms) > 0 {
				related, err = store.FindResourceEvidence(ctx, domain.ResourceEvidenceQuery{
					Terms: args.Terms, Limit: args.Limit,
				})
				if err != nil {
					return Execution{}, err
				}
			}
			report := map[string]any{"related": related}
			all := append([]domain.ResourceEvidence(nil), related...)
			if hasCurrent {
				report["current"] = current
				all = append([]domain.ResourceEvidence{current}, all...)
			}
			sources := resourceEvidenceSources(all)
			return jsonExecution(report, sources, nil)
		},
	}
}

func containsExactResourceURL(allowed []string, target string) bool {
	for _, candidate := range allowed {
		if strings.TrimSpace(candidate) == target {
			return true
		}
	}
	return false
}

func updateBaseStatusDefinition(options ResourceToolOptions) Definition {
	return Definition{
		ResourceHandoffOnly: true,
		NonOwnerReadOnly:    true,
		SideEffect:          true,
		Permission:          ToolPermissionAsk,
		Risk:                ToolRiskMedium,
		Info: toolInfo(
			"update_base_status",
			"Compare and update exactly one Base status field. Use only after reading linked resource evidence, project AGENTS.md rules, and authoritative repair evidence. Approval mode creates an exact pending action instead of writing.",
			map[string]*schema.ParameterInfo{
				"app_token":      {Type: schema.String, Required: true},
				"table_id":       {Type: schema.String, Required: true},
				"record_id":      {Type: schema.String, Required: true},
				"field_name":     {Type: schema.String, Required: true},
				"expected_value": {Type: schema.String, Required: true},
				"desired_value":  {Type: schema.String, Required: true},
			},
		),
		Execute: func(ctx context.Context, raw json.RawMessage) (Execution, error) {
			var args baseStatusUpdateArgs
			if err := decodeArgs(raw, &args); err != nil {
				return Execution{}, err
			}
			if options.Client == nil || options.Actions == nil {
				return Execution{}, errs.NewConfigError(errs.SubtypeNotConfigured, "resource mutation runtime is not configured")
			}
			if err := authorizeBaseStatusUpdate(ctx, options.Evidence, args); err != nil {
				return Execution{}, err
			}
			if err := validateStatusOption(ctx, options.Client, args); err != nil {
				return Execution{}, err
			}
			return executeResourceMutation(ctx, options, "base_status_update", args, func() (any, error) {
				return options.Client.CompareAndUpdateBaseField(ctx, ResourceFieldUpdate{
					AppToken: args.AppToken, TableID: args.TableID, RecordID: args.RecordID,
					FieldName: args.FieldName, Before: args.ExpectedValue, After: args.DesiredValue,
				})
			})
		},
	}
}

func authorizeBaseStatusUpdate(
	ctx context.Context,
	store ResourceEvidenceStore,
	update baseStatusUpdateArgs,
) error {
	if store == nil {
		return errs.NewConfigError(errs.SubtypeNotConfigured, "resource evidence store is not configured")
	}
	evidence, err := store.FindResourceEvidence(ctx, domain.ResourceEvidenceQuery{
		AppToken: update.AppToken, TableID: update.TableID, RecordID: update.RecordID, Limit: 10,
	})
	if err != nil {
		return err
	}
	if len(evidence) == 0 {
		return errs.NewValidationError(
			errs.SubtypeFailedPrecondition,
			"no trusted resource evidence uniquely identifies the requested Base record",
		)
	}
	current := evidence[0]
	if !current.OwnerMentioned {
		return errs.NewPermissionError(
			errs.SubtypeFailedPrecondition,
			"the configured owner is not an assignee or explicit owner mention on the trusted Base record evidence",
		)
	}
	if current.StatusFieldName != update.FieldName ||
		current.StatusValue != update.ExpectedValue {
		return errs.NewValidationError(
			errs.SubtypeFailedPrecondition,
			"trusted Base evidence does not match the proposed status field and expected value",
		)
	}
	return nil
}

func replyResourceCommentDefinition(options ResourceToolOptions) Definition {
	return Definition{
		ResourceHandoffOnly: true,
		NonOwnerReadOnly:    true,
		SideEffect:          true,
		Permission:          ToolPermissionAsk,
		Risk:                ToolRiskMedium,
		Info: toolInfo(
			"reply_resource_comment",
			"Reply once to the exact subscribed document/Base comment after the requested work has a verified result. Approval mode creates an exact pending action.",
			map[string]*schema.ParameterInfo{
				"file_token": {Type: schema.String, Required: true},
				"file_type":  {Type: schema.String, Required: true},
				"comment_id": {Type: schema.String, Required: true},
				"text":       {Type: schema.String, Required: true},
			},
		),
		Execute: func(ctx context.Context, raw json.RawMessage) (Execution, error) {
			var args resourceCommentReplyArgs
			if err := decodeArgs(raw, &args); err != nil {
				return Execution{}, err
			}
			if options.Client == nil || options.Actions == nil {
				return Execution{}, errs.NewConfigError(errs.SubtypeNotConfigured, "resource mutation runtime is not configured")
			}
			if err := authorizeResourceCommentReply(ctx, options.Evidence, args); err != nil {
				return Execution{}, err
			}
			return executeResourceMutation(ctx, options, "resource_comment_reply", args, func() (any, error) {
				return options.Client.ReplyToComment(
					ctx, args.FileToken, args.FileType, args.CommentID, args.Text,
				)
			})
		},
	}
}

func authorizeResourceCommentReply(
	ctx context.Context,
	store ResourceEvidenceStore,
	reply resourceCommentReplyArgs,
) error {
	if store == nil {
		return errs.NewConfigError(errs.SubtypeNotConfigured, "resource evidence store is not configured")
	}
	if strings.TrimSpace(reply.FileToken) == "" ||
		strings.TrimSpace(reply.FileType) == "" ||
		strings.TrimSpace(reply.CommentID) == "" ||
		strings.TrimSpace(reply.Text) == "" {
		return errs.NewValidationError(
			errs.SubtypeInvalidArgument,
			"complete resource comment reply coordinates and text are required",
		)
	}
	dedupKey := workItemDedup(ctx)
	if dedupKey == "" {
		return errs.NewInternalError(
			errs.SubtypeFailedPrecondition,
			"resource comment reply requires a durable work item",
		)
	}
	evidence, err := store.GetResourceEvidenceForWork(ctx, dedupKey)
	if err != nil {
		return err
	}
	if evidence.ID == 0 ||
		!evidence.OwnerMentioned ||
		evidence.FileToken != reply.FileToken ||
		evidence.CommentID != reply.CommentID {
		return errs.NewPermissionError(
			errs.SubtypeFailedPrecondition,
			"resource comment reply is restricted to the exact linked comment that mentioned the configured owner",
		)
	}
	return nil
}

func validateStatusOption(
	ctx context.Context,
	client ResourceMutationClient,
	update baseStatusUpdateArgs,
) error {
	if update.AppToken == "" || update.TableID == "" || update.RecordID == "" ||
		strings.TrimSpace(update.FieldName) == "" ||
		strings.TrimSpace(update.ExpectedValue) == "" ||
		strings.TrimSpace(update.DesiredValue) == "" {
		return errs.NewValidationError(errs.SubtypeInvalidArgument, "complete Base status update coordinates are required")
	}
	fields, err := client.ListBaseFields(ctx, update.AppToken, update.TableID)
	if err != nil {
		return err
	}
	for _, field := range fields {
		if field.Name != update.FieldName || field.Type != 3 {
			continue
		}
		for _, option := range field.Options {
			if option == update.DesiredValue {
				return nil
			}
		}
		return errs.NewValidationError(
			errs.SubtypeFailedPrecondition,
			"desired Base status %q is not an allowed option for %q",
			update.DesiredValue,
			update.FieldName,
		)
	}
	return errs.NewValidationError(
		errs.SubtypeFailedPrecondition,
		"Base status field %q was not found",
		update.FieldName,
	)
}

func executeResourceMutation(
	ctx context.Context,
	options ResourceToolOptions,
	kind string,
	request any,
	execute func() (any, error),
) (Execution, error) {
	dedupKey := workItemDedup(ctx)
	if dedupKey == "" {
		return Execution{}, errs.NewInternalError(errs.SubtypeFailedPrecondition, "resource action requires a durable work item")
	}
	requestData, err := json.Marshal(request)
	if err != nil {
		return Execution{}, err
	}
	requestJSON := string(requestData)
	var actionID int64
	if options.Mode == domain.ModePaused {
		return Execution{}, errs.NewValidationError(
			errs.SubtypeFailedPrecondition,
			"resource mutations are disabled while the agent is paused",
		)
	}
	if options.Mode == domain.ModeApproval {
		var approved bool
		actionID, approved, err = options.Actions.ConsumeResourceAction(ctx, dedupKey, kind, requestJSON)
		if err != nil {
			return Execution{}, err
		}
		if !approved {
			actionID, err = options.Actions.RequestResourceAction(ctx, dedupKey, kind, requestJSON)
			if err != nil {
				return Execution{}, err
			}
			return jsonExecution(map[string]any{
				"status": "approval_required", "action_id": actionID,
				"kind": kind, "request": request,
			}, nil, nil)
		}
	} else {
		var cached string
		var blocked bool
		actionID, cached, blocked, err = options.Actions.BeginResourceAction(ctx, dedupKey, kind, requestJSON)
		if err != nil {
			return Execution{}, err
		}
		if cached != "" {
			return Execution{Content: cached}, nil
		}
		if blocked {
			return Execution{}, errs.NewValidationError(
				errs.SubtypeFailedPrecondition,
				"resource action %d has an uncertain or blocked prior execution; reconcile it before retrying",
				actionID,
			)
		}
	}
	result, runErr := execute()
	responseData, marshalErr := json.Marshal(result)
	if marshalErr != nil {
		return Execution{}, marshalErr
	}
	errorText := ""
	if runErr != nil {
		errorText = runErr.Error()
	}
	if completeErr := options.Actions.CompleteResourceAction(
		ctx, actionID, string(responseData), errorText,
	); completeErr != nil {
		return Execution{}, completeErr
	}
	if runErr != nil {
		return Execution{}, runErr
	}
	return jsonExecution(map[string]any{
		"status": "completed", "action_id": actionID, "result": result,
	}, nil, nil)
}

func resourceEvidenceSources(evidence []domain.ResourceEvidence) []domain.SourceRef {
	seen := map[int64]bool{}
	var sources []domain.SourceRef
	for _, item := range evidence {
		if item.ID == 0 || seen[item.ID] {
			continue
		}
		seen[item.ID] = true
		sources = append(sources, domain.SourceRef{
			RelativePath: fmt.Sprintf("resource_evidence/%d", item.ID),
			Digest:       item.ContentDigest,
			Kind:         "resource_evidence",
		})
	}
	return sources
}
