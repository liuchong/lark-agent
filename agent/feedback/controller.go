// Package feedback manages transient feedback for owner-initiated requests.
package feedback

import (
	"context"
	"encoding/json"
	"time"

	"github.com/liuchong/lark-agent/agent/domain"
	"github.com/liuchong/lark-agent/internal/apperr"
)

// Lark's API identifier for the visible keyboard message reaction.
const workingReactionEmoji = "Typing"
const cleanupRetryDelay = 5 * time.Second

// ReactionMessenger adds and removes reactions as the assistant bot.
type ReactionMessenger interface {
	CreateReactionAsBot(context.Context, string, string) (string, error)
	DeleteReactionAsBot(context.Context, string, string) error
}

type activityStore interface {
	BeginOwnerActivity(context.Context, string, string) (int64, string, bool, error)
	RecordOwnerActivityReaction(context.Context, int64, string) error
	CompleteOwnerActivity(context.Context, int64, string, string) error
	ClaimOwnerActivityCleanup(context.Context, time.Time) (int64, string, string, bool, error)
}

// Controller owns the lifecycle of the owner-request working reaction.
type Controller struct {
	messenger ReactionMessenger
	store     activityStore
}

// NewController creates an owner activity feedback controller.
func NewController(messenger ReactionMessenger, stores ...activityStore) *Controller {
	controller := &Controller{messenger: messenger}
	if len(stores) > 0 {
		controller.store = stores[0]
	}
	return controller
}

type activityToken struct {
	ActionID   int64  `json:"action_id,omitempty"`
	ReactionID string `json:"reaction_id"`
}

// Begin adds the keyboard working reaction before owner-request work starts.
func (c *Controller) Begin(ctx context.Context, item domain.WorkItem) (string, error) {
	if c.messenger == nil {
		return "", errs.NewInternalError(errs.SubtypeFailedPrecondition, "reaction messenger is not configured")
	}
	var actionID int64
	if c.store != nil {
		var reactionID string
		var completed bool
		var err error
		actionID, reactionID, completed, err = c.store.BeginOwnerActivity(
			ctx, item.DedupKey, item.Event.MessageID,
		)
		if err != nil {
			return "", err
		}
		if completed {
			return "", nil
		}
		if reactionID != "" {
			return encodeToken(activityToken{ActionID: actionID, ReactionID: reactionID})
		}
	}
	reactionID, err := c.messenger.CreateReactionAsBot(ctx, item.Event.MessageID, workingReactionEmoji)
	if err != nil {
		if c.store != nil && actionID != 0 {
			_ = c.store.CompleteOwnerActivity(context.WithoutCancel(ctx), actionID, "", err.Error())
		}
		return "", err
	}
	if c.store != nil && actionID != 0 {
		if err := c.store.RecordOwnerActivityReaction(ctx, actionID, reactionID); err != nil {
			_ = c.messenger.DeleteReactionAsBot(context.WithoutCancel(ctx), item.Event.MessageID, reactionID)
			_ = c.store.CompleteOwnerActivity(context.WithoutCancel(ctx), actionID, reactionID, err.Error())
			return "", err
		}
	}
	return encodeToken(activityToken{ActionID: actionID, ReactionID: reactionID})
}

// End removes the working reaction without rerunning the completed work.
func (c *Controller) End(ctx context.Context, item domain.WorkItem, token string) error {
	if c.messenger == nil {
		return errs.NewInternalError(errs.SubtypeFailedPrecondition, "reaction messenger is not configured")
	}
	if token == "" {
		return nil
	}
	activity, err := decodeToken(token)
	if err != nil {
		return err
	}
	lastErr := c.deleteReaction(ctx, item.Event.MessageID, activity.ReactionID)
	if lastErr == nil {
		if c.store != nil && activity.ActionID != 0 {
			return c.store.CompleteOwnerActivity(ctx, activity.ActionID, activity.ReactionID, "")
		}
		return nil
	}
	if c.store != nil && activity.ActionID != 0 {
		_ = c.store.CompleteOwnerActivity(
			context.WithoutCancel(ctx), activity.ActionID, activity.ReactionID, lastErr.Error(),
		)
	}
	return lastErr
}

// Recover retries one previously failed reaction cleanup without rerunning the
// corresponding model or reply.
func (c *Controller) Recover(ctx context.Context) error {
	if c.store == nil {
		return nil
	}
	actionID, messageID, reactionID, found, err := c.store.ClaimOwnerActivityCleanup(
		ctx, time.Now().Add(-cleanupRetryDelay),
	)
	if err != nil || !found {
		return err
	}
	if err := c.deleteReaction(ctx, messageID, reactionID); err != nil {
		_ = c.store.CompleteOwnerActivity(
			context.WithoutCancel(ctx), actionID, reactionID, err.Error(),
		)
		return err
	}
	return c.store.CompleteOwnerActivity(ctx, actionID, reactionID, "")
}

func (c *Controller) deleteReaction(ctx context.Context, messageID, reactionID string) error {
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if err := c.messenger.DeleteReactionAsBot(ctx, messageID, reactionID); err != nil {
			lastErr = err
			if attempt < 2 {
				timer := time.NewTimer(time.Duration(attempt+1) * 100 * time.Millisecond)
				select {
				case <-ctx.Done():
					timer.Stop()
					return ctx.Err()
				case <-timer.C:
				}
			}
			continue
		}
		return nil
	}
	return lastErr
}

func encodeToken(token activityToken) (string, error) {
	encoded, err := json.Marshal(token)
	if err != nil {
		return "", errs.NewInternalError(errs.SubtypeUnknown, "encode owner activity token").WithCause(err)
	}
	return string(encoded), nil
}

func decodeToken(encoded string) (activityToken, error) {
	var token activityToken
	if err := json.Unmarshal([]byte(encoded), &token); err != nil {
		return activityToken{}, errs.NewInternalError(errs.SubtypeInvalidResponse, "decode owner activity token").WithCause(err)
	}
	if token.ReactionID == "" {
		return activityToken{}, errs.NewInternalError(errs.SubtypeInvalidResponse, "owner activity token is missing reaction_id")
	}
	return token, nil
}
