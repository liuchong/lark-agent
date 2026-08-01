package lark

import (
	"context"
	"net/http"
	"net/url"
	"strings"

	errs "github.com/liuchong/lark-agent/internal/apperr"
)

const (
	messageEventType  = "im.message.receive_v1"
	p2pReadScope      = "im:message.p2p_msg:readonly"
	groupAtReadScope  = "im:message.group_at_msg:readonly"
	reactionReadScope = "im:message.reactions:read"
)

type PublishedApp struct {
	VersionID    string
	Version      string
	EventTypes   []string
	TenantScopes []string
}

func CheckPublishedApp(ctx context.Context, caller Caller, appID string) (PublishedApp, error) {
	if caller == nil {
		return PublishedApp{}, errs.NewInternalError(
			errs.SubtypeFailedPrecondition,
			"lark SDK API caller is not configured",
		)
	}
	if strings.TrimSpace(appID) == "" {
		return PublishedApp{}, errs.NewValidationError(
			errs.SubtypeInvalidArgument,
			"app_id is required for realtime preflight",
		).WithParam("app_id")
	}
	result, err := caller.CallAPI(ctx, APIRequest{
		Method: http.MethodGet,
		Path: "/open-apis/application/v6/applications/" +
			url.PathEscape(appID) + "/app_versions",
		Params: map[string]any{"lang": "zh_cn", "page_size": 2},
		As:     IdentityBot,
	})
	if err != nil {
		return PublishedApp{}, err
	}
	for _, item := range arrayValue(responseData(result)["items"]) {
		raw := mapValue(item)
		if intValue(raw["status"]) != 1 || stringValue(raw["publish_time"]) == "" {
			continue
		}
		version := PublishedApp{
			VersionID: stringValue(raw["version_id"]),
			Version:   stringValue(raw["version"]),
		}
		for _, item := range arrayValue(raw["event_infos"]) {
			event := mapValue(item)
			if eventType := stringValue(event["event_type"]); eventType != "" {
				version.EventTypes = append(version.EventTypes, eventType)
			}
		}
		for _, item := range arrayValue(raw["scopes"]) {
			scope := mapValue(item)
			if !containsAnyString(scope["token_types"], "tenant") {
				continue
			}
			if name := stringValue(scope["scope"]); name != "" {
				version.TenantScopes = append(version.TenantScopes, name)
			}
		}
		if !contains(version.EventTypes, messageEventType) {
			return PublishedApp{}, errs.NewValidationError(
				errs.SubtypeFailedPrecondition,
				"published app version does not subscribe to %s",
				messageEventType,
			)
		}
		for _, required := range []string{
			p2pReadScope,
			groupAtReadScope,
			reactionReadScope,
		} {
			if !contains(version.TenantScopes, required) {
				return PublishedApp{}, errs.NewPermissionError(
					errs.SubtypeMissingScope,
					"published app version is missing scope %s",
					required,
				).WithMissingScopes(required)
			}
		}
		return version, nil
	}
	return PublishedApp{}, errs.NewValidationError(
		errs.SubtypeFailedPrecondition,
		"application has no published version",
	)
}

func intValue(value any) int {
	switch typed := value.(type) {
	case int:
		return typed
	case int64:
		return int(typed)
	case float64:
		return int(typed)
	default:
		return 0
	}
}

func containsAnyString(value any, wanted string) bool {
	switch values := value.(type) {
	case []any:
		for _, item := range values {
			if stringValue(item) == wanted {
				return true
			}
		}
	case []string:
		for _, item := range values {
			if item == wanted {
				return true
			}
		}
	}
	return false
}

func contains(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}
