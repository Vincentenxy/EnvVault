package service

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"envVault/internal/auth"
	secretcrypto "envVault/internal/crypto"
	"envVault/internal/domain"
	"envVault/internal/store"
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
	if len(repo.searchCalls) != 1 {
		t.Fatalf("repo.ListSecretsWithCiphertext calls = %d, want 1", len(repo.searchCalls))
	}
	c := repo.searchCalls[0]
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

// TestSecretService_Search_ScopePriority 锁住 v11 search 的"三选一"优先级:
// folderId > environmentId > projectId。Keyword 必须原样透传,scope 之外的字段不动。
func TestSecretService_Search_ScopePriority(t *testing.T) {
	cases := []struct {
		name        string
		in          domain.ListFilter
		wantFolder  string
		wantEnv     string
		wantProject string
		wantKeyword string
	}{
		{
			name:        "folder wins over env and project",
			in:          domain.ListFilter{FolderId: "f1", EnvironmentId: "e1", ProjectId: "p1", Keyword: "K"},
			wantFolder:  "f1",
			wantEnv:     "",
			wantProject: "",
			wantKeyword: "K",
		},
		{
			name:        "env wins over project when folder empty",
			in:          domain.ListFilter{EnvironmentId: "e1", ProjectId: "p1", Keyword: "K"},
			wantFolder:  "",
			wantEnv:     "e1",
			wantProject: "",
			wantKeyword: "K",
		},
		{
			name:        "project kept when only project set",
			in:          domain.ListFilter{ProjectId: "p1", Keyword: "K"},
			wantFolder:  "",
			wantEnv:     "",
			wantProject: "p1",
			wantKeyword: "K",
		},
		{
			name:        "all empty scopes preserved (no narrowing)",
			in:          domain.ListFilter{Keyword: "K"},
			wantFolder:  "",
			wantEnv:     "",
			wantProject: "",
			wantKeyword: "K",
		},
		{
			name:        "empty keyword preserved as-is (means full scan in scope)",
			in:          domain.ListFilter{ProjectId: "p1"},
			wantFolder:  "",
			wantEnv:     "",
			wantProject: "p1",
			wantKeyword: "",
		},
		{
			name:        "folder id with whitespace treated as empty (env kicks in)",
			in:          domain.ListFilter{FolderId: "   ", EnvironmentId: "e1"},
			wantFolder:  "",
			wantEnv:     "e1",
			wantProject: "",
			wantKeyword: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := &recordingRepo{}
			svc := &secretService{repo: repo}
			_, err := svc.Search(context.Background(), auth.UserInfo{UserId: "u-1"}, tc.in, domain.Pagination{PageNum: 1, PageSize: 20})
			if err != nil {
				t.Fatalf("Search should succeed, got %v", err)
			}
			if len(repo.searchCalls) != 1 {
				t.Fatalf("repo.ListSecretsWithCiphertext calls = %d, want 1", len(repo.searchCalls))
			}
			got := repo.searchCalls[0].filter
			if got.FolderId != tc.wantFolder {
				t.Errorf("FolderId = %q, want %q", got.FolderId, tc.wantFolder)
			}
			if got.EnvironmentId != tc.wantEnv {
				t.Errorf("EnvironmentId = %q, want %q", got.EnvironmentId, tc.wantEnv)
			}
			if got.ProjectId != tc.wantProject {
				t.Errorf("ProjectId = %q, want %q", got.ProjectId, tc.wantProject)
			}
			if got.Keyword != tc.wantKeyword {
				t.Errorf("Keyword = %q, want %q", got.Keyword, tc.wantKeyword)
			}
		})
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

// makeEncryptedPayload 构造一段 SecretCiphertext 的 JSON,供 recordingRepo
// 模拟"已加密的 secret 值"。service 端解密会用真实 encryptor;这里我们
// 直接走 svc.encrypt 生成一个真实可解密的 ciphertext,免去 mock 解密逻辑。
func makeEncryptedPayload(t *testing.T, svc *secretService, plaintext string) []byte {
	t.Helper()
	if svc.encryptor == nil {
		t.Fatalf("svc.encryptor is nil; need real encryptor for fixture")
	}
	ct, err := svc.encrypt(context.Background(), plaintext)
	if err != nil {
		t.Fatalf("encrypt fixture: %v", err)
	}
	raw, err := json.Marshal(ct)
	if err != nil {
		t.Fatalf("marshal fixture: %v", err)
	}
	return raw
}

// TestSecretService_Search_ProjectScope_AggregatesValues 锁住 v12 project 维度 search
// 的聚合行为:同一 (folderCode, key) 跨多 env 合并为一条 *domain.SecretGroup,Envs
// 累积为 {envCode: <Secret metadata>};顶层 Code 字段 = secret key;**内层
// Secret 不填 values / value 字段**——project 维度 search 是「跨 env 浏览」
// 语义,前端通过顶层 envCode keys 拿存在性 + 元数据,明文走 reveal 单点接口。
//
// 关键点:跨 env 时每个 env 有自己的 folderId(因为 env 是 folder 的父,folder
// 行持 environment_id,跨 env 不复用),所以分组 key 用 (folderCode, key)
// 而非 (folderId, key)——只有 folderCode 在「同一逻辑 folder 跨 env」语义
// 下是稳定的,这样 dev/test/sim/prod 下 folderCode="ana-svc"、key="URL"
// 的 4 条 secret 才合并为 1 个 group(而非 4 个)。
func TestSecretService_Search_ProjectScope_AggregatesValues(t *testing.T) {
	repo := &recordingRepo{}
	enc, err := secretcrypto.NewAESGCMEncryptorFromBase64("MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=")
	if err != nil {
		t.Fatalf("encryptor: %v", err)
	}
	svc := &secretService{repo: repo, authorizer: &recordingAuthorizer{}, encryptor: enc}
	ctDev := makeEncryptedPayload(t, svc, "dev-val")
	ctTest := makeEncryptedPayload(t, svc, "test-val")
	ctSim := makeEncryptedPayload(t, svc, "sim-val")
	ctProd := makeEncryptedPayload(t, svc, "prod-val")

	// 4 个 env 各自有 folderId 不一样的 folder("ana-svc"),共享同一 folderCode
	// 和同一 secret key "URL"——这是用户期望的「跨 env 同名 folder + 同 key 合并
	// 为 1 组」场景。再加一条 folderCode="globals" 的不应合并到本组。
	repo.searchWithCiphertextResult = domain.PaginatedResult[domain.SecretCacheRecord]{
		Items: []domain.SecretCacheRecord{
			{Secret: domain.Secret{Id: "s-dev", FolderId: "F-dev", FolderCode: "ana-svc", Key: "URL", EnvironmentCode: "dev"}, ValueCiphertext: ctDev},
			{Secret: domain.Secret{Id: "s-test", FolderId: "F-test", FolderCode: "ana-svc", Key: "URL", EnvironmentCode: "test"}, ValueCiphertext: ctTest},
			{Secret: domain.Secret{Id: "s-sim", FolderId: "F-sim", FolderCode: "ana-svc", Key: "URL", EnvironmentCode: "sim"}, ValueCiphertext: ctSim},
			{Secret: domain.Secret{Id: "s-prod", FolderId: "F-prod", FolderCode: "ana-svc", Key: "URL", EnvironmentCode: "prod"}, ValueCiphertext: ctProd},
			{Secret: domain.Secret{Id: "s-globals", FolderId: "F-globals", FolderCode: "globals", Key: "URL", EnvironmentCode: "dev"}, ValueCiphertext: ctDev},
		},
		Total: 5,
	}

	result, err := svc.Search(context.Background(), auth.UserInfo{UserId: "u-1"},
		domain.ListFilter{ProjectId: "P1"}, domain.Pagination{PageNum: 1, PageSize: 20})
	if err != nil {
		t.Fatalf("Search project scope should succeed, got %v", err)
	}
	// 期望 2 组:ana-svc/URL(dev+test+sim+prod 合并),globals/URL(dev)。
	if len(result.Items) != 2 {
		t.Fatalf("got %d groups, want 2 (ana-svc/URL + globals/URL)", len(result.Items))
	}

	// 第一组 ana-svc/URL:Envs 应包含 4 个 env(不同 folderId 但同一 folderCode)。
	g1, ok := result.Items[0].(*domain.SecretGroup)
	if !ok {
		t.Fatalf("result.Items[0] type = %T, want *domain.SecretGroup", result.Items[0])
	}
	if g1.Key != "URL" {
		t.Errorf("group[0].Key = %q, want URL", g1.Key)
	}
	if len(g1.Envs) != 4 {
		t.Fatalf("group[0].Envs has %d envs, want 4 (dev/test/sim/prod)", len(g1.Envs))
	}
	for _, envCode := range []string{"dev", "test", "sim", "prod"} {
		envSecret, exists := g1.Envs[envCode]
		if !exists {
			t.Errorf("group[0].Envs missing %q key", envCode)
			continue
		}
		// 关键断言:每个 env 的 Secret.folderId 不同(因为跨 env 各自有 folder),
		// 但 folderCode 都是 "ana-svc"——这正是「按 folderCode 聚合」想要的效果。
		if envSecret.FolderCode != "ana-svc" || envSecret.Key != "URL" || envSecret.EnvironmentCode != envCode {
			t.Errorf("group[0].Envs[%s] = folderCode=%s key=%s envCode=%s, want ana-svc/URL/%s",
				envCode, envSecret.FolderCode, envSecret.Key, envSecret.EnvironmentCode, envCode)
		}
		// 关键断言:project 维度 search 不填内层 Secret.values / value 字段——
		// JSON 序列化时这两个字段(omitempty)应该不出现。
		if len(envSecret.Values) != 0 {
			t.Errorf("group[0].Envs[%s].Values should be empty (project scope never fills values), got %v",
				envCode, envSecret.Values)
		}
		if envSecret.Value != "" {
			t.Errorf("group[0].Envs[%s].Value should be empty, got %q", envCode, envSecret.Value)
		}
	}

	// 第二组 globals/URL:Envs 仅有 dev。
	g2, ok := result.Items[1].(*domain.SecretGroup)
	if !ok {
		t.Fatalf("result.Items[1] type = %T, want *domain.SecretGroup", result.Items[1])
	}
	if g2.Key != "URL" {
		t.Errorf("group[1].Key = %q, want URL", g2.Key)
	}
	if len(g2.Envs) != 1 {
		t.Fatalf("group[1].Envs has %d envs, want 1 (dev)", len(g2.Envs))
	}
	devSecret, ok := g2.Envs["dev"]
	if !ok {
		t.Fatalf("group[1].Envs[dev] missing")
	}
	if devSecret.FolderCode != "globals" {
		t.Errorf("group[1].Envs[dev].FolderCode = %q, want globals", devSecret.FolderCode)
	}
	if len(devSecret.Values) != 0 {
		t.Errorf("group[1].Envs[dev].Values should be empty, got %v", devSecret.Values)
	}
}

// TestSecretService_Search_ProjectScope_NoValuesEvenWithRevealPerm 锁住 v12:
// project 维度 search 不管用户有没有 secret:reveal 权限,内层 Secret 都不填 values / value。
// 与 env/folder 维度(有 reveal → 填 Values map)形成对比,体现 project 维度的「浏览语义」:
// 前端拿到 group 后用顶层 envCode 键判断存在性,再走 reveal 单点接口拿明文。
func TestSecretService_Search_ProjectScope_NoValuesEvenWithRevealPerm(t *testing.T) {
	repo := &recordingRepo{}
	enc, err := secretcrypto.NewAESGCMEncryptorFromBase64("MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=")
	if err != nil {
		t.Fatalf("encryptor: %v", err)
	}
	ct := makeEncryptedPayload(t, svcForFixture(enc, repo), "real-val")

	// 用全放行的 authorizer(默认有 secret:reveal 权限)——仍不应填充 values。
	svc := &secretService{repo: repo, authorizer: &recordingAuthorizer{}, encryptor: enc}
	repo.searchWithCiphertextResult = domain.PaginatedResult[domain.SecretCacheRecord]{
		Items: []domain.SecretCacheRecord{
			{Secret: domain.Secret{Id: "s1", FolderId: "F1", FolderCode: "ana-svc", Key: "URL", EnvironmentCode: "dev"}, ValueCiphertext: ct},
		},
		Total: 1,
	}

	result, err := svc.Search(context.Background(), auth.UserInfo{UserId: "u-1"},
		domain.ListFilter{ProjectId: "P1"}, domain.Pagination{PageNum: 1, PageSize: 20})
	if err != nil {
		t.Fatalf("Search should not fail: %v", err)
	}
	if len(result.Items) != 1 {
		t.Fatalf("got %d groups, want 1", len(result.Items))
	}
	g, ok := result.Items[0].(*domain.SecretGroup)
	if !ok {
		t.Fatalf("result.Items[0] type = %T, want *domain.SecretGroup", result.Items[0])
	}
	if g.Key != "URL" {
		t.Errorf("group.Key = %q, want URL", g.Key)
	}
	devSecret, exists := g.Envs["dev"]
	if !exists {
		t.Fatalf("group.Envs[dev] should still exist")
	}
	// 关键断言:即便有 reveal 权限,project 维度 search 也不解密、不填 values。
	if len(devSecret.Values) != 0 {
		t.Errorf("group.Envs[dev].Values should be empty in project scope, got %v", devSecret.Values)
	}
	if devSecret.Value != "" {
		t.Errorf("group.Envs[dev].Value should be empty, got %q", devSecret.Value)
	}
}

// TestSecretService_Search_EnvScope_ValuesSingleEntry 锁住 env/folder 维度的 Values
// 形态:items 元素是 domain.Secret(非 SecretGroup),Values 是单 entry map,key = 当前 envCode。
func TestSecretService_Search_EnvScope_ValuesSingleEntry(t *testing.T) {
	repo := &recordingRepo{}
	enc, err := secretcrypto.NewAESGCMEncryptorFromBase64("MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=")
	if err != nil {
		t.Fatalf("encryptor: %v", err)
	}
	ct := makeEncryptedPayload(t, svcForFixture(enc, repo), "env-val")

	svc := &secretService{repo: repo, authorizer: &recordingAuthorizer{}, encryptor: enc}
	repo.searchWithCiphertextResult = domain.PaginatedResult[domain.SecretCacheRecord]{
		Items: []domain.SecretCacheRecord{
			{Secret: domain.Secret{Id: "s1", FolderId: "F1", Key: "URL", EnvironmentCode: "dev"}, ValueCiphertext: ct},
			{Secret: domain.Secret{Id: "s2", FolderId: "F1", Key: "DB", EnvironmentCode: "dev"}, ValueCiphertext: ct},
		},
		Total: 2,
	}

	result, err := svc.Search(context.Background(), auth.UserInfo{UserId: "u-1"},
		domain.ListFilter{EnvironmentId: "E1"}, domain.Pagination{PageNum: 1, PageSize: 20})
	if err != nil {
		t.Fatalf("Search env scope should succeed, got %v", err)
	}
	if len(result.Items) != 2 {
		t.Fatalf("got %d items, want 2 (no aggregation in env scope)", len(result.Items))
	}
	for i := range result.Items {
		// env/folder 维度:items 元素必须是 domain.Secret,不是 SecretGroup。
		sec, ok := result.Items[i].(domain.Secret)
		if !ok {
			t.Fatalf("items[%d] type = %T, want domain.Secret (env scope should NOT be SecretGroup)", i, result.Items[i])
		}
		if len(sec.Values) != 1 {
			t.Errorf("items[%d].Values has %d entries, want 1", i, len(sec.Values))
		}
		if v, ok := sec.Values["dev"]; !ok || v != "env-val" {
			t.Errorf("items[%d].Values[dev] = %q, want env-val", i, v)
		}
	}
}

// svcForFixture 是上面 3 个测试共用的"建 svc 助手":传入 encryptor 和 repo,返
// 回一个可调用 encrypt 的 svc(makeEncryptedPayload 需要它)。enc 与 svc 用同一
// encryptor,保证密文可以被 svc.decrypt 解开。
func svcForFixture(enc secretcrypto.Encryptor, repo *recordingRepo) *secretService {
	return &secretService{repo: repo, authorizer: &recordingAuthorizer{}, encryptor: enc}
}

// denyAllAuthorizer 拒绝所有 Allow 请求,模拟"用户无 secret:reveal 权限"。
type denyAllAuthorizer struct{}

func (denyAllAuthorizer) Allow(_ context.Context, _ auth.UserInfo, _ string, _ auth.Resource) error {
	return auth.ErrPermissionDenied
}

// TestSecretGroup_MarshalJSON_FlattensEnvs 锁住 v12 SecretGroup 的 JSON 序列化:
// 顶层 code 字段 + 各 envCode 展平为顶层键,每个 envCode 对应一个完整 Secret。
// 与产品要求「使用 projectId 查询时返回 {key, <envCode>: {...}}」一一对应。
func TestSecretGroup_MarshalJSON_FlattensEnvs(t *testing.T) {
	group := domain.SecretGroup{
		Key: "OB_USER_11111111111",
		Envs: map[string]domain.Secret{
			"dev":  {Id: "s-dev", Key: "OB_USER_11111111111", EnvironmentCode: "dev", FolderCode: "ana-svc", ProjectCode: "proj-09", OrgCode: "org-01"},
			"test": {Id: "s-test", Key: "OB_USER_11111111111", EnvironmentCode: "test", FolderCode: "ana-svc", ProjectCode: "proj-09", OrgCode: "org-01"},
		},
	}
	raw, err := json.Marshal(group)
	if err != nil {
		t.Fatalf("MarshalJSON failed: %v", err)
	}
	// 反序列化为 generic map,确认顶层键形态。
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("unmarshal back: %v", err)
	}
	if _, ok := m["key"]; !ok {
		t.Errorf("top-level key 'key' missing, got keys: %v", keysOf(m))
	}
	if _, ok := m["dev"]; !ok {
		t.Errorf("top-level envCode 'dev' missing, got keys: %v", keysOf(m))
	}
	if _, ok := m["test"]; !ok {
		t.Errorf("top-level envCode 'test' missing, got keys: %v", keysOf(m))
	}
	// 反序列化 key 字符串确认内容(应该等于 secret key,与数据库 secrets.key 对齐)。
	var key string
	if err := json.Unmarshal(m["key"], &key); err != nil {
		t.Fatalf("unmarshal key: %v", err)
	}
	if key != "OB_USER_11111111111" {
		t.Errorf("key = %q, want OB_USER_11111111111", key)
	}
	// dev 内部应有完整 Secret 字段。
	var devEnv domain.Secret
	if err := json.Unmarshal(m["dev"], &devEnv); err != nil {
		t.Fatalf("unmarshal dev env: %v", err)
	}
	if devEnv.Id != "s-dev" || devEnv.EnvironmentCode != "dev" {
		t.Errorf("dev env = id=%s envCode=%s, want s-dev/dev", devEnv.Id, devEnv.EnvironmentCode)
	}
}

func keysOf(m map[string]json.RawMessage) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
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
// 可选 denyFolderIds:对列出的 folderId 返 ErrPermissionDenied,用于模拟
// batchCreate 中"部分 folder 缺权限"的场景。
type recordingAuthorizer struct {
	calls []struct {
		user     auth.UserInfo
		action   string
		resource auth.Resource
	}
	denyFolderIds map[string]bool
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
	if r.denyFolderIds != nil && resource.Type == "folder" && r.denyFolderIds[resource.Id] {
		return auth.ErrPermissionDenied
	}
	return nil
}

// recordingRepo 只为 List/Search 测试提供最小替身:实现 store.ResourceRepository,
// 只把 ListSecrets / ListSecretsWithCiphertext 的入参(callerUserId, action, filter)记下来以便断言;
// 其他方法 panic,避免被误调用。
type recordingRepo struct {
	listCalls []struct {
		callerUserId string
		action       string
		filter       domain.ListFilter
	}
	searchCalls []struct {
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
	listAcrossEnvsCalls []struct {
		callerUserId string
		action       string
		projectId    string
		folderCode   string
		key          string
		envCodes     []string
	}
	// listAcrossEnvsSecrets / listAcrossEnvsCiphertexts 是 ListSecretsByProjectFolderKey
	// 的预设返回值,默认 nil → mock 返 (nil, nil, nil)。
	listAcrossEnvsSecrets     []domain.Secret
	listAcrossEnvsCiphertexts [][]byte
	listInProjectCalls        []struct {
		callerUserId string
		action       string
		projectId    string
		folderCode   string
		envCodes     []string
	}
	// listInProjectSecrets / listInProjectCiphertexts 是 ListSecretsInProjectByEnvs
	// 的预设返回值,默认 nil → mock 返 (nil, nil, nil)。
	listInProjectSecrets     []domain.Secret
	listInProjectCiphertexts [][]byte
	recordAuditCalls         []struct {
		actor        string
		resourceType string
		resourceId   string
		action       string
	}
	batchCreateCalls [][]store.BatchCreateSecretItem
	batchCreateErr   error
	// searchWithCiphertextResult 是 ListSecretsWithCiphertext 的预设返回值,
	// 测试在创建 repo 时填好,Search 内部会原样透传给聚合函数。
	searchWithCiphertextResult domain.PaginatedResult[domain.SecretCacheRecord]
}

func (r *recordingRepo) ListSecrets(_ context.Context, callerUserId, action string, filter domain.ListFilter, _ domain.Pagination) (domain.PaginatedResult[domain.Secret], error) {
	r.listCalls = append(r.listCalls, struct {
		callerUserId string
		action       string
		filter       domain.ListFilter
	}{callerUserId: callerUserId, action: action, filter: filter})
	return domain.PaginatedResult[domain.Secret]{}, nil
}

func (r *recordingRepo) ListSecretsWithCiphertext(_ context.Context, callerUserId, action string, filter domain.ListFilter, _ domain.Pagination) (domain.PaginatedResult[domain.SecretCacheRecord], error) {
	r.searchCalls = append(r.searchCalls, struct {
		callerUserId string
		action       string
		filter       domain.ListFilter
	}{callerUserId: callerUserId, action: action, filter: filter})
	return r.searchWithCiphertextResult, nil
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

func (r *recordingRepo) ListSecretsByProjectFolderKey(_ context.Context, callerUserId, action, projectId, folderCode, key string, envCodes []string) ([]domain.Secret, [][]byte, error) {
	// 拷贝 envCodes 避免测试间共享底层 slice。
	envCopy := make([]string, len(envCodes))
	copy(envCopy, envCodes)
	r.listAcrossEnvsCalls = append(r.listAcrossEnvsCalls, struct {
		callerUserId string
		action       string
		projectId    string
		folderCode   string
		key          string
		envCodes     []string
	}{callerUserId: callerUserId, action: action, projectId: projectId, folderCode: folderCode, key: key, envCodes: envCopy})
	return r.listAcrossEnvsSecrets, r.listAcrossEnvsCiphertexts, nil
}

func (r *recordingRepo) ListSecretsInProjectByEnvs(_ context.Context, callerUserId, action, projectId, folderCode string, envCodes []string) ([]domain.Secret, [][]byte, error) {
	envCopy := make([]string, len(envCodes))
	copy(envCopy, envCodes)
	r.listInProjectCalls = append(r.listInProjectCalls, struct {
		callerUserId string
		action       string
		projectId    string
		folderCode   string
		envCodes     []string
	}{callerUserId: callerUserId, action: action, projectId: projectId, folderCode: folderCode, envCodes: envCopy})
	return r.listInProjectSecrets, r.listInProjectCiphertexts, nil
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

// =====================================================================
// ListAcrossEnvs 行为测试(参数透传到 repo)
// =====================================================================

// fakeSecretService 构造一个最小可用的 SecretService 用来跑 ListAcrossEnvs 的
// 参数透传测试。repo 是 recordingRepo,只记录调用入参,不做真实 DB 操作。
// recordingAuthorizer 默认 Allow 放行;fakeEncryptor 走文件下方的真 fake 实现。
func fakeSecretService(t *testing.T) (*secretService, *recordingRepo, auth.UserInfo) {
	t.Helper()
	repo := &recordingRepo{}
	svc := &secretService{
		repo:       repo,
		authorizer: &recordingAuthorizer{},
		encryptor:  fakeEncryptor{},
	}
	return svc, repo, auth.UserInfo{UserId: "u-tester"}
}

// fakeEncryptor 在文件下方定义,本测试直接复用。

// TestListAcrossEnvs_KeyEmpty_FolderCodeProvided 锁住 v12 bug fix:
// key 为空时,folderCode 仍要作为过滤条件传给 repo(SQL 层会做
// `($5::text = ” or f.code = $5)` 兜底)。修复前 service 把 folderCode 丢了,
// 修复后必须把 folderCode 透传到 ListSecretsInProjectByEnvs。
// 同时锁住 v12 permission model:repo action 必须是 "secret:read"(原 reveal)。
func TestListAcrossEnvs_KeyEmpty_FolderCodeProvided(t *testing.T) {
	svc, repo, user := fakeSecretService(t)
	_, err := svc.ListAcrossEnvs(context.Background(), user,
		"p-uuid", "ana-svc", "", []string{"dev", "test", "sim", "prod"}, "actor-x")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(repo.listInProjectCalls) != 1 {
		t.Fatalf("expected 1 ListSecretsInProjectByEnvs call, got %d", len(repo.listInProjectCalls))
	}
	call := repo.listInProjectCalls[0]
	if call.action != "secret:read" {
		t.Errorf("action = %q, want %q (v12 起改用 secret:read 收窄)", call.action, "secret:read")
	}
	if call.folderCode != "ana-svc" {
		t.Errorf("folderCode = %q, want %q (must be propagated when key is empty)", call.folderCode, "ana-svc")
	}
	if call.projectId != "p-uuid" {
		t.Errorf("projectId = %q, want p-uuid", call.projectId)
	}
	if got := call.envCodes; len(got) != 4 || got[0] != "dev" || got[3] != "prod" {
		t.Errorf("envCodes = %v, want [dev test sim prod]", got)
	}
}

// TestListAcrossEnvs_KeyEmpty_FolderCodeEmpty 锁住 key+folderCode 都空时
// service 走「项目下所有 (folder, key)」分支,folderCode 透传空串(由 SQL 兜底)。
func TestListAcrossEnvs_KeyEmpty_FolderCodeEmpty(t *testing.T) {
	svc, repo, user := fakeSecretService(t)
	_, err := svc.ListAcrossEnvs(context.Background(), user,
		"p-uuid", "", "", []string{"dev"}, "actor-x")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(repo.listInProjectCalls) != 1 {
		t.Fatalf("expected 1 ListSecretsInProjectByEnvs call, got %d", len(repo.listInProjectCalls))
	}
	call := repo.listInProjectCalls[0]
	if call.action != "secret:read" {
		t.Errorf("action = %q, want %q (v12 起改用 secret:read 收窄)", call.action, "secret:read")
	}
	if got := call.folderCode; got != "" {
		t.Errorf("folderCode = %q, want empty (caller 不传时 SQL 走 $5::text='' 兜底)", got)
	}
}

// TestListAcrossEnvs_KeyProvided 锁住 key 非空时走「精确」分支(folderCode 必传,
// 走 ListSecretsByProjectFolderKey 而非 ListSecretsInProjectByEnvs)。
func TestListAcrossEnvs_KeyProvided(t *testing.T) {
	svc, repo, user := fakeSecretService(t)
	_, err := svc.ListAcrossEnvs(context.Background(), user,
		"p-uuid", "ana-svc", "DATABASE_URL", []string{"dev"}, "actor-x")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(repo.listAcrossEnvsCalls) != 1 {
		t.Fatalf("expected 1 ListSecretsByProjectFolderKey call, got %d", len(repo.listAcrossEnvsCalls))
	}
	call := repo.listAcrossEnvsCalls[0]
	if call.action != "secret:read" {
		t.Errorf("action = %q, want %q (v12 起改用 secret:read 收窄)", call.action, "secret:read")
	}
	if len(repo.listInProjectCalls) != 0 {
		t.Errorf("key 非空不应走 ListSecretsInProjectByEnvs,got %d calls", len(repo.listInProjectCalls))
	}
}

// TestListAcrossEnvs_EnvSecretValueFolderId 锁住 EnvSecretValue.FolderId 字段被填充。
// 跨 env 时每个 env 有自己的 folderId(folder 行持 environment_id,不跨 env 共享),
// 4 个 env 共享 folderCode="ana-svc" 但 folderId 各自不同。secretService 在构造
// EnvSecretValue 时必须从 secret.FolderId 透传,前端才能直接通过响应知道该 env 下
// secret 实际所在的 folder。
func TestListAcrossEnvs_EnvSecretValueFolderId(t *testing.T) {
	svc, repo, user := fakeSecretService(t)

	wantFolderIds := map[string]string{
		"dev":  "F-dev",
		"test": "F-test",
		"sim":  "F-sim",
		"prod": "F-prod",
	}
	secrets := make([]domain.Secret, 0, len(wantFolderIds))
	ciphertexts := make([][]byte, 0, len(wantFolderIds))
	for _, envCode := range []string{"dev", "test", "sim", "prod"} {
		ct, err := svc.encryptor.Encrypt(context.Background(), []byte("v-"+envCode))
		if err != nil {
			t.Fatalf("encrypt fixture: %v", err)
		}
		raw, err := json.Marshal(ct)
		if err != nil {
			t.Fatalf("marshal fixture: %v", err)
		}
		secrets = append(secrets, domain.Secret{
			Id:              "s-" + envCode,
			ProjectCode:     "p1",
			FolderId:        wantFolderIds[envCode],
			FolderCode:      "ana-svc",
			Key:             "DATABASE_URL",
			EnvironmentCode: envCode,
			Version:         1,
			Comment:         "c-" + envCode,
		})
		ciphertexts = append(ciphertexts, raw)
	}
	repo.listAcrossEnvsSecrets = secrets
	repo.listAcrossEnvsCiphertexts = ciphertexts

	result, err := svc.ListAcrossEnvs(context.Background(), user,
		"p-uuid", "ana-svc", "DATABASE_URL", []string{"dev", "test", "sim", "prod"}, "actor-x")
	if err != nil {
		t.Fatalf("ListAcrossEnvs: %v", err)
	}
	if len(result) != 1 {
		t.Fatalf("got %d groups, want 1", len(result))
	}
	group := result[0]
	if len(group.Envs) != 4 {
		t.Fatalf("group.Envs has %d envs, want 4", len(group.Envs))
	}
	for envCode, want := range wantFolderIds {
		v, ok := group.Envs[envCode]
		if !ok {
			t.Errorf("group.Envs missing %q", envCode)
			continue
		}
		if v == nil {
			t.Errorf("group.Envs[%q] is nil, want populated value", envCode)
			continue
		}
		if v.FolderId != want {
			t.Errorf("group.Envs[%q].FolderId = %q, want %q", envCode, v.FolderId, want)
		}
	}
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
func (r *recordingRepo) ListFolderChildren(context.Context, string, []string) (map[string][]domain.Entity, error) {
	panic("not implemented")
}
func (r *recordingRepo) GetFolder(_ context.Context, id string) (domain.Entity, error) {
	return domain.Entity{Id: id, Code: "globals"}, nil
}

func (r *recordingRepo) GetFolderContext(_ context.Context, id string) (string, string, string, int, error) {
	return "env-1", "proj-1", "", 1, nil
}

func (r *recordingRepo) GetFolderByCode(context.Context, string, string) (domain.Entity, error) {
	panic("not implemented")
}
func (r *recordingRepo) UpdateFolder(context.Context, string, string, string, string) (domain.Entity, error) {
	panic("not implemented")
}
func (r *recordingRepo) DeleteFolder(context.Context, string, string) (domain.CascadeScope, error) {
	panic("not implemented")
}
func (r *recordingRepo) CreateSecret(context.Context, string, string, string, string, domain.SecretCiphertext) (domain.Secret, error) {
	panic("not implemented")
}
func (r *recordingRepo) BatchCreateSecrets(_ context.Context, items []store.BatchCreateSecretItem) ([]domain.Secret, error) {
	r.batchCreateCalls = append(r.batchCreateCalls, items)
	if r.batchCreateErr != nil {
		return nil, r.batchCreateErr
	}
	// 成功路径:返回与 items 数量相同的 stub secret(无 path/codes,只占位)
	out := make([]domain.Secret, len(items))
	for i, it := range items {
		out[i] = domain.Secret{
			Id:          "secret-" + it.Key,
			FolderId:    it.FolderId,
			Key:         it.Key,
			OrgCode:     "o1",
			ProjectCode: "p1",
		}
	}
	return out, nil
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
func (r *recordingRepo) ListFoldersInProject(context.Context, string, string) ([]domain.FolderInProject, error) {
	panic("not implemented")
}
func (r *recordingRepo) CreateFoldersAcrossEnvs(context.Context, string, string, string, string, string, []string) ([]domain.Entity, error) {
	panic("not implemented")
}
func (r *recordingRepo) CreateTopLevelFoldersInEnvs(context.Context, string, string, string, string, []string) ([]domain.Entity, error) {
	panic("not implemented")
}

// =====================================================================
// v11: SecretService.BatchCreate 测试
// =====================================================================

// TestBatchCreate_Success 锁住 happy path:
//   - 4 个 env × 1 key 展开为 4 个 target
//   - 逐条加密 + 权限 check 通过
//   - repo.BatchCreateSecrets 拿到 N 条 item(folderId / key / comment 透传)
//   - 成功时返 nil(不返 response struct)
func TestBatchCreate_Success(t *testing.T) {
	repo := &recordingRepo{}
	svc := &secretService{
		repo:       repo,
		authorizer: &recordingAuthorizer{}, // 默认放行
		encryptor:  fakeEncryptor{},
	}
	req := BatchCreateRequest{
		SecretList: []BatchCreateSecretSpec{
			{
				Key:     "DATABASE_URL",
				Comment: "db url",
				Envs: []BatchCreateEnvTarget{
					{EnvCode: "dev", FolderId: "folder-dev", Value: "d"},
					{EnvCode: "test", FolderId: "folder-test", Value: "t"},
					{EnvCode: "sim", FolderId: "folder-sim", Value: "s"},
					{EnvCode: "prod", FolderId: "folder-prod", Value: "p"},
				},
			},
		},
	}
	if err := svc.BatchCreate(context.Background(), auth.UserInfo{UserId: "u-1"}, req, "actor-1"); err != nil {
		t.Fatalf("BatchCreate happy path should succeed, got %v", err)
	}
	if len(repo.batchCreateCalls) != 1 {
		t.Fatalf("repo.BatchCreateSecrets calls = %d, want 1", len(repo.batchCreateCalls))
	}
	items := repo.batchCreateCalls[0]
	if len(items) != 4 {
		t.Fatalf("batch items = %d, want 4 (4 envs × 1 key)", len(items))
	}
	// 验证顺序:dev → test → sim → prod
	expected := []struct{ folderId, key, comment string }{
		{"folder-dev", "DATABASE_URL", "db url"},
		{"folder-test", "DATABASE_URL", "db url"},
		{"folder-sim", "DATABASE_URL", "db url"},
		{"folder-prod", "DATABASE_URL", "db url"},
	}
	for i, want := range expected {
		if items[i].FolderId != want.folderId || items[i].Key != want.key || items[i].Comment != want.comment {
			t.Errorf("items[%d] = {FolderId:%q, Key:%q, Comment:%q}, want {FolderId:%q, Key:%q, Comment:%q}",
				i, items[i].FolderId, items[i].Key, items[i].Comment, want.folderId, want.key, want.comment)
		}
	}
}

// TestBatchCreate_PermissionDenied 锁住 authorizer 拒绝 → 整批拒绝。
// 模拟某个 env 的 folder 上 secret:create 失败,任一 target 拒绝即全部 rollback(不写库)。
func TestBatchCreate_PermissionDenied(t *testing.T) {
	repo := &recordingRepo{}
	// authorizer:对 prod 的 folder 拒绝
	authz := &recordingAuthorizer{
		denyFolderIds: map[string]bool{"folder-prod": true},
	}
	svc := &secretService{
		repo:       repo,
		authorizer: authz,
		encryptor:  fakeEncryptor{},
	}
	req := BatchCreateRequest{
		SecretList: []BatchCreateSecretSpec{
			{
				Key: "DATABASE_URL",
				Envs: []BatchCreateEnvTarget{
					{EnvCode: "dev", FolderId: "folder-dev", Value: "d"},
					{EnvCode: "prod", FolderId: "folder-prod", Value: "p"},
				},
			},
		},
	}
	err := svc.BatchCreate(context.Background(), auth.UserInfo{UserId: "u-1"}, req, "actor-1")
	if err == nil {
		t.Fatalf("permission denied on prod folder should reject whole batch")
	}
	if !strings.Contains(err.Error(), "secret:create") {
		t.Errorf("err = %v, want to contain 'secret:create'", err)
	}
	if len(repo.batchCreateCalls) != 0 {
		t.Errorf("repo.BatchCreateSecrets should NOT be called when permission denied; got %d calls", len(repo.batchCreateCalls))
	}
}

// TestBatchCreate_KeyConflict 锁住 repo 返 ErrConflict → service 透传。
func TestBatchCreate_KeyConflict(t *testing.T) {
	repo := &recordingRepo{}
	// 把 BatchCreateSecrets 改写为返 ErrConflict
	repo.batchCreateErr = domain.ErrConflict
	svc := &secretService{
		repo:       repo,
		authorizer: &recordingAuthorizer{},
		encryptor:  fakeEncryptor{},
	}
	req := BatchCreateRequest{
		SecretList: []BatchCreateSecretSpec{
			{
				Key: "DATABASE_URL",
				Envs: []BatchCreateEnvTarget{
					{EnvCode: "dev", FolderId: "folder-dev", Value: "d"},
				},
			},
		},
	}
	err := svc.BatchCreate(context.Background(), auth.UserInfo{UserId: "u-1"}, req, "actor-1")
	if !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("expected domain.ErrConflict, got %v", err)
	}
}

// TestBatchCreate_EmptySecretList 锁住空 secretList 在 service 端被拒绝。
func TestBatchCreate_EmptySecretList(t *testing.T) {
	repo := &recordingRepo{}
	svc := &secretService{
		repo:       repo,
		authorizer: &recordingAuthorizer{},
		encryptor:  fakeEncryptor{},
	}
	req := BatchCreateRequest{}
	err := svc.BatchCreate(context.Background(), auth.UserInfo{UserId: "u-1"}, req, "actor-1")
	if err == nil {
		t.Fatalf("empty secretList should be rejected")
	}
	if !strings.Contains(err.Error(), "secretList") {
		t.Errorf("err = %v, want to contain 'secretList'", err)
	}
}

// TestBatchCreate_InvalidKey 锁住非法 key 格式在 service 端被拒绝。
func TestBatchCreate_InvalidKey(t *testing.T) {
	repo := &recordingRepo{}
	svc := &secretService{
		repo:       repo,
		authorizer: &recordingAuthorizer{},
		encryptor:  fakeEncryptor{},
	}
	req := BatchCreateRequest{
		SecretList: []BatchCreateSecretSpec{
			{
				Key: "lower_case",
				Envs: []BatchCreateEnvTarget{
					{EnvCode: "dev", FolderId: "folder-dev", Value: "d"},
				},
			},
		},
	}
	err := svc.BatchCreate(context.Background(), auth.UserInfo{UserId: "u-1"}, req, "actor-1")
	if err == nil {
		t.Fatalf("invalid key should be rejected")
	}
	if !strings.Contains(err.Error(), "key") {
		t.Errorf("err = %v, want to contain 'key'", err)
	}
}

// TestBatchCreate_EmptyEnvs 锁住空 Envs(每条 item 至少要有 1 个 env)在 service 端被拒绝。
func TestBatchCreate_EmptyEnvs(t *testing.T) {
	repo := &recordingRepo{}
	svc := &secretService{
		repo:       repo,
		authorizer: &recordingAuthorizer{},
		encryptor:  fakeEncryptor{},
	}
	req := BatchCreateRequest{
		SecretList: []BatchCreateSecretSpec{
			{Key: "DATABASE_URL"},
		},
	}
	err := svc.BatchCreate(context.Background(), auth.UserInfo{UserId: "u-1"}, req, "actor-1")
	if err == nil {
		t.Fatalf("empty envs should be rejected")
	}
	if !strings.Contains(err.Error(), "env") {
		t.Errorf("err = %v, want to contain 'env'", err)
	}
}

// TestBatchCreate_EmptyFolderId 锁住 env.folderId 为空在 service 端被拒绝。
func TestBatchCreate_EmptyFolderId(t *testing.T) {
	repo := &recordingRepo{}
	svc := &secretService{
		repo:       repo,
		authorizer: &recordingAuthorizer{},
		encryptor:  fakeEncryptor{},
	}
	req := BatchCreateRequest{
		SecretList: []BatchCreateSecretSpec{
			{
				Key:  "DATABASE_URL",
				Envs: []BatchCreateEnvTarget{{EnvCode: "dev", FolderId: "", Value: "d"}},
			},
		},
	}
	err := svc.BatchCreate(context.Background(), auth.UserInfo{UserId: "u-1"}, req, "actor-1")
	if err == nil {
		t.Fatalf("empty folderId should be rejected")
	}
	if !strings.Contains(err.Error(), "folderId") {
		t.Errorf("err = %v, want to contain 'folderId'", err)
	}
}

// TestBatchCreate_EmptyUserRejects 锁住空 user.UserId → ErrPermissionDenied。
func TestBatchCreate_EmptyUserRejects(t *testing.T) {
	repo := &recordingRepo{}
	svc := &secretService{
		repo:       repo,
		authorizer: &recordingAuthorizer{},
		encryptor:  fakeEncryptor{},
	}
	req := BatchCreateRequest{
		SecretList: []BatchCreateSecretSpec{
			{
				Key:  "DATABASE_URL",
				Envs: []BatchCreateEnvTarget{{EnvCode: "dev", FolderId: "folder-dev", Value: "d"}},
			},
		},
	}
	err := svc.BatchCreate(context.Background(), auth.UserInfo{UserId: ""}, req, "actor-1")
	if !errors.Is(err, auth.ErrPermissionDenied) {
		t.Fatalf("empty user should return ErrPermissionDenied, got %v", err)
	}
	if len(repo.batchCreateCalls) != 0 {
		t.Errorf("repo.BatchCreateSecrets should NOT be called when user is empty; got %d calls", len(repo.batchCreateCalls))
	}
}

// fakeEncryptor 提供最小的 Encrypt/Decrypt 给 service 用。
type fakeEncryptor struct{}

func (fakeEncryptor) Encrypt(_ context.Context, plaintext []byte) (secretcrypto.Ciphertext, error) {
	return secretcrypto.Ciphertext{Algorithm: "fake", Nonce: []byte("n"), Data: append([]byte("enc:"), plaintext...)}, nil
}
func (fakeEncryptor) Decrypt(_ context.Context, ct secretcrypto.Ciphertext) ([]byte, error) {
	if len(ct.Data) < 4 || string(ct.Data[:4]) != "enc:" {
		return nil, errors.New("fake: bad payload")
	}
	return ct.Data[4:], nil
}
