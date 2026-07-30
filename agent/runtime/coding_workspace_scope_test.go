package runtime

import (
	"strings"
	"testing"

	agentcontext "github.com/liuchong/lark-agent/agent/context"
	"github.com/liuchong/lark-agent/agent/domain"
)

func TestRequestedCodingWorkspaceScopeUsesExactConfiguredRootPath(t *testing.T) {
	for _, test := range []struct {
		name    string
		content string
		want    string
	}{
		{
			name:    "workspace root basename",
			content: "只看 sample-org/sample-project/sample-module 里的示例事件实现",
			want:    "sample-project/sample-module",
		},
		{
			name:    "absolute workspace path",
			content: "检查 /workspace/sample-org/sample-project/sample-module/sample-client",
			want:    "sample-project/sample-module/sample-client",
		},
		{
			name:    "api route is not a workspace scope",
			content: "检查 /api/messages/modify 的返回值",
		},
		{
			name:    "sibling name without configured root is not inferred",
			content: "检查 Sample-Module/sample-client",
		},
		{
			name:    "explicit workspace relative scope",
			content: "只看 sample-project/sample-module 里的示例事件实现",
			want:    "sample-project/sample-module",
		},
		{
			name:    "plain workspace relative scope",
			content: "请检查 sample-project/sample-module 里的示例事件实现",
			want:    "sample-project/sample-module",
		},
		{
			name:    "multiple workspace relative paths are not forced",
			content: "比较 sample-project/sample-module 和 sample-project/sample-service 的示例事件实现",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			got := requestedCodingWorkspaceScope(agentcontext.Bundle{
				Environment: agentcontext.EnvironmentSnapshot{
					WorkspaceRoot:     "/workspace/sample-org",
					WorkspaceRealRoot: "/workspace/sample-org",
					Directory: []agentcontext.DirectoryEntry{{
						Path: "sample-project",
						Kind: "dir",
					}},
				},
				Event: domain.NormalizedEvent{Content: test.content},
			})
			if got != test.want {
				t.Fatalf("scope=%q want=%q", got, test.want)
			}
		})
	}
}

func TestRequestedCodingWorkspaceScopeUsesMostRecentBoundedConversationPath(t *testing.T) {
	got := requestedCodingWorkspaceScope(agentcontext.Bundle{
		Environment: agentcontext.EnvironmentSnapshot{
			WorkspaceRoot:     "/workspace/sample-org",
			WorkspaceRealRoot: "/workspace/sample-org",
		},
		Event: domain.NormalizedEvent{
			MessageID: "om_follow_up",
			SenderID:  "ou_owner",
			Content:   "结合上一条，继续核对请求、回调和本地收敛。",
		},
		Conversation: []domain.NormalizedEvent{
			{MessageID: "om_old", SenderID: "ou_other", Content: "以前讨论 sample-org/Sample-Module"},
			{MessageID: "om_scope", SenderID: "ou_owner", Content: "这次只看 sample-org/sample-project/sample-module"},
			{MessageID: "om_follow_up", SenderID: "ou_owner", Content: "结合上一条，继续核对请求、回调和本地收敛。"},
		},
	})
	if got != "sample-project/sample-module" {
		t.Fatalf("scope=%q", got)
	}
}

func TestRequestedCodingWorkspaceScopeIgnoresUnrelatedOrOtherSenderHistory(t *testing.T) {
	base := agentcontext.Bundle{
		Environment: agentcontext.EnvironmentSnapshot{
			WorkspaceRoot:     "/workspace/sample-org",
			WorkspaceRealRoot: "/workspace/sample-org",
		},
		Event: domain.NormalizedEvent{
			MessageID: "om_current",
			SenderID:  "ou_owner",
			Content:   "检查消息修改行为",
		},
		Conversation: []domain.NormalizedEvent{{
			MessageID: "om_history",
			SenderID:  "ou_owner",
			Content:   "以前讨论 sample-org/Sample-Module",
		}},
	}
	if got := requestedCodingWorkspaceScope(base); got != "" {
		t.Fatalf("unrelated history scope=%q", got)
	}
	base.Event.Content = "结合上一条检查消息修改行为"
	base.Conversation[0].SenderID = "ou_other"
	if got := requestedCodingWorkspaceScope(base); got != "" {
		t.Fatalf("other sender history scope=%q", got)
	}
}

func TestValidateCodingWorkspaceScopeRejectsSiblingAndUnscopedTools(t *testing.T) {
	const scope = "sample-project/sample-module"
	for _, test := range []struct {
		name      string
		tool      string
		arguments string
		wantError bool
	}{
		{
			name:      "scoped plan",
			tool:      "submit_investigation_plan",
			arguments: `{"entry_points":["sample-project/sample-module/sample-client","sample-project/sample-module/go"]}`,
		},
		{
			name:      "sibling plan",
			tool:      "submit_investigation_plan",
			arguments: `{"entry_points":["Sample-Module/sample-client"]}`,
			wantError: true,
		},
		{
			name:      "scoped read",
			tool:      "read_workspace",
			arguments: `{"path":"sample-project/sample-module/sample-client/request.kt"}`,
		},
		{
			name:      "sibling read",
			tool:      "read_workspace",
			arguments: `{"path":"Sample-Module/sample-client/request.kt"}`,
			wantError: true,
		},
		{
			name:      "search requires path",
			tool:      "search_workspace",
			arguments: `{"query":"SampleRequest"}`,
			wantError: true,
		},
		{
			name:      "unscoped exploration",
			tool:      "explore_workspace",
			arguments: `{"queries":["SampleRequest"]}`,
			wantError: true,
		},
		{
			name:      "unscoped symbol index",
			tool:      "search_code_symbols",
			arguments: `{"query":"SampleRequest"}`,
			wantError: true,
		},
		{
			name:      "unscoped call trace",
			tool:      "trace_code_path",
			arguments: `{"symbol":"SampleOperation"}`,
			wantError: true,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := validateCodingWorkspaceScope(test.tool, test.arguments, scope)
			if test.wantError && (err == nil || !strings.Contains(err.Error(), scope)) {
				t.Fatalf("err=%v", err)
			}
			if !test.wantError && err != nil {
				t.Fatalf("err=%v", err)
			}
		})
	}
}
