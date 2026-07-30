package runtime

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
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

func TestRequestedCodingWorkspaceScopeTreatsCurrentProjectAsPriorContextReference(t *testing.T) {
	bundle := agentcontext.Bundle{
		Environment: agentcontext.EnvironmentSnapshot{
			WorkspaceRoot:     "/workspace/sample-org",
			WorkspaceRealRoot: "/workspace/sample-org",
			Directory: []agentcontext.DirectoryEntry{
				{Path: "sample-project", Kind: "dir"},
				{Path: "Sample-Module", Kind: "dir"},
			},
		},
		Event: domain.NormalizedEvent{
			MessageID: "om_question",
			SenderID:  "ou_owner",
			Content:   "请从当前项目证据回答示例事件链路。",
		},
		Conversation: []domain.NormalizedEvent{{
			MessageID: "om_scope",
			SenderID:  "ou_owner",
			Content:   "这次只看 sample-org/sample-project/sample-module，不要看同名旧项目。",
		}},
	}
	if got := requestedCodingWorkspaceScope(bundle); got != "sample-project/sample-module" {
		t.Fatalf("scope=%q", got)
	}
	bundle.Conversation[0].SenderID = "ou_other"
	if got := requestedCodingWorkspaceScope(bundle); got != "" {
		t.Fatalf("scope inherited from another sender: %q", got)
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

func TestPrepareCodingWorkspaceToolArgumentsNormalizesAndConfinesPaths(t *testing.T) {
	const scope = "sample-project/sample-module"
	for _, test := range []struct {
		name        string
		tool        string
		arguments   string
		wantPath    string
		wantEntries []string
		wantError   bool
	}{
		{
			name:      "workspace-root-prefixed plan",
			tool:      "submit_investigation_plan",
			arguments: `{"entry_points":["sample-org/sample-project/sample-module/sample-client","sample-org/sample-project/sample-module/go"]}`,
			wantEntries: []string{
				"sample-project/sample-module/sample-client",
				"sample-project/sample-module/go",
			},
		},
		{
			name:      "sibling plan",
			tool:      "submit_investigation_plan",
			arguments: `{"entry_points":["Sample-Module/sample-client"]}`,
			wantError: true,
		},
		{
			name:      "workspace-root-prefixed read",
			tool:      "read_workspace",
			arguments: `{"path":"sample-org/sample-project/sample-module/sample-client/request.kt"}`,
			wantPath:  "sample-project/sample-module/sample-client/request.kt",
		},
		{
			name:      "repository-relative read",
			tool:      "read_workspace",
			arguments: `{"path":"sample-client/request.kt"}`,
			wantPath:  "sample-project/sample-module/sample-client/request.kt",
		},
		{
			name:      "repository-relative plan",
			tool:      "submit_investigation_plan",
			arguments: `{"entry_points":["sample-client","docs/sample-event.md"]}`,
			wantEntries: []string{
				"sample-project/sample-module/sample-client",
				"sample-project/sample-module/docs/sample-event.md",
			},
		},
		{
			name:      "sibling read",
			tool:      "read_workspace",
			arguments: `{"path":"Sample-Module/sample-client/request.kt"}`,
			wantError: true,
		},
		{
			name:      "case-mismatched full scope",
			tool:      "read_workspace",
			arguments: `{"path":"sample-project/Sample-Module/sample-client/request.kt"}`,
			wantError: true,
		},
		{
			name:      "search inherits exact scope",
			tool:      "search_workspace",
			arguments: `{"query":"SampleRequest"}`,
			wantPath:  scope,
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
			prepared, err := prepareCodingWorkspaceToolArguments(
				test.tool,
				test.arguments,
				scope,
				"/workspace/sample-org",
			)
			if test.wantError && (err == nil || !strings.Contains(err.Error(), scope)) {
				t.Fatalf("err=%v", err)
			}
			if !test.wantError && err != nil {
				t.Fatalf("err=%v", err)
			}
			if err != nil || test.wantPath == "" {
				if err != nil || len(test.wantEntries) == 0 {
					return
				}
			}
			var decoded struct {
				Path        string   `json:"path"`
				EntryPoints []string `json:"entry_points"`
			}
			if err := json.Unmarshal([]byte(prepared), &decoded); err != nil {
				t.Fatal(err)
			}
			if decoded.Path != test.wantPath {
				t.Fatalf("path=%q want=%q prepared=%s", decoded.Path, test.wantPath, prepared)
			}
			if len(test.wantEntries) > 0 &&
				strings.Join(decoded.EntryPoints, ",") != strings.Join(test.wantEntries, ",") {
				t.Fatalf("entry_points=%v want=%v prepared=%s", decoded.EntryPoints, test.wantEntries, prepared)
			}
		})
	}
}

func TestValidateCodingWorkspaceToolRealPathRejectsSymlinkAndCaseEscapes(t *testing.T) {
	root := t.TempDir()
	for _, directory := range []string{
		"sample-project/sample-module/sample-client",
		"Sample-Module",
	} {
		if err := os.MkdirAll(filepath.Join(root, directory), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(
		filepath.Join(root, "sample-project/sample-module/sample-client/Request.kt"),
		[]byte("request"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(root, "Sample-Module/secret.txt"),
		[]byte("sibling secret"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(
		filepath.Join(root, "Sample-Module"),
		filepath.Join(root, "sample-project/sample-module/legacy-link"),
	); err != nil {
		t.Fatal(err)
	}

	for _, test := range []struct {
		name      string
		path      string
		wantError bool
	}{
		{
			name: "valid exact path",
			path: "sample-project/sample-module/sample-client/Request.kt",
		},
		{
			name:      "symlink to sibling",
			path:      "sample-project/sample-module/legacy-link/secret.txt",
			wantError: true,
		},
		{
			name:      "case-insensitive directory lookup",
			path:      "sample-project/sample-module/Sample-Client/Request.kt",
			wantError: true,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := validateCodingWorkspaceToolRealPath(
				"read_workspace",
				`{"path":`+strconv.Quote(test.path)+`}`,
				"sample-project/sample-module",
				root,
			)
			if test.wantError && err == nil {
				t.Fatal("expected exact real-path rejection")
			}
			if !test.wantError && err != nil {
				t.Fatalf("unexpected validation error: %v", err)
			}
		})
	}
}
