package storage

import (
	"context"
	"testing"

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
