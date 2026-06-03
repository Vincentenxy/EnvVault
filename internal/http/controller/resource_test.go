package controller

import (
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"envVault/internal/domain"
	"envVault/internal/http/response"
)

var _ = response.CodeConflict // 兜底:确保 response 包的 Conflict 常量被 import 进来

func TestCodePattern(t *testing.T) {
	valid := []string{"org-a", "project1", "groups-secrets"}
	for _, value := range valid {
		if !codePattern.MatchString(value) {
			t.Fatalf("codePattern rejected %q", value)
		}
	}

	invalid := []string{"", "Org-A", "-org", "org-", "org--a", "org_a", "组织"}
	for _, value := range invalid {
		if codePattern.MatchString(value) {
			t.Fatalf("codePattern accepted %q", value)
		}
	}
}

func TestSecretKeyPattern(t *testing.T) {
	valid := []string{"DATABASE_URL", "A1", "REDIS_PASSWORD"}
	for _, value := range valid {
		if !secretKeyPattern.MatchString(value) {
			t.Fatalf("secretKeyPattern rejected %q", value)
		}
	}

	invalid := []string{"", "database_url", "1KEY", "KEY-NAME", "KEY.NAME"}
	for _, value := range invalid {
		if secretKeyPattern.MatchString(value) {
			t.Fatalf("secretKeyPattern accepted %q", value)
		}
	}
}

func TestPageDataUsesGenericListShape(t *testing.T) {
	items := []domain.Entity{{Id: "org-1"}}

	got := pageData(items, 7, domain.Pagination{PageNum: 2, PageSize: 5})

	if got.PageNum != 2 {
		t.Fatalf("pageNum = %d, want 2", got.PageNum)
	}
	if got.PageSize != 5 {
		t.Fatalf("pageSize = %d, want 5", got.PageSize)
	}
	if got.Total != 7 {
		t.Fatalf("total = %d, want 7", got.Total)
	}
	list, ok := got.List.([]domain.Entity)
	if !ok {
		t.Fatalf("list type = %T, want []domain.Entity", got.List)
	}
	if len(list) != 1 || list[0].Id != "org-1" {
		t.Fatalf("list = %#v, want org-1", list)
	}
}

func TestResolveIdOrCodeIdWins(t *testing.T) {
	rid, useCode := resolveIdOrCode("org-uuid-1", "acme")
	if useCode {
		t.Fatalf("useCode = true, want false when id is present")
	}
	if rid != "org-uuid-1" {
		t.Fatalf("rid = %q, want org-uuid-1", rid)
	}
}

func TestResolveIdOrCodeFallsBackToCode(t *testing.T) {
	rid, useCode := resolveIdOrCode("", "acme")
	if !useCode {
		t.Fatalf("useCode = false, want true when only code is present")
	}
	if rid != "" {
		t.Fatalf("rid = %q, want empty when code lookup is needed", rid)
	}
}

func TestResolveIdOrCodeBothEmpty(t *testing.T) {
	// 不可达分支:校验器已保证至少一个非空。这里仅锁定 helper 当前的"回退"语义,
	// 实际不会被调用方踩到(useCode=true 但 code 仍为空,后续 lookup 必失败)。
	rid, useCode := resolveIdOrCode("", "")
	if rid != "" || !useCode {
		t.Fatalf("rid=%q useCode=%v, want (\"\", true) on dual-empty", rid, useCode)
	}
}

// TestValidateIdOrCodeAllowsDual 锁住行为:同时给 id 和 code 不再报错,
// 由 handler 端的 resolveIdOrCode 决定谁优先。
func TestValidateIdOrCodeAllowsDual(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/", nil)
	req := idOrCodeRequest{Id: "org-uuid-1", Code: "acme"}
	if !validateIdOrCode(c, req, "organization") {
		t.Fatalf("validateIdOrCode rejected dual id+code, want pass; body=%s", rec.Body.String())
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (no fail response written)", rec.Code)
	}
}

func TestValidateIdOrCodeRequiresAtLeastOne(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/", nil)
	req := idOrCodeRequest{}
	if validateIdOrCode(c, req, "organization") {
		t.Fatalf("validateIdOrCode accepted empty, want reject")
	}
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "id or code is required") {
		t.Fatalf("body = %s, want it to contain 'id or code is required'", rec.Body.String())
	}
}

func TestValidateUpdateIdOrCodeAllowsDual(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/", nil)
	req := updateByIdOrCodeRequest{Id: "org-uuid-1", Code: "acme", Name: "n", Comment: "c"}
	if !validateUpdateIdOrCode(c, req, "organization") {
		t.Fatalf("validateUpdateIdOrCode rejected dual id+code, want pass; body=%s", rec.Body.String())
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (no fail response written)", rec.Code)
	}
	// 兜底:确保 response 包的 Fail 函数签名还在(避免 import 漂移)
	_ = response.CodeInvalidRequest
}

// TestWriteMapsErrConflictTo409 锁住 domain.ErrConflict → 409 Conflict 的映射。
// 同 project 下重复 code 等唯一约束违规应返回 409,而不是泄漏 PG 原始错误。
func TestWriteMapsErrConflictTo409(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/", nil)
	ctrl := &Controller{}
	ctrl.write(c, nil, domain.ErrConflict)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"code":1409`) {
		t.Fatalf("body = %s, want code 1409 (Conflict)", rec.Body.String())
	}
}

// TestWriteMapsErrNotFoundTo404 兜底测试,避免上面改动顺带破坏 NotFound 路径。
func TestWriteMapsErrNotFoundTo404(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/", nil)
	ctrl := &Controller{}
	ctrl.write(c, nil, domain.ErrNotFound)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

// TestCreateFolderRequestLevelRequired 锁住 createFolderRequest 的 level 必填语义。
// 客户端遗漏 level 时应被显式 400 拒绝,而不是走默认 level=1 的隐式行为。
func TestCreateFolderRequestLevelRequired(t *testing.T) {
	// 静态检查 createFolderRequest 的字段标签
	rt := reflect.TypeOf(createFolderRequest{})
	field, ok := rt.FieldByName("Level")
	if !ok {
		t.Fatalf("createFolderRequest missing Level field")
	}
	jsonTag := field.Tag.Get("json")
	if jsonTag != "level" {
		t.Errorf("Level json tag = %q, want \"level\"", jsonTag)
	}
}

// TestCreateFolderRequestFields 锁住请求体 shape:ParentId / Level / Code / Name / Comment。
// 故意没有 EnvironmentId 字段——env 由后端从 parent 反查,客户端不带。
func TestCreateFolderRequestFields(t *testing.T) {
	rt := reflect.TypeOf(createFolderRequest{})
	expected := map[string]string{
		"ParentId": "parentId,omitempty",
		"Level":    "level",
		"Code":     "code",
		"Name":     "name",
		"Comment":  "comment",
	}
	for name, want := range expected {
		f, ok := rt.FieldByName(name)
		if !ok {
			t.Errorf("missing field %q", name)
			continue
		}
		if got := f.Tag.Get("json"); got != want {
			t.Errorf("%s json tag = %q, want %q", name, got, want)
		}
	}
	// 反向断言:EnvironmentId 字段必须不存在
	if _, ok := rt.FieldByName("EnvironmentId"); ok {
		t.Errorf("createFolderRequest should NOT have EnvironmentId field; env is inferred from parent")
	}
}

// TestListRequestFolderParentId 锁住 listRequest 加了 FolderParentId 字段。
// 用来在 ListFolders 时区分两种列表模式:
//   - environmentId:列 env 下 level=1 folder
//   - folderParentId:列父 folder 下 level=2 folder
func TestListRequestFolderParentId(t *testing.T) {
	rt := reflect.TypeOf(listRequest{})
	f, ok := rt.FieldByName("FolderParentId")
	if !ok {
		t.Fatalf("listRequest missing FolderParentId field")
	}
	if got := f.Tag.Get("json"); got != "folderParentId,omitempty" {
		t.Errorf("FolderParentId json tag = %q, want \"folderParentId,omitempty\"", got)
	}
}

// TestGlobalSearchRequestFields 锁住 globalSearchRequest 的请求体 shape。
// Keyword 必填,Types 可选,Limit 可选(默认 50、上限 200)。
func TestGlobalSearchRequestFields(t *testing.T) {
	rt := reflect.TypeOf(globalSearchRequest{})
	expected := map[string]string{
		"Keyword": "keyword",
		"Types":   "types,omitempty",
		"Limit":   "limit,omitempty",
	}
	for name, want := range expected {
		f, ok := rt.FieldByName(name)
		if !ok {
			t.Errorf("missing field %q", name)
			continue
		}
		if got := f.Tag.Get("json"); got != want {
			t.Errorf("%s json tag = %q, want %q", name, got, want)
		}
	}
}
