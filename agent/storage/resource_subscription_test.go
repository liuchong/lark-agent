package storage

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/liuchong/lark-agent/agent/domain"
)

func TestResourceSubscriptionPersistsNormalizedResource(t *testing.T) {
	store := openStore(t)
	sub, err := store.UpsertResourceSubscription(context.Background(), domain.ResourceSubscription{
		OriginalURL:  "https://example.larksuite.com/base/basExampleAppToken001?table=tblExampleTable001&view=vewExampleView001",
		ResourceType: "base",
		AppToken:     "basExampleAppToken001",
		TableID:      "tblExampleTable001",
		ViewID:       "vewExampleView001",
		MonitorModes: []string{"base_record", "base_field"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if sub.ID == 0 || sub.Status != domain.ResourceSubscriptionPending {
		t.Fatalf("subscription=%+v", sub)
	}
	list, err := store.ListResourceSubscriptions(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].TableID != "tblExampleTable001" || list[0].ViewID != "vewExampleView001" {
		t.Fatalf("list=%+v", list)
	}
}

func TestResourceEvidencePersistsDeduplicatesAndFindsBug(t *testing.T) {
	store := openStore(t)
	evidence := domain.ResourceEvidence{
		DedupKey:        "notification:om_docs_bug",
		SourceKind:      domain.ResourceEvidenceNotification,
		SourceID:        "om_docs_bug",
		ResourceType:    "base",
		AppToken:        "bas_bug",
		TableID:         "tbl_bug",
		RecordID:        "rec_bug",
		OriginalURL:     "https://example.larksuite.com/record/shr_bug",
		Title:           "归档示例条目后列表未刷新",
		IssueKey:        "BUG-99999",
		StatusFieldID:   "fld_status",
		StatusFieldName: "状态",
		StatusValue:     "待修改",
		AssigneeOpenIDs: []string{"ou_owner"},
		OwnerMentioned:  true,
		ContentDigest:   "sha256:bug",
		ObservedAt:      time.Date(2026, 8, 6, 1, 0, 0, 0, time.UTC),
	}
	first, inserted, err := store.RecordResourceEvidence(context.Background(), evidence)
	if err != nil || !inserted || first.ID == 0 {
		t.Fatalf("first=%+v inserted=%v err=%v", first, inserted, err)
	}
	second, inserted, err := store.RecordResourceEvidence(context.Background(), evidence)
	if err != nil || inserted || second.ID != first.ID {
		t.Fatalf("second=%+v inserted=%v err=%v", second, inserted, err)
	}
	found, err := store.FindResourceEvidence(context.Background(), domain.ResourceEvidenceQuery{
		Terms: []string{"归档示例条目", "BUG-99999"}, Limit: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(found) != 1 || found[0].RecordID != "rec_bug" ||
		len(found[0].AssigneeOpenIDs) != 1 || found[0].AssigneeOpenIDs[0] != "ou_owner" {
		t.Fatalf("found=%+v", found)
	}
}

func TestResourceEvidenceMigrationIsSchemaVersion19(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	var version int
	if err := store.db.QueryRow(`SELECT version FROM schema_version LIMIT 1`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version < 19 {
		t.Fatalf("schema version=%d, want at least 19", version)
	}
}

func TestExactResourceEvidenceQueryReturnsLatestAssignmentState(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	ctx := context.Background()
	base := domain.ResourceEvidence{
		SourceKind: domain.ResourceEvidenceRecordEvent, SourceID: "event_record",
		ResourceType: "base", AppToken: "bas_bug", TableID: "tbl_bug", RecordID: "rec_bug",
		StatusFieldName: "状态", StatusValue: "待修改",
	}
	assigned := base
	assigned.DedupKey = "record:assigned"
	assigned.OwnerMentioned = true
	assigned.ContentDigest = "sha256:assigned"
	assigned.ObservedAt = time.Date(2026, 8, 6, 1, 0, 0, 0, time.UTC)
	if _, _, err := store.RecordResourceEvidence(ctx, assigned); err != nil {
		t.Fatal(err)
	}
	unassigned := base
	unassigned.DedupKey = "record:unassigned"
	unassigned.OwnerMentioned = false
	unassigned.ContentDigest = "sha256:unassigned"
	unassigned.ObservedAt = time.Date(2026, 8, 6, 2, 0, 0, 0, time.UTC)
	if _, _, err := store.RecordResourceEvidence(ctx, unassigned); err != nil {
		t.Fatal(err)
	}
	found, err := store.FindResourceEvidence(ctx, domain.ResourceEvidenceQuery{
		AppToken: "bas_bug", TableID: "tbl_bug", RecordID: "rec_bug", Limit: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(found) != 2 || found[0].OwnerMentioned {
		t.Fatalf("exact record query did not return latest assignment first: %+v", found)
	}
}
