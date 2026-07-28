// Package github provides the typed GitHub event and REST boundary.
package github

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/liuchong/lark-agent/agent/domain"
)

const ReferenceMarkerPrefix = "[lark-agent-github-ref:v1:"

type Reference = domain.GitHubReference
type ReferenceKind = domain.GitHubReferenceKind

const (
	ReferenceWorkflowRun = domain.GitHubReferenceWorkflowRun
	ReferencePullRequest = domain.GitHubReferencePullRequest
)

// EncodeReferenceMarker serializes one validated reference into a compact
// line-safe marker.
func EncodeReferenceMarker(ref Reference, signingKey string) (string, error) {
	if err := ref.Validate(); err != nil {
		return "", err
	}
	if strings.TrimSpace(signingKey) == "" {
		return "", fmt.Errorf("github reference signing key is required")
	}
	data, err := json.Marshal(ref)
	if err != nil {
		return "", fmt.Errorf("encode github reference: %w", err)
	}
	signature := referenceSignature(data, signingKey)
	return ReferenceMarkerPrefix +
		base64.RawURLEncoding.EncodeToString(data) + "." +
		base64.RawURLEncoding.EncodeToString(signature) + "]", nil
}

// ParseReferenceMarker parses at most one marker from untrusted message text.
func ParseReferenceMarker(content, signingKey string) (Reference, bool, error) {
	if strings.TrimSpace(signingKey) == "" {
		return Reference{}, false, fmt.Errorf("github reference signing key is required")
	}
	start := strings.Index(content, ReferenceMarkerPrefix)
	if start < 0 {
		return Reference{}, false, nil
	}
	encodedStart := start + len(ReferenceMarkerPrefix)
	endOffset := strings.IndexByte(content[encodedStart:], ']')
	if endOffset < 0 {
		return Reference{}, false, fmt.Errorf("github reference marker is not terminated")
	}
	end := encodedStart + endOffset
	if strings.Contains(content[end+1:], ReferenceMarkerPrefix) {
		return Reference{}, false, fmt.Errorf("multiple github reference markers are not allowed")
	}
	encodedPayload, encodedSignature, found := strings.Cut(content[encodedStart:end], ".")
	if !found || strings.Contains(encodedSignature, ".") {
		return Reference{}, false, fmt.Errorf("github reference marker signature is missing")
	}
	data, err := base64.RawURLEncoding.DecodeString(encodedPayload)
	if err != nil {
		return Reference{}, false, fmt.Errorf("decode github reference marker: %w", err)
	}
	signature, err := base64.RawURLEncoding.DecodeString(encodedSignature)
	if err != nil {
		return Reference{}, false, fmt.Errorf("decode github reference signature: %w", err)
	}
	if !hmac.Equal(signature, referenceSignature(data, signingKey)) {
		return Reference{}, false, fmt.Errorf("github reference marker signature is invalid")
	}
	var ref Reference
	if err := json.Unmarshal(data, &ref); err != nil {
		return Reference{}, false, fmt.Errorf("parse github reference marker: %w", err)
	}
	if err := ref.Validate(); err != nil {
		return Reference{}, false, err
	}
	return ref, true, nil
}

func referenceSignature(data []byte, signingKey string) []byte {
	mac := hmac.New(sha256.New, []byte(signingKey))
	_, _ = mac.Write(data)
	return mac.Sum(nil)
}

// StableNotificationKey returns a Lark-compatible idempotency UUID.
func StableNotificationKey(chatID string, ref Reference) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(chatID) + "\x00" + ref.ExternalKey() + "\x00v1"))
	return fmt.Sprintf("ghn-%x", sum[:16])
}
