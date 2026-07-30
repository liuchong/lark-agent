package domain

import "testing"

func TestIsCodingQuestionRecognizesSourceInvestigationLanguage(t *testing.T) {
	for _, content := range []string{
		"请检查 Workspace 内示例文件预览上传与审核相关的生产入口",
		"请从源码确认缩略图审核流程",
		"这个代码入口为什么每次都访问 SampleDB",
		"请核对仓库里的一个相关测试或实现文件作为证据",
	} {
		if !IsCodingQuestion(content) {
			t.Fatalf("content=%q was not classified as coding", content)
		}
	}
	for _, content := range []string{
		"请检查本周销售数据并给出业务结论",
		"请检查 Workspace 内本周销售数据并给出业务结论",
		"请分析仓库库存周转并给出业务建议",
	} {
		if IsCodingQuestion(content) {
			t.Fatalf("ordinary business investigation %q was classified as coding", content)
		}
	}
}
