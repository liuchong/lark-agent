package tools

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/cloudwego/eino/schema"

	"github.com/liuchong/lark-agent/agent/domain"
	"github.com/liuchong/lark-agent/agent/workspace"
	"github.com/liuchong/lark-agent/internal/apperr"
)

// CodeIndexProvider adapts an optional local code graph or memory index.
type CodeIndexProvider interface {
	SearchCodeSymbols(ctx context.Context, query string, maxResults int) (CodeIndexSearchResult, error)
	TraceCodePath(ctx context.Context, symbol string, maxDepth int) (CodeIndexTraceResult, error)
}

// CodeIndexSearchResult is the model-visible code index search response.
type CodeIndexSearchResult struct {
	IndexAvailable bool                     `json:"index_available"`
	Provider       string                   `json:"provider,omitempty"`
	Query          string                   `json:"query"`
	Results        []workspace.SearchResult `json:"results,omitempty"`
	Fallback       *workspace.SearchReport  `json:"fallback,omitempty"`
	Reason         string                   `json:"reason,omitempty"`
	SuggestedTools []string                 `json:"suggested_tools,omitempty"`
}

// CodeIndexTraceResult is the model-visible symbol/path tracing response.
type CodeIndexTraceResult struct {
	IndexAvailable bool               `json:"index_available"`
	Provider       string             `json:"provider,omitempty"`
	Symbol         string             `json:"symbol"`
	Paths          []CodePath         `json:"paths,omitempty"`
	Reason         string             `json:"reason,omitempty"`
	SuggestedTools []string           `json:"suggested_tools,omitempty"`
	Sources        []domain.SourceRef `json:"sources,omitempty"`
}

// CodePath records a compact traced relationship from the optional index.
type CodePath struct {
	From    string           `json:"from"`
	To      string           `json:"to"`
	Kind    string           `json:"kind,omitempty"`
	Source  domain.SourceRef `json:"source,omitempty"`
	Summary string           `json:"summary,omitempty"`
}

// CodeIndexDefinitions exposes optional read-only code index tools. If no
// provider is configured, search falls back to bounded workspace search and
// trace returns an actionable unavailable response.
func CodeIndexDefinitions(scope *workspace.Scope, provider CodeIndexProvider) []Definition {
	return []Definition{
		searchCodeSymbolsDefinition(scope, provider),
		traceCodePathDefinition(provider),
	}
}

func searchCodeSymbolsDefinition(scope *workspace.Scope, provider CodeIndexProvider) Definition {
	return Definition{
		Info: toolInfo("search_code_symbols", "Search code symbols and paths before broad text search. If no code index is configured, returns a bounded workspace-search fallback with index_available=false.", map[string]*schema.ParameterInfo{
			"query":       {Type: schema.String, Required: true},
			"max_results": {Type: schema.Integer},
		}),
		Execute: func(ctx context.Context, raw json.RawMessage) (Execution, error) {
			var args struct {
				Query      string `json:"query"`
				MaxResults int    `json:"max_results"`
			}
			if err := decodeArgs(raw, &args); err != nil {
				return Execution{}, err
			}
			args.Query = strings.TrimSpace(args.Query)
			if args.Query == "" {
				return Execution{}, errs.NewValidationError(errs.SubtypeInvalidArgument, "search_code_symbols query is required").WithParam("query")
			}
			if args.MaxResults <= 0 || args.MaxResults > 40 {
				args.MaxResults = 20
			}
			if provider != nil {
				result, err := provider.SearchCodeSymbols(ctx, args.Query, args.MaxResults)
				if err != nil {
					return Execution{}, err
				}
				return jsonExecution(result, codeIndexSearchSources(result), nil)
			}
			report, err := scope.SearchTextReportContext(ctx, workspace.SearchOptions{
				Query:          args.Query,
				MaxResults:     args.MaxResults,
				MaxFiles:       2000,
				MaxDirectories: 600,
			})
			if err != nil {
				return Execution{}, err
			}
			result := CodeIndexSearchResult{
				IndexAvailable: false,
				Query:          args.Query,
				Fallback:       &report,
				Reason:         "code index provider is not configured",
				SuggestedTools: []string{"read_workspace", "search_workspace"},
			}
			return jsonExecution(result, searchReportSources(report), nil)
		},
	}
}

func traceCodePathDefinition(provider CodeIndexProvider) Definition {
	return Definition{
		Info: toolInfo("trace_code_path", "Trace calls or symbol relationships in the optional code index. If unavailable, use search_code_symbols, search_workspace, and read_workspace.", map[string]*schema.ParameterInfo{
			"symbol":    {Type: schema.String, Required: true},
			"max_depth": {Type: schema.Integer},
		}),
		Execute: func(ctx context.Context, raw json.RawMessage) (Execution, error) {
			var args struct {
				Symbol   string `json:"symbol"`
				MaxDepth int    `json:"max_depth"`
			}
			if err := decodeArgs(raw, &args); err != nil {
				return Execution{}, err
			}
			args.Symbol = strings.TrimSpace(args.Symbol)
			if args.Symbol == "" {
				return Execution{}, errs.NewValidationError(errs.SubtypeInvalidArgument, "trace_code_path symbol is required").WithParam("symbol")
			}
			if args.MaxDepth <= 0 || args.MaxDepth > 8 {
				args.MaxDepth = 4
			}
			if provider != nil {
				result, err := provider.TraceCodePath(ctx, args.Symbol, args.MaxDepth)
				if err != nil {
					return Execution{}, err
				}
				return jsonExecution(result, result.Sources, nil)
			}
			result := CodeIndexTraceResult{
				IndexAvailable: false,
				Symbol:         args.Symbol,
				Reason:         "code index provider is not configured",
				SuggestedTools: []string{"search_code_symbols", "search_workspace", "read_workspace"},
			}
			return jsonExecution(result, nil, nil)
		},
	}
}

func codeIndexSearchSources(result CodeIndexSearchResult) []domain.SourceRef {
	var sources []domain.SourceRef
	for _, hit := range result.Results {
		sources = append(sources, hit.Source)
	}
	if result.Fallback != nil {
		sources = append(sources, searchReportSources(*result.Fallback)...)
	}
	return sources
}

func searchReportSources(report workspace.SearchReport) []domain.SourceRef {
	sources := make([]domain.SourceRef, 0, len(report.Results))
	for _, result := range report.Results {
		sources = append(sources, result.Source)
	}
	return sources
}
