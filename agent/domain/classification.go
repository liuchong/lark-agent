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
