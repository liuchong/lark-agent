package resource

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/liuchong/lark-agent/agent/domain"
	errs "github.com/liuchong/lark-agent/internal/apperr"
	servicelark "github.com/liuchong/lark-agent/internal/lark"
)

type fakeResourceClient struct {
	resolved servicelark.ResourceRef
	fields   []servicelark.BaseField
	record   servicelark.BaseRecord
	records  []servicelark.BaseRecord
	comment  servicelark.ResourceComment
	remote   servicelark.RemoteSubscription
}

func (f *fakeResourceClient) ResolveResource(context.Context, servicelark.ResourceRef) (servicelark.ResourceRef, error) {
	return f.resolved, nil
}

func (f *fakeResourceClient) EnsureCommentSubscription(context.Context, servicelark.ResourceRef) (servicelark.RemoteSubscription, error) {
	return f.remote, nil
}

func (f *fakeResourceClient) ListBaseFields(context.Context, string, string) ([]servicelark.BaseField, error) {
	return append([]servicelark.BaseField(nil), f.fields...), nil
}

func (f *fakeResourceClient) GetBaseRecord(context.Context, string, string, string) (servicelark.BaseRecord, error) {
	return f.record, nil
}

func (f *fakeResourceClient) ListBaseRecords(context.Context, string, string, string) ([]servicelark.BaseRecord, error) {
	return append([]servicelark.BaseRecord(nil), f.records...), nil
}

func (f *fakeResourceClient) GetComment(
	context.Context,
	string,
	string,
	string,
) (servicelark.ResourceComment, error) {
	return f.comment, nil
}

type fakeResourceStore struct {
	subscriptions []domain.ResourceSubscription
	evidence      []domain.ResourceEvidence
	work          []domain.WorkItem
}

func (s *fakeResourceStore) ListResourceSubscriptions(context.Context) ([]domain.ResourceSubscription, error) {
	return append([]domain.ResourceSubscription(nil), s.subscriptions...), nil
}

func (s *fakeResourceStore) UpsertResourceSubscription(
	_ context.Context,
	sub domain.ResourceSubscription,
) (domain.ResourceSubscription, error) {
	if sub.ID == 0 {
		sub.ID = 1
	}
	for i := range s.subscriptions {
		if s.subscriptions[i].OriginalURL == sub.OriginalURL {
			s.subscriptions[i] = sub
			return sub, nil
		}
	}
	s.subscriptions = append(s.subscriptions, sub)
	return sub, nil
}

func (s *fakeResourceStore) RecordResourceEvidence(
	_ context.Context,
	evidence domain.ResourceEvidence,
) (domain.ResourceEvidence, bool, error) {
	for _, existing := range s.evidence {
		if existing.DedupKey == evidence.DedupKey {
			return existing, false, nil
		}
	}
	evidence.ID = int64(len(s.evidence) + 1)
	s.evidence = append(s.evidence, evidence)
	return evidence, true, nil
}

func (s *fakeResourceStore) FindResourceEvidence(
	_ context.Context,
	query domain.ResourceEvidenceQuery,
) ([]domain.ResourceEvidence, error) {
	var out []domain.ResourceEvidence
	for _, evidence := range s.evidence {
		if query.AppToken != "" && evidence.AppToken != query.AppToken {
			continue
		}
		if query.TableID != "" && evidence.TableID != query.TableID {
			continue
		}
		if query.RecordID != "" && evidence.RecordID != query.RecordID {
			continue
		}
		out = append(out, evidence)
	}
	return out, nil
}

func (s *fakeResourceStore) RecordWorkIntake(
	_ context.Context,
	item domain.WorkItem,
) (domain.IntakeReceipt, error) {
	for _, existing := range s.work {
		if existing.DedupKey == item.DedupKey {
			return domain.IntakeReceipt{Disposition: domain.IntakeDuplicate}, nil
		}
	}
	s.work = append(s.work, item)
	return domain.IntakeReceipt{Disposition: domain.IntakeAdmitted}, nil
}

func TestMonitorIngestsDocsNotificationAndCreatesResourceHandoff(t *testing.T) {
	client := &fakeResourceClient{
		resolved: servicelark.ResourceRef{
			OriginalURL:  "https://example.larksuite.com/record/shr_bug",
			ResourceType: servicelark.ResourceTypeBase,
			AppToken:     "bas_bug", FileToken: "bas_bug", FileType: "bitable",
			TableID: "tbl_bug", RecordID: "rec_bug",
		},
		fields: []servicelark.BaseField{
			{ID: "fld_title", Name: "问题", Type: 1, Primary: true},
			{ID: "fld_status", Name: "状态", Type: 3, Options: []servicelark.BaseOption{
				{ID: "opt_todo", Name: "待修改"}, {ID: "opt_resolved", Name: "已解决"},
			}},
			{ID: "fld_owner", Name: "经办人", Type: 11},
		},
		record: servicelark.BaseRecord{ID: "rec_bug", Fields: map[string]any{
			"问题":  "BUG-99999 归档示例条目后列表未刷新",
			"状态":  "待修改",
			"经办人": []any{map[string]any{"id": "ou_owner", "name": "测试负责人"}},
		}},
	}
	store := &fakeResourceStore{}
	monitor := NewMonitor(client, store, Config{OwnerOpenID: "ou_owner"})
	accepted, err := monitor.IngestResourceNotification(context.Background(), domain.NormalizedEvent{
		Source: domain.SourcePoll, EventID: "poll:om_docs_notice", MessageID: "om_docs_notice",
		SenderID: "cli_docs", SenderType: "app",
		Content:      "BUG-99999 中提到了你",
		ResourceURLs: []string{"https://example.larksuite.com/record/shr_bug"},
		CreatedAt:    time.Date(2026, 8, 6, 2, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !accepted || len(store.evidence) != 1 || len(store.work) != 1 {
		t.Fatalf("accepted=%v evidence=%+v work=%+v", accepted, store.evidence, store.work)
	}
	evidence := store.evidence[0]
	if evidence.IssueKey != "BUG-99999" || evidence.StatusValue != "待修改" ||
		!evidence.OwnerMentioned || evidence.RecordID != "rec_bug" {
		t.Fatalf("evidence=%+v", evidence)
	}
	if store.work[0].WorkKind != domain.WorkKindResourceHandoff ||
		store.work[0].ResourceEvidenceID != evidence.ID {
		t.Fatalf("work=%+v", store.work[0])
	}
}

func TestMonitorSyncResolvesWikiAndActivatesRemoteSubscription(t *testing.T) {
	client := &fakeResourceClient{
		resolved: servicelark.ResourceRef{
			ResourceType: servicelark.ResourceTypeBase,
			AppToken:     "bas_bug", FileToken: "bas_bug", FileType: "bitable", TableID: "tbl_bug",
		},
		remote: servicelark.RemoteSubscription{ID: "sub_bug", Active: true, FileType: "bitable"},
	}
	store := &fakeResourceStore{subscriptions: []domain.ResourceSubscription{{
		ID: 1, OriginalURL: "https://example.larksuite.com/wiki/wik_bug?table=tbl_bug",
		ResourceType: "wiki", WikiNodeToken: "wik_bug", TableID: "tbl_bug",
		Status: domain.ResourceSubscriptionPending,
	}}}
	result, err := NewMonitor(client, store, Config{OwnerOpenID: "ou_owner"}).
		SyncSubscriptions(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.Active != 1 || len(store.subscriptions) != 1 ||
		store.subscriptions[0].Status != domain.ResourceSubscriptionActive ||
		store.subscriptions[0].RemoteSubscriptionID != "sub_bug" ||
		store.subscriptions[0].AppToken != "bas_bug" ||
		!reflect.DeepEqual(store.subscriptions[0].MonitorModes,
			[]string{"base_record", "base_field", "cloud_docs_notice"}) {
		t.Fatalf("result=%+v subscriptions=%+v", result, store.subscriptions)
	}
}

func TestResourceErrorSummaryPreservesSafeProviderDiagnostics(t *testing.T) {
	summary := resourceErrorSummary(
		errs.NewAPIError(errs.SubtypeServerError, "field validation failed").
			WithCode(131002).
			WithIdentity("user").
			WithParam("token").
			WithHint("check token type"),
	)
	for _, want := range []string{
		"field validation failed", "code=131002", "category=api",
		"identity=user", "field=token", "hint=check token type",
	} {
		if !strings.Contains(summary, want) {
			t.Fatalf("summary missing %q: %s", want, summary)
		}
	}
}

func TestMonitorReconciliationColdStartDoesNotCreateWorkThenChangedRecordDoes(t *testing.T) {
	client := &fakeResourceClient{
		resolved: servicelark.ResourceRef{
			ResourceType: servicelark.ResourceTypeBase,
			AppToken:     "bas_bug", FileToken: "bas_bug", FileType: "bitable", TableID: "tbl_bug",
		},
		fields: []servicelark.BaseField{
			{ID: "fld_title", Name: "问题", Type: 1, Primary: true},
			{ID: "fld_status", Name: "状态", Type: 3},
			{ID: "fld_owner", Name: "经办人", Type: 11},
		},
		records: []servicelark.BaseRecord{{ID: "rec_bug", Fields: map[string]any{
			"问题": "BUG-99999 归档示例条目", "状态": "待修改",
			"经办人": []any{map[string]any{"id": "ou_owner"}},
		}}},
	}
	store := &fakeResourceStore{subscriptions: []domain.ResourceSubscription{{
		ID: 1, OriginalURL: "https://example.larksuite.com/base/bas_bug?table=tbl_bug",
		ResourceType: "base", AppToken: "bas_bug", FileToken: "bas_bug", TableID: "tbl_bug",
		Status: domain.ResourceSubscriptionActive,
	}}}
	monitor := NewMonitor(client, store, Config{OwnerOpenID: "ou_owner"})
	if _, err := monitor.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(store.evidence) != 1 || len(store.work) != 0 || store.subscriptions[0].Cursor == "" {
		t.Fatalf("cold evidence=%+v work=%+v sub=%+v", store.evidence, store.work, store.subscriptions[0])
	}
	client.records[0].Fields["状态"] = "验证中"
	if _, err := monitor.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(store.evidence) != 2 || len(store.work) != 1 {
		t.Fatalf("changed evidence=%+v work=%+v", store.evidence, store.work)
	}
}

func TestMonitorCommentEventCreatesOneOwnerHandoffWithoutReplyingToApp(t *testing.T) {
	store := &fakeResourceStore{subscriptions: []domain.ResourceSubscription{{
		ID: 1, OriginalURL: "https://example.larksuite.com/base/bas_bug?table=tbl_bug",
		ResourceType: "base", FileToken: "bas_bug", AppToken: "bas_bug", TableID: "tbl_bug",
		Status: domain.ResourceSubscriptionActive,
	}}}
	result, err := NewMonitor(&fakeResourceClient{comment: servicelark.ResourceComment{
		ID: "comment_bug", Text: "BUG-99999 示例列表刷新问题请处理",
	}}, store, Config{OwnerOpenID: "ou_owner"}).
		HandleResourceSignal(context.Background(), servicelark.ResourceSignal{
			Kind: servicelark.ResourceSignalComment, EventID: "evt_comment",
			FileToken: "bas_bug", FileType: "bitable", CommentID: "comment_bug",
			ToOpenID: "ou_owner", Mentioned: true,
			ObservedAt: time.Date(2026, 8, 6, 2, 30, 0, 0, time.UTC),
		})
	if err != nil {
		t.Fatal(err)
	}
	if result.Evidence != 1 || result.WorkCreated != 1 ||
		len(store.evidence) != 1 || store.evidence[0].CommentID != "comment_bug" ||
		store.evidence[0].IssueKey != "BUG-99999" ||
		len(store.work) != 1 || store.work[0].Event.ChatID != "" {
		t.Fatalf("result=%+v evidence=%+v work=%+v", result, store.evidence, store.work)
	}
}
