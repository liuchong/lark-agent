package feedback

import (
	"context"
	"testing"
	"time"

	"github.com/liuchong/lark-agent/agent/domain"
)

type fakeReactionMessenger struct {
	createdMessage string
	createdEmoji   string
	deletedMessage string
	deletedID      string
}

type fakeActivityStore struct {
	completedID int64
}

func (*fakeActivityStore) BeginOwnerActivity(context.Context, string, string) (int64, string, bool, error) {
	return 0, "", false, nil
}

func (*fakeActivityStore) RecordOwnerActivityReaction(context.Context, int64, string) error {
	return nil
}

func (f *fakeActivityStore) CompleteOwnerActivity(_ context.Context, actionID int64, _, _ string) error {
	f.completedID = actionID
	return nil
}

func (*fakeActivityStore) ClaimOwnerActivityCleanup(
	context.Context, time.Time,
) (int64, string, string, bool, error) {
	return 42, "om_stale", "reaction_stale", true, nil
}

func (f *fakeReactionMessenger) CreateReactionAsBot(_ context.Context, messageID, emojiType string) (string, error) {
	f.createdMessage = messageID
	f.createdEmoji = emojiType
	return "reaction_typing", nil
}

func (f *fakeReactionMessenger) DeleteReactionAsBot(_ context.Context, messageID, reactionID string) error {
	f.deletedMessage = messageID
	f.deletedID = reactionID
	return nil
}

func TestControllerAddsAndRemovesKeyboardWorkingReaction(t *testing.T) {
	messenger := &fakeReactionMessenger{}
	controller := NewController(messenger)
	item := domain.NewWorkItem(domain.NormalizedEvent{MessageID: "om_owner_request"})
	reactionID, err := controller.Begin(context.Background(), item)
	if err != nil {
		t.Fatal(err)
	}
	if err := controller.End(context.Background(), item, reactionID); err != nil {
		t.Fatal(err)
	}
	if messenger.createdMessage != "om_owner_request" ||
		messenger.createdEmoji != "Typing" ||
		messenger.deletedMessage != "om_owner_request" ||
		messenger.deletedID != "reaction_typing" {
		t.Fatalf("messenger=%+v", messenger)
	}
}

func TestControllerRecoversBlockedCleanupWithoutStartingWork(t *testing.T) {
	messenger := &fakeReactionMessenger{}
	store := &fakeActivityStore{}
	controller := NewController(messenger, store)
	if err := controller.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	if messenger.deletedMessage != "om_stale" ||
		messenger.deletedID != "reaction_stale" ||
		store.completedID != 42 ||
		messenger.createdMessage != "" {
		t.Fatalf("messenger=%+v store=%+v", messenger, store)
	}
}
