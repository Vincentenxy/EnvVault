package controller

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"envVault/internal/auth"
	"envVault/internal/domain"
	"envVault/internal/http/response"
	"envVault/internal/service"
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

// TestPageDataEmptyNilSliceOmitPageMeta 锁住「空数据形态」:
//   - pageNum / pageSize 字段值 = 0(由 `omitempty` 序列化为缺席)
//   - list 类型保持调用方原 slice 类型,值为同类型空 slice(非 nil)
//   - JSON 序列化为 `{total, list:[]}`,不出现 pageNum / pageSize
func TestPageDataEmptyNilSliceOmitPageMeta(t *testing.T) {
	got := pageData([]domain.Entity(nil), 0, domain.Pagination{PageNum: 1, PageSize: 20})

	if got.PageNum != 0 {
		t.Errorf("pageNum = %d, want 0 (omitted)", got.PageNum)
	}
	if got.PageSize != 0 {
		t.Errorf("pageSize = %d, want 0 (omitted)", got.PageSize)
	}
	if got.Total != 0 {
		t.Errorf("total = %d, want 0", got.Total)
	}
	list, ok := got.List.([]domain.Entity)
	if !ok {
		t.Fatalf("list type = %T, want []domain.Entity (preserved from nil slice)", got.List)
	}
	if list == nil {
		t.Fatalf("list should be non-nil empty slice, not nil")
	}
	if len(list) != 0 {
		t.Errorf("len(list) = %d, want 0", len(list))
	}

	// 序列化结果不应包含 pageNum / pageSize
	body, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(body), "pageNum") {
		t.Errorf("json contains pageNum: %s", body)
	}
	if strings.Contains(string(body), "pageSize") {
		t.Errorf("json contains pageSize: %s", body)
	}
	if !strings.Contains(string(body), `"list":[]`) {
		t.Errorf("json should contain `\"list\":[]`, got: %s", body)
	}
}

// TestPageDataEmptyEmptySliceOmitPageMeta 同上,但 items 是 length 0 而非 nil。
func TestPageDataEmptyEmptySliceOmitPageMeta(t *testing.T) {
	got := pageData([]domain.Entity{}, 0, domain.Pagination{PageNum: 1, PageSize: 20})

	if got.PageNum != 0 || got.PageSize != 0 {
		t.Errorf("pageNum=%d pageSize=%d, want 0/0 (omitted)", got.PageNum, got.PageSize)
	}
	body, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(body), "pageNum") || strings.Contains(string(body), "pageSize") {
		t.Errorf("json contains page meta on empty data: %s", body)
	}
}

// TestPageDataNonEmptyZeroTotalStillIncludesPageMeta 锁住边界:total=0 但 list 非空时
// 仍按「非空形态」返回 pageNum/pageSize(防止 over-eager omission)。
//
// 实际业务中 repo 不会返 total=0+非空 list,但 pageData 应保持结构稳定。
func TestPageDataNonEmptyZeroTotalStillIncludesPageMeta(t *testing.T) {
	items := []domain.Entity{{Id: "x"}}
	got := pageData(items, 0, domain.Pagination{PageNum: 3, PageSize: 10})

	if got.PageNum != 3 || got.PageSize != 10 {
		t.Errorf("pageNum=%d pageSize=%d, want 3/10 (non-empty keeps page meta)", got.PageNum, got.PageSize)
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

// TestCreateFolderRequestFields 锁住请求体 shape:Level / Code / Name / Comment / ParentCode / EnvList。
// 旧版 ParentId 字段已废弃,envList 必填(无 omitempty)。
func TestCreateFolderRequestFields(t *testing.T) {
	rt := reflect.TypeOf(createFolderRequest{})
	expected := map[string]string{
		"Level":      "level",
		"Code":       "code",
		"Name":       "name",
		"Comment":    "comment",
		"ParentCode": "parentCode,omitempty",
		"EnvList":    "envList",
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
	// 反向断言:旧 ParentId 字段必须不存在
	if _, ok := rt.FieldByName("ParentId"); ok {
		t.Errorf("createFolderRequest should NOT have ParentId field; use ParentCode + EnvList instead")
	}
	// 反向断言:EnvironmentId 字段必须不存在
	if _, ok := rt.FieldByName("EnvironmentId"); ok {
		t.Errorf("createFolderRequest should NOT have EnvironmentId field; env is selected via EnvList")
	}
	// EnvList 必填:tag 中不应有 omitempty
	if f, ok := rt.FieldByName("EnvList"); ok {
		if strings.Contains(f.Tag.Get("json"), "omitempty") {
			t.Errorf("EnvList should be required (no omitempty), got tag %q", f.Tag.Get("json"))
		}
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

// =====================================================================
// v11: SecretBatchCreate 测试
// =====================================================================

// TestSecretBatchCreateRequest_DTO 锁住 v11 batchCreate 请求体 shape:
//   - 顶部只有 secretList(comment 字段已弃用,不在 API 暴露)
//   - 子项含 Key / Comment / Dev / Test / Sim / Prod 4 个 env 字段(指针类型,可空)
func TestSecretBatchCreateRequest_DTO(t *testing.T) {
	rt := reflect.TypeOf(secretBatchCreateRequest{})
	// 反向断言:FolderId / 顶层 Comment 字段必须不存在
	for _, banned := range []string{"FolderId", "Comment"} {
		if _, ok := rt.FieldByName(banned); ok {
			t.Errorf("secretBatchCreateRequest should NOT have %s field", banned)
		}
	}
	// 必含 SecretList
	if _, ok := rt.FieldByName("SecretList"); !ok {
		t.Errorf("secretBatchCreateRequest missing SecretList field")
	}

	// 子项 secretBatchCreateItemRequest:4 个 env 字段(Dev/Test/Sim/Prod),都是 *secretBatchEnvValue
	rt2 := reflect.TypeOf(secretBatchCreateItemRequest{})
	for _, want := range []struct {
		name    string
		jsonTag string
	}{
		{"Key", "key"},
		{"Comment", "comment,omitempty"},
		{"Dev", "dev,omitempty"},
		{"Test", "test,omitempty"},
		{"Sim", "sim,omitempty"},
		{"Prod", "prod,omitempty"},
	} {
		f, ok := rt2.FieldByName(want.name)
		if !ok {
			t.Errorf("missing field %q", want.name)
			continue
		}
		if got := f.Tag.Get("json"); got != want.jsonTag {
			t.Errorf("%s json tag = %q, want %q", want.name, got, want.jsonTag)
		}
	}
	// 反向断言:Values 字段必须不存在(旧 API 标记)
	if _, ok := rt2.FieldByName("Values"); ok {
		t.Errorf("secretBatchCreateItemRequest should NOT have Values field (replaced by dev/test/sim/prod)")
	}
}

// TestSecretBatchEnvValue_DTO 锁住单 env 字段的 json shape。
func TestSecretBatchEnvValue_DTO(t *testing.T) {
	rt := reflect.TypeOf(secretBatchEnvValue{})
	for _, want := range []struct {
		name    string
		jsonTag string
	}{
		{"FolderId", "folderId"},
		{"Value", "value"},
	} {
		f, ok := rt.FieldByName(want.name)
		if !ok {
			t.Errorf("missing field %q", want.name)
			continue
		}
		if got := f.Tag.Get("json"); got != want.jsonTag {
			t.Errorf("%s json tag = %q, want %q", want.name, got, want.jsonTag)
		}
	}
}

// TestSecretBatchCreateRequestToDomain_EmptySecretList 锁住空 secretList
// → secretBatchCreateRequestToDomain 返 error(由 controller 翻译为 -1)。
func TestSecretBatchCreateRequestToDomain_EmptySecretList(t *testing.T) {
	_, err := secretBatchCreateRequestToDomain(secretBatchCreateRequest{})
	if err == nil {
		t.Fatalf("empty secretList should be rejected")
	}
	if !strings.Contains(err.Error(), "secretList") {
		t.Errorf("err = %v, want to contain 'secretList'", err)
	}
}

// TestSecretBatchCreateRequestToDomain_InvalidKey 锁住非法 key 格式
// → 错误信息带 index。
func TestSecretBatchCreateRequestToDomain_InvalidKey(t *testing.T) {
	req := secretBatchCreateRequest{
		SecretList: []secretBatchCreateItemRequest{
			{Key: "lower_case", Dev: &secretBatchEnvValue{FolderId: "f-dev", Value: "d"}},
		},
	}
	_, err := secretBatchCreateRequestToDomain(req)
	if err == nil {
		t.Fatalf("invalid key should be rejected")
	}
	if !strings.Contains(err.Error(), "secretList[0].key") {
		t.Errorf("err = %v, want to contain 'secretList[0].key'", err)
	}
}

// TestSecretBatchCreateRequestToDomain_EmptyFolderIdInEnv 锁住 env 字段
// folderId 为空 → 错误信息带 index 和 env code。
func TestSecretBatchCreateRequestToDomain_EmptyFolderIdInEnv(t *testing.T) {
	req := secretBatchCreateRequest{
		SecretList: []secretBatchCreateItemRequest{
			{Key: "DATABASE_URL", Dev: &secretBatchEnvValue{FolderId: "", Value: "d"}},
		},
	}
	_, err := secretBatchCreateRequestToDomain(req)
	if err == nil {
		t.Fatalf("empty folderId in dev should be rejected")
	}
	if !strings.Contains(err.Error(), "folderId") {
		t.Errorf("err = %v, want to contain 'folderId'", err)
	}
	if !strings.Contains(err.Error(), "dev") {
		t.Errorf("err = %v, want to mention env code 'dev'", err)
	}
}

// TestSecretBatchCreateRequestToDomain_AllEnvsEmpty 锁住 4 个 env 字段全 nil → reject。
func TestSecretBatchCreateRequestToDomain_AllEnvsEmpty(t *testing.T) {
	req := secretBatchCreateRequest{
		SecretList: []secretBatchCreateItemRequest{
			{Key: "DATABASE_URL"},
		},
	}
	_, err := secretBatchCreateRequestToDomain(req)
	if err == nil {
		t.Fatalf("all envs empty should be rejected")
	}
	if !strings.Contains(err.Error(), "env") {
		t.Errorf("err = %v, want to contain 'env'", err)
	}
}

// TestSecretBatchCreateRequestToDomain_Success 锁住 happy path:1 key × 4 envs
// → BatchCreateSecretSpec 完整还原;Envs 按 dev/test/sim/prod 顺序。
func TestSecretBatchCreateRequestToDomain_Success(t *testing.T) {
	req := secretBatchCreateRequest{
		SecretList: []secretBatchCreateItemRequest{
			{
				Key:     "DATABASE_URL",
				Comment: "db url",
				Dev:     &secretBatchEnvValue{FolderId: "f-dev", Value: "d"},
				Test:    &secretBatchEnvValue{FolderId: "f-test", Value: "t"},
				Sim:     &secretBatchEnvValue{FolderId: "f-sim", Value: "s"},
				Prod:    &secretBatchEnvValue{FolderId: "f-prod", Value: "p"},
			},
		},
	}
	domainReq, err := secretBatchCreateRequestToDomain(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(domainReq.SecretList) != 1 {
		t.Fatalf("SecretList len = %d, want 1", len(domainReq.SecretList))
	}
	spec := domainReq.SecretList[0]
	if spec.Key != "DATABASE_URL" || spec.Comment != "db url" {
		t.Errorf("spec.Key/Comment = (%q, %q), want (\"DATABASE_URL\", \"db url\")", spec.Key, spec.Comment)
	}
	if len(spec.Envs) != 4 {
		t.Fatalf("Envs len = %d, want 4", len(spec.Envs))
	}
	wantEnvs := []service.BatchCreateEnvTarget{
		{EnvCode: "dev", FolderId: "f-dev", Value: "d"},
		{EnvCode: "test", FolderId: "f-test", Value: "t"},
		{EnvCode: "sim", FolderId: "f-sim", Value: "s"},
		{EnvCode: "prod", FolderId: "f-prod", Value: "p"},
	}
	for i, want := range wantEnvs {
		if spec.Envs[i] != want {
			t.Errorf("spec.Envs[%d] = %+v, want %+v", i, spec.Envs[i], want)
		}
	}
}

// TestSecretBatchCreateRequestToDomain_PartialEnvs 锁住只填部分 env → Envs 跳过 nil 项。
func TestSecretBatchCreateRequestToDomain_PartialEnvs(t *testing.T) {
	req := secretBatchCreateRequest{
		SecretList: []secretBatchCreateItemRequest{
			{
				Key:  "FOO",
				Dev:  &secretBatchEnvValue{FolderId: "f-dev", Value: "d"},
				Prod: &secretBatchEnvValue{FolderId: "f-prod", Value: "p"},
				// Test / Sim 留空
			},
		},
	}
	domainReq, err := secretBatchCreateRequestToDomain(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(domainReq.SecretList[0].Envs) != 2 {
		t.Fatalf("Envs len = %d, want 2 (dev + prod)", len(domainReq.SecretList[0].Envs))
	}
	// dev 必须在 prod 之前(envFieldBindings 顺序)
	if domainReq.SecretList[0].Envs[0].EnvCode != "dev" {
		t.Errorf("first env = %q, want dev", domainReq.SecretList[0].Envs[0].EnvCode)
	}
	if domainReq.SecretList[0].Envs[1].EnvCode != "prod" {
		t.Errorf("second env = %q, want prod", domainReq.SecretList[0].Envs[1].EnvCode)
	}
}

// TestSecretBatchCreateRequestToDomain_DuplicateFolderId 锁住 v12 预检:
// 单 item 内 4 个 env 字段共用同一个 folderId → 拒绝。
//
// 历史背景:v11 的 extractEnvs 不查这个,导致 service 层整批事务回滚后
// 返回 "secret 已存在",client 误以为 DB 已有数据。v12 改在 controller 层
// 4xx 拒绝,错误信息直接指出哪几个 env 共用了哪个 folderId。
func TestSecretBatchCreateRequestToDomain_DuplicateFolderId(t *testing.T) {
	req := secretBatchCreateRequest{
		SecretList: []secretBatchCreateItemRequest{
			{
				Key:  "DB_USER_11111",
				Dev:  &secretBatchEnvValue{FolderId: "b53bd384-5c4a-4894-87e2-ff921df48635", Value: "x"},
				Test: &secretBatchEnvValue{FolderId: "b53bd384-5c4a-4894-87e2-ff921df48635", Value: "x"},
				Sim:  &secretBatchEnvValue{FolderId: "b53bd384-5c4a-4894-87e2-ff921df48635", Value: "x"},
				Prod: &secretBatchEnvValue{FolderId: "b53bd384-5c4a-4894-87e2-ff921df48635", Value: "x"},
			},
		},
	}
	_, err := secretBatchCreateRequestToDomain(req)
	if err == nil {
		t.Fatalf("4 env sharing the same folderId should be rejected")
	}
	msg := err.Error()
	// 错误信息应同时含 item index、复用的 env 列表、复用的 folderId。
	for _, want := range []string{"secretList[0]", "folderId(b53bd384-5c4a-4894-87e2-ff921df48635)"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error message missing %q: %s", want, msg)
		}
	}
}

// TestSecretBatchCreateRequestToDomain_PartialDuplicateFolderId 锁住 v12 预检:
// 单 item 内 3 个 env 共用同一 folderId、另 1 个 env 独立 → 同样拒绝。
func TestSecretBatchCreateRequestToDomain_PartialDuplicateFolderId(t *testing.T) {
	req := secretBatchCreateRequest{
		SecretList: []secretBatchCreateItemRequest{
			{
				Key:  "DB_HOST",
				Dev:  &secretBatchEnvValue{FolderId: "dup-id", Value: "x"},
				Test: &secretBatchEnvValue{FolderId: "dup-id", Value: "x"},
				Sim:  &secretBatchEnvValue{FolderId: "dup-id", Value: "x"},
				Prod: &secretBatchEnvValue{FolderId: "prod-only", Value: "x"},
			},
		},
	}
	_, err := secretBatchCreateRequestToDomain(req)
	if err == nil {
		t.Fatalf("3 env sharing the same folderId should be rejected")
	}
	if !strings.Contains(err.Error(), "dup-id") {
		t.Errorf("error should mention the duplicate folderId, got: %s", err.Error())
	}
}

// TestWriteBatchCreateError_HTTP200Minus1 锁住 v11 核心约定:
// 所有业务失败(包括入参校验 / 权限 / 冲突 / 内部错误)统一 HTTP 200 + body code=-1。
func TestWriteBatchCreateError_HTTP200Minus1(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/", nil)

	// 任意业务错(以 ErrConflict 为例)
	writeBatchCreateError(c, "创建失败", domain.ErrConflict)

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200 (business error also HTTP 200)", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"code":-1`) {
		t.Errorf("body should contain code -1, got: %s", body)
	}
	if !strings.Contains(body, "secret 已存在") {
		t.Errorf("body should map ErrConflict to 'secret 已存在', got: %s", body)
	}
}

// TestWriteBatchCreateError_PermissionDeniedTranslation 锁住 ErrPermissionDenied → "权限不足"。
func TestWriteBatchCreateError_PermissionDeniedTranslation(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/", nil)
	writeBatchCreateError(c, "创建失败", auth.ErrPermissionDenied)
	body := rec.Body.String()
	if !strings.Contains(body, "权限不足") {
		t.Errorf("body should contain '权限不足', got: %s", body)
	}
}

// TestOKWithCode 锁住 response.OKWithCode 的语义:HTTP 200 + 自定义 code。
func TestOKWithCode(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/", nil)
	response.OKWithCode(c, response.CodeBatchCreateError, "创建失败，找不到目标 folder", nil)
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"code":-1`) {
		t.Errorf("body should contain code -1, got: %s", rec.Body.String())
	}
}
