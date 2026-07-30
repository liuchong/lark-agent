package runtime

import (
	"encoding/json"
	"path"
	"regexp"
	"strings"

	"github.com/liuchong/lark-agent/agent/context"
	errs "github.com/liuchong/lark-agent/internal/apperr"
)

var workspacePathTokenPattern = regexp.MustCompile(`[A-Za-z0-9._~/-]+`)

func requestedCodingWorkspaceScope(bundle context.Bundle) string {
	root := strings.TrimSpace(bundle.Environment.WorkspaceRealRoot)
	if root == "" {
		root = strings.TrimSpace(bundle.Environment.WorkspaceRoot)
	}
	root = strings.TrimSuffix(strings.ReplaceAll(root, `\`, "/"), "/")
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
		"previous", "above", "earlier", "continue",
	} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

func validateCodingWorkspaceScope(toolName, arguments, scope string) error {
	if scope == "" {
		return nil
	}
	switch toolName {
	case "submit_investigation_plan":
		var input struct {
			EntryPoints []string `json:"entry_points"`
		}
		if err := json.Unmarshal([]byte(arguments), &input); err != nil {
			return nil
		}
		for _, entryPoint := range input.EntryPoints {
			if !workspacePathWithinScope(entryPoint, scope) {
				return exactWorkspaceScopeError(scope, entryPoint)
			}
		}
	case "read_workspace", "list_workspace", "search_workspace":
		var input struct {
			Path string `json:"path"`
		}
		if err := json.Unmarshal([]byte(arguments), &input); err != nil {
			return nil
		}
		if !workspacePathWithinScope(input.Path, scope) {
			return exactWorkspaceScopeError(scope, input.Path)
		}
	case "explore_workspace", "search_code_symbols", "trace_code_path", "shell":
		return errs.NewValidationError(
			errs.SubtypeInvalidArgument,
			"tool %s cannot guarantee exact workspace scope %s; use path-scoped workspace tools",
			toolName,
			scope,
		)
	}
	return nil
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
