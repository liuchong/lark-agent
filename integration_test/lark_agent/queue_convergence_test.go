package larkagent_test

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/liuchong/lark-agent/agent/app"
	agentcontext "github.com/liuchong/lark-agent/agent/context"
	"github.com/liuchong/lark-agent/agent/domain"
	"github.com/liuchong/lark-agent/agent/replymatch"
	"github.com/liuchong/lark-agent/agent/router"
	"github.com/liuchong/lark-agent/agent/storage"
	errs "github.com/liuchong/lark-agent/internal/apperr"
)

type convergenceContextBuilder struct{}

func (convergenceContextBuilder) Build(item domain.WorkItem) (agentcontext.Bundle, error) {
	return agentcontext.Bundle{
		Event:    item.Event,
		WorkKind: item.WorkKind,
		User:     agentcontext.UserProfile{OpenID: "ou_owner"},
	}, nil
}

type nonConvergentDecider struct{}

func (nonConvergentDecider) Decide(context.Context, agentcontext.Bundle) (domain.Decision, error) {
	return domain.Decision{}, errs.NewInternalError(
		errs.SubtypeModelNonConvergence,
		"model did not submit a terminal decision after 3 attempts",
	)
}

type terminalFailureCapture struct {
	calls int
	item  domain.WorkItem
	err   error
}

type alwaysAmbiguousResolver struct{}

func (alwaysAmbiguousResolver) Resolve(
	context.Context,
	domain.WorkItem,
) (replymatch.Resolution, error) {
	return replymatch.Resolution{
		Result:     replymatch.ResultAmbiguous,
		Confidence: 0,
		Reason:     "semantic context remained incomplete",
	}, nil
}

func (c *terminalFailureCapture) HandleTerminalFailure(
	_ context.Context,
	item domain.WorkItem,
	err error,
) error {
	c.calls++
	c.item = item
	c.err = err
	return nil
}

func TestQueueCancellationAndCrossSessionApprovalRoundTrip(t *testing.T) {
	bin := buildAgentBinary(t)
	statePath := filepath.Join(t.TempDir(), "state.db")
	first, err := storage.Open(statePath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := first.MarkCurrentSessionReady(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, messageID := range []string{
		"om_integration_cancel",
		"om_integration_keep",
		"om_integration_approval",
	} {
		if _, err := first.EnqueueEvent(domain.NormalizedEvent{
			MessageID: messageID,
			Content:   messageID,
		}); err != nil {
			t.Fatal(err)
		}
	}
	items, err := first.ListWorkItems()
	if err != nil {
		t.Fatal(err)
	}
	ids := make(map[string]int64, len(items))
	for _, item := range items {
		ids[item.Event.MessageID] = item.ID
	}
	approvalID, err := first.RequestShellApproval(
		context.Background(),
		domain.DedupKey(domain.NormalizedEvent{
			MessageID: "om_integration_approval",
			Content:   "om_integration_approval",
		}),
		"gofmt -w .",
		".",
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}

	current, err := storage.Open(statePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = current.Close() })
	ready, err := current.MarkCurrentSessionReady(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	code, stdout, stderr := runAgent(
		t,
		bin,
		"--state", statePath,
		"queue", "cancel",
		"--all-interrupted",
		"--keep-work-id", fmt.Sprint(ids["om_integration_keep"]),
		"--keep-work-id", fmt.Sprint(ids["om_integration_approval"]),
		"--reason", "integration audit",
	)
	if code != 0 || !strings.Contains(stdout, `"changed":1`) {
		t.Fatalf("cancel exit=%d stdout=%s stderr=%s", code, stdout, stderr)
	}

	code, stdout, stderr = runAgent(
		t,
		bin,
		"--state", statePath,
		"approval", "approve", fmt.Sprint(approvalID),
	)
	if code != 0 || !strings.Contains(stdout, `"action":"approve"`) {
		t.Fatalf("approve exit=%d stdout=%s stderr=%s", code, stdout, stderr)
	}
	items, err = current.ListWorkItems()
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range items {
		switch item.Event.MessageID {
		case "om_integration_cancel":
			if item.Status != domain.StatusCancelled {
				t.Fatalf("cancelled item=%+v", item)
			}
		case "om_integration_keep":
			if item.Status != domain.StatusInterrupted {
				t.Fatalf("kept item=%+v", item)
			}
		case "om_integration_approval":
			if item.Status != domain.StatusReceived || item.SessionID != ready.ID {
				t.Fatalf("approved item=%+v ready=%+v", item, ready)
			}
		}
	}
}

func TestModelNonConvergenceMovesLeasedWorkDirectlyToDeadLetter(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "state.db")
	store, err := storage.Open(statePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.EnqueueEvent(domain.NormalizedEvent{
		MessageID: "om_integration_non_convergence",
		SenderID:  "ou_teammate",
		Content:   "@Owner 看看删号报 10005",
		Mentions:  []domain.Mention{{OpenID: "ou_owner"}},
	}); err != nil {
		t.Fatal(err)
	}
	terminal := &terminalFailureCapture{}
	daemon := app.NewDaemon(
		store,
		router.New(router.Config{OwnerOpenID: "ou_owner", Mode: domain.ModeAuto}),
		app.WithContextBuilder(convergenceContextBuilder{}),
		app.WithDecider(nonConvergentDecider{}),
		app.WithTerminalFailureHandler(terminal),
	)
	if _, err := daemon.RunOnce(context.Background()); err == nil {
		t.Fatal("non-convergent model run unexpectedly succeeded")
	}
	items, err := store.ListWorkItems()
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Status != domain.StatusDeadLetter ||
		items[0].RetryCount != 1 {
		t.Fatalf("items=%+v", items)
	}
	if terminal.calls != 1 || terminal.item.ID != items[0].ID ||
		terminal.err == nil {
		t.Fatalf("terminal failure capture=%+v", terminal)
	}
}

func TestAmbiguousDelegatedContextConvergesAtRetryCeiling(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "state.db")
	store, err := storage.Open(statePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	store.ConfigureRecovery(1)
	item := enqueueDueDelegatedItem(t, store, domain.NormalizedEvent{
		Source:    domain.SourcePoll,
		EventID:   "poll:om_ambiguous_ceiling",
		MessageID: "om_ambiguous_ceiling",
		ChatID:    "oc_group",
		ChatType:  "group",
		SenderID:  "ou_teammate",
		Content:   "@测试负责人 请结合前文处理",
		Mentions:  []domain.Mention{{OpenID: "ou_owner"}},
		CreatedAt: store.CurrentSession().StartedAt.Add(time.Second),
	})
	terminal := &terminalFailureCapture{}
	daemon := app.NewDaemon(
		store,
		router.New(router.Config{OwnerOpenID: "ou_owner", Mode: domain.ModeAuto}),
		app.WithDelegatedReplyResolver(alwaysAmbiguousResolver{}, 0.85, time.Minute),
		app.WithTerminalFailureHandler(terminal),
	)

	result, err := daemon.RunOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	current, err := store.GetWorkItem(context.Background(), item.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Processed ||
		current.Status != domain.StatusDeadLetter ||
		!current.NextAttemptAt.IsZero() {
		t.Fatalf("result=%+v current=%+v", result, current)
	}
	if terminal.calls != 1 || terminal.item.ID != item.ID || terminal.err == nil {
		t.Fatalf("terminal failure capture=%+v", terminal)
	}
	required, err := store.ListRequiredOwnerResolutionNotifications(
		context.Background(),
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(required) != 1 ||
		required[0].ID == 0 ||
		required[0].WorkItemID != item.ID ||
		required[0].Reason != "owner_reply_ambiguous" {
		t.Fatalf("required owner resolutions=%+v", required)
	}
}

func TestTerminalOwnerResolutionGenerationsSurviveResumeAndRepeatFailure(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "state.db")
	store, err := storage.Open(statePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	store.ConfigureRecovery(1)
	item := enqueueDueDelegatedItem(t, store, domain.NormalizedEvent{
		Source:    domain.SourcePoll,
		EventID:   "poll:om_terminal_generations",
		MessageID: "om_terminal_generations",
		ChatID:    "oc_group",
		ChatType:  "group",
		SenderID:  "ou_teammate",
		Content:   "@测试负责人 请结合前文处理",
		Mentions:  []domain.Mention{{OpenID: "ou_owner"}},
		CreatedAt: store.CurrentSession().StartedAt.Add(time.Second),
	})
	terminalize := func(worker, reason string) {
		t.Helper()
		claimed, ok, err := store.ClaimNext(worker)
		if err != nil || !ok || claimed.ID != item.ID {
			t.Fatalf("claimed=%+v ok=%v err=%v", claimed, ok, err)
		}
		if err := store.DeferWaitingUserClaim(
			claimed.ID,
			claimed.LeaseBy,
			reason,
			time.Minute,
		); err != nil {
			t.Fatal(err)
		}
	}

	terminalize("first-generation", "first terminal generation")
	first, err := store.ListRequiredOwnerResolutionNotifications(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 1 || first[0].ID == 0 {
		t.Fatalf("first=%+v", first)
	}
	firstActionID, firstKey, send, err := store.BeginOwnerResolutionNotificationForRequirement(
		context.Background(),
		first[0].ID,
		item.ID,
		"first terminal summary",
	)
	if err != nil || !send {
		t.Fatalf("first action=%d key=%q send=%v err=%v", firstActionID, firstKey, send, err)
	}
	if len(firstKey) > 50 {
		t.Fatalf("first public key is too long: %d %q", len(firstKey), firstKey)
	}
	if err := store.CompleteOwnerResolutionNotification(context.Background(), firstActionID, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ResumeWork(context.Background(), domain.ResumeWorkRequest{
		WorkItemID:    item.ID,
		ForceTerminal: true,
	}); err != nil {
		t.Fatal(err)
	}

	terminalize("second-generation", "second terminal generation")
	if err := store.ConvergeOwnerResolutionRequirements(context.Background()); err != nil {
		t.Fatal(err)
	}
	second, err := store.ListRequiredOwnerResolutionNotifications(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(second) != 1 || second[0].ID == first[0].ID {
		t.Fatalf("first=%+v second=%+v", first, second)
	}
	secondActionID, secondKey, send, err := store.BeginOwnerResolutionNotificationForRequirement(
		context.Background(),
		second[0].ID,
		item.ID,
		"second terminal summary",
	)
	if err != nil || !send {
		t.Fatalf("second action=%d key=%q send=%v err=%v", secondActionID, secondKey, send, err)
	}
	if len(secondKey) > 50 {
		t.Fatalf("second public key is too long: %d %q", len(secondKey), secondKey)
	}
	if secondActionID == firstActionID || secondKey == firstKey {
		t.Fatalf("reused generation identity: first=(%d,%q) second=(%d,%q)",
			firstActionID, firstKey, secondActionID, secondKey)
	}
}
