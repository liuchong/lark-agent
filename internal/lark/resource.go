package lark

import (
	"net/url"
	"strings"

	errs "github.com/liuchong/lark-agent/internal/apperr"
)

type ResourceType string

const (
	ResourceTypeWiki     ResourceType = "wiki"
	ResourceTypeBase     ResourceType = "base"
	ResourceTypeDocument ResourceType = "document"
)

type ResourceRef struct {
	OriginalURL   string       `json:"url"`
	ResourceType  ResourceType `json:"resource_type"`
	FileToken     string       `json:"file_token,omitempty"`
	WikiNodeToken string       `json:"wiki_node_token,omitempty"`
	AppToken      string       `json:"app_token,omitempty"`
	TableID       string       `json:"table_id,omitempty"`
	ViewID        string       `json:"view_id,omitempty"`
}

func ParseResourceURL(raw string) (ResourceRef, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return ResourceRef{}, errs.NewValidationError(errs.SubtypeInvalidArgument, "parse resource URL").WithCause(err)
	}
	parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	if len(parts) < 2 {
		return ResourceRef{}, errs.NewValidationError(errs.SubtypeInvalidArgument, "unsupported resource URL").WithParam("url")
	}
	ref := ResourceRef{
		OriginalURL: raw,
		TableID:     parsed.Query().Get("table"),
		ViewID:      parsed.Query().Get("view"),
	}
	switch parts[0] {
	case "wiki":
		ref.ResourceType = ResourceTypeWiki
		ref.WikiNodeToken = parts[1]
	case "base":
		ref.ResourceType = ResourceTypeBase
		ref.AppToken = parts[1]
	case "doc", "docs", "docx", "sheet", "sheets":
		ref.ResourceType = ResourceTypeDocument
		ref.FileToken = parts[1]
	default:
		return ResourceRef{}, errs.NewValidationError(errs.SubtypeInvalidArgument, "unsupported resource URL path %q", parts[0]).
			WithParam("url")
	}
	return ref, nil
}
