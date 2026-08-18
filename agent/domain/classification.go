package domain

import "strings"

var codingQuestionMarkers = []string{
	"api",
	"接口",
	"代码",
	"数据库",
	"sampledb",
	"mysql",
	"redis",
	"函数",
	"class",
	"基于代码",
	"bug",
	"报错",
	"sdk",
	"回调",
	"限流",
	"缓存",
	"endpoint",
	"handler",
	"repository",
	"源码",
	"源代码",
	"生产入口",
	"代码入口",
	"实现文件",
	"测试文件",
	"相关测试",
	"单元测试",
	"集成测试",
	"代码仓库",
	"仓库代码",
}

// IsCodingQuestion reports whether a request explicitly needs source-code
// investigation rather than the simple-question tool policy.
func IsCodingQuestion(content string) bool {
	lower := strings.ToLower(content)
	for _, marker := range codingQuestionMarkers {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

var workspaceWriteRequestMarkers = []string{
	"请修改", "帮我改", "改一下", "改成", "把它改", "将它改",
	"修改这个", "修改文件", "修改代码",
	"请修复", "帮我修", "修一下", "修复这个", "修复文件", "修好",
	"请实现", "帮我实现", "实现一下", "实现这个",
	"新建文件", "创建文件", "写入文件", "覆盖这个文件", "打补丁", "应用补丁",
	"please fix", "fix this", "fix the",
	"implement this", "please implement", "implement the",
	"edit this", "edit the file", "modify this", "change this file",
	"write a file", "create a file",
	"apply this patch", "apply the patch", "replace this",
}

// IsWorkspaceWriteRequested reports whether the target message explicitly asks
// to modify, fix, or implement workspace files. Locating an implementation
// file is not a write request.
func IsWorkspaceWriteRequested(content string) bool {
	lower := strings.ToLower(content)
	for _, marker := range workspaceWriteRequestMarkers {
		if strings.Contains(lower, strings.ToLower(marker)) {
			return true
		}
	}
	return false
}
