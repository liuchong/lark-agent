package runtime

import (
	"encoding/json"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/cloudwego/eino/schema"

	"github.com/liuchong/lark-agent/agent/context"
	errs "github.com/liuchong/lark-agent/internal/apperr"
)

var workspacePathTokenPattern = regexp.MustCompile(`[A-Za-z0-9._~/-]+`)

func requestedCodingWorkspaceScope(bundle context.Bundle) string {
	root := codingWorkspaceRoot(bundle)
	rootName := path.Base(root)
	if root == "" || rootName == "." || rootName == "/" {
		return ""
	}
	topLevelDirectories := make(map[string]bool, len(bundle.Environment.Directory))
	for _, entry := range bundle.Environment.Directory {
		if entry.Kind != "dir" {
			continue
		}
		cleaned, ok := cleanWorkspaceScopePath(entry.Path)
		if !ok {
			continue
		}
		topLevelDirectories[strings.SplitN(cleaned, "/", 2)[0]] = true
	}
	if scope := workspaceScopeFromText(
		bundle.Event.Content,
		root,
		rootName,
		topLevelDirectories,
	); scope != "" {
		return scope
	}
	if bundle.Event.SenderID == "" || !referencesPriorWorkspaceContext(bundle.Event.Content) {
		return ""
	}
	for i := len(bundle.Conversation) - 1; i >= 0; i-- {
		event := bundle.Conversation[i]
		if event.MessageID != "" && event.MessageID == bundle.Event.MessageID {
			continue
		}
		if event.SenderID == "" || event.SenderID != bundle.Event.SenderID {
			continue
		}
		if scope := workspaceScopeFromText(
			event.Content,
			root,
			rootName,
			topLevelDirectories,
		); scope != "" {
			return scope
		}
	}
	return ""
}

func workspaceScopeFromText(
	text string,
	root string,
	rootName string,
	topLevelDirectories map[string]bool,
) string {
	candidates := make(map[string]bool)
	for _, token := range workspacePathTokenPattern.FindAllString(text, -1) {
		candidate := ""
		switch {
		case strings.HasPrefix(token, root+"/"):
			candidate = strings.TrimPrefix(token, root+"/")
		case strings.HasPrefix(token, rootName+"/"):
			candidate = strings.TrimPrefix(token, rootName+"/")
		case !strings.HasPrefix(token, "/"):
			cleaned, ok := cleanWorkspaceScopePath(token)
			if ok &&
				strings.Contains(cleaned, "/") &&
				topLevelDirectories[strings.SplitN(cleaned, "/", 2)[0]] {
				candidate = cleaned
			}
		}
		if scopedPath, ok := cleanWorkspaceScopePath(candidate); ok {
			candidates[scopedPath] = true
		}
	}
	if len(candidates) != 1 {
		return ""
	}
	for candidate := range candidates {
		return candidate
	}
	return ""
}

func referencesPriorWorkspaceContext(text string) bool {
	lower := strings.ToLower(text)
	for _, marker := range []string{
		"上一条", "上面", "前面", "前文", "刚才", "结合", "继续",
		"当前项目", "这个项目", "该项目",
		"previous", "above", "earlier", "continue",
		"current project", "this project", "the project",
	} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

func codingWorkspaceRoot(bundle context.Bundle) string {
	root := strings.TrimSpace(bundle.Environment.WorkspaceRealRoot)
	if root == "" {
		root = strings.TrimSpace(bundle.Environment.WorkspaceRoot)
	}
	return strings.TrimSuffix(strings.ReplaceAll(root, `\`, "/"), "/")
}

func exactScopeToolInfos(infos []*schema.ToolInfo) []*schema.ToolInfo {
	filtered := make([]*schema.ToolInfo, 0, len(infos))
	for _, info := range infos {
		if info != nil && exactScopeToolAllowed(info.Name) {
			filtered = append(filtered, info)
		}
	}
	return filtered
}

func exactScopeToolAllowed(toolName string) bool {
	switch toolName {
	case "list_workspace",
		"search_workspace",
		"read_workspace",
		"get_lark_context",
		"submit_investigation_plan",
		"submit_decision":
		return true
	default:
		return false
	}
}

func exactCodingWorkspaceScopePrompt(scope string, maxToolCalls int) string {
	return fmt.Sprintf(
		"Exact coding workspace scope: %s. This exact scope is already a readable subtree inside the configured workspace root; do not request changing workspace_root and do not substitute a similarly named repository. "+
			"Use only paths inside this scope. Path-scoped tools may use either the full scope path or a repository-relative path such as sample-client/...; the runtime safely prefixes repository-relative paths with %s. "+
			"Use at most two bounded locating searches, then read the promising production files and answer every requested field. If a requested serialized payload is declared only as String, bytes, raw JSON, or another opaque container, that declaration does not prove the concrete shape; use a remaining bounded read for current docs, tests, protocol definitions, or serialization code. "+
			"The investigation tool-call limit is %d; reserve calls for authoritative reads and submit_decision.",
		scope,
		scope,
		maxToolCalls,
	)
}

func prepareCodingWorkspaceToolArguments(
	toolName string,
	arguments string,
	scope string,
	workspaceRoot string,
) (string, error) {
	if scope == "" {
		return arguments, nil
	}
	if !exactScopeToolAllowed(toolName) {
		return "", errs.NewValidationError(
			errs.SubtypeInvalidArgument,
			"tool %s cannot guarantee exact workspace scope %s; use path-scoped workspace tools",
			toolName,
			scope,
		)
	}
	var payload map[string]json.RawMessage
	if err := json.Unmarshal([]byte(arguments), &payload); err != nil {
		return arguments, nil
	}
	switch toolName {
	case "submit_investigation_plan":
		var entryPoints []string
		if raw, ok := payload["entry_points"]; ok {
			if err := json.Unmarshal(raw, &entryPoints); err != nil {
				return arguments, nil
			}
		}
		for index, entryPoint := range entryPoints {
			normalized, err := normalizeCodingWorkspacePath(
				entryPoint,
				scope,
				workspaceRoot,
			)
			if err != nil {
				return "", err
			}
			entryPoints[index] = normalized
		}
		if len(entryPoints) > 0 {
			payload["entry_points"], _ = json.Marshal(entryPoints)
		}
	case "list_workspace", "search_workspace", "read_workspace":
		var requestedPath string
		if raw, ok := payload["path"]; ok {
			if err := json.Unmarshal(raw, &requestedPath); err != nil {
				return arguments, nil
			}
		}
		if strings.TrimSpace(requestedPath) == "" &&
			(toolName == "list_workspace" || toolName == "search_workspace") {
			requestedPath = scope
		}
		normalized, err := normalizeCodingWorkspacePath(
			requestedPath,
			scope,
			workspaceRoot,
		)
		if err != nil {
			return "", err
		}
		payload["path"], _ = json.Marshal(normalized)
	default:
		return arguments, nil
	}
	prepared, err := json.Marshal(payload)
	if err != nil {
		return arguments, nil
	}
	return string(prepared), nil
}

func validateCodingWorkspaceToolRealPath(
	toolName string,
	arguments string,
	scope string,
	workspaceRoot string,
) error {
	switch toolName {
	case "list_workspace", "search_workspace", "read_workspace":
	default:
		return nil
	}
	var payload struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal([]byte(arguments), &payload); err != nil {
		return nil
	}
	candidate, ok := cleanWorkspaceScopePath(payload.Path)
	if !ok || !workspacePathWithinScope(candidate, scope) {
		return exactWorkspaceScopeError(scope, payload.Path)
	}
	workspaceRoot = filepath.Clean(workspaceRoot)
	if workspaceRoot == "." || !filepath.IsAbs(workspaceRoot) {
		return errs.NewValidationError(
			errs.SubtypeInvalidArgument,
			"configured workspace root is not an absolute path",
		)
	}
	if exact, err := hasExactFilesystemCase(workspaceRoot, candidate); err != nil {
		return err
	} else if !exact {
		return exactWorkspaceScopeError(scope, payload.Path)
	}
	scopeReal, err := filepath.EvalSymlinks(filepath.Join(
		workspaceRoot,
		filepath.FromSlash(scope),
	))
	if err != nil {
		return errs.NewValidationError(
			errs.SubtypeInvalidArgument,
			"exact workspace scope %s cannot be resolved",
			scope,
		).WithCause(err)
	}
	candidateReal, err := filepath.EvalSymlinks(filepath.Join(
		workspaceRoot,
		filepath.FromSlash(candidate),
	))
	if err != nil {
		// Let the path-scoped tool return its normal not-found error. A missing
		// target cannot expose data through a symlink.
		if os.IsNotExist(err) {
			return nil
		}
		return errs.NewValidationError(
			errs.SubtypeInvalidArgument,
			"path %s cannot be resolved inside exact workspace scope %s",
			payload.Path,
			scope,
		).WithCause(err)
	}
	within, err := realPathWithin(scopeReal, candidateReal)
	if err != nil {
		return errs.NewValidationError(
			errs.SubtypeInvalidArgument,
			"path %s cannot be compared with exact workspace scope %s",
			payload.Path,
			scope,
		).WithCause(err)
	}
	if !within {
		return errs.NewValidationError(
			errs.SubtypeInvalidArgument,
			"path %s resolves outside exact workspace scope %s",
			payload.Path,
			scope,
		)
	}
	return nil
}

func hasExactFilesystemCase(root, relative string) (bool, error) {
	current := root
	for _, component := range strings.Split(filepath.FromSlash(relative), string(filepath.Separator)) {
		entries, err := os.ReadDir(current)
		if err != nil {
			if os.IsNotExist(err) {
				return true, nil
			}
			return false, err
		}
		exact := false
		caseInsensitive := false
		for _, entry := range entries {
			if entry.Name() == component {
				exact = true
				break
			}
			if strings.EqualFold(entry.Name(), component) {
				caseInsensitive = true
			}
		}
		if !exact {
			if caseInsensitive {
				return false, nil
			}
			// Preserve the tool's normal not-found behavior for a genuinely
			// absent path while rejecting existing differently cased entries.
			return true, nil
		}
		current = filepath.Join(current, component)
	}
	return true, nil
}

func realPathWithin(root, candidate string) (bool, error) {
	relative, err := filepath.Rel(root, candidate)
	if err != nil {
		return false, err
	}
	return relative == "." ||
		(relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))), nil
}

func normalizeCodingWorkspacePath(candidate, scope, workspaceRoot string) (string, error) {
	candidate = strings.TrimSpace(strings.ReplaceAll(candidate, `\`, "/"))
	workspaceRoot = strings.TrimSuffix(
		strings.TrimSpace(strings.ReplaceAll(workspaceRoot, `\`, "/")),
		"/",
	)
	rootName := path.Base(workspaceRoot)
	switch {
	case workspaceRoot != "" && strings.HasPrefix(candidate, workspaceRoot+"/"):
		candidate = strings.TrimPrefix(candidate, workspaceRoot+"/")
	case rootName != "" && rootName != "." && rootName != "/" &&
		strings.HasPrefix(candidate, rootName+"/"):
		candidate = strings.TrimPrefix(candidate, rootName+"/")
	}
	cleaned, ok := cleanWorkspaceScopePath(candidate)
	if !ok {
		return "", exactWorkspaceScopeError(scope, candidate)
	}
	if workspacePathWithinScope(cleaned, scope) {
		return cleaned, nil
	}
	if hasCaseMismatchedScopePrefix(cleaned, scope) {
		return "", exactWorkspaceScopeError(scope, candidate)
	}
	scopeBase := path.Base(scope)
	firstComponent := strings.SplitN(cleaned, "/", 2)[0]
	if strings.EqualFold(firstComponent, scopeBase) && firstComponent != scopeBase {
		return "", exactWorkspaceScopeError(scope, candidate)
	}
	if firstComponent == scopeBase {
		cleaned = path.Join(path.Dir(scope), cleaned)
		if workspacePathWithinScope(cleaned, scope) {
			return cleaned, nil
		}
	}
	scoped := path.Join(scope, cleaned)
	if !workspacePathWithinScope(scoped, scope) {
		return "", exactWorkspaceScopeError(scope, candidate)
	}
	return scoped, nil
}

func hasCaseMismatchedScopePrefix(candidate, scope string) bool {
	candidateParts := strings.Split(candidate, "/")
	scopeParts := strings.Split(scope, "/")
	if len(candidateParts) < len(scopeParts) {
		return false
	}
	mismatch := false
	for index, scopePart := range scopeParts {
		if !strings.EqualFold(candidateParts[index], scopePart) {
			return false
		}
		if candidateParts[index] != scopePart {
			mismatch = true
		}
	}
	return mismatch
}

func workspacePathWithinScope(candidate, scope string) bool {
	cleaned, ok := cleanWorkspaceScopePath(candidate)
	if !ok {
		return false
	}
	return cleaned == scope || strings.HasPrefix(cleaned, scope+"/")
}

func cleanWorkspaceScopePath(candidate string) (string, bool) {
	candidate = strings.TrimSpace(strings.ReplaceAll(candidate, `\`, "/"))
	if candidate == "" || strings.HasPrefix(candidate, "/") {
		return "", false
	}
	cleaned := path.Clean(candidate)
	if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return "", false
	}
	return cleaned, true
}

func exactWorkspaceScopeError(scope, attempted string) error {
	attempted = strings.TrimSpace(attempted)
	if attempted == "" {
		attempted = "<missing path>"
	}
	return errs.NewValidationError(
		errs.SubtypeInvalidArgument,
		"user named exact workspace scope %s; path %s is outside that scope and similarly named sibling projects must not be substituted",
		scope,
		attempted,
	)
}
