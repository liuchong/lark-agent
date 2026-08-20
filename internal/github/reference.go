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

const ReferenceMarkerPrefix = "[lark-agent-github-ref:v2:"
const legacyReferenceMarkerPrefix = "[lark-agent-github-ref:v1:"
const referenceMarkerSignatureBytes = 16

type Reference = domain.GitHubReference
type ReferenceKind = domain.GitHubReferenceKind

const (
	ReferenceWorkflowRun      = domain.GitHubReferenceWorkflowRun
	ReferencePullRequest      = domain.GitHubReferencePullRequest
	ReferenceIssue            = domain.GitHubReferenceIssue
	ReferencePush             = domain.GitHubReferencePush
	ReferenceRelease          = domain.GitHubReferenceRelease
	ReferenceWorkflowDispatch = domain.GitHubReferenceWorkflowDispatch
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
	data, err := json.Marshal(compactReferenceFrom(ref))
	if err != nil {
		return "", fmt.Errorf("encode github reference: %w", err)
	}
	signature := referenceSignature(data, signingKey)[:referenceMarkerSignatureBytes]
	return ReferenceMarkerPrefix +
		base64.RawURLEncoding.EncodeToString(data) + "." +
		base64.RawURLEncoding.EncodeToString(signature) + "]", nil
}

// ParseReferenceMarker parses at most one marker from untrusted message text.
func ParseReferenceMarker(content, signingKey string) (Reference, bool, error) {
	if strings.TrimSpace(signingKey) == "" {
		return Reference{}, false, fmt.Errorf("github reference signing key is required")
	}
	prefix, start, compact, found := locateReferenceMarker(content)
	if !found {
		return Reference{}, false, nil
	}
	encodedStart := start + len(prefix)
	endOffset := strings.IndexByte(content[encodedStart:], ']')
	if endOffset < 0 {
		return Reference{}, false, fmt.Errorf("github reference marker is not terminated")
	}
	end := encodedStart + endOffset
	if _, _, _, found := locateReferenceMarker(content[end+1:]); found {
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
	expected := referenceSignature(data, signingKey)
	if compact {
		expected = expected[:referenceMarkerSignatureBytes]
	}
	if !hmac.Equal(signature, expected) {
		return Reference{}, false, fmt.Errorf("github reference marker signature is invalid")
	}
	var ref Reference
	if compact {
		var compactRef compactReference
		if err := json.Unmarshal(data, &compactRef); err != nil {
			return Reference{}, false, fmt.Errorf("parse github reference marker: %w", err)
		}
		ref = compactRef.Reference()
	} else {
		if err := json.Unmarshal(data, &ref); err != nil {
			return Reference{}, false, fmt.Errorf("parse github reference marker: %w", err)
		}
	}
	if err := ref.Validate(); err != nil {
		return Reference{}, false, err
	}
	return ref, true, nil
}

type compactReference struct {
	Repository         string        `json:"r"`
	Kind               ReferenceKind `json:"k"`
	WorkflowRunID      int64         `json:"w,omitempty"`
	WorkflowRunAttempt int           `json:"a,omitempty"`
	PullRequestNumber  int           `json:"p,omitempty"`
	IssueNumber        int           `json:"i,omitempty"`
	CommentID          int64         `json:"c,omitempty"`
	HeadSHA            string        `json:"h,omitempty"`
	Ref                string        `json:"f,omitempty"`
	TagName            string        `json:"t,omitempty"`
}

func compactReferenceFrom(ref Reference) compactReference {
	return compactReference{
		Repository:         ref.Repository,
		Kind:               ref.Kind,
		WorkflowRunID:      ref.WorkflowRunID,
		WorkflowRunAttempt: ref.WorkflowRunAttempt,
		PullRequestNumber:  ref.PullRequestNumber,
		IssueNumber:        ref.IssueNumber,
		CommentID:          ref.CommentID,
		HeadSHA:            ref.HeadSHA,
		Ref:                ref.Ref,
		TagName:            ref.TagName,
	}
}

func (r compactReference) Reference() Reference {
	return Reference{
		SchemaVersion:      1,
		Repository:         r.Repository,
		Kind:               r.Kind,
		WorkflowRunID:      r.WorkflowRunID,
		WorkflowRunAttempt: r.WorkflowRunAttempt,
		PullRequestNumber:  r.PullRequestNumber,
		IssueNumber:        r.IssueNumber,
		CommentID:          r.CommentID,
		HeadSHA:            r.HeadSHA,
		Ref:                r.Ref,
		TagName:            r.TagName,
	}
}

func locateReferenceMarker(content string) (string, int, bool, bool) {
	v2 := strings.Index(content, ReferenceMarkerPrefix)
	v1 := strings.Index(content, legacyReferenceMarkerPrefix)
	switch {
	case v2 < 0 && v1 < 0:
		return "", -1, false, false
	case v1 < 0 || (v2 >= 0 && v2 < v1):
		return ReferenceMarkerPrefix, v2, true, true
	default:
		return legacyReferenceMarkerPrefix, v1, false, true
	}
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

// StableSmartCommandKey returns a Lark-compatible idempotency UUID for one
// smart-command Lark send. It must not collide with notify keys.
func StableSmartCommandKey(chatID string, ref Reference) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(chatID) + "\x00" + ref.ExternalKey() + "\x00v1"))
	return fmt.Sprintf("ghs-%x", sum[:16])
}
