// Package rules loads workspace-local agent rules.
package rules

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strings"

	"github.com/liuchong/lark-agent/agent/domain"
	"github.com/liuchong/lark-agent/agent/workspace"
)

// File is a loaded workspace rule file.
type File struct {
	Source  domain.SourceRef `json:"source" yaml:"source"`
	Content string           `json:"content" yaml:"content"`
}

// Set is a versioned rule snapshot.
type Set struct {
	Version string `json:"version" yaml:"version"`
	Files   []File `json:"files" yaml:"files"`
}

// Load reads AGENTS.md and .agents files within the workspace boundary.
func Load(scope *workspace.Scope) (Set, error) {
	refs, err := scope.WalkRules()
	if err != nil {
		return Set{}, err
	}
	sort.Slice(refs, func(i, j int) bool { return refs[i].RelativePath < refs[j].RelativePath })
	files := make([]File, 0, len(refs))
	var versionInput strings.Builder
	for _, ref := range refs {
		data, source, err := scope.ReadText(ref.RelativePath, 128*1024)
		if err != nil {
			return Set{}, err
		}
		files = append(files, File{Source: source, Content: string(data)})
		versionInput.WriteString(source.RelativePath)
		versionInput.WriteString(source.Digest)
	}
	sum := sha256.Sum256([]byte(versionInput.String()))
	return Set{Version: hex.EncodeToString(sum[:8]), Files: files}, nil
}
