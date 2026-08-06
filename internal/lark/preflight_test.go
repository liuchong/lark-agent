package lark

import (
	"context"
	"strings"
	"testing"
)

func TestPreflightRequiresPublishedMessageEventAndScopes(t *testing.T) {
	caller := &fakeCaller{response: map[string]any{
		"data": map[string]any{
			"items": []any{map[string]any{
				"version_id":   "v1",
				"version":      "1.0.0",
				"status":       float64(1),
				"publish_time": "2026-07-24T00:00:00Z",
				"event_infos": []any{
					map[string]any{"event_type": "im.message.receive_v1"},
				},
				"scopes": []any{
					map[string]any{
						"scope":       "im:message.p2p_msg:readonly",
						"token_types": []any{"tenant"},
					},
					map[string]any{
						"scope":       "im:message.group_at_msg:readonly",
						"token_types": []any{"tenant"},
					},
					map[string]any{
						"scope":       "im:message.reactions:read",
						"token_types": []any{"tenant"},
					},
				},
			}},
		},
	}}
	version, err := CheckPublishedApp(context.Background(), caller, "cli_test")
	if err != nil {
		t.Fatal(err)
	}
	if version.VersionID != "v1" || caller.req.As != IdentityBot {
		t.Fatalf("version=%+v request=%+v", version, caller.req)
	}
}

func TestPreflightRejectsMissingReactionReadScope(t *testing.T) {
	caller := &fakeCaller{response: map[string]any{
		"data": map[string]any{
			"items": []any{map[string]any{
				"version_id":   "v1",
				"status":       float64(1),
				"publish_time": "2026-07-24T00:00:00Z",
				"event_infos": []any{
					map[string]any{"event_type": "im.message.receive_v1"},
				},
				"scopes": []any{
					map[string]any{
						"scope":       "im:message.p2p_msg:readonly",
						"token_types": []any{"tenant"},
					},
					map[string]any{
						"scope":       "im:message.group_at_msg:readonly",
						"token_types": []any{"tenant"},
					},
				},
			}},
		},
	}}
	_, err := CheckPublishedApp(context.Background(), caller, "cli_test")
	if err == nil || !strings.Contains(err.Error(), "reactions") {
		t.Fatalf("error=%v", err)
	}
}

func TestPreflightRejectsMissingGroupAtScope(t *testing.T) {
	caller := &fakeCaller{response: map[string]any{
		"data": map[string]any{
			"items": []any{map[string]any{
				"version_id":   "v1",
				"status":       float64(1),
				"publish_time": "2026-07-24T00:00:00Z",
				"event_infos": []any{
					map[string]any{"event_type": "im.message.receive_v1"},
				},
				"scopes": []any{
					map[string]any{
						"scope":       "im:message.p2p_msg:readonly",
						"token_types": []any{"tenant"},
					},
				},
			}},
		},
	}}
	_, err := CheckPublishedApp(context.Background(), caller, "cli_test")
	if err == nil || !strings.Contains(err.Error(), "group_at") {
		t.Fatalf("error=%v", err)
	}
}
