package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/liuchong/lark-agent/agent/domain"
)

type fakeResourceToolStore struct {
	current   domain.ResourceEvidence
	requested int
	approved  bool
	completed int
}

func (f *fakeResourceToolStore) GetResourceEvidenceForWork(
	context.Context,
	string,
) (domain.ResourceEvidence, error) {
	return f.current, nil
}

func (f *fakeResourceToolStore) FindResourceEvidence(
	context.Context,
	domain.ResourceEvidenceQuery,
) ([]domain.ResourceEvidence, error) {
	return []domain.ResourceEvidence{f.current}, nil
}

func (f *fakeResourceToolStore) RequestResourceAction(
	context.Context,
	string,
	string,
	string,
) (int64, error) {
	f.requested++
	return 42, nil
}

func (f *fakeResourceToolStore) ConsumeResourceAction(
	context.Context,
	string,
	string,
	string,
) (int64, bool, error) {
	return 42, f.approved, nil
}

func (f *fakeResourceToolStore) BeginResourceAction(
	context.Context,
	string,
	string,
	string,
) (int64, string, bool, error) {
	return 42, "", false, nil
}

func (f *fakeResourceToolStore) CompleteResourceAction(
	context.Context,
	int64,
	string,
	string,
) error {
	f.completed++
	return nil
}

type fakeResourceMutationClient struct {
	updates int
	replies int
}

func (f *fakeResourceMutationClient) ListBaseFields(
	context.Context,
	string,
	string,
) ([]ResourceField, error) {
	return []ResourceField{{Name: "状态", Type: 3, Options: []string{"待修改", "已解决"}}}, nil
}

func (f *fakeResourceMutationClient) CompareAndUpdateBaseField(
	context.Context,
	ResourceFieldUpdate,
) (any, error) {
	f.updates++
	return map[string]any{"verified": true}, nil
}

func (f *fakeResourceMutationClient) ReplyToComment(
	context.Context,
	string,
	string,
	string,
	string,
) (any, error) {
	f.replies++
	return map[string]any{"reply_id": "reply_1"}, nil
}

func TestResourceToolsAreVisibleOnlyForResourceHandoffs(t *testing.T) {
	store := &fakeResourceToolStore{}
	client := &fakeResourceMutationClient{}
	registry, err := NewRegistry(ResourceDefinitions(ResourceToolOptions{
		Mode: domain.ModeAuto, Evidence: store, Actions: store, Client: client,
	})...)
	if err != nil {
		t.Fatal(err)
	}
	if got := registry.InfosFor(InvocationScope{ReadOnly: true}); len(got) != 0 {
		t.Fatalf("ordinary non-owner tools=%+v", got)
	}
	got := registry.InfosFor(InvocationScope{
		ReadOnly: true, WorkKind: domain.WorkKindResourceHandoff,
	})
	if len(got) != 4 {
		t.Fatalf("resource tools=%+v", got)
	}
}

func TestResourceEvidenceRejectsURLOutsideBoundedConversation(t *testing.T) {
	store := authorizedResourceToolStore()
	registry, err := NewRegistry(ResourceDefinitions(ResourceToolOptions{
		Mode: domain.ModeAuto, Evidence: store,
	})...)
	if err != nil {
		t.Fatal(err)
	}
	ctx := WithWorkItemDedup(context.Background(), "work:resource")
	ctx = WithInvocationScope(ctx, InvocationScope{
		ReadOnly:     true,
		WorkKind:     domain.WorkKindResourceHandoff,
		ResourceURLs: []string{"https://example.larksuite.com/base/allowed"},
	})
	if _, err := registry.Execute(
		ctx,
		"get_resource_evidence",
		json.RawMessage(`{"resource_url":"https://example.larksuite.com/base/other"}`),
	); err == nil || !strings.Contains(err.Error(), "bounded conversation") {
		t.Fatalf("err=%v", err)
	}
}

func TestResourceStatusUpdateRequestsApprovalBeforeWriting(t *testing.T) {
	store := authorizedResourceToolStore()
	client := &fakeResourceMutationClient{}
	registry, err := NewRegistry(ResourceDefinitions(ResourceToolOptions{
		Mode: domain.ModeApproval, Evidence: store, Actions: store, Client: client,
	})...)
	if err != nil {
		t.Fatal(err)
	}
	ctx := WithWorkItemDedup(context.Background(), "work:resource")
	ctx = WithInvocationScope(ctx, InvocationScope{
		ReadOnly: true, WorkKind: domain.WorkKindResourceHandoff,
	})
	result, err := registry.Execute(ctx, "update_base_status", json.RawMessage(`{
		"app_token":"bas_bug","table_id":"tbl_bug","record_id":"rec_bug",
		"field_name":"状态","expected_value":"待修改","desired_value":"已解决"
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if store.requested != 1 || client.updates != 0 ||
		!strings.Contains(result.Content, `"approval_required"`) {
		t.Fatalf("requested=%d updates=%d result=%s", store.requested, client.updates, result.Content)
	}
}

func TestResourceStatusUpdateAutoModeUsesCompareAndVerify(t *testing.T) {
	store := authorizedResourceToolStore()
	client := &fakeResourceMutationClient{}
	registry, err := NewRegistry(ResourceDefinitions(ResourceToolOptions{
		Mode: domain.ModeAuto, Evidence: store, Actions: store, Client: client,
	})...)
	if err != nil {
		t.Fatal(err)
	}
	ctx := WithWorkItemDedup(context.Background(), "work:resource")
	ctx = WithInvocationScope(ctx, InvocationScope{
		ReadOnly: true, WorkKind: domain.WorkKindResourceHandoff,
	})
	result, err := registry.Execute(ctx, "update_base_status", json.RawMessage(`{
		"app_token":"bas_bug","table_id":"tbl_bug","record_id":"rec_bug",
		"field_name":"状态","expected_value":"待修改","desired_value":"已解决"
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if client.updates != 1 || store.completed != 1 ||
		!strings.Contains(result.Content, `"completed"`) {
		t.Fatalf("updates=%d completed=%d result=%s", client.updates, store.completed, result.Content)
	}
}

func TestResourceCommentReplyRequiresExactLinkedOwnerMention(t *testing.T) {
	store := authorizedResourceToolStore()
	store.current.FileToken = "bas_bug"
	store.current.CommentID = "comment_owner"
	client := &fakeResourceMutationClient{}
	registry, err := NewRegistry(ResourceDefinitions(ResourceToolOptions{
		Mode: domain.ModeAuto, Evidence: store, Actions: store, Client: client,
	})...)
	if err != nil {
		t.Fatal(err)
	}
	ctx := WithWorkItemDedup(context.Background(), "work:resource")
	ctx = WithInvocationScope(ctx, InvocationScope{
		ReadOnly: true, WorkKind: domain.WorkKindResourceHandoff,
	})
	_, err = registry.Execute(ctx, "reply_resource_comment", json.RawMessage(`{
		"file_token":"bas_bug","file_type":"bitable",
		"comment_id":"comment_other","text":"已处理"
	}`))
	if err == nil || client.replies != 0 {
		t.Fatalf("err=%v replies=%d", err, client.replies)
	}
	result, err := registry.Execute(ctx, "reply_resource_comment", json.RawMessage(`{
		"file_token":"bas_bug","file_type":"bitable",
		"comment_id":"comment_owner","text":"已处理"
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if client.replies != 1 || !strings.Contains(result.Content, `"completed"`) {
		t.Fatalf("replies=%d result=%s", client.replies, result.Content)
	}
}

func authorizedResourceToolStore() *fakeResourceToolStore {
	return &fakeResourceToolStore{current: domain.ResourceEvidence{
		ID: 1, AppToken: "bas_bug", TableID: "tbl_bug", RecordID: "rec_bug",
		StatusFieldName: "状态", StatusValue: "待修改", OwnerMentioned: true,
		ContentDigest: "sha256:evidence",
	}}
}
