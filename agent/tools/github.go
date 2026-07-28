package tools

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/cloudwego/eino/schema"

	"github.com/liuchong/lark-agent/agent/domain"
	"github.com/liuchong/lark-agent/internal/apperr"
	internalgithub "github.com/liuchong/lark-agent/internal/github"
)

type GitHubContextFetcher interface {
	FetchContext(
		context.Context,
		internalgithub.Reference,
		[]internalgithub.Section,
	) (internalgithub.ContextResult, error)
}

// GitHubContextDefinition exposes only section selection. Repository and
// object identifiers always come from the verified invocation scope.
func GitHubContextDefinition(fetcher GitHubContextFetcher) Definition {
	return Definition{
		Info: toolInfo(
			"get_github_context",
			"Read fresh bounded GitHub facts for the verified workflow or pull request quoted by this Lark message.",
			map[string]*schema.ParameterInfo{
				"sections": {
					Type: schema.Array,
					ElemInfo: &schema.ParameterInfo{
						Type: schema.String,
						Enum: []string{"summary", "checks", "files", "reviews"},
					},
				},
			},
		),
		Permission:              ToolPermissionAllow,
		Risk:                    ToolRiskLow,
		NonOwnerReadOnly:        true,
		RequiresGitHubReference: true,
		Execute: func(ctx context.Context, raw json.RawMessage) (Execution, error) {
			var args struct {
				Sections []internalgithub.Section `json:"sections"`
			}
			if err := decodeArgs(raw, &args); err != nil {
				return Execution{}, err
			}
			scope, ok := invocationScope(ctx)
			if !ok || scope.GitHubReference == nil {
				return Execution{}, errs.NewValidationError(
					errs.SubtypeFailedPrecondition,
					"get_github_context requires a verified GitHub reference",
				)
			}
			result, err := fetcher.FetchContext(ctx, *scope.GitHubReference, args.Sections)
			if err != nil {
				return Execution{}, err
			}
			data, err := json.Marshal(result)
			if err != nil {
				return Execution{}, err
			}
			source := githubSource(result.Reference, data)
			return Execution{
				Content: string(data),
				Sources: []domain.SourceRef{source},
			}, nil
		},
	}
}

func githubSource(ref domain.GitHubReference, data []byte) domain.SourceRef {
	sum := sha256Sum(data)
	path := "github://" + ref.Repository
	switch ref.Kind {
	case domain.GitHubReferenceWorkflowRun:
		path += "/actions/runs/" + strconv.FormatInt(ref.WorkflowRunID, 10)
	case domain.GitHubReferencePullRequest:
		path += "/pull/" + strconv.Itoa(ref.PullRequestNumber)
	}
	return domain.SourceRef{
		RelativePath: path,
		Digest:       sum,
		Kind:         "github",
	}
}

func sha256Sum(data []byte) string {
	sum := sha256.Sum256(data)
	return fmt.Sprintf("sha256:%x", sum[:])
}
