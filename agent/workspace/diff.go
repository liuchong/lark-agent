package workspace

import (
	"fmt"
	"strings"
)

func unifiedReplacementDiff(path, original string, edits []appliedEdit) string {
	var builder strings.Builder
	builder.WriteString("--- a/")
	builder.WriteString(path)
	builder.WriteString("\n+++ b/")
	builder.WriteString(path)
	builder.WriteString("\n")
	for _, edit := range edits {
		builder.WriteString("@@\n")
		writeDiffLines(&builder, "-", edit.OldText)
		writeDiffLines(&builder, "+", edit.NewText)
	}
	diff := builder.String()
	if len(diff) > 32*1024 {
		return diff[:32*1024] + "\n... diff truncated"
	}
	return diff
}

func unifiedFileDiff(path, oldContent, newContent string) string {
	if oldContent == newContent {
		return ""
	}
	var builder strings.Builder
	builder.WriteString("--- a/")
	builder.WriteString(path)
	builder.WriteString("\n+++ b/")
	builder.WriteString(path)
	builder.WriteString("\n@@\n")
	writeDiffLines(&builder, "-", oldContent)
	writeDiffLines(&builder, "+", newContent)
	diff := builder.String()
	if len(diff) > 32*1024 {
		return diff[:32*1024] + "\n... diff truncated"
	}
	return diff
}

func writeDiffLines(builder *strings.Builder, prefix, text string) {
	if text == "" {
		fmt.Fprintf(builder, "%s\n", prefix)
		return
	}
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	for _, line := range lines {
		builder.WriteString(prefix)
		builder.WriteString(line)
		builder.WriteString("\n")
	}
}
