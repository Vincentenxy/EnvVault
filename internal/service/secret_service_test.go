package service

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"envVault/internal/auth"
	"envVault/internal/domain"
)

// TestParseSecretPathHappyPath 锁住 "org.proj.env.folder.KEY" 5 段解析。
func TestParseSecretPathHappyPath(t *testing.T) {
	org, proj, env, folder, key, err := parseSecretPath("o1.p1.dev.globals.FOO")
	if err != nil {
		t.Fatalf("parseSecretPath returned error: %v", err)
	}
	if org != "o1" || proj != "p1" || env != "dev" || folder != "globals" || key != "FOO" {
		t.Fatalf("unexpected segments: %q %q %q %q %q", org, proj, env, folder, key)
	}
}

// TestParseSecretPathEmptySegment 任一段为空都报错,防止半截路径泄漏。
func TestParseSecretPathEmptySegment(t *testing.T) {
	cases := []string{
		"o1.p1.dev.globals.",
		"o1.p1.dev..FOO",
		"o1.p1..globals.FOO",
		"o1..dev.globals.FOO",
		".p1.dev.globals.FOO",
	}
	for _, p := range cases {
		if _, _, _, _, _, err := parseSecretPath(p); err == nil {
			t.Fatalf("parseSecretPath(%q) should error on empty segment", p)
		}
	}
}

// TestParseSecretPathWrongSegmentCount 段数不是 5 时直接 reject。
func TestParseSecretPathWrongSegmentCount(t *testing.T) {
	cases := []string{
		"o1.p1.dev.globals",           // 4 段
		"o1.p1.dev.globals.FOO.EXTRA", // 6 段
		"o1.p1.FOO",                   // 3 段
		"",                            // 0 段
		"FOO",                         // 1 段
	}
	for _, p := range cases {
		if _, _, _, _, _, err := parseSecretPath(p); err == nil {
			t.Fatalf("parseSecretPath(%q) should error on wrong segment count", p)
		}
	}
}

// TestParseSecretPathLeadingTrailingSpace 路径两侧空白应被 TrimSpace 吃掉。
func TestParseSecretPathLeadingTrailingSpace(t *testing.T) {
	org, proj, env, folder, key, err := parseSecretPath("  o1.p1.dev.globals.FOO  ")
	if err != nil {
		t.Fatalf("parseSecretPath returned error: %v", err)
	}
	if org != "o1" || proj != "p1" || env != "dev" || folder != "globals" || key != "FOO" {
		t.Fatalf("unexpected segments after trim: %q %q %q %q %q", org, proj, env, folder, key)
	}
}

// TestParseFolderPathHappyPath 锁住 "org.proj.env.folder" 4 段解析。
func TestParseFolderPathHappyPath(t *testing.T) {
	org, proj, env, folder, err := parseFolderPath("o1.p1.dev.globals")
	if err != nil {
		t.Fatalf("parseFolderPath returned error: %v", err)
	}
	if org != "o1" || proj != "p1" || env != "dev" || folder != "globals" {
		t.Fatalf("unexpected segments: %q %q %q %q", org, proj, env, folder)
	}
}

// TestParseFolderPathEmptyAndWrongSegment 空段、3/5 段都 reject;防止半截路径泄漏到 batch reveal。
func TestParseFolderPathEmptyAndWrongSegment(t *testing.T) {
	cases := []string{
		"o1.p1.dev.globals.",          // 5 段(含空段)
		"o1.p1.dev..globals",          // 4 段含空段
		"o1.p1..globals",              // 4 段含空段(env 缺失)
		".p1.dev.globals",             // 4 段含空段(org 缺失)
		"o1.p1.dev",                   // 3 段
		"o1.p1.dev.globals.FOO.EXTRA", // 6 段
		"",                            // 0 段
		"FOO",                         // 1 段
	}
	for _, p := range cases {
		if _, _, _, _, err := parseFolderPath(p); err == nil {
			t.Fatalf("parseFolderPath(%q) should error", p)
		}
	}
}

// 上面那一坨废弃,重写:
//
// v7 起 list/search 不再走 listScope 入口校验(由 repo SQL narrowing 收窄),
// 改为直接透传 user.UserId 给 repo.ListSecrets(action="secret:list" / "secret:search")。
// 这些测试锁住"空 user.UserId → 拒绝;非空 → 透传"的契约。
func TestSecretService_List_PassesCallerUserId(t *testing.T) {
	repo := &recordingRepo{}
	svc := &secretService{repo: repo}
	filter := domain.ListFilter{EnvironmentId: "env1"}
	_, err := svc.List(context.Background(), auth.UserInfo{UserId: "u-1"}, filter, domain.Pagination{PageNum: 1, PageSize: 20})
	if err != nil {
		t.Fatalf("List with valid user should succeed, got %v", err)
	}
	if len(repo.listCalls) != 1 {
		t.Fatalf("repo.ListSecrets calls = %d, want 1", len(repo.listCalls))
	}
	c := repo.listCalls[0]
	if c.callerUserId != "u-1" {
		t.Errorf("callerUserId = %q, want u-1", c.callerUserId)
	}
	if c.action != "secret:list" {
		t.Errorf("action = %q, want secret:list", c.action)
	}
	if c.filter.EnvironmentId != "env1" {
		t.Errorf("filter.EnvironmentId = %q, want env1", c.filter.EnvironmentId)
	}
}

func TestSecretService_List_NoUserIdRejects(t *testing.T) {
	repo := &recordingRepo{}
	svc := &secretService{repo: repo}
	_, err := svc.List(context.Background(), auth.UserInfo{UserId: ""}, domain.ListFilter{EnvironmentId: "env1"}, domain.Pagination{PageNum: 1, PageSize: 20})
	if !errors.Is(err, auth.ErrPermissionDenied) {
		t.Fatalf("List with empty user should return ErrPermissionDenied, got %v", err)
	}
	if len(repo.listCalls) != 0 {
		t.Errorf("repo.ListSecrets should NOT be called when user is empty; got %d calls", len(repo.listCalls))
	}
}

func TestSecretService_Search_PassesActionSearch(t *testing.T) {
	repo := &recordingRepo{}
	svc := &secretService{repo: repo}
	filter := domain.ListFilter{EnvironmentId: "env1", Keyword: "FOO"}
	_, err := svc.Search(context.Background(), auth.UserInfo{UserId: "u-1"}, filter, domain.Pagination{PageNum: 1, PageSize: 20})
	if err != nil {
		t.Fatalf("Search with valid user should succeed, got %v", err)
	}
	if len(repo.listCalls) != 1 {
		t.Fatalf("repo.ListSecrets calls = %d, want 1", len(repo.listCalls))
	}
	c := repo.listCalls[0]
	if c.callerUserId != "u-1" {
		t.Errorf("callerUserId = %q, want u-1", c.callerUserId)
	}
	if c.action != "secret:search" {
		t.Errorf("action = %q, want secret:search", c.action)
	}
	if c.filter.Keyword != "FOO" {
		t.Errorf("filter.Keyword = %q, want FOO", c.filter.Keyword)
	}
}

func TestSecretService_Search_NoUserIdRejects(t *testing.T) {
	repo := &recordingRepo{}
	svc := &secretService{repo: repo}
	_, err := svc.Search(context.Background(), auth.UserInfo{UserId: ""}, domain.ListFilter{EnvironmentId: "env1", Keyword: "FOO"}, domain.Pagination{PageNum: 1, PageSize: 20})
	if !errors.Is(err, auth.ErrPermissionDenied) {
		t.Fatalf("Search with empty user should return ErrPermissionDenied, got %v", err)
	}
	if len(repo.listCalls) != 0 {
		t.Errorf("repo.ListSecrets should NOT be called when user is empty; got %d calls", len(repo.listCalls))
	}
}

// TestSecretService_Get_NoUserIdRejects 锁住 service 入口对空 user.UserId 的拒绝。
// 与 auth.RBACAuthorizer.Allow 的空 UserId 行为对齐:返 ErrPermissionDenied。
func TestSecretService_Get_NoUserIdRejects(t *testing.T) {
	fake := &recordingAuthorizer{} // 默认所有 Allow 返 nil
	svc := &secretService{authorizer: fake}
	_, err := svc.Get(context.Background(), auth.UserInfo{UserId: ""}, "secret-id-1")
	if !errors.Is(err, auth.ErrPermissionDenied) {
		t.Fatalf("Get with empty user should return ErrPermissionDenied, got %v", err)
	}
}

// TestSecretService_BatchRevealByPath_NoUserIdRejects 锁住 service 入口对空 user.UserId 的拒绝。
// 与 List / Search 行为对齐:空 user → 拒绝,repo 不被调用。
func TestSecretService_BatchRevealByPath_NoUserIdRejects(t *testing.T) {
	repo := &recordingRepo{}
	svc := &secretService{repo: repo}
	_, _, err := svc.BatchRevealByPath(context.Background(), auth.UserInfo{UserId: ""}, "o1.p1.dev.globals", []string{"FOO"}, "actor-1")
	if !errors.Is(err, auth.ErrPermissionDenied) {
		t.Fatalf("BatchRevealByPath with empty user should return ErrPermissionDenied, got %v", err)
	}
	if len(repo.batchRevealCalls) != 0 {
		t.Errorf("repo.BatchRevealSecretsByPath should NOT be called when user is empty; got %d calls", len(repo.batchRevealCalls))
	}
}

// TestSecretService_BatchRevealByPath_PassesCallerUserIdAndKeys 锁住:
//   - path 4 段解析正确(callerUserId / 4 个 code 全部透传给 repo)
//   - keys 透传(包括空数组/缺省的「不限」分支)
//   - action = "secret:reveal"
func TestSecretService_BatchRevealByPath_PassesCallerUserIdAndKeys(t *testing.T) {
	repo := &recordingRepo{}
	svc := &secretService{repo: repo}
	keys := []string{"DATABASE_URL", "API_KEY", "MISSING_KEY"}
	_, _, err := svc.BatchRevealByPath(context.Background(), auth.UserInfo{UserId: "u-1"}, "o1.p1.dev.globals", keys, "actor-1")
	if err != nil {
		t.Fatalf("BatchRevealByPath with valid user should succeed, got %v", err)
	}
	if len(repo.batchRevealCalls) != 1 {
		t.Fatalf("repo.BatchRevealSecretsByPath calls = %d, want 1", len(repo.batchRevealCalls))
	}
	c := repo.batchRevealCalls[0]
	if c.callerUserId != "u-1" {
		t.Errorf("callerUserId = %q, want u-1", c.callerUserId)
	}
	if c.action != "secret:reveal" {
		t.Errorf("action = %q, want secret:reveal", c.action)
	}
	if c.orgCode != "o1" || c.projectCode != "p1" || c.envCode != "dev" || c.folderCode != "globals" {
		t.Errorf("4-segment path not parsed correctly: %q %q %q %q", c.orgCode, c.projectCode, c.envCode, c.folderCode)
	}
	if !reflect.DeepEqual(c.keys, keys) {
		t.Errorf("keys = %v, want %v", c.keys, keys)
	}
}

// TestSecretService_BatchRevealByPath_EmptyKeysPassesEmpty 锁住 keys 缺省(nil)时透传 nil 给 repo,
// 配合 SQL 中 `cardinality($6::text[]) = 0` 走「不限」分支。
func TestSecretService_BatchRevealByPath_EmptyKeysPassesEmpty(t *testing.T) {
	repo := &recordingRepo{}
	svc := &secretService{repo: repo}
	_, _, err := svc.BatchRevealByPath(context.Background(), auth.UserInfo{UserId: "u-1"}, "o1.p1.dev.globals", nil, "actor-1")
	if err != nil {
		t.Fatalf("BatchRevealByPath with nil keys should succeed, got %v", err)
	}
	if len(repo.batchRevealCalls) != 1 {
		t.Fatalf("repo.BatchRevealSecretsByPath calls = %d, want 1", len(repo.batchRevealCalls))
	}
	if c := repo.batchRevealCalls[0]; len(c.keys) != 0 {
		t.Errorf("keys should be empty/nil, got %v", c.keys)
	}
}

// recordingAuthorizer 是 auth.Authorizer 的最小测试替身:记录每次 Allow 调用,
// 默认放行(nil)。空 user.UserId 视为拒绝(与真实 RBACAuthorizer 行为对齐)。
type recordingAuthorizer struct {
	calls []struct {
		user     auth.UserInfo
		action   string
		resource auth.Resource
	}
}

func (r *recordingAuthorizer) Allow(_ context.Context, user auth.UserInfo, action string, resource auth.Resource) error {
	r.calls = append(r.calls, struct {
		user     auth.UserInfo
		action   string
		resource auth.Resource
	}{user: user, action: action, resource: resource})
	if user.UserId == "" {
		return auth.ErrPermissionDenied
	}
	return nil
}

// recordingRepo 只为 List/Search 测试提供最小替身:实现 store.ResourceRepository,
// 只把 ListSecrets 的入参(callerUserId, action, filter)记下来以便断言;
// 其他方法 panic,避免被误调用。
type recordingRepo struct {
	listCalls []struct {
		callerUserId string
		action       string
		filter       domain.ListFilter
	}
	batchRevealCalls []struct {
		callerUserId string
		action       string
		orgCode      string
		projectCode  string
		envCode      string
		folderCode   string
		keys         []string
	}
	recordAuditCalls []struct {
		actor        string
		resourceType string
		resourceId   string
		action       string
	}
}

func (r *recordingRepo) ListSecrets(_ context.Context, callerUserId, action string, filter domain.ListFilter, _ domain.Pagination) (domain.PaginatedResult[domain.Secret], error) {
	r.listCalls = append(r.listCalls, struct {
		callerUserId string
		action       string
		filter       domain.ListFilter
	}{callerUserId: callerUserId, action: action, filter: filter})
	return domain.PaginatedResult[domain.Secret]{}, nil
}

func (r *recordingRepo) BatchRevealSecretsByPath(_ context.Context, callerUserId, action, orgCode, projectCode, envCode, folderCode string, keys []string) ([]domain.Secret, [][]byte, error) {
	// 拷贝 keys 避免测试间共享底层 slice。
	keysCopy := make([]string, len(keys))
	copy(keysCopy, keys)
	r.batchRevealCalls = append(r.batchRevealCalls, struct {
		callerUserId string
		action       string
		orgCode      string
		projectCode  string
		envCode      string
		folderCode   string
		keys         []string
	}{callerUserId: callerUserId, action: action, orgCode: orgCode, projectCode: projectCode, envCode: envCode, folderCode: folderCode, keys: keysCopy})
	return nil, nil, nil
}

func (r *recordingRepo) RecordAudit(_ context.Context, actor, resourceType, resourceId, action string, _ []byte) error {
	r.recordAuditCalls = append(r.recordAuditCalls, struct {
		actor        string
		resourceType string
		resourceId   string
		action       string
	}{actor: actor, resourceType: resourceType, resourceId: resourceId, action: action})
	return nil
}

// 以下未实现方法用于满足 store.ResourceRepository 接口(测试不会调到)。
func (r *recordingRepo) CreateOrganization(context.Context, string, string, string, string) (domain.Entity, error) {
	panic("not implemented")
}
func (r *recordingRepo) ListOrganizations(context.Context, string, domain.Pagination) (domain.PaginatedResult[domain.Entity], error) {
	panic("not implemented")
}
func (r *recordingRepo) GetOrganization(context.Context, string) (domain.Entity, error) {
	panic("not implemented")
}
func (r *recordingRepo) GetOrganizationByCode(context.Context, string) (domain.Entity, error) {
	panic("not implemented")
}
func (r *recordingRepo) UpdateOrganization(context.Context, string, string, string, string) (domain.Entity, error) {
	panic("not implemented")
}
func (r *recordingRepo) DeleteOrganization(context.Context, string, string, bool) (domain.CascadeScope, error) {
	panic("not implemented")
}
func (r *recordingRepo) CreateProject(context.Context, string, string, string, string, string, []domain.EnvSpec) (domain.Entity, error) {
	panic("not implemented")
}
func (r *recordingRepo) ListProjects(context.Context, string, string, domain.Pagination) (domain.PaginatedResult[domain.Entity], error) {
	panic("not implemented")
}
func (r *recordingRepo) GetProject(context.Context, string) (domain.Entity, error) {
	panic("not implemented")
}
func (r *recordingRepo) GetProjectByCode(context.Context, string, string) (domain.Entity, error) {
	panic("not implemented")
}
func (r *recordingRepo) UpdateProject(context.Context, string, string, string, string) (domain.Entity, error) {
	panic("not implemented")
}
func (r *recordingRepo) DeleteProject(context.Context, string, string) (domain.CascadeScope, error) {
	panic("not implemented")
}
func (r *recordingRepo) CreateEnvironment(context.Context, string, string, string, string, string) (domain.Entity, error) {
	panic("not implemented")
}
func (r *recordingRepo) ListEnvironments(context.Context, string, string, domain.Pagination) (domain.PaginatedResult[domain.Entity], error) {
	panic("not implemented")
}
func (r *recordingRepo) GetEnvironment(context.Context, string) (domain.Entity, error) {
	panic("not implemented")
}
func (r *recordingRepo) GetEnvironmentByCode(context.Context, string, string) (domain.Entity, error) {
	panic("not implemented")
}
func (r *recordingRepo) UpdateEnvironment(context.Context, string, string, string, string) (domain.Entity, error) {
	panic("not implemented")
}
func (r *recordingRepo) DeleteEnvironment(context.Context, string, string) (domain.CascadeScope, error) {
	panic("not implemented")
}
func (r *recordingRepo) ListEnvironmentTemplates(context.Context, string, string, domain.Pagination) (domain.PaginatedResult[domain.EnvironmentTemplate], error) {
	panic("not implemented")
}
func (r *recordingRepo) GetEnvironmentTemplate(context.Context, string) (domain.EnvironmentTemplate, error) {
	panic("not implemented")
}
func (r *recordingRepo) GetEnvironmentTemplateByCode(context.Context, string, string) (domain.EnvironmentTemplate, error) {
	panic("not implemented")
}
func (r *recordingRepo) CreateFolder(context.Context, string, string, string, string, string, string, int) (domain.Entity, error) {
	panic("not implemented")
}
func (r *recordingRepo) ListFolders(context.Context, string, string, string, domain.Pagination) (domain.PaginatedResult[domain.Entity], error) {
	panic("not implemented")
}
func (r *recordingRepo) GetFolder(context.Context, string) (domain.Entity, error) {
	panic("not implemented")
}
func (r *recordingRepo) GetFolderByCode(context.Context, string, string) (domain.Entity, error) {
	panic("not implemented")
}
func (r *recordingRepo) UpdateFolder(context.Context, string, string, string, string) (domain.Entity, error) {
	panic("not implemented")
}
func (r *recordingRepo) GetFolderContext(context.Context, string) (string, string, string, int, error) {
	panic("not implemented")
}
func (r *recordingRepo) DeleteFolder(context.Context, string, string) (domain.CascadeScope, error) {
	panic("not implemented")
}
func (r *recordingRepo) CreateSecret(context.Context, string, string, string, string, domain.SecretCiphertext) (domain.Secret, error) {
	panic("not implemented")
}
func (r *recordingRepo) GetSecret(context.Context, string) (domain.Secret, error) {
	panic("not implemented")
}
func (r *recordingRepo) GetSecretByKey(context.Context, string, string) (domain.Secret, error) {
	panic("not implemented")
}
func (r *recordingRepo) GetSecretByPath(context.Context, string, string, string, string, string) (domain.Secret, error) {
	panic("not implemented")
}
func (r *recordingRepo) GetSecretCiphertext(context.Context, string) (domain.Secret, domain.SecretCiphertext, error) {
	panic("not implemented")
}
func (r *recordingRepo) UpdateSecret(context.Context, string, string, string, string, domain.SecretCiphertext) (domain.Secret, error) {
	panic("not implemented")
}
func (r *recordingRepo) DeleteSecret(context.Context, string, string) error {
	panic("not implemented")
}
func (r *recordingRepo) ListSecretCacheRecords(context.Context) ([]domain.SecretCacheRecord, error) {
	panic("not implemented")
}
func (r *recordingRepo) ListAuditRecords(context.Context, string, string, domain.Pagination) (domain.PaginatedResult[domain.AuditRecord], error) {
	panic("not implemented")
}
func (r *recordingRepo) CacheUserLabel(string, string) {
	panic("not implemented")
}
func (r *recordingRepo) ListAllOrganizationsForTree(context.Context, string) ([]domain.Entity, error) {
	panic("not implemented")
}
func (r *recordingRepo) ListAllProjectsForTree(context.Context, string) ([]domain.Entity, error) {
	panic("not implemented")
}
func (r *recordingRepo) ListAllEnvironmentsForTree(context.Context, string) ([]domain.Entity, error) {
	panic("not implemented")
}
func (r *recordingRepo) ListAllFoldersForTree(context.Context, string) ([]domain.FolderTreeEntry, error) {
	panic("not implemented")
}
