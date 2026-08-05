package context

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

type PromptSectionKind string

const (
	PromptSectionStableCore    PromptSectionKind = "stable_core"
	PromptSectionTaskFlow      PromptSectionKind = "task_flow"
	PromptSectionUntrusted     PromptSectionKind = "untrusted_data"
	PromptSectionCurrentInput  PromptSectionKind = "current_input"
	PromptSectionDynamicStatus PromptSectionKind = "dynamic_status"
)

type PromptSection struct {
	Kind       PromptSectionKind
	Content    string
	TrustLevel string
	Stable     bool
}

func AgentPromptSections(bundle Bundle) []PromptSection {
	return []PromptSection{
		{
			Kind:       PromptSectionStableCore,
			Content:    AgentSystemPrompt(),
			TrustLevel: "system",
			Stable:     true,
		},
		{
			Kind:       PromptSectionTaskFlow,
			Content:    AgentTaskProcessPrompt(bundle),
			TrustLevel: "runtime",
			Stable:     true,
		},
		{
			Kind:       PromptSectionCurrentInput,
			Content:    AgentUserPrompt(bundle),
			TrustLevel: "untrusted_current_input",
			Stable:     false,
		},
	}
}

func StablePromptHash(sections []PromptSection) string {
	hash := sha256.New()
	for _, section := range sections {
		if !section.Stable {
			continue
		}
		hash.Write([]byte(section.Kind))
		hash.Write([]byte{0})
		hash.Write([]byte(strings.TrimSpace(section.Content)))
		hash.Write([]byte{0})
	}
	return "sha256:" + hex.EncodeToString(hash.Sum(nil))
}
