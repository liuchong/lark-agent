package tools

import (
	"context"
	"encoding/json"

	"github.com/cloudwego/eino/schema"

	"github.com/liuchong/lark-agent/agent/domain"
	"github.com/liuchong/lark-agent/internal/apperr"
	internalgithub "github.com/liuchong/lark-agent/internal/github"
)

type GitHubFileReader interface {
	GetFile(context.Context, internalgithub.Reference, string) (internalgithub.FileContent, error)
}

type GitHubComparer interface {
	Compare(context.Context, internalgithub.Reference) (internalgithub.CompareResult, error)
}

func GitHubFileDefinition(reader GitHubFileReader) Definition {
	return Definition{
		Info: toolInfo(
			"get_github_file",
			"Read one repository file at the verified event SHA. Path is relative to the repository root.",
			map[string]*schema.ParameterInfo{
				"path": {Type: schema.String, Required: true},
			},
		),
		Permission:              ToolPermissionAllow,
		Risk:                    ToolRiskLow,
		NonOwnerReadOnly:        true,
		RequiresGitHubReference: true,
		Execute: func(ctx context.Context, raw json.RawMessage) (Execution, error) {
			var args struct {
				Path string `json:"path"`
			}
			if err := decodeArgs(raw, &args); err != nil {
				return Execution{}, err
			}
			scope, ok := invocationScope(ctx)
			if !ok || scope.GitHubReference == nil {
				return Execution{}, errs.NewValidationError(
					errs.SubtypeFailedPrecondition,
					"get_github_file requires a verified GitHub reference",
				)
			}
			if scope.GitHubReference.HeadSHA == "" {
				return Execution{}, errs.NewValidationError(
					errs.SubtypeFailedPrecondition,
					"get_github_file requires head_sha",
				)
			}
			result, err := reader.GetFile(ctx, *scope.GitHubReference, args.Path)
			if err != nil {
				return Execution{}, err
			}
			data, err := json.Marshal(result)
			if err != nil {
				return Execution{}, err
			}
			return Execution{
				Content: string(data),
				Sources: []domain.SourceRef{githubSource(*scope.GitHubReference, data)},
			}, nil
		},
	}
}

func GitHubCompareDefinition(comparer GitHubComparer) Definition {
	return Definition{
		Info: toolInfo(
			"get_github_compare",
			"Compare the verified before and head SHAs, or the previous release tag for a release event.",
			map[string]*schema.ParameterInfo{},
		),
		Permission:              ToolPermissionAllow,
		Risk:                    ToolRiskLow,
		NonOwnerReadOnly:        true,
		RequiresGitHubReference: true,
		Execute: func(ctx context.Context, raw json.RawMessage) (Execution, error) {
			if err := decodeArgs(raw, &struct{}{}); err != nil {
				return Execution{}, err
			}
			scope, ok := invocationScope(ctx)
			if !ok || scope.GitHubReference == nil {
				return Execution{}, errs.NewValidationError(
					errs.SubtypeFailedPrecondition,
					"get_github_compare requires a verified GitHub reference",
				)
			}
			result, err := comparer.Compare(ctx, *scope.GitHubReference)
			if err != nil {
				return Execution{}, err
			}
			return jsonExecution(result, []domain.SourceRef{githubSource(*scope.GitHubReference, nil)}, nil)
		},
	}
}
