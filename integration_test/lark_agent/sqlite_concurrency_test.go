package larkagent_test

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/liuchong/lark-agent/agent/domain"
	"github.com/liuchong/lark-agent/agent/storage"
)

func TestConcurrentForegroundRunStartsWaitForBriefSQLiteWriter(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "state.db")
	store, err := storage.Open(statePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("close store: %v", err)
		}
	})

	events := []domain.NormalizedEvent{
		{MessageID: "om_concurrent_group", Content: "group request"},
		{MessageID: "om_concurrent_private", Content: "private request"},
	}
	for _, event := range events {
		if _, err := store.EnqueueEvent(event); err != nil {
			t.Fatal(err)
		}
	}

	lockDB, err := sql.Open("sqlite", statePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := lockDB.Close(); err != nil {
			t.Errorf("close lock database: %v", err)
		}
	})
	lockConn, err := lockDB.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := lockConn.Close(); err != nil {
			t.Errorf("close lock connection: %v", err)
		}
	})
	if _, err := lockConn.ExecContext(context.Background(), "BEGIN IMMEDIATE"); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	started := make(chan struct{}, len(events))
	results := make(chan error, len(events))
	for index, event := range events {
		go func(index int, event domain.NormalizedEvent) {
			started <- struct{}{}
			_, err := store.StartAgentRun(
				ctx,
				event,
				fmt.Sprintf("model-%d", index),
				"config",
			)
			results <- err
		}(index, event)
	}
	for range events {
		<-started
	}
	time.Sleep(250 * time.Millisecond)
	if _, err := lockConn.ExecContext(context.Background(), "COMMIT"); err != nil {
		t.Fatal(err)
	}

	for range events {
		if err := <-results; err != nil {
			t.Errorf("start concurrent foreground run: %v", err)
		}
	}
}
