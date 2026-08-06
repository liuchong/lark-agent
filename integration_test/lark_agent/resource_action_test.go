package larkagent_test

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/liuchong/lark-agent/agent/domain"
	agentresource "github.com/liuchong/lark-agent/agent/resource"
	"github.com/liuchong/lark-agent/agent/storage"
	agenttools "github.com/liuchong/lark-agent/agent/tools"
	servicelark "github.com/liuchong/lark-agent/internal/lark"
)

type resourceActionClient struct {
	replies int
}

func (c *resourceActionClient) ListBaseFields(
	context.Context,
	string,
	string,
) ([]agenttools.ResourceField, error) {
	return nil, nil
}

func (c *resourceActionClient) CompareAndUpdateBaseField(
	context.Context,
	agenttools.ResourceFieldUpdate,
) (any, error) {
	return nil, nil
}

func (c *resourceActionClient) ReplyToComment(
	context.Context,
	string,
	string,
	string,
	string,
) (any, error) {
	c.replies++
	return map[string]any{"reply_id": "reply_verified"}, nil
}

type notificationResourceClient struct{}

func (notificationResourceClient) ResolveResource(
	_ context.Context,
	ref servicelark.ResourceRef,
) (servicelark.ResourceRef, error) {
	ref.ResourceType = servicelark.ResourceTypeBase
	ref.FileToken = "bas_bug"
	ref.FileType = "bitable"
	ref.AppToken = "bas_bug"
	ref.TableID = "tbl_bug"
	ref.RecordID = "rec_bug"
	return ref, nil
}

func (notificationResourceClient) EnsureCommentSubscription(
	context.Context,
	servicelark.ResourceRef,
) (servicelark.RemoteSubscription, error) {
	return servicelark.RemoteSubscription{ID: "sub_1", Active: true}, nil
}

func (notificationResourceClient) ListBaseFields(
	context.Context,
	string,
	string,
) ([]servicelark.BaseField, error) {
	return []servicelark.BaseField{
		{ID: "fld_title", Name: "问题", Type: 1, Primary: true},
		{ID: "fld_status", Name: "状态", Type: 3},
		{ID: "fld_owner", Name: "负责人", Type: 11},
	}, nil
}

func (notificationResourceClient) GetBaseRecord(
	context.Context,
	string,
	string,
	string,
) (servicelark.BaseRecord, error) {
	return servicelark.BaseRecord{ID: "rec_bug", Fields: map[string]any{
		"问题": "归档示例条目后列表未刷新",
		"状态": "待修改",
		"负责人": []any{
			map[string]any{"open_id": "ou_owner"},
		},
	}}, nil
}

func (notificationResourceClient) ListBaseRecords(
	context.Context,
	string,
	string,
	string,
) ([]servicelark.BaseRecord, error) {
	return nil, nil
}

func (notificationResourceClient) GetComment(
	context.Context,
	string,
	string,
	string,
) (servicelark.ResourceComment, error) {
	return servicelark.ResourceComment{}, nil
}

func TestResourceCommentActionCannotEscapeLinkedOwnerMention(t *testing.T) {
	ctx := context.Background()
	store, err := storage.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	evidence, inserted, err := store.RecordResourceEvidence(ctx, domain.ResourceEvidence{
		DedupKey: "comment:event_1", SourceKind: domain.ResourceEvidenceCommentEvent,
		SourceID: "event_1", ResourceType: "base", FileToken: "bas_bug",
		CommentID: "comment_owner", OwnerMentioned: true,
		ContentDigest: "sha256:comment-owner", ObservedAt: time.Now().UTC(),
	})
	if err != nil || !inserted {
		t.Fatalf("evidence=%+v inserted=%t err=%v", evidence, inserted, err)
	}
	event := domain.NormalizedEvent{
		Source: domain.SourceSchedule, EventID: "resource:comment_owner",
		MessageID: "resource:comment_owner", SenderType: "resource",
		Content: "subscribed comment mentioned owner", CreatedAt: time.Now().UTC(),
	}
	item := domain.NewWorkItem(event)
	item.DedupKey = domain.DedupKey(event)
	item.ResourceEvidenceID = evidence.ID
	item.WorkKind = domain.WorkKindResourceHandoff
	receipt, err := store.RecordWorkIntake(ctx, item)
	if err != nil {
		t.Fatal(err)
	}
	if receipt.Disposition != domain.IntakeAdmitted {
		t.Fatalf("receipt=%+v", receipt)
	}
	client := &resourceActionClient{}
	registry, err := agenttools.NewRegistry(agenttools.ResourceDefinitions(agenttools.ResourceToolOptions{
		Mode: domain.ModeAuto, Evidence: store, Actions: store, Client: client,
	})...)
	if err != nil {
		t.Fatal(err)
	}
	ctx = agenttools.WithWorkItemDedup(ctx, item.DedupKey)
	ctx = agenttools.WithInvocationScope(ctx, agenttools.InvocationScope{
		ReadOnly: true, WorkKind: domain.WorkKindResourceHandoff,
	})
	_, err = registry.Execute(ctx, "reply_resource_comment", json.RawMessage(`{
		"file_token":"bas_bug","file_type":"bitable",
		"comment_id":"comment_other","text":"已处理"
	}`))
	if err == nil || client.replies != 0 {
		t.Fatalf("arbitrary comment err=%v replies=%d", err, client.replies)
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

func TestApplicationResourceNotificationCreatesOwnerPrivateNotifyWork(t *testing.T) {
	ctx := context.Background()
	store, err := storage.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	monitor := agentresource.NewMonitor(notificationResourceClient{}, store, agentresource.Config{
		OwnerOpenID: "ou_owner",
	})
	accepted, err := monitor.IngestResourceNotification(ctx, domain.NormalizedEvent{
		Source: domain.SourcePoll, MessageID: "om_app_notice", EventID: "om_app_notice",
		SenderID: "cli_a4f", SenderType: "app",
		Content: "你在问题记录中被提及",
		ResourceURLs: []string{
			"https://example.larksuite.com/base/bas_bug?table=tbl_bug&record=rec_bug",
		},
		CreatedAt: time.Now().UTC(),
	})
	if err != nil || !accepted {
		t.Fatalf("accepted=%t err=%v", accepted, err)
	}
	items, err := store.ListWorkItems()
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].WorkKind != domain.WorkKindResourceHandoff ||
		items[0].Event.SenderType != "resource" || items[0].Event.ChatID != "" ||
		items[0].ResourceEvidenceID == 0 {
		t.Fatalf("items=%+v", items)
	}
	if _, err := store.GetResourceEvidenceForWork(ctx, items[0].DedupKey); err != nil {
		t.Fatal(err)
	}
	if items[0].Event.ChatID != "" || items[0].Event.SenderID != "" {
		t.Fatalf("resource notification retained an app-facing destination: %+v", items[0].Event)
	}
}
