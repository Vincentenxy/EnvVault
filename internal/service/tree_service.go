package service

import (
	"context"
	"errors"
	"sort"
	"time"

	"envVault/internal/auth"
	"envVault/internal/domain"
	"envVault/internal/logging"
	rediscache "envVault/internal/store/redis"
)

// orphanSentinelId 是"虚拟孤儿根"节点的 id,用于把父不可见但子可见的子节点
// 挂在同一根下,前端可以单独识别它(不要把它当作真实资源去 GET)。
const orphanSentinelId = "__orphans__"

// TreeRepository 是 tree service 需要的 repo 最小接口(4 个 ListAll*ForTree + 1 个按 project 列 folder),
// 与 store.ResourceRepository 解耦便于测试 mock。
type TreeRepository interface {
	ListAllOrganizationsForTree(ctx context.Context, callerUserId string) ([]domain.Entity, error)
	ListAllProjectsForTree(ctx context.Context, callerUserId string) ([]domain.Entity, error)
	ListAllEnvironmentsForTree(ctx context.Context, callerUserId string) ([]domain.Entity, error)
	ListAllFoldersForTree(ctx context.Context, callerUserId string) ([]domain.FolderTreeEntry, error)
	ListFoldersInProject(ctx context.Context, callerUserId, projectId string) ([]domain.FolderInProject, error)
}

// treeCacheMetaReader 是 TreeService 读 cache 的最小接口,只暴露 ListAllMeta,
// 与 internal/store/redis.Cache 解耦便于 mock。
type treeCacheMetaReader interface {
	ListAllMeta(ctx context.Context) (rediscache.TreeMetaSnapshot, error)
	WarmTree(ctx context.Context, snapshot rediscache.TreeWarmSnapshot) error
}

// TreeService 集中 tree 接口的业务编排:
//
//   - 优先读 Redis cache(4 类散装 HASH)
//   - cache miss 或 cache 不可用 → 走 DB 4 个 ListAll*ForTree → 回填 cache
//   - 内存里建 4 级 map 索引,按 ParentId 关系拼父子
//   - RBAC 收窄:每类调 authorizer.Allow 过滤(由 SQL narrowing 出来的全集已带初步收窄,
//     这里再走一次 4 个 :read 码的二次过滤,与 GlobalSearch 风格一致)
//   - 父不可见子可见的孤儿节点:includeOrphans=true 时挂虚拟根,=false 时丢弃
//   - maxDepth 控制截断:1=org / 2=+project / 3=+env / 4=+folder
//
// GetProjectFolderTree:按 project 维度聚合 folder 结构(level=1 按 code 聚合,
// 每组带 envList;subFolders 跟随父层挂载)。不走 cache——单 project 量级小,
// 走 DB 加 RBAC narrowing 一次 SQL 即可。
type TreeService interface {
	GetTree(ctx context.Context, user auth.UserInfo, req domain.TreeRequest) (domain.ResourceTree, error)
	GetProjectFolderTree(ctx context.Context, user auth.UserInfo, req domain.ProjectFolderRequest) (domain.ProjectFolderTree, error)
}

type treeService struct {
	repo       TreeRepository
	cache      treeCacheMetaReader // 可为 nil
	authorizer auth.Authorizer
}

func NewTreeService(repo TreeRepository, cache treeCacheMetaReader, authorizer auth.Authorizer) TreeService {
	return &treeService{repo: repo, cache: cache, authorizer: authorizer}
}

// GetTree 是 tree 接口的主入口。Source 字段标识本次数据是 cache 还是 database。
func (s *treeService) GetTree(ctx context.Context, user auth.UserInfo, req domain.TreeRequest) (domain.ResourceTree, error) {
	if req.MaxDepth <= 0 || req.MaxDepth > 4 {
		req.MaxDepth = 4
	}
	tree := domain.ResourceTree{
		GeneratedAt: time.Now().UTC(),
	}

	// 1. 读 cache(如果可用)
	var snap rediscache.TreeMetaSnapshot
	var sourceFromCache bool
	if s.cache != nil {
		meta, err := s.cache.ListAllMeta(ctx)
		if err != nil {
			logging.Warn(ctx, "TreeService.GetTree", "redis ListAllMeta failed, fallback to DB",
				logging.F("error", err))
		} else if !meta.Empty() {
			snap = meta
			sourceFromCache = true
		}
	}

	// 2. cache miss / cache nil → 走 DB,顺手异步回填
	if !sourceFromCache {
		orgs, err := s.repo.ListAllOrganizationsForTree(ctx, user.UserId)
		if err != nil {
			return tree, err
		}
		projects, err := s.repo.ListAllProjectsForTree(ctx, user.UserId)
		if err != nil {
			return tree, err
		}
		envs, err := s.repo.ListAllEnvironmentsForTree(ctx, user.UserId)
		if err != nil {
			return tree, err
		}
		folders, err := s.repo.ListAllFoldersForTree(ctx, user.UserId)
		if err != nil {
			return tree, err
		}
		// 异步回填(不阻塞首请求响应)
		if s.cache != nil {
			snap = rediscache.TreeMetaSnapshot{
				Orgs: orgs, Projects: projects, Envs: envs, Folders: folders,
			}
			warmSnapshot := rediscache.TreeWarmSnapshot{
				Orgs: orgs, Projects: projects, Envs: envs, Folders: folders,
			}
			cacheRef := s.cache
			go func() {
				wctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer cancel()
				if werr := cacheRef.WarmTree(wctx, warmSnapshot); werr != nil {
					logging.Warn(wctx, "TreeService.warmCache", "WarmTree failed",
						logging.F("error", werr))
				}
			}()
		} else {
			snap = rediscache.TreeMetaSnapshot{
				Orgs: orgs, Projects: projects, Envs: envs, Folders: folders,
			}
		}
	}

	tree.Source = "database"
	if sourceFromCache {
		tree.Source = "cache"
	}

	// 3. RBAC 二次收窄(per-node,4 个 :read 码)。snap 里的条目已带 SQL narrowing,
	// 这里再做一次"对每个具体 id 调 authorizer"过滤,与 ListSecrets 行为对齐。
	visibleOrgs, err := s.filterOrgsByAllow(ctx, user, snap.Orgs)
	if err != nil {
		return tree, err
	}
	visibleProjects, err := s.filterProjectsByAllow(ctx, user, snap.Projects)
	if err != nil {
		return tree, err
	}
	visibleEnvs, err := s.filterEnvsByAllow(ctx, user, snap.Envs)
	if err != nil {
		return tree, err
	}
	visibleFolders, err := s.filterFoldersByAllow(ctx, user, snap.Folders)
	if err != nil {
		return tree, err
	}

	tree.Stats = domain.TreeStats{
		Organizations: len(visibleOrgs),
		Projects:      len(visibleProjects),
		Environments:  len(visibleEnvs),
		Folders:       len(visibleFolders),
	}

	// 4. 组装树
	roots, dropped, orphans := s.buildTree(
		visibleOrgs, visibleProjects, visibleEnvs, visibleFolders, req.IncludeOrphans,
	)
	tree.Roots = roots
	tree.Stats.Dropped = dropped
	tree.Stats.Orphans = orphans

	// 5. maxDepth 截断
	s.applyMaxDepth(&tree, req.MaxDepth)
	return tree, nil
}

// filterOrgsByAllow 用 authorizer.Allow 过滤 org 列表;ErrPermissionDenied 与
// ErrNotFound 单条过滤掉,其他错误透传(例如 Redis 不可用)。
func (s *treeService) filterOrgsByAllow(ctx context.Context, user auth.UserInfo, items []domain.Entity) ([]domain.Entity, error) {
	out := make([]domain.Entity, 0, len(items))
	for i := range items {
		if err := s.authorizer.Allow(ctx, user, "org:read", auth.Resource{Type: "organization", Id: items[i].Id}); err != nil {
			if errors.Is(err, auth.ErrPermissionDenied) || errors.Is(err, domain.ErrNotFound) {
				continue
			}
			return nil, err
		}
		out = append(out, items[i])
	}
	return out, nil
}

func (s *treeService) filterProjectsByAllow(ctx context.Context, user auth.UserInfo, items []domain.Entity) ([]domain.Entity, error) {
	out := make([]domain.Entity, 0, len(items))
	for i := range items {
		if err := s.authorizer.Allow(ctx, user, "project:read", auth.Resource{Type: "project", Id: items[i].Id}); err != nil {
			if errors.Is(err, auth.ErrPermissionDenied) || errors.Is(err, domain.ErrNotFound) {
				continue
			}
			return nil, err
		}
		out = append(out, items[i])
	}
	return out, nil
}

func (s *treeService) filterEnvsByAllow(ctx context.Context, user auth.UserInfo, items []domain.Entity) ([]domain.Entity, error) {
	out := make([]domain.Entity, 0, len(items))
	for i := range items {
		if err := s.authorizer.Allow(ctx, user, "env:read", auth.Resource{Type: "environment", Id: items[i].Id}); err != nil {
			if errors.Is(err, auth.ErrPermissionDenied) || errors.Is(err, domain.ErrNotFound) {
				continue
			}
			return nil, err
		}
		out = append(out, items[i])
	}
	return out, nil
}

func (s *treeService) filterFoldersByAllow(ctx context.Context, user auth.UserInfo, items []domain.FolderTreeEntry) ([]domain.FolderTreeEntry, error) {
	out := make([]domain.FolderTreeEntry, 0, len(items))
	for i := range items {
		if err := s.authorizer.Allow(ctx, user, "folder:read", auth.Resource{Type: "folder", Id: items[i].Id}); err != nil {
			if errors.Is(err, auth.ErrPermissionDenied) || errors.Is(err, domain.ErrNotFound) {
				continue
			}
			return nil, err
		}
		out = append(out, items[i])
	}
	return out, nil
}

// buildTree 在内存里把 4 类可见节点拼成父子树。
// 返回:
//   - roots:顶层节点(organization)数组
//   - dropped:被 RBAC 收窄丢掉的节点数(含级联丢弃,即父丢则所有子也算丢)
//   - orphans:父不可见但子可见,被移到虚拟根或丢弃的节点数
//
// 全程使用 *TreeNode 引用 + in-place 修改,避免 TreeNode 值拷贝丢失子挂载。
func (s *treeService) buildTree(
	orgs []domain.Entity,
	projects []domain.Entity,
	envs []domain.Entity,
	folders []domain.FolderTreeEntry,
	includeOrphans bool,
) ([]domain.TreeNode, int, int) {
	// 1. 投影成 *TreeNode,id → *node 索引
	orgNodeMap := make(map[string]*domain.TreeNode, len(orgs))
	for i := range orgs {
		n := projectOrgNode(orgs[i])
		orgNodeMap[orgs[i].Id] = n
	}
	projNodeMap := make(map[string]*domain.TreeNode, len(projects))
	for i := range projects {
		n := projectProjectNode(projects[i])
		projNodeMap[projects[i].Id] = n
	}
	envNodeMap := make(map[string]*domain.TreeNode, len(envs))
	for i := range envs {
		n := projectEnvNode(envs[i])
		envNodeMap[envs[i].Id] = n
	}
	folderNodeMap := make(map[string]*domain.TreeNode, len(folders))
	for i := range folders {
		n := projectFolderNode(folders[i])
		folderNodeMap[folders[i].Id] = n
	}

	// 2. 父可见性集合
	envVisible := make(map[string]bool, len(envs))
	for i := range envs {
		envVisible[envs[i].Id] = true
	}
	folderVisible := make(map[string]bool, len(folders))
	for i := range folders {
		folderVisible[folders[i].Id] = true
	}
	projVisible := make(map[string]bool, len(projects))
	for i := range projects {
		projVisible[projects[i].Id] = true
	}
	orgVisible := make(map[string]bool, len(orgs))
	for i := range orgs {
		orgVisible[orgs[i].Id] = true
	}

	// 3. 父子挂载(folder → env,folder → folder;env → project;project → org)
	//    父不可见的子节点压入 orphan 桶,在最后统一挂到虚拟根(includeOrphans=true)或丢弃。
	var folderOrphans, envOrphans, projectOrphans int
	orphanFolders := make([]*domain.TreeNode, 0)
	for i := range folders {
		f := folders[i]
		node := folderNodeMap[f.Id]
		if f.Level == 1 {
			if !envVisible[f.EnvironmentId] {
				folderOrphans++
				orphanFolders = append(orphanFolders, node)
				continue
			}
			envNodeMap[f.EnvironmentId].Children = append(envNodeMap[f.EnvironmentId].Children, *node)
		} else {
			if !folderVisible[f.ParentId] {
				folderOrphans++
				orphanFolders = append(orphanFolders, node)
				continue
			}
			folderNodeMap[f.ParentId].Children = append(folderNodeMap[f.ParentId].Children, *node)
		}
	}

	orphanEnvs := make([]*domain.TreeNode, 0)
	for i := range envs {
		e := envs[i]
		node := envNodeMap[e.Id]
		if !projVisible[e.ParentId] {
			envOrphans++
			orphanEnvs = append(orphanEnvs, node)
			continue
		}
		projNodeMap[e.ParentId].Children = append(projNodeMap[e.ParentId].Children, *node)
	}

	orphanProjects := make([]*domain.TreeNode, 0)
	for i := range projects {
		p := projects[i]
		node := projNodeMap[p.Id]
		if !orgVisible[p.ParentId] {
			projectOrphans++
			orphanProjects = append(orphanProjects, node)
			continue
		}
		orgNodeMap[p.ParentId].Children = append(orgNodeMap[p.ParentId].Children, *node)
	}

	// 4. roots
	roots := make([]domain.TreeNode, 0, len(orgs))
	for _, id := range orderedOrgIds(orgs) {
		if n, ok := orgNodeMap[id]; ok {
			roots = append(roots, *n)
		}
	}

	// 5. 挂孤儿(可选)
	totalOrphans := folderOrphans + envOrphans + projectOrphans
	if includeOrphans && totalOrphans > 0 {
		// folder 的孤儿与 env 的孤儿是不同资源类型,分别组织成 children。
		// 简单做法:把 3 类孤儿拍平,都挂到虚拟根的 children 里。
		orphan := domain.TreeNode{
			Id:   orphanSentinelId,
			Type: domain.TreeNodeFolder, // 占位,实际是混合类型
			Code: "__orphans__",
			Name: "Orphan nodes (parent not accessible)",
			Children: append(
				append(derefOrphans(orphanFolders), derefOrphans(orphanEnvs)...),
				derefOrphans(orphanProjects)...,
			),
		}
		roots = append(roots, orphan)
	}

	return roots, 0, totalOrphans
}

// orderedOrgIds 返回 org id 列表(按 name 排序),与 ListOrganizations 的 order by 行为一致;
// 用 keys 顺序会有 map 随机化问题,故显式按传入顺序遍历。
// 注意:orgs/Projects 等 slice 本身是按 name asc 排过的(见 ListAllXxxForTree SQL),
// 这里直接保留该顺序即可。
func orderedOrgIds(orgs []domain.Entity) []string {
	ids := make([]string, 0, len(orgs))
	for _, o := range orgs {
		ids = append(ids, o.Id)
	}
	return ids
}

// derefOrphans 把 *TreeNode 切片解引用成值切片。空切片直接返 nil 让 JSON 序列化
// 时输出 null(虚拟根 children 不会为空,所以走到这里一定有元素)。
func derefOrphans(s []*domain.TreeNode) []domain.TreeNode {
	if len(s) == 0 {
		return nil
	}
	out := make([]domain.TreeNode, 0, len(s))
	for _, n := range s {
		if n != nil {
			out = append(out, *n)
		}
	}
	return out
}

// applyMaxDepth 截断树:1=org,2=+project,3=+env,4=+folder(含 level=2)。
func (s *treeService) applyMaxDepth(tree *domain.ResourceTree, maxDepth int) {
	if maxDepth >= 4 {
		return
	}
	for i := range tree.Roots {
		truncateChildren(&tree.Roots[i], maxDepth, 1)
	}
}

func truncateChildren(n *domain.TreeNode, maxDepth, currentDepth int) {
	if currentDepth >= maxDepth {
		n.Children = []domain.TreeNode{}
		// 也要清掉 stats 里的子项计数?stats 在 buildTree 后已经按 RBAC 收窄结果填过,
		// 截断不影响"可见集合大小",只影响 children 字段。这里保持 stats 不变。
		return
	}
	for i := range n.Children {
		truncateChildren(&n.Children[i], maxDepth, currentDepth+1)
	}
}

// 4 个 Entity → TreeNode 投影函数。Children 强制空 slice,前端免判空。
func projectOrgNode(e domain.Entity) *domain.TreeNode {
	return &domain.TreeNode{
		Id:       e.Id,
		Type:     domain.TreeNodeOrganization,
		Code:     e.Code,
		Name:     e.Name,
		Comment:  e.Comment,
		Children: []domain.TreeNode{},
	}
}
func projectProjectNode(e domain.Entity) *domain.TreeNode {
	return &domain.TreeNode{
		Id:       e.Id,
		Type:     domain.TreeNodeProject,
		ParentId: e.ParentId,
		Code:     e.Code,
		Name:     e.Name,
		Comment:  e.Comment,
		Children: []domain.TreeNode{},
	}
}
func projectEnvNode(e domain.Entity) *domain.TreeNode {
	return &domain.TreeNode{
		Id:       e.Id,
		Type:     domain.TreeNodeEnvironment,
		ParentId: e.ParentId,
		Code:     e.Code,
		Name:     e.Name,
		Comment:  e.Comment,
		Children: []domain.TreeNode{},
	}
}
func projectFolderNode(f domain.FolderTreeEntry) *domain.TreeNode {
	return &domain.TreeNode{
		Id:       f.Id,
		Type:     domain.TreeNodeFolder,
		Level:    f.Level,
		ParentId: parentIdForNode(f),
		Code:     f.Code,
		Name:     f.Name,
		Comment:  f.Comment,
		Children: []domain.TreeNode{},
	}
}

// parentIdForNode 决定 TreeNode.ParentId 字段填什么:
//   - level=1:父是 env,填 envId(让前端能定位 env)
//   - level=2:父是 folder,填父 folder id
func parentIdForNode(f domain.FolderTreeEntry) string {
	if f.Level == 1 {
		return f.EnvironmentId
	}
	return f.ParentId
}

// GetProjectFolderTree 按 project 维度返回 folder 结构(level=1 按 code 聚合,每组带
// envList;level=2 子层跟随父层挂载在 SubFolders 下,同样按 code 聚合)。
//
// 流程:
//  1. repo.ListFoldersInProject(callerUserId, projectId) 一次 SQL 拉所有 level=1+2 folder
//     (RBAC narrowing 已在 SQL 层做)
//  2. service 按 (code) 聚合 level=1,生成 FolderGroup 列表(envList 按 env id 去重保持顺序)
//  3. 按 (parent_id → 父 group, child_code) 聚合 level=2,挂到父 group.SubFolders
//  4. 二次 authorizer.Allow("folder:read", folder) 过滤(与 GetTree 一致,SQL 之外的兜底)
//
// 返回的 envList 元素是 env id(UUID);id / name / comment 从该 code 在第一个 env 中
// 的实例取(同名 folder 的 name/comment 通常一致;不一致以第一个为准,前端如需精确
// 可按 env 拉详情)。
//
// 关键约定:只要父 group 出现在结果里,它的所有 subFolders(由 SQL narrowing 限制)
// 都一定存在——SQL 同时拉父子两层,service 不可能出现"父在但子丢"的情况。
func (s *treeService) GetProjectFolderTree(
	ctx context.Context, user auth.UserInfo, req domain.ProjectFolderRequest,
) (domain.ProjectFolderTree, error) {
	tree := domain.ProjectFolderTree{FolderList: []domain.FolderGroup{}}

	rows, err := s.repo.ListFoldersInProject(ctx, user.UserId, req.ProjectId)
	if err != nil {
		return tree, err
	}
	if len(rows) == 0 {
		return tree, nil
	}

	// 1. 二次 RBAC 收窄:SQL 已带 narrowing,这里 per-folder 再过 authorizer.Allow。
	//    单条失败仅过滤掉,其他错误透传(例如 authorizer 内部 DB 故障)。
	visible := make([]domain.FolderInProject, 0, len(rows))
	for i := range rows {
		rid := rows[i].Id
		if err := s.authorizer.Allow(ctx, user, "folder:read", auth.Resource{Type: "folder", Id: rid}); err != nil {
			if errors.Is(err, auth.ErrPermissionDenied) || errors.Is(err, domain.ErrNotFound) {
				continue
			}
			return tree, err
		}
		visible = append(visible, rows[i])
	}

	// 2. 按 level 分桶(level=1 一组,level=2 一组;rows 已按 level asc 排序)。
	var level1 []domain.FolderInProject
	var level2 []domain.FolderInProject
	for i := range visible {
		if visible[i].Level == 1 {
			level1 = append(level1, visible[i])
		} else {
			level2 = append(level2, visible[i])
		}
	}

	// 3. 聚合 level=1:按 code 聚合(envList 按出现顺序去重,id/name/comment 取首个)。
	byCodeL1 := make(map[string]*l1Aggregate)
	for i := range level1 {
		f := level1[i]
		a, ok := byCodeL1[f.Code]
		if !ok {
			a = &l1Aggregate{
				group: &domain.FolderGroup{
					Id:      f.Id,
					Code:    f.Code,
					Name:    f.Name,
					Comment: f.Comment,
					EnvList: []domain.EnvRef{},
					// SubFolders 在聚合 level=2 时填充,初始化为 [] 防 JSON null
					SubFolders: []domain.SubFolderGroup{},
				},
				envSet: make(map[string]struct{}),
			}
			byCodeL1[f.Code] = a
		}
		if _, dup := a.envSet[f.EnvironmentId]; !dup {
			a.envSet[f.EnvironmentId] = struct{}{}
			a.envList = append(a.envList, domain.EnvRef{
				Id:       f.EnvironmentId,
				Code:     f.EnvironmentCode,
				FolderId: f.Id,
			})
		}
	}

	// 4. 聚合 level=2:按 (父 folder code, 子 code) 聚合(不是按 parent_id)。
	//    理由:同一 code 的父 folder 在不同 env 下 id 不同(分别 <uuid>),如果按 parent_id
	//    聚合,同名子 stripe 会被分裂到 2 个父 group 下;但用户的契约是
	//    "子目录跟随父目录"——只要父的 code 相同,子层就合并。
	//    聚合 key 形如 "payment/stripe",envList 跨所有父实例合并(去重保序)。
	//    反向契约:父 group 不可见(RBAC 收窄)时,其所有子实例被整体跳过。
	parentIdToCode := make(map[string]string, len(level1))
	for i := range level1 {
		parentIdToCode[level1[i].Id] = level1[i].Code
	}
	// 按 (parentCode, childCode) 聚合
	type childAggKey struct {
		parentCode string
		childCode  string
	}
	childAgg := make(map[childAggKey]*l2Aggregate)
	for i := range level2 {
		f := level2[i]
		parentCode, ok := parentIdToCode[f.ParentId]
		if !ok {
			// 父不可见 → 子层跳过
			continue
		}
		key := childAggKey{parentCode: parentCode, childCode: f.Code}
		a, ok := childAgg[key]
		if !ok {
			a = &l2Aggregate{
				group: &domain.SubFolderGroup{
					Id:      f.Id,
					Code:    f.Code,
					Name:    f.Name,
					Comment: f.Comment,
					EnvList: []domain.EnvRef{},
				},
				envSet: make(map[string]struct{}),
			}
			childAgg[key] = a
		}
		if _, dup := a.envSet[f.EnvironmentId]; !dup {
			a.envSet[f.EnvironmentId] = struct{}{}
			a.group.EnvList = append(a.group.EnvList, domain.EnvRef{
				Id:       f.EnvironmentId,
				Code:     f.EnvironmentCode,
				FolderId: f.Id,
			})
		}
	}
	// 把聚合结果挂到对应父 group 下,按 child code 字母序输出
	childrenByParent := make(map[string][]*l2Aggregate)
	for key, a := range childAgg {
		childrenByParent[key.parentCode] = append(childrenByParent[key.parentCode], a)
	}
	for _, parentGroup := range byCodeL1 {
		children := childrenByParent[parentGroup.group.Code]
		// 按 child code 字母序
		sort.SliceStable(children, func(i, j int) bool {
			return children[i].group.Code < children[j].group.Code
		})
		for _, c := range children {
			parentGroup.group.SubFolders = append(parentGroup.group.SubFolders, *c.group)
		}
	}

	// 5. 按 code 字母序输出 level=1 group
	for _, code := range sortedStringKeys(byCodeL1) {
		a := byCodeL1[code]
		a.group.EnvList = a.envList
		tree.FolderList = append(tree.FolderList, *a.group)
	}
	return tree, nil
}

// l1Aggregate / l2Aggregate 是 GetProjectFolderTree 内部聚合的中间结构。
// envSet + envList 保序去重(envList 是最终返回的 EnvRef 列表)。
type l1Aggregate struct {
	group   *domain.FolderGroup
	envSet  map[string]struct{}
	envList []domain.EnvRef
}
type l2Aggregate struct {
	group  *domain.SubFolderGroup
	envSet map[string]struct{}
}

// sortedStringKeys 返回 map 所有 key 排序后的字符串切片(泛用 helper,避免为
// 每个 map 类型都写一个排序 wrapper)。
func sortedStringKeys[T any](m map[string]T) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
