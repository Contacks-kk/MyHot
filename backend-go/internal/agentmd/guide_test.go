package agentmd

import (
	"strings"
	"testing"
)

func TestGuideListsMigratedRoutesAndRequiresUserSession(t *testing.T) {
	service := NewService(nil, "Example", "https://site.invalid", map[string]string{
		"ai-models": "模型", "paper": "论文", "industry": "行业",
	})
	service.SetCategoryOrder([]string{"ai-models", "industry", "paper"})
	guide := service.Guide()
	for _, want := range []string{
		"# Example 使用说明（给 Agent）",
		"https://site.invalid/api/v1/agent/latest",
		"https://site.invalid/api/v1/agent/search?q=关键词",
		"https://site.invalid/api/v1/agent/daily",
		"category=ai-models（模型）、industry（行业）、paper（论文）",
		"所有内容地址都需要当前用户的有效会话 Cookie",
		"未登录返回 401",
		"https://site.invalid/openapi-v1.json",
	} {
		if !strings.Contains(guide, want) {
			t.Errorf("guide missing %q:\n%s", want, guide)
		}
	}
	if strings.Contains(guide, "匿名只读") || !strings.HasSuffix(guide, "\n") {
		t.Fatalf("guide access or newline contract is incorrect:\n%s", guide)
	}
}
