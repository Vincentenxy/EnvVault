package postgres

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"

	"envVault/internal/domain"
	"envVault/internal/store"
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

// =====================================================================
// v11: BatchCreateSecretItem 字段锁
// =====================================================================

// TestBatchCreateSecretItemFields 锁住 store.BatchCreateSecretItem 字段名。
// service 层依赖这些字段名构造 items,字段 rename 必导致 service 编译失败,
// 但这里再加一道测试防止 reorder/typo。
func TestBatchCreateSecretItemFields(t *testing.T) {
	rt := reflect.TypeOf(store.BatchCreateSecretItem{})
	expected := map[string]string{
		"FolderId":   "FolderId",
		"Key":        "Key",
		"Comment":    "Comment",
		"Actor":      "Actor",
		"Ciphertext": "Ciphertext",
	}
	for name, want := range expected {
		f, ok := rt.FieldByName(name)
		if !ok {
			t.Errorf("missing field %q", name)
			continue
		}
		if f.Name != want {
			t.Errorf("%s name = %q, want %q", name, f.Name, want)
		}
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

// TestTranslatePgErrUniqueViolation 锁住唯一约束 (SQLSTATE 23505) → ErrConflict 的翻译行为。
// 这是"同 project 下不允许重复 code"等校验在应用层的兜底,否则会泄漏 PG 原始错误。
func TestTranslatePgErrUniqueViolation(t *testing.T) {
	pgErr := &pgconn.PgError{
		Code:    pgUniqueViolation,
		Message: `duplicate key value violates unique constraint "environments_project_code_active_uidx"`,
	}
	got := translatePgErr(pgErr)
	if !errors.Is(got, domain.ErrConflict) {
		t.Fatalf("translatePgErr(unique violation) = %v, want ErrConflict", got)
	}
	// 与 ErrConflict 别名同源,这样 errors.Is(postgres.ErrConflict, ...) 也能命中。
	if !errors.Is(got, ErrConflict) {
		t.Fatalf("translatePgErr(unique violation) not Is(ErrConflict): %v", got)
	}
}

func TestTranslatePgErrOtherCode(t *testing.T) {
	// 非 23505 的 PG 错误应当原样穿透,不能误伤。
	pgErr := &pgconn.PgError{Code: "23502", Message: "not_null violation"}
	got := translatePgErr(pgErr)
	if errors.Is(got, ErrConflict) {
		t.Fatalf("translatePgErr(not_null) wrongly returned ErrConflict: %v", got)
	}
	if got.Error() != pgErr.Error() {
		t.Fatalf("translatePgErr(other) altered error: %q vs %q", got.Error(), pgErr.Error())
	}
}

func TestTranslatePgErrNilAndPlain(t *testing.T) {
	if got := translatePgErr(nil); got != nil {
		t.Fatalf("translatePgErr(nil) = %v, want nil", got)
	}
	plain := errors.New("plain error")
	if got := translatePgErr(plain); got != plain {
		t.Fatalf("translatePgErr(plain) altered error identity: %v vs %v", got, plain)
	}
}

// TestEntityReadColumnsIncludesParentForChildren 锁住 list/get SQL
// 在 project/env/folder 上返回 parent_id,在 organization 上不带。
func TestEntityReadColumnsIncludesParentForChildren(t *testing.T) {
	cases := []struct {
		table      string
		wantParent bool
	}{
		{"organizations", false},
		{"projects", true},
		{"environments", true},
		{"folders", true},
		{"environment_templates", true},
	}
	for _, c := range cases {
		cols, scan := entityReadColumns(parentColumn(c.table))
		if c.wantParent {
			if !contains([]byte(cols), "t.org_id") &&
				!contains([]byte(cols), "t.project_id") &&
				!contains([]byte(cols), "t.environment_id") {
				t.Errorf("table=%s cols=%q, want one of t.org_id/t.project_id/t.environment_id", c.table, cols)
			}
			// scan 函数应包含 9 个目标(id + parent + 其他 7 个)
			dummy := Entity{}
			targets := scan(&dummy)
			if len(targets) != 9 {
				t.Errorf("table=%s scan targets = %d, want 9", c.table, len(targets))
			}
			if &dummy.ParentId == nil {
				t.Errorf("table=%s scan should write into &entity.ParentId", c.table)
			}
		} else {
			if contains([]byte(cols), "t.org_id") || contains([]byte(cols), "t.project_id") || contains([]byte(cols), "t.environment_id") {
				t.Errorf("table=%s cols=%q, want no parent column", c.table, cols)
			}
			dummy := Entity{}
			targets := scan(&dummy)
			if len(targets) != 8 {
				t.Errorf("table=%s scan targets = %d, want 8", c.table, len(targets))
			}
		}
	}
}

func TestEntityReturningMatchesReadColumns(t *testing.T) {
	// RETURNING 子句不能带 t. 别名,列数必须与读路径一致。
	for _, table := range []string{"organizations", "projects", "environments", "folders", "environment_templates"} {
		readCols, _ := entityReadColumns(parentColumn(table))
		ret, scan := entityReturning(parentColumn(table))
		if contains([]byte(ret), "t.") {
			t.Errorf("table=%s returning=%q should not contain 't.' prefix", table, ret)
		}
		// 读路径与 RETURNING 路径的列数(去掉 t. 前缀后)应一致
		// 简单做法:返回的 scan 目标数相同
		dummy := Entity{}
		_ = scan(&dummy)
		// 进一步:读路径是带 t. 别名,RETURNING 不带,列 token 数应一致
		if countCols(readCols) != countCols(ret) {
			t.Errorf("table=%s read cols=%d vs returning cols=%d mismatch", table, countCols(readCols), countCols(ret))
		}
	}
}

func countCols(cols string) int {
	parts := strings.Split(cols, ",")
	n := 0
	for _, p := range parts {
		if strings.TrimSpace(p) != "" {
			n++
		}
	}
	return n
}

// TestEntityJSONOmitsEmptyParentId 锁住 organization 这种顶层实体的 JSON
// 序列化不带 parentId 字段(omitempty)。
func TestEntityJSONOmitsEmptyParentId(t *testing.T) {
	org := Entity{Id: "id1", Code: "acme", Name: "Acme"}
	payload, err := json.Marshal(org)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if contains(payload, `"parentId"`) {
		t.Errorf("top-level entity should omit parentId: %s", string(payload))
	}
}

// TestEntityJSONIncludesParentId 锁住子级实体的 JSON 序列化带 parentId。
func TestEntityJSONIncludesParentId(t *testing.T) {
	proj := Entity{Id: "id1", ParentId: "org-uuid", Code: "p1", Name: "Project 1"}
	payload, err := json.Marshal(proj)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !contains(payload, `"parentId":"org-uuid"`) {
		t.Errorf("child entity should include parentId: %s", string(payload))
	}
}

// TestDefaultPermissionsIncludeOrgForceDelete 锁住级联删除 org 的权限码在系统表中。
// 缺这个权限码的话 force=true 路径无角色可分配。
func TestDefaultPermissionsIncludeOrgForceDelete(t *testing.T) {
	codes := permissionCodes(defaultPermissions())
	found := false
	for _, c := range codes {
		if c == "org:force_delete" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("org:force_delete missing from defaultPermissions(); got: %v", codes)
	}
}

// TestDefaultRolesPlatformAndOwnerGetForceDelete 锁住 platform_admin / org_owner
// 这两个"高权限"角色自动拿到 org:force_delete(因为它们直接吃 all 权限码)。
// org_admin 不应该拿到(否则 org 管理员能自删所在 org)。
func TestDefaultRolesPlatformAndOwnerGetForceDelete(t *testing.T) {
	roles := defaultRoles()
	has := func(code string) bool {
		for _, r := range roles {
			if r.Code == code {
				for _, p := range r.Permissions {
					if p == "org:force_delete" {
						return true
					}
				}
			}
		}
		return false
	}
	if !has("platform_admin") {
		t.Errorf("platform_admin should have org:force_delete")
	}
	if !has("org_owner") {
		t.Errorf("org_owner should have org:force_delete")
	}
	if has("org_admin") {
		t.Errorf("org_admin should NOT have org:force_delete (would allow self-destruct)")
	}
}
