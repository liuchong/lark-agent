package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"strings"

	"github.com/cloudwego/eino/schema"

	"github.com/liuchong/lark-agent/agent/domain"
	"github.com/liuchong/lark-agent/agent/workspace"
	"github.com/liuchong/lark-agent/internal/apperr"
)

// WorkspaceDefinitions exposes bounded, read-only workspace inspection tools.
func WorkspaceDefinitions(scope *workspace.Scope) []Definition {
	definitions := []Definition{
		listWorkspaceDefinition(scope),
		exploreWorkspaceDefinition(scope),
		searchWorkspaceDefinition(scope),
		readWorkspaceDefinition(scope),
		readWorkspaceRulesDefinition(scope),
		listSkillsDefinition(scope),
		loadSkillDefinition(scope),
	}
	for index := range definitions {
		definitions[index].NonOwnerReadOnly = true
	}
	return definitions
}

func exploreWorkspaceDefinition(scope *workspace.Scope) Definition {
	return Definition{
		Info: toolInfo("explore_workspace", "Run a bounded read-only exploration subtask over a few precise queries and return a compact evidence summary. It never edits files or spawns nested agents.", map[string]*schema.ParameterInfo{
			"focus":                 {Type: schema.String, Required: true},
			"queries":               {Type: schema.Array, Required: true, ElemInfo: &schema.ParameterInfo{Type: schema.String}},
			"max_results_per_query": {Type: schema.Integer},
		}),
		Permission: ToolPermissionAllow,
		Risk:       ToolRiskLow,
		Execute: func(ctx context.Context, raw json.RawMessage) (Execution, error) {
			var args struct {
				Focus              string   `json:"focus"`
				Queries            []string `json:"queries"`
				MaxResultsPerQuery int      `json:"max_results_per_query"`
			}
			if err := decodeArgs(raw, &args); err != nil {
				return Execution{}, err
			}
			args.Focus = strings.TrimSpace(args.Focus)
			if args.Focus == "" {
				return Execution{}, errs.NewValidationError(errs.SubtypeInvalidArgument, "explore_workspace focus is required").WithParam("focus")
			}
			queries := nonEmptyToolStrings(args.Queries)
			if len(queries) == 0 {
				return Execution{}, errs.NewValidationError(errs.SubtypeInvalidArgument, "explore_workspace queries are required").WithParam("queries")
			}
			if len(queries) > 6 {
				queries = queries[:6]
			}
			if args.MaxResultsPerQuery <= 0 || args.MaxResultsPerQuery > 10 {
				args.MaxResultsPerQuery = 5
			}
			report := exploreWorkspaceReport{
				Focus:   args.Focus,
				Queries: make([]exploreQueryReport, 0, len(queries)),
			}
			var sources []domain.SourceRef
			for _, query := range queries {
				search, err := scope.SearchTextReportContext(ctx, workspace.SearchOptions{
					Query:          query,
					MaxResults:     args.MaxResultsPerQuery,
					MaxFiles:       1200,
					MaxDirectories: 400,
				})
				if err != nil {
					return Execution{}, err
				}
				report.Queries = append(report.Queries, exploreQueryReport{
					Query:              query,
					Results:            search.Results,
					Truncated:          search.Truncated,
					FilesScanned:       search.FilesScanned,
					DirectoriesScanned: search.DirectoriesScanned,
				})
				sources = append(sources, searchReportSources(search)...)
				report.Truncated = report.Truncated || search.Truncated
			}
			report.SuggestedNext = "Use read_workspace on the highest-signal paths before making code claims."
			return jsonExecution(report, sources, nil)
		},
	}
}

type exploreWorkspaceReport struct {
	Focus         string               `json:"focus"`
	Queries       []exploreQueryReport `json:"queries"`
	Truncated     bool                 `json:"truncated"`
	SuggestedNext string               `json:"suggested_next"`
}

type exploreQueryReport struct {
	Query              string                   `json:"query"`
	Results            []workspace.SearchResult `json:"results"`
	Truncated          bool                     `json:"truncated"`
	FilesScanned       int                      `json:"files_scanned"`
	DirectoriesScanned int                      `json:"directories_scanned"`
}

func listWorkspaceDefinition(scope *workspace.Scope) Definition {
	return Definition{
		Info: toolInfo("list_workspace", "List a bounded workspace directory. Use it to understand structure before reading files.", map[string]*schema.ParameterInfo{
			"path":      {Type: schema.String},
			"max_depth": {Type: schema.Integer},
		}),
		Execute: func(ctx context.Context, raw json.RawMessage) (Execution, error) {
			var args struct {
				Path     string `json:"path"`
				MaxDepth int    `json:"max_depth"`
			}
			if err := decodeArgs(raw, &args); err != nil {
				return Execution{}, err
			}
			if args.MaxDepth <= 0 || args.MaxDepth > 4 {
				args.MaxDepth = 2
			}
			snapshot, err := scope.ListDirectory(workspace.DirectoryOptions{
				Path:          args.Path,
				MaxDepth:      args.MaxDepth,
				MaxEntries:    400,
				MaxPerDir:     80,
				IncludeHidden: false,
			})
			return jsonExecution(snapshot, nil, err)
		},
	}
}

func searchWorkspaceDefinition(scope *workspace.Scope) Definition {
	return Definition{
		Info: toolInfo("search_workspace", "Search bounded workspace text by a case-insensitive literal phrase or by requiring every whitespace-separated term in one file. Set path to the exact repository or subtree when one is named, inspect snippets, then read relevant files.", map[string]*schema.ParameterInfo{
			"query":       {Type: schema.String, Required: true},
			"path":        {Type: schema.String},
			"max_results": {Type: schema.Integer},
		}),
		Execute: func(ctx context.Context, raw json.RawMessage) (Execution, error) {
			var args struct {
				Query      string `json:"query"`
				Path       string `json:"path"`
				MaxResults int    `json:"max_results"`
			}
			if err := decodeArgs(raw, &args); err != nil {
				return Execution{}, err
			}
			if strings.TrimSpace(args.Query) == "" {
				return Execution{}, errs.NewValidationError(errs.SubtypeInvalidArgument, "search_workspace query is required")
			}
			if args.MaxResults <= 0 || args.MaxResults > 40 {
				args.MaxResults = 20
			}
			report, err := scope.SearchTextReportContext(ctx, workspace.SearchOptions{
				Query:          args.Query,
				Path:           args.Path,
				MaxResults:     args.MaxResults,
				MaxFiles:       2000,
				MaxDirectories: 600,
			})
			if err != nil {
				return Execution{}, err
			}
			sources := make([]domain.SourceRef, 0, len(report.Results))
			for _, result := range report.Results {
				sources = append(sources, result.Source)
			}
			return jsonExecution(report, sources, nil)
		},
	}
}

func readWorkspaceDefinition(scope *workspace.Scope) Definition {
	return Definition{
		Info: toolInfo("read_workspace", "Read one bounded text file after locating it. Returns a digest-backed source reference.", map[string]*schema.ParameterInfo{
			"path":      {Type: schema.String, Required: true},
			"max_bytes": {Type: schema.Integer},
		}),
		Execute: func(_ context.Context, raw json.RawMessage) (Execution, error) {
			var args struct {
				Path     string `json:"path"`
				MaxBytes int64  `json:"max_bytes"`
			}
			if err := decodeArgs(raw, &args); err != nil {
				return Execution{}, err
			}
			if args.MaxBytes <= 0 || args.MaxBytes > 256*1024 {
				args.MaxBytes = 64 * 1024
			}
			data, source, err := scope.ReadText(args.Path, args.MaxBytes)
			if err != nil {
				return Execution{}, err
			}
			return Execution{Content: string(data), Sources: []domain.SourceRef{source}}, nil
		},
	}
}

func readWorkspaceRulesDefinition(scope *workspace.Scope) Definition {
	return Definition{
		Info: toolInfo("read_workspace_rules", "Read applicable AGENTS.md and .agents rule metadata for a workspace path before acting.", map[string]*schema.ParameterInfo{
			"path": {Type: schema.String},
		}),
		Execute: func(_ context.Context, raw json.RawMessage) (Execution, error) {
			var args struct {
				Path string `json:"path"`
			}
			if err := decodeArgs(raw, &args); err != nil {
				return Execution{}, err
			}
			controls, err := scope.DiscoverControlFiles(8, 512)
			if err != nil {
				return Execution{}, err
			}
			target := filepath.ToSlash(filepath.Clean(args.Path))
			if target == "." {
				target = ""
			}
			var files []map[string]string
			var sources []domain.SourceRef
			for _, path := range controls.RuleFiles {
				if !ruleApplies(path, target) {
					continue
				}
				data, source, readErr := scope.ReadText(path, 128*1024)
				if readErr != nil {
					return Execution{}, readErr
				}
				files = append(files, map[string]string{"path": path, "content": string(data)})
				sources = append(sources, source)
			}
			return jsonExecution(files, sources, nil)
		},
	}
}

func listSkillsDefinition(scope *workspace.Scope) Definition {
	return Definition{
		Info: toolInfo("list_skills", "List workspace-local SKILL.md paths. Load a skill only when its description is relevant.", nil),
		Execute: func(_ context.Context, raw json.RawMessage) (Execution, error) {
			if err := decodeArgs(raw, &struct{}{}); err != nil {
				return Execution{}, err
			}
			controls, err := scope.DiscoverControlFiles(8, 512)
			return jsonExecution(controls.SkillFiles, nil, err)
		},
	}
}

func loadSkillDefinition(scope *workspace.Scope) Definition {
	return Definition{
		Info: toolInfo("load_skill", "Load one workspace-local SKILL.md after list_skills identifies it as relevant.", map[string]*schema.ParameterInfo{
			"path": {Type: schema.String, Required: true},
		}),
		Execute: func(_ context.Context, raw json.RawMessage) (Execution, error) {
			var args struct {
				Path string `json:"path"`
			}
			if err := decodeArgs(raw, &args); err != nil {
				return Execution{}, err
			}
			controls, err := scope.DiscoverControlFiles(8, 512)
			if err != nil {
				return Execution{}, err
			}
			requested := filepath.ToSlash(filepath.Clean(args.Path))
			if !contains(controls.SkillFiles, requested) {
				return Execution{}, errs.NewValidationError(errs.SubtypeInvalidArgument, "skill is not available: %s", args.Path)
			}
			data, source, err := scope.ReadText(requested, 128*1024)
			if err != nil {
				return Execution{}, err
			}
			return Execution{Content: string(data), Sources: []domain.SourceRef{source}}, nil
		},
	}
}

func toolInfo(name, description string, params map[string]*schema.ParameterInfo) *schema.ToolInfo {
	info := &schema.ToolInfo{Name: name, Desc: description}
	if params != nil {
		info.ParamsOneOf = schema.NewParamsOneOfByParams(params)
	}
	return info
}

func decodeArgs(raw json.RawMessage, target any) error {
	if len(bytes.TrimSpace(raw)) == 0 {
		raw = json.RawMessage(`{}`)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return errs.NewValidationError(errs.SubtypeInvalidArgument, "invalid agent tool arguments").WithCause(err)
	}
	return nil
}

func jsonExecution(value any, sources []domain.SourceRef, err error) (Execution, error) {
	if err != nil {
		return Execution{}, err
	}
	data, err := json.Marshal(value)
	if err != nil {
		return Execution{}, errs.NewInternalError(errs.SubtypeUnknown, "encode agent tool result").WithCause(err)
	}
	return Execution{Content: string(data), Sources: sources}, nil
}

func ruleApplies(rulePath, target string) bool {
	rulePath = filepath.ToSlash(filepath.Clean(rulePath))
	if rulePath == "AGENTS.md" || strings.HasPrefix(rulePath, ".agents/") {
		return true
	}
	if filepath.Base(rulePath) != "AGENTS.md" {
		return false
	}
	dir := filepath.ToSlash(filepath.Dir(rulePath))
	return target == dir || strings.HasPrefix(target, dir+"/")
}

func contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func nonEmptyToolStrings(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			out = append(out, value)
		}
	}
	return out
}
