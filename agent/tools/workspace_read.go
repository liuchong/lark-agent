package tools

import "github.com/liuchong/lark-agent/agent/workspace"

// WorkspaceReadDefinitions exposes list/search/read only. Smart commands that
// are not GitHub-backed may register these; github run must not.
func WorkspaceReadDefinitions(scope *workspace.Scope) []Definition {
	definitions := []Definition{
		listWorkspaceDefinition(scope),
		searchWorkspaceDefinition(scope),
		readWorkspaceDefinition(scope),
	}
	for index := range definitions {
		definitions[index].NonOwnerReadOnly = true
	}
	return definitions
}
