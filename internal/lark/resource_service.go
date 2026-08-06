package lark

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"reflect"
	"strings"

	errs "github.com/liuchong/lark-agent/internal/apperr"
)

// ResourceService is the typed Wiki, Drive, and Base boundary used by the
// resource monitor. Every production request still goes through Client and the
// official public Go SDK.
type ResourceService struct {
	caller Caller
}

type RemoteSubscription struct {
	ID       string `json:"id"`
	Active   bool   `json:"active"`
	FileType string `json:"file_type"`
}

type BaseOption struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type BaseField struct {
	ID      string       `json:"id"`
	Name    string       `json:"name"`
	Type    int          `json:"type"`
	Primary bool         `json:"primary"`
	Options []BaseOption `json:"options,omitempty"`
}

type BaseRecord struct {
	ID     string         `json:"id"`
	Fields map[string]any `json:"fields"`
}

type BaseFieldUpdate struct {
	AppToken  string `json:"app_token"`
	TableID   string `json:"table_id"`
	RecordID  string `json:"record_id"`
	FieldName string `json:"field_name"`
	Before    any    `json:"before"`
	After     any    `json:"after"`
}

type BaseFieldUpdateResult struct {
	Before   any  `json:"before"`
	After    any  `json:"after"`
	Verified bool `json:"verified"`
}

type CommentReplyResult struct {
	ReplyID string `json:"reply_id"`
}

type ResourceComment struct {
	ID       string `json:"id"`
	UserID   string `json:"user_id,omitempty"`
	Quote    string `json:"quote,omitempty"`
	Text     string `json:"text,omitempty"`
	IsSolved bool   `json:"is_solved,omitempty"`
}

func NewResourceService(caller Caller) *ResourceService {
	return &ResourceService{caller: caller}
}

func (s *ResourceService) ResolveResource(ctx context.Context, ref ResourceRef) (ResourceRef, error) {
	if s == nil || s.caller == nil {
		return ResourceRef{}, errs.NewConfigError(errs.SubtypeNotConfigured, "lark resource service is not configured")
	}
	switch ref.ResourceType {
	case ResourceTypeWiki:
		if strings.TrimSpace(ref.WikiNodeToken) == "" {
			return ResourceRef{}, errs.NewValidationError(errs.SubtypeInvalidArgument, "wiki node token is required")
		}
		var response struct {
			Data struct {
				Node struct {
					ObjType  string `json:"obj_type"`
					ObjToken string `json:"obj_token"`
					Title    string `json:"title"`
				} `json:"node"`
			} `json:"data"`
		}
		if err := s.callTyped(ctx, APIRequest{
			Method: http.MethodGet,
			Path:   "/open-apis/wiki/v2/spaces/get_node",
			Params: map[string]any{"token": ref.WikiNodeToken},
			As:     IdentityUser,
		}, &response); err != nil {
			return ResourceRef{}, err
		}
		if response.Data.Node.ObjToken == "" || response.Data.Node.ObjType == "" {
			return ResourceRef{}, errs.NewInternalError(errs.SubtypeInvalidResponse, "wiki node response is missing object coordinates")
		}
		ref.FileToken = response.Data.Node.ObjToken
		ref.FileType = response.Data.Node.ObjType
		if response.Data.Node.ObjType == "bitable" {
			ref.ResourceType = ResourceTypeBase
			ref.AppToken = response.Data.Node.ObjToken
		} else {
			ref.ResourceType = ResourceTypeDocument
		}
		return ref, nil
	case ResourceTypeBaseRecord:
		if strings.TrimSpace(ref.RecordShareToken) == "" {
			return ResourceRef{}, errs.NewValidationError(errs.SubtypeInvalidArgument, "record share token is required")
		}
		var response struct {
			Data struct {
				BaseToken string `json:"base_token"`
				TableID   string `json:"table_id"`
				RecordID  string `json:"record_id"`
			} `json:"data"`
		}
		if err := s.callTyped(ctx, APIRequest{
			Method: http.MethodGet,
			Path:   "/open-apis/base/v3/record_share/" + url.PathEscape(ref.RecordShareToken) + "/meta",
			As:     IdentityUser,
		}, &response); err != nil {
			return ResourceRef{}, err
		}
		if response.Data.BaseToken == "" || response.Data.TableID == "" || response.Data.RecordID == "" {
			return ResourceRef{}, errs.NewInternalError(errs.SubtypeInvalidResponse, "record share response is missing Base coordinates")
		}
		ref.ResourceType = ResourceTypeBase
		ref.AppToken = response.Data.BaseToken
		ref.FileToken = response.Data.BaseToken
		ref.FileType = "bitable"
		ref.TableID = response.Data.TableID
		ref.RecordID = response.Data.RecordID
		return ref, nil
	case ResourceTypeBase:
		if ref.AppToken == "" {
			return ResourceRef{}, errs.NewValidationError(errs.SubtypeInvalidArgument, "Base app token is required")
		}
		ref.FileToken = ref.AppToken
		ref.FileType = "bitable"
		return ref, nil
	case ResourceTypeDocument:
		if ref.FileToken == "" {
			return ResourceRef{}, errs.NewValidationError(errs.SubtypeInvalidArgument, "document file token is required")
		}
		if ref.FileType == "" {
			ref.FileType = "docx"
		}
		return ref, nil
	default:
		return ResourceRef{}, errs.NewValidationError(errs.SubtypeInvalidArgument, "unsupported resource type %q", ref.ResourceType)
	}
}

func (s *ResourceService) EnsureCommentSubscription(
	ctx context.Context,
	ref ResourceRef,
) (RemoteSubscription, error) {
	return s.SetCommentSubscription(ctx, ref, true)
}

func (s *ResourceService) SetCommentSubscription(
	ctx context.Context,
	ref ResourceRef,
	enabled bool,
) (RemoteSubscription, error) {
	resolved, err := s.ResolveResource(ctx, ref)
	if err != nil {
		return RemoteSubscription{}, err
	}
	if resolved.FileToken == "" || resolved.FileType == "" {
		return RemoteSubscription{}, errs.NewValidationError(errs.SubtypeInvalidArgument, "resource file token and type are required")
	}
	var response struct {
		Data struct {
			SubscriptionID string `json:"subscription_id"`
			IsSubscribe    bool   `json:"is_subcribe"`
			FileType       string `json:"file_type"`
		} `json:"data"`
	}
	if err := s.callTyped(ctx, APIRequest{
		Method: http.MethodPost,
		Path:   "/open-apis/drive/v1/files/" + url.PathEscape(resolved.FileToken) + "/subscriptions",
		Data: map[string]any{
			"subscription_type": "comment_update",
			"is_subcribe":       enabled,
			"file_type":         resolved.FileType,
		},
		As: IdentityUser,
	}, &response); err != nil {
		return RemoteSubscription{}, err
	}
	if enabled && response.Data.SubscriptionID == "" {
		return RemoteSubscription{}, errs.NewInternalError(errs.SubtypeInvalidResponse, "file subscription response is missing subscription_id")
	}
	return RemoteSubscription{
		ID: response.Data.SubscriptionID, Active: response.Data.IsSubscribe, FileType: response.Data.FileType,
	}, nil
}

func (s *ResourceService) ListBaseFields(
	ctx context.Context,
	appToken string,
	tableID string,
) ([]BaseField, error) {
	if appToken == "" || tableID == "" {
		return nil, errs.NewValidationError(errs.SubtypeInvalidArgument, "Base app_token and table_id are required")
	}
	var fields []BaseField
	pageToken := ""
	for {
		var response struct {
			Data struct {
				Items []struct {
					ID       string `json:"field_id"`
					Name     string `json:"field_name"`
					Type     int    `json:"type"`
					Primary  bool   `json:"is_primary"`
					Property struct {
						Options []BaseOption `json:"options"`
					} `json:"property"`
				} `json:"items"`
				HasMore   bool   `json:"has_more"`
				PageToken string `json:"page_token"`
			} `json:"data"`
		}
		params := map[string]any{"page_size": 100}
		if pageToken != "" {
			params["page_token"] = pageToken
		}
		if err := s.callTyped(ctx, APIRequest{
			Method: http.MethodGet,
			Path: "/open-apis/bitable/v1/apps/" + url.PathEscape(appToken) +
				"/tables/" + url.PathEscape(tableID) + "/fields",
			Params: params,
			As:     IdentityUser,
		}, &response); err != nil {
			return nil, err
		}
		for _, item := range response.Data.Items {
			if item.ID == "" || item.Name == "" {
				return nil, errs.NewInternalError(errs.SubtypeInvalidResponse, "Base field response is missing field identity")
			}
			fields = append(fields, BaseField{
				ID: item.ID, Name: item.Name, Type: item.Type, Primary: item.Primary,
				Options: append([]BaseOption(nil), item.Property.Options...),
			})
		}
		if !response.Data.HasMore {
			break
		}
		if response.Data.PageToken == "" || response.Data.PageToken == pageToken {
			return nil, errs.NewInternalError(errs.SubtypeInvalidResponse, "Base field pagination is missing progress")
		}
		pageToken = response.Data.PageToken
	}
	return fields, nil
}

func (s *ResourceService) GetBaseRecord(
	ctx context.Context,
	appToken string,
	tableID string,
	recordID string,
) (BaseRecord, error) {
	if appToken == "" || tableID == "" || recordID == "" {
		return BaseRecord{}, errs.NewValidationError(errs.SubtypeInvalidArgument, "Base record coordinates are required")
	}
	var response struct {
		Data struct {
			Record struct {
				ID     string         `json:"record_id"`
				Fields map[string]any `json:"fields"`
			} `json:"record"`
		} `json:"data"`
	}
	if err := s.callTyped(ctx, APIRequest{
		Method: http.MethodGet,
		Path: "/open-apis/bitable/v1/apps/" + url.PathEscape(appToken) +
			"/tables/" + url.PathEscape(tableID) + "/records/" + url.PathEscape(recordID),
		Params: map[string]any{"user_id_type": "open_id"},
		As:     IdentityUser,
	}, &response); err != nil {
		return BaseRecord{}, err
	}
	if response.Data.Record.ID == "" || response.Data.Record.Fields == nil {
		return BaseRecord{}, errs.NewInternalError(errs.SubtypeInvalidResponse, "Base record response is missing record data")
	}
	return BaseRecord{ID: response.Data.Record.ID, Fields: response.Data.Record.Fields}, nil
}

func (s *ResourceService) ListBaseRecords(
	ctx context.Context,
	appToken string,
	tableID string,
	viewID string,
) ([]BaseRecord, error) {
	if appToken == "" || tableID == "" {
		return nil, errs.NewValidationError(errs.SubtypeInvalidArgument, "Base app_token and table_id are required")
	}
	const maxRecords = 1000
	var records []BaseRecord
	pageToken := ""
	for {
		var response struct {
			Data struct {
				Items []struct {
					ID     string         `json:"record_id"`
					Fields map[string]any `json:"fields"`
				} `json:"items"`
				HasMore   bool   `json:"has_more"`
				PageToken string `json:"page_token"`
			} `json:"data"`
		}
		params := map[string]any{"page_size": 100, "user_id_type": "open_id"}
		if viewID != "" {
			params["view_id"] = viewID
		}
		if pageToken != "" {
			params["page_token"] = pageToken
		}
		if err := s.callTyped(ctx, APIRequest{
			Method: http.MethodGet,
			Path: "/open-apis/bitable/v1/apps/" + url.PathEscape(appToken) +
				"/tables/" + url.PathEscape(tableID) + "/records",
			Params: params,
			As:     IdentityUser,
		}, &response); err != nil {
			return nil, err
		}
		for _, item := range response.Data.Items {
			if item.ID == "" || item.Fields == nil {
				return nil, errs.NewInternalError(errs.SubtypeInvalidResponse, "Base record list is missing record data")
			}
			records = append(records, BaseRecord{ID: item.ID, Fields: item.Fields})
		}
		if len(records) > maxRecords {
			return nil, errs.NewValidationError(
				errs.SubtypeFailedPrecondition,
				"Base reconciliation exceeds the bounded %d-record limit",
				maxRecords,
			)
		}
		if !response.Data.HasMore {
			break
		}
		if response.Data.PageToken == "" || response.Data.PageToken == pageToken {
			return nil, errs.NewInternalError(errs.SubtypeInvalidResponse, "Base record pagination is missing progress")
		}
		pageToken = response.Data.PageToken
	}
	return records, nil
}

func (s *ResourceService) CompareAndUpdateBaseField(
	ctx context.Context,
	update BaseFieldUpdate,
) (BaseFieldUpdateResult, error) {
	if update.AppToken == "" || update.TableID == "" || update.RecordID == "" ||
		strings.TrimSpace(update.FieldName) == "" {
		return BaseFieldUpdateResult{}, errs.NewValidationError(errs.SubtypeInvalidArgument, "Base field update coordinates are required")
	}
	current, err := s.GetBaseRecord(ctx, update.AppToken, update.TableID, update.RecordID)
	if err != nil {
		return BaseFieldUpdateResult{}, err
	}
	before, exists := current.Fields[update.FieldName]
	if !exists {
		return BaseFieldUpdateResult{}, errs.NewValidationError(errs.SubtypeFailedPrecondition, "Base status field %q no longer exists", update.FieldName)
	}
	if !resourceValueEquivalent(before, update.Before) {
		return BaseFieldUpdateResult{}, errs.NewValidationError(
			errs.SubtypeFailedPrecondition,
			"Base field %q changed before the prepared update",
			update.FieldName,
		)
	}
	var response struct {
		Data struct {
			Record struct {
				ID string `json:"record_id"`
			} `json:"record"`
		} `json:"data"`
	}
	if err := s.callTyped(ctx, APIRequest{
		Method: http.MethodPut,
		Path: "/open-apis/bitable/v1/apps/" + url.PathEscape(update.AppToken) +
			"/tables/" + url.PathEscape(update.TableID) + "/records/" + url.PathEscape(update.RecordID),
		Data: map[string]any{"fields": map[string]any{update.FieldName: update.After}},
		As:   IdentityUser,
	}, &response); err != nil {
		return BaseFieldUpdateResult{}, err
	}
	verified, err := s.GetBaseRecord(ctx, update.AppToken, update.TableID, update.RecordID)
	if err != nil {
		return BaseFieldUpdateResult{}, err
	}
	after, exists := verified.Fields[update.FieldName]
	if !exists || !resourceValueEquivalent(after, update.After) {
		return BaseFieldUpdateResult{}, errs.NewInternalError(
			errs.SubtypeInvalidResponse,
			"Base field update did not verify after write",
		)
	}
	return BaseFieldUpdateResult{Before: before, After: after, Verified: true}, nil
}

func (s *ResourceService) ReplyToComment(
	ctx context.Context,
	fileToken, fileType, commentID, text string,
) (CommentReplyResult, error) {
	if strings.TrimSpace(fileToken) == "" || strings.TrimSpace(fileType) == "" ||
		strings.TrimSpace(commentID) == "" || strings.TrimSpace(text) == "" {
		return CommentReplyResult{}, errs.NewValidationError(
			errs.SubtypeInvalidArgument,
			"file_token, file_type, comment_id, and reply text are required",
		)
	}
	var response struct {
		Data struct {
			ReplyID string `json:"reply_id"`
			Reply   struct {
				ReplyID string `json:"reply_id"`
			} `json:"reply"`
		} `json:"data"`
	}
	err := s.callTyped(ctx, APIRequest{
		Method: http.MethodPost,
		Path: "/open-apis/drive/v1/files/" + url.PathEscape(fileToken) +
			"/comments/" + url.PathEscape(commentID) + "/replies",
		Params: map[string]any{"file_type": fileType, "user_id_type": "open_id"},
		Data: map[string]any{"content": map[string]any{"elements": []any{
			map[string]any{"type": "text_run", "text_run": map[string]any{"text": text}},
		}}},
		As: IdentityUser,
	}, &response)
	if err != nil {
		return CommentReplyResult{}, err
	}
	replyID := response.Data.ReplyID
	if replyID == "" {
		replyID = response.Data.Reply.ReplyID
	}
	if replyID == "" {
		return CommentReplyResult{}, errs.NewInternalError(
			errs.SubtypeInvalidResponse,
			"Lark comment reply response is missing reply_id",
		)
	}
	return CommentReplyResult{ReplyID: replyID}, nil
}

func (s *ResourceService) GetComment(
	ctx context.Context,
	fileToken, fileType, commentID string,
) (ResourceComment, error) {
	if fileToken == "" || fileType == "" || commentID == "" {
		return ResourceComment{}, errs.NewValidationError(
			errs.SubtypeInvalidArgument,
			"file_token, file_type, and comment_id are required",
		)
	}
	var response struct {
		Data struct {
			CommentID string `json:"comment_id"`
			UserID    string `json:"user_id"`
			Quote     string `json:"quote"`
			IsSolved  bool   `json:"is_solved"`
			ReplyList struct {
				Replies []struct {
					Content struct {
						Elements []struct {
							TextRun struct {
								Text string `json:"text"`
							} `json:"text_run"`
						} `json:"elements"`
					} `json:"content"`
				} `json:"replies"`
			} `json:"reply_list"`
		} `json:"data"`
	}
	if err := s.callTyped(ctx, APIRequest{
		Method: http.MethodGet,
		Path: "/open-apis/drive/v1/files/" + url.PathEscape(fileToken) +
			"/comments/" + url.PathEscape(commentID),
		Params: map[string]any{
			"file_type": fileType, "user_id_type": "open_id", "need_reaction": false,
		},
		As: IdentityUser,
	}, &response); err != nil {
		return ResourceComment{}, err
	}
	var parts []string
	for _, reply := range response.Data.ReplyList.Replies {
		for _, element := range reply.Content.Elements {
			if text := strings.TrimSpace(element.TextRun.Text); text != "" {
				parts = append(parts, text)
			}
		}
	}
	id := response.Data.CommentID
	if id == "" {
		id = commentID
	}
	return ResourceComment{
		ID: id, UserID: response.Data.UserID, Quote: response.Data.Quote,
		Text: strings.Join(parts, "\n"), IsSolved: response.Data.IsSolved,
	}, nil
}

func (s *ResourceService) callTyped(ctx context.Context, request APIRequest, target any) error {
	raw, err := s.caller.CallAPI(ctx, request)
	if err != nil {
		return err
	}
	data, err := json.Marshal(raw)
	if err != nil {
		return errs.NewInternalError(errs.SubtypeInvalidResponse, "encode typed Lark resource response").WithCause(err)
	}
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	if err := decoder.Decode(target); err != nil {
		return errs.NewInternalError(errs.SubtypeInvalidResponse, "decode typed Lark resource response").WithCause(err)
	}
	return nil
}

func normalizeJSONValue(value any) any {
	data, err := json.Marshal(value)
	if err != nil {
		return fmt.Sprint(value)
	}
	var normalized any
	if json.Unmarshal(data, &normalized) != nil {
		return fmt.Sprint(value)
	}
	return normalized
}

func resourceValueEquivalent(actual, expected any) bool {
	if reflect.DeepEqual(normalizeJSONValue(actual), normalizeJSONValue(expected)) {
		return true
	}
	expectedText, ok := expected.(string)
	return ok && resourceValueString(actual) == strings.TrimSpace(expectedText)
}

func resourceValueString(value any) string {
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
		parts := make([]string, 0, len(typed))
		for _, item := range typed {
			if text := resourceValueString(item); text != "" {
				parts = append(parts, text)
			}
		}
		return strings.Join(parts, ", ")
	}
	return ""
}
