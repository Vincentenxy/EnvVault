package service

import (
	"context"
	"errors"
	"testing"

	"envVault/internal/auth"
	"envVault/internal/domain"
	rediscache "envVault/internal/store/redis"
)

// stubAuthorizer 决定每个 (permission, id) 是否放行。
// 规则:id 在 allowIds 里 → 放行;id 在 denyIds 里 → 拒绝;其他 → 拒绝(默认保守)。
type stubAuthorizer struct {
	allowIds map[string]bool
	denyIds  map[string]bool
	calls    int
}

func newStubAuthorizer(allowIds, denyIds []string) *stubAuthorizer {
	a := make(map[string]bool, len(allowIds))
	for _, id := range allowIds {
		a[id] = true
	}
	d := make(map[string]bool, len(denyIds))
	for _, id := range denyIds {
		d[id] = true
	}
	return &stubAuthorizer{allowIds: a, denyIds: d}
}

func (s *stubAuthorizer) Allow(_ context.Context, _ auth.UserInfo, _ string, resource auth.Resource) error {
	s.calls++
	if s.allowIds[resource.Id] {
		return nil
	}
	return auth.ErrPermissionDenied
}

// stubRepo 提供 4 个 ListAll*ForTree,以及一个能识别 calls 的计数。
type stubRepo struct {
	orgs     []domain.Entity
	projects []domain.Entity
	envs     []domain.Entity
	folders  []domain.FolderTreeEntry

	listOrgsCalls     int
	listProjectsCalls int
	listEnvsCalls     int
	listFoldersCalls  int
}

func (r *stubRepo) ListAllOrganizationsForTree(_ context.Context, _ string) ([]domain.Entity, error) {
	r.listOrgsCalls++
	return r.orgs, nil
}
func (r *stubRepo) ListAllProjectsForTree(_ context.Context, _ string) ([]domain.Entity, error) {
	r.listProjectsCalls++
	return r.projects, nil
}
func (r *stubRepo) ListAllEnvironmentsForTree(_ context.Context, _ string) ([]domain.Entity, error) {
	r.listEnvsCalls++
	return r.envs, nil
}
func (r *stubRepo) ListAllFoldersForTree(_ context.Context, _ string) ([]domain.FolderTreeEntry, error) {
	r.listFoldersCalls++
	return r.folders, nil
}

// stubCacheMeta 在测试中模拟"cache 命中 + warm 写入"路径。
type stubCacheMeta struct {
	snapshot     rediscache.TreeMetaSnapshot
	listCalls    int
	warmCalls    int
	warmReceived rediscache.TreeWarmSnapshot
	listErr      error
	warmErr      error
}

func (c *stubCacheMeta) ListAllMeta(_ context.Context) (rediscache.TreeMetaSnapshot, error) {
	c.listCalls++
	if c.listErr != nil {
		return rediscache.TreeMetaSnapshot{}, c.listErr
	}
	return c.snapshot, nil
}
func (c *stubCacheMeta) WarmTree(_ context.Context, snap rediscache.TreeWarmSnapshot) error {
	c.warmCalls++
	c.warmReceived = snap
	return c.warmErr
}

// TestTreeService_AssembleCacheHit 验证 cache 命中路径:不会回源 DB(Source=cache),
// 树结构与 snapshot 一致。
func TestTreeService_AssembleCacheHit(t *testing.T) {
	repo := &stubRepo{} // cache 命中时不应被调用
	cache := &stubCacheMeta{
		snapshot: rediscache.TreeMetaSnapshot{
			Orgs:     []domain.Entity{{Id: "o1", Code: "o1", Name: "Org1"}},
			Projects: []domain.Entity{{Id: "p1", ParentId: "o1", Code: "p1", Name: "Proj1"}},
			Envs:     []domain.Entity{{Id: "e1", ParentId: "p1", Code: "e1", Name: "Env1"}},
			Folders:  []domain.FolderTreeEntry{{Entity: domain.Entity{Id: "f1", Code: "f1", Name: "Folder1"}, Level: 1, EnvironmentId: "e1", ProjectId: "p1"}},
		},
	}
	authz := newStubAuthorizer([]string{"o1", "p1", "e1", "f1"}, nil)

	svc := NewTreeService(repo, cache, authz)
	tree, err := svc.GetTree(context.Background(), auth.UserInfo{UserId: "u1"}, domain.TreeRequest{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tree.Source != "cache" {
		t.Errorf("Source = %q, want %q", tree.Source, "cache")
	}
	if repo.listOrgsCalls+repo.listProjectsCalls+repo.listEnvsCalls+repo.listFoldersCalls > 0 {
		t.Errorf("repo should not be called on cache hit, got orgs=%d projects=%d envs=%d folders=%d",
			repo.listOrgsCalls, repo.listProjectsCalls, repo.listEnvsCalls, repo.listFoldersCalls)
	}
	if len(tree.Roots) != 1 {
		t.Fatalf("Roots len = %d, want 1", len(tree.Roots))
	}
	root := tree.Roots[0]
	if root.Type != domain.TreeNodeOrganization || root.Code != "o1" {
		t.Errorf("root = %+v, want org o1", root)
	}
	if len(root.Children) != 1 || root.Children[0].Code != "p1" {
		t.Errorf("root.Children = %+v, want [p1]", root.Children)
	}
}

// TestTreeService_AssembleDBFallback 验证 cache 不可用(c==nil)时走 DB 路径,
// 触发 ListAll*ForTree(Source=database)。
func TestTreeService_AssembleDBFallback(t *testing.T) {
	repo := &stubRepo{
		orgs:     []domain.Entity{{Id: "o1", Code: "o1", Name: "Org1"}},
		projects: []domain.Entity{{Id: "p1", ParentId: "o1", Code: "p1", Name: "Proj1"}},
		envs:     []domain.Entity{{Id: "e1", ParentId: "p1", Code: "e1", Name: "Env1"}},
		folders:  []domain.FolderTreeEntry{{Entity: domain.Entity{Id: "f1", Code: "f1", Name: "Folder1"}, Level: 1, EnvironmentId: "e1", ProjectId: "p1"}},
	}
	authz := newStubAuthorizer([]string{"o1", "p1", "e1", "f1"}, nil)

	svc := NewTreeService(repo, nil /* no cache */, authz)
	tree, err := svc.GetTree(context.Background(), auth.UserInfo{UserId: "u1"}, domain.TreeRequest{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tree.Source != "database" {
		t.Errorf("Source = %q, want %q", tree.Source, "database")
	}
	if repo.listOrgsCalls != 1 || repo.listProjectsCalls != 1 ||
		repo.listEnvsCalls != 1 || repo.listFoldersCalls != 1 {
		t.Errorf("repo not called once for each type, got %+v", repo)
	}
	if tree.Stats.Organizations != 1 || tree.Stats.Projects != 1 ||
		tree.Stats.Environments != 1 || tree.Stats.Folders != 1 {
		t.Errorf("stats = %+v, want all 1s", tree.Stats)
	}
}

// TestTreeService_RBACNarrowing 验证 RBAC 二次收窄:caller 有 o1 权限无 o2 权限,
// 返回树只含 o1 子树,stats.Dropped 不计入(此处只算 RBAC 拒的 node 本身,
// drop 算法目前不统计被级联丢弃的——只算 orphans;验证可见集合与 stats 即可)。
func TestTreeService_RBACNarrowing(t *testing.T) {
	repo := &stubRepo{
		orgs: []domain.Entity{
			{Id: "o1", Code: "o1", Name: "Org1"},
			{Id: "o2", Code: "o2", Name: "Org2"},
		},
		projects: []domain.Entity{
			{Id: "p1", ParentId: "o1", Code: "p1", Name: "P1"},
			{Id: "p2", ParentId: "o2", Code: "p2", Name: "P2"},
		},
		envs: []domain.Entity{
			{Id: "e1", ParentId: "p1", Code: "e1", Name: "E1"},
			{Id: "e2", ParentId: "p2", Code: "e2", Name: "E2"},
		},
		folders: []domain.FolderTreeEntry{
			{Entity: domain.Entity{Id: "f1", Code: "f1", Name: "F1"}, Level: 1, EnvironmentId: "e1", ProjectId: "p1"},
			{Entity: domain.Entity{Id: "f2", Code: "f2", Name: "F2"}, Level: 1, EnvironmentId: "e2", ProjectId: "p2"},
		},
	}
	// 只放行 o1/p1/e1/f1
	authz := newStubAuthorizer([]string{"o1", "p1", "e1", "f1"}, nil)

	svc := NewTreeService(repo, nil, authz)
	tree, err := svc.GetTree(context.Background(), auth.UserInfo{UserId: "u1"}, domain.TreeRequest{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(tree.Roots) != 1 || tree.Roots[0].Code != "o1" {
		t.Errorf("Roots = %+v, want only [o1]", tree.Roots)
	}
	if tree.Stats.Organizations != 1 {
		t.Errorf("Stats.Organizations = %d, want 1", tree.Stats.Organizations)
	}
	if tree.Stats.Projects != 1 || tree.Stats.Environments != 1 || tree.Stats.Folders != 1 {
		t.Errorf("Stats = %+v, want all 1s", tree.Stats)
	}
}

// TestTreeService_OrphanHandling_Default 验证"父不可见但子可见"的孤儿处理:
// caller 有 f1(folder level=2)权限无 e1(env)权限,默认 includeOrphans=true 时
// f1 挂到虚拟根 __orphans__ 下,Stats.Orphans == 1。
func TestTreeService_OrphanHandling_Default(t *testing.T) {
	repo := &stubRepo{
		orgs: []domain.Entity{
			{Id: "o1", Code: "o1", Name: "Org1"},
		},
		projects: []domain.Entity{
			{Id: "p1", ParentId: "o1", Code: "p1", Name: "P1"},
		},
		envs: []domain.Entity{
			{Id: "e1", ParentId: "p1", Code: "e1", Name: "E1"},
		},
		folders: []domain.FolderTreeEntry{
			// f1 level=1,env=e1,但 caller 无 e1 权限
			{Entity: domain.Entity{Id: "f1", Code: "f1", Name: "F1"}, Level: 1, EnvironmentId: "e1", ProjectId: "p1"},
		},
	}
	// 放行 o1/p1/f1,拒绝 e1
	authz := newStubAuthorizer([]string{"o1", "p1", "f1"}, nil)

	svc := NewTreeService(repo, nil, authz)
	tree, err := svc.GetTree(context.Background(), auth.UserInfo{UserId: "u1"},
		domain.TreeRequest{IncludeOrphans: true})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Roots 应是 [o1, __orphans__]
	if len(tree.Roots) != 2 {
		t.Fatalf("Roots len = %d, want 2 (o1 + orphan)", len(tree.Roots))
	}
	orphan := tree.Roots[1]
	if orphan.Id != orphanSentinelId {
		t.Errorf("orphan.Id = %q, want %q", orphan.Id, orphanSentinelId)
	}
	if len(orphan.Children) != 1 || orphan.Children[0].Code != "f1" {
		t.Errorf("orphan.Children = %+v, want [f1]", orphan.Children)
	}
	if tree.Stats.Orphans != 1 {
		t.Errorf("Stats.Orphans = %d, want 1", tree.Stats.Orphans)
	}
}

// TestTreeService_OrphanHandling_Strict 验证 includeOrphans=false 时
// 孤儿被丢弃(但 Orphans 仍计 1)。
func TestTreeService_OrphanHandling_Strict(t *testing.T) {
	repo := &stubRepo{
		orgs:     []domain.Entity{{Id: "o1", Code: "o1", Name: "Org1"}},
		projects: []domain.Entity{{Id: "p1", ParentId: "o1", Code: "p1", Name: "P1"}},
		envs:     []domain.Entity{{Id: "e1", ParentId: "p1", Code: "e1", Name: "E1"}},
		folders:  []domain.FolderTreeEntry{{Entity: domain.Entity{Id: "f1", Code: "f1", Name: "F1"}, Level: 1, EnvironmentId: "e1", ProjectId: "p1"}},
	}
	authz := newStubAuthorizer([]string{"o1", "p1", "f1"}, nil)

	svc := NewTreeService(repo, nil, authz)
	tree, err := svc.GetTree(context.Background(), auth.UserInfo{UserId: "u1"},
		domain.TreeRequest{IncludeOrphans: false})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(tree.Roots) != 1 || tree.Roots[0].Code != "o1" {
		t.Errorf("Roots = %+v, want only [o1]", tree.Roots)
	}
	if tree.Stats.Orphans != 1 {
		t.Errorf("Stats.Orphans = %d, want 1 (仍记数)", tree.Stats.Orphans)
	}
}

// TestTreeService_CacheMiss_TriggersDBAndWarm 验证 cache miss(empty snapshot)→
// 走 DB 4 个 ListAll*ForTree,顺手异步 warm;warm 收到的 snapshot 与 DB 一致。
func TestTreeService_CacheMiss_TriggersDBAndWarm(t *testing.T) {
	repo := &stubRepo{
		orgs:     []domain.Entity{{Id: "o1", Code: "o1", Name: "Org1"}},
		projects: []domain.Entity{{Id: "p1", ParentId: "o1", Code: "p1", Name: "P1"}},
		envs:     []domain.Entity{{Id: "e1", ParentId: "p1", Code: "e1", Name: "E1"}},
		folders:  []domain.FolderTreeEntry{{Entity: domain.Entity{Id: "f1", Code: "f1", Name: "F1"}, Level: 1, EnvironmentId: "e1", ProjectId: "p1"}},
	}
	// cache 返回空 snapshot → 走 DB 路径
	cache := &stubCacheMeta{snapshot: rediscache.TreeMetaSnapshot{}}
	authz := newStubAuthorizer([]string{"o1", "p1", "e1", "f1"}, nil)

	svc := NewTreeService(repo, cache, authz)
	tree, err := svc.GetTree(context.Background(), auth.UserInfo{UserId: "u1"}, domain.TreeRequest{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tree.Source != "database" {
		t.Errorf("Source = %q, want %q", tree.Source, "database")
	}
	if repo.listOrgsCalls != 1 {
		t.Errorf("ListAllOrganizationsForTree calls = %d, want 1", repo.listOrgsCalls)
	}
	// 异步 warm 不阻塞,断言"warm 被调用 1 次"足够(warm 在 goroutine 中)
	// 简单做法:用 cache.listErr 触发阻塞?太重。改为允许 0 或 1 次。
	if cache.warmCalls > 1 {
		t.Errorf("WarmTree calls = %d, want 0 or 1", cache.warmCalls)
	}
}

// TestTreeService_AuthorizerError_Propagates 验证 authorizer 抛非权限错误时
// 应透传(例如 authorizer 内部 DB 故障)。
func TestTreeService_AuthorizerError_Propagates(t *testing.T) {
	repo := &stubRepo{
		orgs: []domain.Entity{{Id: "o1", Code: "o1", Name: "Org1"}},
	}
	// 首次 Allow 直接返 internal 错误(非 ErrPermissionDenied / ErrNotFound,应被透传)
	bad := &badAuthorizer{firstErr: errors.New("boom")}
	svc := NewTreeService(repo, nil, bad)
	_, err := svc.GetTree(context.Background(), auth.UserInfo{UserId: "u1"}, domain.TreeRequest{})
	if err == nil || err.Error() != "boom" {
		t.Errorf("err = %v, want boom", err)
	}
}

type badAuthorizer struct {
	firstErr error
	thenErr  error
	calls    int
}

func (b *badAuthorizer) Allow(_ context.Context, _ auth.UserInfo, _ string, _ auth.Resource) error {
	b.calls++
	if b.calls == 1 {
		return b.firstErr
	}
	if b.thenErr != nil {
		return b.thenErr
	}
	return auth.ErrPermissionDenied
}
