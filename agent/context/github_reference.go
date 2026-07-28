package context

import (
	"fmt"
	"strings"
	"time"

	"github.com/liuchong/lark-agent/agent/domain"
	internalgithub "github.com/liuchong/lark-agent/internal/github"
)

// ResolveGitHubReference verifies control data only from explicit same-chat
// relations authored by the configured current Lark application.
func ResolveGitHubReference(
	target domain.NormalizedEvent,
	conversation []domain.NormalizedEvent,
	currentAppID string,
	allowedRepositories []string,
	signingKey string,
) (domain.ExternalReference, bool, error) {
	if strings.TrimSpace(target.ChatID) == "" || strings.TrimSpace(currentAppID) == "" {
		return domain.ExternalReference{}, false, nil
	}
	allowed := make(map[string]bool, len(allowedRepositories))
	for _, repository := range allowedRepositories {
		allowed[strings.ToLower(strings.TrimSpace(repository))] = true
	}
	relationIDs := []string{target.ReplyToMessageID, target.RootMessageID}
	var selected *domain.ExternalReference
	for _, relationID := range relationIDs {
		if relationID == "" {
			continue
		}
		message, ok := messageByID(conversation, relationID)
		if !ok || message.ChatID != target.ChatID ||
			message.SenderType != "app" || message.SenderID != currentAppID {
			continue
		}
		ref, found, err := internalgithub.ParseReferenceMarker(message.Content, signingKey)
		if err != nil {
			continue
		}
		if !found {
			continue
		}
		if !allowed[strings.ToLower(ref.Repository)] {
			return domain.ExternalReference{}, false, fmt.Errorf("github repository %q is not allowed", ref.Repository)
		}
		candidate := domain.ExternalReference{
			Provider:      "github",
			Kind:          string(ref.Kind),
			ExternalKey:   ref.ExternalKey(),
			LarkMessageID: message.MessageID,
			ChatID:        message.ChatID,
			SenderAppID:   message.SenderID,
			Reference:     ref,
			VerifiedAt:    time.Now().UTC(),
		}
		if selected != nil && selected.Reference != candidate.Reference {
			return domain.ExternalReference{}, false, fmt.Errorf("conflicting trusted github references in reply chain")
		}
		selected = &candidate
	}
	if selected == nil {
		return domain.ExternalReference{}, false, nil
	}
	return *selected, true, nil
}

func messageByID(messages []domain.NormalizedEvent, messageID string) (domain.NormalizedEvent, bool) {
	for _, message := range messages {
		if message.MessageID == messageID {
			return message, true
		}
	}
	return domain.NormalizedEvent{}, false
}
