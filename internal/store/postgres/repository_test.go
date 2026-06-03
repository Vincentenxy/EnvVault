package postgres

import (
	"encoding/json"
	"testing"
)

// TestParentColumnEnvironmentsPointsToProject 锁住 v3 重构:
// environments 表的"父列"必须指向 project_id,而不是 org_id。
// 这是本次重构修复 rbac.go scope SQL 隐含 bug 的关键。
func TestParentColumnEnvironmentsPointsToProject(t *testing.T) {
	if got := parentColumn("environments"); got != "project_id" {
		t.Fatalf("parentColumn(environments) = %q, want project_id", got)
	}
	if got := parentColumn("projects"); got != "org_id" {
		t.Fatalf("parentColumn(projects) = %q, want org_id", got)
	}
	if got := parentColumn("folders"); got != "environment_id" {
		t.Fatalf("parentColumn(folders) = %q, want environment_id", got)
	}
	if got := parentColumn("secrets"); got != "" {
		t.Fatalf("parentColumn(secrets) = %q, want empty", got)
	}
}

// TestEnvSpecStruct 验证 EnvSpec 类型在 CreateProject 入参中能被直接构造。
func TestEnvSpecStruct(t *testing.T) {
	spec := EnvSpec{Code: "dev", Name: "Development", Comment: "main dev env"}
	if spec.Code != "dev" || spec.Name != "Development" || spec.Comment != "main dev env" {
		t.Fatalf("EnvSpec fields not preserved: %+v", spec)
	}
}

// TestEnvironmentTemplateJSONTags 验证 EnvironmentTemplate 序列化字段名与设计一致。
// 前端模板列表依赖这些字段名,变更时必须同步 OpenAPI 与 DESIGN。
func TestEnvironmentTemplateJSONTags(t *testing.T) {
	tpl := EnvironmentTemplate{
		Id:        "11111111-1111-1111-1111-111111111111",
		OrgId:     "22222222-2222-2222-2222-222222222222",
		Code:      "dev",
		Name:      "Dev",
		Comment:   "first write wins",
		CreatedBy: "alice",
		UpdatedBy: "alice",
	}
	payload, err := json.Marshal(tpl)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, key := range []string{`"id"`, `"orgId"`, `"code"`, `"name"`, `"createdBy"`, `"updatedBy"`} {
		if !contains(payload, key) {
			t.Fatalf("JSON %s missing key %s", string(payload), key)
		}
	}
	// comment 在 omitempty 下应省略空字符串对应的键
	empty := EnvironmentTemplate{Id: "1", Code: "x", Name: "x"}
	emptyPayload, _ := json.Marshal(empty)
	if contains(emptyPayload, `"comment"`) {
		t.Fatalf("empty comment should be omitted: %s", string(emptyPayload))
	}
}

// TestBuildSecretPathJoinsOrgProjectEnvFolderKey 锁住 secret path 拼接格式。
func TestBuildSecretPathJoinsOrgProjectEnvFolderKey(t *testing.T) {
	got := buildSecretPath(Secret{
		OrgCode:         "o1",
		ProjectCode:     "p1",
		EnvironmentCode: "dev",
		FolderCode:      "globals",
		Key:             "FOO",
	})
	if got != "o1.p1.dev.globals.FOO" {
		t.Fatalf("path = %q, want o1.p1.dev.globals.FOO", got)
	}
}

// TestBuildSecretPathEmptyPart 验证任意一段缺失都返回空,防止半截路径泄漏。
func TestBuildSecretPathEmptyPart(t *testing.T) {
	if got := buildSecretPath(Secret{OrgCode: "o", ProjectCode: "", EnvironmentCode: "e", FolderCode: "f", Key: "k"}); got != "" {
		t.Fatalf("path with empty part = %q, want empty", got)
	}
}

func contains(haystack []byte, needle string) bool {
	if len(needle) == 0 {
		return true
	}
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if string(haystack[i:i+len(needle)]) == needle {
			return true
		}
	}
	return false
}
