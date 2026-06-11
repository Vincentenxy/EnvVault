# EnvVault 后续设计待办

状态：本文件中的业务路径 `code` 设计已按清库重建方式落地到 `configs/schema.sql`、核心 CRUD 接口、Redis Secret 缓存和 `design/api/core.yaml`。后续如果已有生产数据，需要另写迁移脚本，本次不包含历史数据迁移。

v3 增量：环境归项目所有（`environments.project_id`），新增 `environment_templates` org 层只读模板汇总，删除 `project_environments` 关联表，代码与 schema 已按清库重建方式落地（v2 草稿"Org 共享 env"与"Org 共享 env 权限设计"两节已标记为 superseded）。

v4 增量（架构瘦身）：去掉 7 个透传 service（Org/Project/Env/EnvTpl/Folder/Audit/User）与强类型 ID、`RoleInput`/`GrantInput` 入参结构;`internal/service/` 只保留 `SecretService`(加密/缓存/审计编排)与 `RBACService`(授权计算);handler → repo 直接调用;`Domain` 仅保留数据形状(无业务方法)。核心功能(secrets 管理、权限管控、JWT 认证、审计)与对外 API 无破坏性变更。后续平台扩展(OAuth、K8s、SDK、Helm、metrics、schema 迁移、secret 版本历史、KMS)记入文末"待办清单"。

v5 增量（RBAC 接入 + 路径访问 + 命名澄清）：所有数据 handler(`/org/*`、`/project/*`、`/env/*`、`/env/template/*`、`/folder/*`、`/secret/*`、`/audit/*`)统一接入 `allowScope`;`org_admin` / `project_admin` / `project_viewer` 等角色真正生效。新增路径访问 `org_code.project_code.env_code.folder_code.KEY`,**单 SQL 5 表 join** 完成 4 级 code→id 解析,新增 `POST /api/v1/secret/path/info` 和 `/secret/path/reveal`。在 domain 层和 HTTP API 字段中显式表达 `userId` / `roleType` / `resourceType` / `resourceId` 三段式,RBAC 授权请求/响应支持新旧 alias 并存(无 breaking change)。数据库 schema 与 `permissionCode` / `defaultPermissions` / `defaultRoles` 不动。

v6 增量（权限下沉到 service + RBAC 管理接口边界校验）：所有权限判定走 `auth.Authorizer.Allow` 统一接口;调用点从 controller 下沉到 service(`SecretService` 9 个方法 + `RBACService` 11 个方法)。`RBACService` 的 5 个写方法额外叠加 4 条边界:caller 在目标 scope 持有 `rbac:*` 权限码、被授权/创建角色的 permissions 必须是 caller 有效权限的子集、`global` scope 仅 `platform_admin` 可操作、`platform_admin` 角色仅 `platform_admin` caller 可授予/创建;`RevokeRole` 加「最后一个 `org_owner」保护,撤销后剩余 `< 1` 时返 409。OpenAPI `design/api/rbac.yaml` 给 7 个管理接口加 description 块,显式说明 caller 范围。设计总纲 `design/rbac_degisn.md` 不动(作为权威设计来源保留)。

v7 增量（list 接口按 caller 权限自动收窄可见作用域）：v5/v6 完成了单点操作鉴权,但 list 接口始终返 parent 下全量。本轮在 repo SQL 层面加 `user_read_scopes` CTE,按 caller 的 `user_role_bindings` 把 6 个 list 方法的 WHERE 收窄到 caller 实际有 scope 的资源;cascade 链:org 自己 → project(+org)→ env(+project+org)→ folder(+env+project+org)→ secret(+folder+env+project+org)。`org_admin` 绑在 (org, X) 时能 list 到 X org 自身、X 下所有 project / env / folder / secret(以前 X org 自身看不到,因为 list 入口 allowScope 是 global-only)。`platform_admin` 看到全量。无任何 binding 的 user 拿到空 list(隐式空 list,不 403,符合"我看的就是我能看的"心智模型)。controller 入口 `allowScope` 移除,改为 `auth.UserFromContext(c).UserId` 透传给 repo。`SecretCache` 仍保留但 `Search` 不再走它(cache 不感知 user,无法收窄);`Search` 本就走 DB trigram 索引,影响可控。详细见 `design/DESIGN.md`「List 接口按 caller 权限收窄（v7）」节。

v8 增量（批量 reveal 接口 `POST /api/v1/secret/path/batchReveal`）：SDK / K8s 集成方需要一次拉多个 key,串行 N 次 `/secret/path/reveal` 网络往返 + 鉴权 + audit 重复成本高,与现有 list/search 一次性拉一批的语义不一致。新增端点按 `org.proj.env.folder` 4 段路径 + 可选 keys 列表,**一次性**返回所有命中 key 的明文 + 元数据;**不分页**、**无上限**(用户决策);复用 v7 `userReadScopeCTE` 做 cascade narrowing,`secret:reveal` 权限从 (secret, folder, env, project, org) 任一层 binding 自动展开;**整批 1 条 audit**(`action="reveal_batch"`、`resource_type="folder"`、`encrypted_value=jsonb([keys...])`)。`notFound` 字段只在 request `keys` 非空时出现(无对照时不返)。本轮**不动** RBAC 端 list 收窄、audit 收窄、GlobalSearch 收窄(P1),继续保留。详细见 `design/DESIGN.md`「Secret 路径访问 - 批量 reveal（v8）」节。

v9 增量（自注册 / 登录 / 强制登出 / 改密）：envVault 之前没有用户管理入口,`users` 表只由 `SyncUser`(lazy) / `GrantRole` / `EnsureBootstrapAdmin` 三条隐式路径写入;前端无注册页,运维只能通过 bootstrap admin 拉起第一个用户。本轮加 4 个端点:
- `POST /api/v1/auth/register` — 邮箱 + name + 密码自助注册;**首用户自动获得 `platform_admin`(global)**,后续注册无角色(待 admin grant)。密码 argon2id(m=64MB, t=3, p=2)、最小长度 12 字符(可配)。
- `POST /api/v1/auth/login` — 邮箱 + 密码登录;**统一返 `bad credentials`**(不区分「邮箱不存在」/「密码错」,防 user enumeration);Redis sliding window 频控 5 次/min/IP,触发后 15min 封禁(均可在 config 调)。
- `POST /api/v1/auth/logout` — 强制登出;`UPDATE users.tokens_valid_after = NOW()` + 进程内 tokens_cache 立即 Set;最迟 1min 内全集群同步(refresher 周期)。
- `POST /api/v1/auth/changePassword` — 改密;`oldPassword` 校验通过后原子 UPDATE `password_hash` + bump `tokens_valid_after`(等同 logout 所有旧 token)。

底层机制:
- `users` 表加 `password_hash` / `password_algo` / `tokens_valid_after` 3 列;`email` 加 partial unique index(`where email <> ''`)。新增 `login_attempts` 表(风控 + 审计)。
- `domain.User` 加 3 个字段:`PasswordHash` / `PasswordAlgo`(`json:"-"` 永不外泄)+ `TokensValidAfter`(`json:"tokensValidAfter,omitempty"`)。
- 新增 `store.AuthRepository` 接口 + `postgres.AuthStore` 实现;`service.AuthService` 业务编排;`internal/auth/ratelimit` Redis sliding window;`internal/auth.TokensCache` 进程内缓存 + 后台 refresher;`internal/auth.JWTRegisteredClaimsAt` / `Claims.IsRevokedBy` 辅助。
- `internal/auth.JWTMiddleware` 接入 `TokensCache`;签名校验通过后比对 `cache.Get(userId) > iat`,触发 401 (`ErrTokenRevoked`)。
- 新增业务码:`response.CodeRateLimited = 1429`(对应 429)。
- `config.AuthConfig` 加 8 个可配字段:`register_enabled` / `password_min_length` / `login_rate_limit` / `login_rate_limit_window` / `lockout_duration` / `tokens_cache_refresh` / `token_ttl` / `private_key`(生产 RSA/ECDSA/Ed25519 PEM)。

详细见 `design/DESIGN.md`「认证 & 用户（v9）」节。

v10 增量（Folder list 嵌套子 folder,消除 N+1 round-trip）:`POST /api/v1/folder/list`
在 `environmentId` 模式(level=1 父列表)下,前端通常还要对每个父 folder 再调一次
list(传 `folderParentId`)才能拼出两级树;在 env 下 folder 数量不大时,这 N+1 次
往返既慢又费 audit/audit-quota。本轮加 `includeSubfolders` 开关:
- 请求 body 在 PageRequest + `environmentId`/`folderParentId` 基础上加可选
  `includeSubfolders:boolean`(默认 `false`)。
- 校验:`includeSubfolders=true` 时必须传 `environmentId`,否则
  `400 1002 includeSubfolders only valid with environmentId`(`folderParentId`
  模式当前 schema 只到 level=2,无 level=3 可嵌)。
- 响应:触发后 `data.list` 元素从 `Entity` 升级为 `FolderWithSubfolders`
  (`allOf: [Entity, { subfolders: [Entity] }]`),`subfolders` 永远是数组(空时 `[]`
  而非 `null`);不传或 `false` 时 list 项完全等同现有 `Entity`,**不带** `subfolders`
  字段(omitempty),**与历史 100% 兼容**。
- 权限:子 folder 走与父 folder 同样的 `folder:read` narrowing,复用 v7
  `userReadScopeCTE` / `narrowingPredicate` / `scopeNarrowingWhere`;narrowing
  chain 与 `ListFolders` 一致 `(folder, environment, project, organization)`;
  父通过但子不通过 → 子不出现在 `subfolders`。
- 数据层:新增 `ResourceRepository.ListFolderChildren(ctx, callerUserId, parentIds) (map[string][]Entity, error)`,
  单 SQL `WHERE t.parent_id = ANY($3::uuid[])` + narrowing 链;`ORDER BY t.parent_id ASC, t.name ASC`;
  空 `parentIds` 直接返回空 map(不发 SQL);返回 map 始终非 nil。
- 关键文件:`internal/store/store.go`(接口)、`internal/store/postgres/repository.go`(实现)、
  `internal/http/controller/resource_folder.go`(DTO × 2 + 改 1 个分支 + 新 validator)、
  `design/api/core.yaml`(request + response schema + description)、`design/DESIGN.md` 新增「Folder list 嵌套子 folder（v10）」节。

详细见 `design/DESIGN.md`「Folder 路径访问 - Folder list 嵌套子 folder（v10）」节。

v11 增量（Secret 批量创建,跨 env 同 key 多 value,显式指定每个 env 的目标 folder）:`POST /api/v1/secret/create` 是单条创建。当用户要在一个 project 下多个 env 里给同一个 key(如 `DATABASE_URL`)设置不同 value 时,需要 N 次 round-trip,N 次权限校验 + N 条 audit。本轮加 `POST /api/v1/secrets/batchCreate`(注意:复数 `secrets`,与单条 `/secret/*` 区分,**单独路由组**,不混在 `/secret/*` 下),接收 `secretList`(`[{key, comment?, envList: [{envCode, folderId, value}, ...]}]`),**每个 env entry 客户端显式指定目标 folderId + value**,**单事务**完成 N 条 INSERT + 1 条 batch audit,全成功或全 rollback。
- 端点: `POST /api/v1/secrets/batchCreate`(独立路由组 `/secrets`,不与单条 `/secret/*` 混)。
- 入参: `{secretList: [{key, comment?, envList: [{envCode, folderId, value}, ...]}]}`。要求 `secretList` 非空,每条 `key` 通过 `^[A-Z][A-Z0-9_]*$`,每条 `envList` 至少 1 项,每项 `envCode` / `folderId` 必填;同 item 内 `envCode` 不可重复、`folderId` 不可重复。
- v12 调整:把原 4 个硬编码 env 字段(`dev` / `test` / `sim` / `prod`)替换为 `envList` 数组。env 不再限于标准 4 个,项目下任意 env code 都可作为入参;envList 长度 = 该 key 要创建的 secret 数。service 层对 envList 做 trim + 去重 + 顺序透传。
- 关键约束(**业务错也 HTTP 200,统一 code=-1**):与单条 CreateSecret / UpdateSecret 不同,本端点**所有失败**(包括入参校验 / 权限 / 冲突 / 内部错误)统一走 HTTP 200 + body code=-1 + msg 中文描述。前端按 `body.code` 判定,只要 != 0 即失败。约定:
  - 成功 → 200 + body code=0 + `msg: "success"` + `data: null`
  - 任何失败 → 200 + body code=-1 + `msg: "<前缀>，<描述>"` + `data: null`
    - 入参校验失败 → `msg: "入参校验，<校验错误描述>"`
    - 权限不足 → `msg: "创建失败，权限不足"`
    - 目标 folder 不存在 → `msg: "创建失败，目标 folder 不存在"`
    - (folder, key) 冲突 → `msg: "创建失败，secret 已存在"`
    - 其他内部错误 → `msg: "创建失败，<err.Error()>"`
- 编排:`SecretService.BatchCreate` 1) 防御性二次校验 `user.UserId` 非空 / `secretList` 非空 / 每条 `key` 合法 / 每条 `envList` 非空 / 每个 entry `envCode`+`folderId` 非空;2) 展开 `secretList × envList` 为 (envCode, folderId, value, key, comment) target 序列;3) 对每个 target 单独 `secret:create` 权限 check(v7 cascade narrowing),任一拒绝即整批失败、err 透传;4) 构造 `[]store.BatchCreateSecretItem` 调 `r.BatchCreateSecrets`;5) commit 后逐条 `cacheUpsert` 到 Redis SecretCache。
- 事务:repo `BatchCreateSecrets` 单 SQL 事务,N 条 INSERT(每条 RETURNING id)+ 1 条 `recordAuditTx(action="create_batch", resource_type="folder", resource_id=第一条 item 的 folder_id, encrypted_value=jsonb([{envCode, key, secretId}, ...]))`。任一 INSERT 失败(unique violation 等)→ 整批 rollback,`translatePgErr` 翻译为 `domain.ErrConflict` 透传。
- 不支持:`folderParentId` 嵌套 / `includeSubfolders` 嵌套(与单条 CreateSecret 保持一致);env entry `envCode` / `folderId` 为空字符串(整条 item 拒绝);同 item 内重复 `envCode` 或重复 `folderId`(入参错);跨 project 批量创建(每条 item 各自的 folderId 必须在 caller 有 secret:create 权限,无 project 级批量语义)。
- 关键文件:`internal/store/store.go`(`ResourceRepository.BatchCreateSecrets` + `BatchCreateSecretItem`)、`internal/store/postgres/repository.go`(`BatchCreateSecrets` 单事务 N INSERT + 1 batch audit)、`internal/service/secret_service.go`(`SecretService.BatchCreate` + 3 个新类型 `BatchCreateRequest` / `BatchCreateSecretSpec` / `BatchCreateEnvTarget`)、`internal/http/controller/resource_secret_batch.go`(handler + DTO + `writeBatchCreateError`)、`internal/http/response/response.go`(`CodeBatchCreateError = -1` + `OKWithCode`)、`internal/http/router.go`(`/secrets` 路由组下的 `/batchCreate`)、`design/api/core.yaml`(新端点 + 新 schema)、`design/DESIGN.md` 新增「Secret 批量创建（v11）」节。

v12 增量（Secret 跨 env 列表查询,精确查 (project, [folderCode], [keyList]) 跨 envList）:`/secret/list` / `/secret/search` / `/secret/path/batchReveal` 已有端点都存在 over-fetch / under-fetch 的痛点:要么一次只能查一个 env(单 folder 维度),要么聚合整个项目全量(group by 之后),无法精确指定某几个 key 在某几个 env 下的值。v12 新增 `POST /api/v1/secrets/list`(与 batchCreate 同在 `secrets` 复数路由组):**精确查 (project, folderCode, keyList) 在上送 envList 命中 env 下的值**,跨 env 一次性 reveal。
- 端点:`POST /api/v1/secrets/list`。
- 关键扩展:**`keyList` 为可选**。keyList 非空 → 精确查 (folderCode, k) 跨 envList 中每个 k,folderCode 必填;keyList 为空 → 返 (folder, key) 跨 envList,**`folderCode` 是独立过滤维度**(`''` = 跨所有 folder,非空 = 限定到该 folder)。
- 入参:`{projectId, folderCode?, keyList?, envList}`。`projectId` 必填;`keyList` 非空时对每个 key 校验 `^[A-Z][A-Z0-9_]*$` + `folderCode` 必填;`keyList` 长度 1..32(trim+去重+空过滤后);`envList` 必填(1..32,trim+去重+空过滤);keyList 为空时 `folderCode` 仍可作为有效过滤条件。
- 响应:`data` 永远是 `SecretAcrossEnvs` 数组(keyList 长度=1 → 1 元素;keyList 长度=N → N 元素,未命中的 key 用占位元素;keyList 为空 → 命中的 (folder, key) 组数,无命中返 `[]`)。`SecretAcrossEnvs` 走自定义 `MarshalJSON` 把 env 展平到顶层:`{projectCode, key, comment, "<envCode>": {value, version, comment, updatedAt} | null, ...}`。未命中的 env 序列化为 `null`(用 `*EnvSecretValue` 指针 + 自定义 `MarshalJSON` 兜底)。**不回显请求参数**:`projectId` / `folderCode` 不放进响应;`projectCode` 是从 secret 派生的标识,保留供前端展示。
- 权限:`secret:read`(v12 起由 `secret:reveal` 放宽为 `secret:read`),走 v7 cascade narrowing 自动收窄(无 read 权限的 secret 在 SQL 层就被收窄掉,不进 result)。语义:持有 `secret:read` 即有资格看到 secret 的明文;service 端不再单独校验 `secret:reveal`,所有 result 内的 secret 都直接解密填明文。这把"是否能看到明文"从细粒度 reveal 降级为粗粒度 read,符合批量浏览场景(持有 read 即可直接看到 plaintext);单点 `/secret/reveal` 仍走 `secret:reveal` 保持细粒度控制。SQL 用 `user.UserId` + action=`secret:read` 走两个 repo 方法:`ListSecretsByProjectFolderKey(keys []string)`(keyList 非空精确,SQL 用 `cardinality($5::text[]) > 0 and s.key = any($5::text[])` 匹配)/ `ListSecretsInProjectByEnvs(folderCode?)`(keyList 为空,folderCode 用 `$5::text = '' or f.code = $5` 兜底),共用 v7 `userReadScopeCTE` + `scopeNarrowingWhere`。
- Audit:整批 1 条 `action="reveal_batch"`,`resource_type="project"`,`resource_id=projectId`。payload 区分两种形态:
  - keyList 非空:`{folderCode, keys:[...], envList, hits}`(同 BatchRevealByPath,key 字段改为 keys 数组)
  - keyList 为空:`{keys:[], envList, keyCount, totalHits, items:[{folderCode, key}, ...]}`
  无命中(空 groups)时不写 audit。
- 编排:`SecretService.ListAcrossEnvs` 1) 防御性二次校验 `user.UserId` 非空 / `projectId` 非空 / keyList 格式(非空时逐个) / envList trim+去重+空过滤+长度 1..32 / keyList 长度上限 32;2) 按 keyList 是否为空分两个 repo 分支拉取(folderCode 始终透传);3) keyList 非空分支按 cleanedKeys 长度返 N 元素(含未命中占位),keyList 为空分支按 (folderCode, key) 聚合为多组 `SecretAcrossEnvs`;4) 解密每条填 `EnvSecretValue`;5) 整批 1 条 audit(有命中才写);6) 返 `[]*SecretAcrossEnvs`。
- bug fix 记录:首版 `key 为空` 分支没把 `folderCode` 透传到 repo,导致 `folderCode` 传了不生效。v12.x 修复:`ListSecretsInProjectByEnvs` 接口增 `folderCode` 参数,SQL 加 `($5::text = '' or f.code = $5)` 兜底;`service.ListAcrossEnvs` 始终把 `folderCode` 透传(无论 keyList 是否为空)。回归测试 `TestListAcrossEnvs_KeyEmpty_FolderCodeProvided` 锁住此行为。v12.x 进一步把请求字段从 `key`(string)升级为 `keyList`(`[]string`),Repo `ListSecretsByProjectFolderKey` 签名同步改为接收 `keys []string`,SQL 用 `s.key = any($5::text[])` 精确匹配;回归测试 `TestListAcrossEnvs_KeyListMultiple` / `TestListAcrossEnvs_KeyListInvalid` / `TestListAcrossEnvs_KeyListWithoutFolderCode` 锁住 keyList 行为。
- 关键文件:`internal/domain/cross_env_secret.go`(`EnvSecretValue` + `SecretAcrossEnvs` + 自定义 `MarshalJSON`)、`internal/store/store.go`(`ResourceRepository.ListSecretsByProjectFolderKey` + `ListSecretsInProjectByEnvs(folderCode, envCodes)`)、`internal/store/postgres/repository.go`(两条 SQL 实现,共用 v7 cascade narrowing,folderCode 兜底分支;keyList 用 `cardinality($5::text[]) > 0 and s.key = any($5::text[])` 过滤)、`internal/service/secret_service.go`(`SecretService.ListAcrossEnvs` + 实现,folderCode 始终透传;cleanedKeys = trim+去重+空过滤+正则+长度≤32)、`internal/http/controller/resource_secret.go`(`ListSecretsAcrossEnvs` handler + `secretListAcrossEnvsRequest` DTO,`KeyList []string` 字段)、`internal/http/router.go`(`/secrets` 路由组下的 `/list`)、`design/api/core.yaml`(新端点 schema)、`design/DESIGN.md` 新增「Secret 跨 env 列表（v12）」节。

详细见 `design/DESIGN.md`「Secret 批量创建（v11）」节、「Secret 跨 env 列表（v12）」节。

## v5：数据接口 RBAC 接入 + 路径访问 + RBAC 命名澄清

> 本节为 v5 的最终设计,已按代码落地。覆盖三块改动:
> 1. 数据 handler 全部接入 `allowScope`(Phase A);
> 2. Secret 路径访问 `org_code.project_code.env_code.folder_code.KEY`(Phase B);
> 3. RBAC 字段命名在 domain 和 API 层统一为 `userId` / `roleType` / `resourceType` / `resourceId`(Phase C)。

### 1. 数据 handler RBAC 接入矩阵

每个 handler 的 `allowScope` 矩阵(scope 取请求体中最深的可用字段):

| Handler | Permission | scopeType | scopeId 来源 |
| --- | --- | --- | --- |
| `ListOrganizations` | `org:read` | `global` | `""` |
| `CreateOrganization` | `org:create` | `global` | `""` |
| `GetOrganization` | `org:read` | by-code: 先 `GetOrganizationByCode` 拿 id,再 `organization`/`id`;by-id: `organization`/`req.Id` | 视入口 |
| `UpdateOrganization` | `org:update` | 同上 | 同上 |
| `DeleteOrganization` | `org:delete` | 同上 | 同上 |
| `ListProjects` | `project:read` | `organization` | `req.OrgId` |
| `CreateProject` | `project:create` | `organization` | `req.ParentId` |
| `GetProject` / `UpdateProject` / `DeleteProject` | `project:read/update/delete` | by-code: `project`/`req.Id`(先 `GetProjectByCode` 拿 id);by-id: `project`/`req.Id` | 视入口 |
| `ListEnvironments` | `env:read` | `project` | `req.ProjectId` |
| `CreateEnvironment` | `env:create` | `project` | `req.ParentId` |
| `GetEnvironment` / `UpdateEnvironment` / `DeleteEnvironment` | `env:read/update/delete` | by-code: `environment`/`id`;by-id: `environment`/`req.Id` | 视入口 |
| `ListEnvironmentTemplates` | `env:template:read` | `organization` | `req.OrgId` |
| `GetEnvironmentTemplate` | `env:template:read` | by-code: `env_template`/`id`;by-id: `env_template`/`req.Id` | 视入口 |
| `ListFolders` | `folder:read` | `environment` | `req.EnvironmentId` |
| `CreateFolder` | `folder:create` | `environment` | `req.ParentId` |
| `GetFolder` / `UpdateFolder` / `DeleteFolder` | `folder:read/update/delete` | by-code: `folder`/`id`;by-id: `folder`/`req.Id` | 视入口 |
| `CreateSecret` | `secret:create` | `folder` | `req.FolderId` |
| `UpdateSecret` | `secret:update` | `secret` | `req.Id` |
| `RevealSecret` | **`secret:reveal`** | `secret` | `req.Id` |
| `GetSecret` | `secret:read` | `secret` | `req.Id` |
| `ListSecrets` | `secret:list` | `FolderId` 非空走 `folder`;否则 `environment` | 取最深 |
| `SearchSecrets` | **`secret:search`** | 同上 | 取最深 |
| `DeleteSecret` | `secret:delete` | `secret` | `req.Id` |
| `ListAuditRecords` | `audit:read` | `req.ResourceType`/`req.ResourceId`;空时回退 `("global", "")` | 视入口 |

关键点:

- **`RevealSecret` 用 `secret:reveal`**,不与 `secret:read` 混用。`project_viewer` 有 read 但无 reveal,`project_developer` 同时有两者。
- **`SearchSecrets` 用 `secret:search`**(跨 folder 的 keyword 检索,权限上比 list 高一档)。
- **`ListSecrets` / `SearchSecrets` scope 策略**:FolderId 非空走 `folder` scope,否则 EnvironmentId 非空走 `environment` scope。RBAC 继承链在 `ResourceScopes` 内部走完。
- **`Get/Update/Delete Organization` by-code 路径**:先 `GetOrganizationByCode` 拿 id,再以 id 做 RBAC 校验,保证 `org_admin` 绑在 org X 上后用 `code` 也能命中。
- **`DeleteSecret` 重构**:从 `ctrl.delete(c, fn)` 展开为 `bind → allowScope → Delete`,权限校验在删之前,先于业务事务。
- `platform_admin` 默认拥有所有权限码,`org_admin` / `project_admin` / `project_viewer` / `project_developer` 等角色在 v5 真正生效。

### 2. Secret 路径访问

#### 2.1 路径格式

```text
org_code.project_code.env_code.folder_code.KEY
```

例如:`o1.p1.dev.globals.DATABASE_URL`。

#### 2.2 单 SQL 5 表 join

`internal/store/postgres/repository.go::GetSecretByPath` 一次 round-trip 解析:

```sql
select s.id, o.id, o.code, p.id, p.code, e.id, e.code, s.folder_id, f.code, s.key, s.comment, s.version,
       s.created_by, s.updated_by,
       s.created_at, s.updated_at
from secrets s
join folders f
  on f.id = s.folder_id
 and f.code = $5
 and f.is_deleted = false
join environments e
  on e.id = f.environment_id
 and e.code = $4
 and e.is_deleted = false
join projects p
  on p.id = e.project_id
 and p.code = $3
 and p.is_deleted = false
join organizations o
  on o.id = p.org_id
 and o.code = $2
 and o.is_deleted = false
where s.key = $1
  and s.is_deleted = false
limit 1
```

执行计划:

- 4 步 index-nested-loop,每步命中一个 `(parent_id, code) where is_deleted = false` 唯一索引;
- 任意一段 code 找不到 → 0 rows → `ErrNotFound`;
- `Path` 字段由 `buildSecretPath` 自动拼接,值与 SQL 中的 code 一致。

#### 2.3 接口

| 路径 | 用途 | 权限 |
| --- | --- | --- |
| `POST /api/v1/secret/path/info` | 路径查询 secret metadata,不返回明文 value | `secret:read` |
| `POST /api/v1/secret/path/reveal` | 路径查询 secret 明文 value,走加密 + reveal 审计 | `secret:reveal` |

请求体:

```json
{ "path": "o1.p1.dev.globals.DATABASE_URL" }
```

#### 2.4 解析规则

`internal/service/secret_service.go::parseSecretPath`:

- 段间分隔符 `.`,共 5 段;
- 任意一段为空或段数 != 5 → 返回 `invalid secret path` 错误;
- 路径两侧空白被 `TrimSpace` 吃掉,中间空白不被处理。

#### 2.5 效率与侧信道

- 4 级 code 解析 + 最终 secret 查找,共 5 步 index-nested-loop,全走唯一索引,实测单次 < 5ms(本地 PG,常规数据量);
- 相对"先 4 个 lookup 接口再 reveal"方案减少 4 次网络 round-trip,SDK 端代码从 5 次请求降为 1 次;
- 路径接口在 `GetByPath` 成功后做 RBAC(`secret:read` / `secret:reveal`);无权限用户拿到 403,不会泄漏 secret 元数据。本次不隐藏侧信道(无权用户仍可通过 200 vs 403 时延差推断存在性),后续若需要严格防探测,把 `allowScope` 失败时的 403 改 404 即可。

### 3. RBAC 命名澄清

#### 3.1 字段映射

domain 层和 HTTP API 层把内部实现术语映射为 SDK 友好的"用户-角色-资源"三段式:

| 内部表/字段 | domain / API 字段 | 说明 |
| --- | --- | --- |
| `users.external_user_id` | `userId` | 客户端标识 |
| `roles.code` | `roleType` | 角色码,例如 `org_admin` |
| `user_role_bindings.scope_type` | `resourceType` | `global` / `organization` / `project` / `environment` / `folder` / `secret` / `env_template` |
| `user_role_bindings.scope_id` | `resourceId` | global 时为空 |
| `user_role_bindings.expires_at` | `expiresAt` | RFC3339 |
| `user_role_bindings.granted_by` | `grantedBy` | 授权人 external_user_id |
| `user_role_bindings.created_at` | `grantedAt` | 授权时间 |

#### 3.2 domain 层

`internal/domain/rbac.go` 新增 `RoleGrant` 类型(语义别名,字段一一对应)和 `(RoleBinding).ToGrant()` 转换方法。`RoleBinding` 字段保持不变,store/service 层不动。新类型仅在 HTTP API 响应中用。

#### 3.3 HTTP API alias

请求体 `roleGrantRequest` / `userLookupRequest` / `pagedUserLookupRequest` 新增 alias 字段:

| 旧字段 | 新 alias | 解析优先级 |
| --- | --- | --- |
| `externalUserId` | `userId` | alias 非空(TrimSpace)时优先,否则回退旧字段 |
| `roleCode` | `roleType` | 同上 |
| `scopeType` | `resourceType` | 同上 |
| `scopeId` | `resourceId` | 同上 |

旧字段仍然兼容,绑定逻辑取 alias 优先。响应(`ListRoleBindings` / `ListUserGrants` / `GrantRole`)字段从 `RoleBinding` 整结构转成 `RoleGrant`,SDK 看到的是三段式;其他模块仍按 `RoleBinding` 消费,无 breaking change。

### 4. 风险

- `allowScope` 接入是隐式 breaking change:旧客户端过去能"通杀"的接口现在按 role 拒绝。`platform_admin` 默认拥有所有 code,部署时确保至少一个 platform_admin 存在(已有 `ENVVAULT_BOOTSTRAP_ADMIN_USER_ID` 兜底)。
- `Get/Update/Delete Organization` by-code 路径多一次 `GetOrganizationByCode`,走 `(code) where is_deleted=false` 唯一索引,可忽略。
- `path/info` / `path/reveal` 的侧信道:本期不处理。
- `RoleGrant` 响应字段是新增,`RoleBinding` 内部不变,其他模块(store/service)不感知;只有 controller 出口变。
- 未触及 schema 与 `defaultPermissions` / `defaultRoles`。

---

## v6:权限下沉到 service + RBAC 管理接口边界校验

> 本节为 v6 的设计目标与已落地 / 未落地项。设计总纲见 `design/rbac_degisn.md`,本次不重写,本节只登记 v6 的具体改动。
>
> 核心改动:
> 1. 权限判定统一接口:所有调用走 `auth.Authorizer.Allow`(已存在,本次无接口改动);
> 2. 权限调用点从 controller 下沉到 service(`SecretService` 9 + `RBACService` 11,共 20 个方法);
> 3. RBAC 管理接口加 caller 权限子集校验 + platform_admin 保护 + 最后一个 owner 保护;
> 4. OpenAPI 文档显式标注 RBAC 管理接口的 super-admin 限定语义。

### 1. 权限判定统一接口(确认现状)

`internal/auth/rbac.go:28-30` 的 `Authorizer` 接口就是设计文档 §1.5 要求的"独立的一部分":

```go
type Authorizer interface {
    Allow(ctx context.Context, user UserInfo, action string, resource Resource) error
}
```

所有调用方(controller、service、未来的 worker / SDK / cron)共用这个接口。**本次只迁移调用点,不改动接口**。

### 2. Service 层调用点矩阵

v6 起,以下 20 个 service 方法入口第一行 `s.authorizer.Allow(ctx, user, permCode, resource)`:

#### 2.1 SecretService(9 个方法)

| 方法 | permission code | scope |
|---|---|---|
| `Create` | `secret:create` | folder |
| `Update` | `secret:update` | secret |
| `Delete` | `secret:delete` | secret |
| `Get` | `secret:read` | secret |
| `Reveal` | `secret:reveal` | secret(独立码) |
| `GetByPath` | `secret:read` | secret(先解 path 拿 id) |
| `RevealByPath` | `secret:reveal` | secret(先解 path 拿 id) |
| `List` | `secret:list` | folder(envId 时 env) |
| `Search` | `secret:search` | folder(envId 时 env) |

List / Search 共享 `listScope` helper,FolderId 优先 → folder,否则 EnvironmentId → environment。

#### 2.2 RBACService(11 个方法,5 写 + 6 读)

| 方法 | permission code | 额外边界 |
|---|---|---|
| `ListPermissions` | — | 系统只读,任何已认证 user 都能调 |
| `ListRoles` | `rbac:role:read` | — |
| `GetRole` | `rbac:role:read` | global scope(GetRole 不知 caller 的目标 scope) |
| `CreateRole` | `rbac:role:manage` | ① caller perm 子集 ② global 仅 platform_admin ③ platform_admin 角色仅 platform_admin caller |
| `UpdateRole` | 同上 | 同上 + system 角色不可改 |
| `DeleteRole` | `rbac:role:manage` | global scope,system 角色不可删 |
| `ListUsers` | `rbac:binding:read` | — |
| `SyncUser` | — | 任何已认证 user 都能 sync 自己的 user 行 |
| `ListRoleBindings` | `rbac:binding:read` | — |
| `ListUserGrants` | `rbac:binding:read` | global scope |
| `GrantRole` | `rbac:binding:manage` | ① caller perm 子集 ② global 仅 platform_admin ③ platform_admin 仅 platform_admin caller |
| `RevokeRole` | `rbac:binding:manage` | ① global 仅 platform_admin ② 最后一个 `org_owner` 保护 |
| `EffectivePermissions` | `rbac:binding:read` | — |

### 3. 边界校验实现要点

#### 3.1 caller 权限子集(`checkCallerPermissionSubset`)

走 `repo.EffectivePermissions(caller, targetScope)` 拿 caller 有效权限集合,逐个比对被授权/创建角色的 `permissions` 数组。任一缺失返 `ErrPermissionDenied`。

#### 3.2 global scope 仅 platform_admin(`checkGlobalScopeOnlyPlatform`)

`scopeType == "global"` 且 caller 不是 `platform_admin` → 拒绝。

`isPlatformAdmin(caller)` 走 `ListUserGrants(caller, {1, 200})` 检查是否有 `RoleCode=platform_admin` + `ScopeType=global` 的 binding。

#### 3.3 platform_admin 角色保护

`GrantRole` / `CreateRole` / `UpdateRole` 入参 `code == "platform_admin"` 且 caller 不是 `platform_admin` → 拒绝。

#### 3.4 最后一个 `org_owner` 保护(`RevokeRole`)

撤销前 `ListRoleBindings(organization, scopeId, {1, 200})` 拉所有 active binding,统计 caller 即将撤销的 `org_owner` binding 数量,撤销后剩 `< 1` → 返 `ErrConflict` (409)。

### 4. Controller 改造

- 删除 17 处 `ctrl.allowScope(...)` 调用;
- 替换为 `user := auth.UserFromContext(c)`,然后 `s.Xxx(ctx, user, ...)` 透传;
- `secretListAllowScope` helper 删,逻辑搬进 `SecretService.listScope`;
- `ensureRBAC` 保留(它只检查 `ctrl.rbac != nil`,给 RBAC 管理 handler 做「service 是否就绪」的健康门)。

### 5. OpenAPI 文档

`design/api/rbac.yaml` 给以下 7 个管理接口加 description 块,显式说明 caller 范围 / 子集校验 / platform_admin 保护 / audit:

- `/rbac/role/{list,info,create,update,delete}` 5 个
- `/rbac/binding/{list,grant,revoke}` 3 个

合计 8 个 description 块(`/role/list` 算在 5 个里)。

### 6. 风险

- **API 行为可能改变**:`SecretService.Create` 等方法的 error 返回多了 `ErrPermissionDenied`(原 `allowScope` 在 controller 时同样返 1403,行为一致),业务无感。
- **签名级 refactor**:service 方法签名加 `user auth.UserInfo` 是 breaking change,所有 service 调用方(controller、未来的 SDK / worker)需同步更新。本次只 controller 一处调用,无第三方。
- **无 schema 变动**,无 migration 成本。
- **`isPlatformAdmin` 走 ListUserGrants**:假设单 user active binding < 200(实际 < 50),一次查询足够;若未来单 user 持有上千 binding,需换成 `HasActiveRoleBinding` 单点查询。暂不做。

### 7. v6 不做的项(留 todo)

- ✅ ~~list / search 接口的「自动收窄可见作用域」~~:v7 已完成,见下方 v7 节。
- 临时授权过期清理(后台 cron)
- 权限缓存 + 失效广播
- 审计查询细粒度过滤(及 SQL 收窄)
- `path/info` / `path/reveal` 侧信道防探测
- 7 个数据资源(Org/Project/Env/EnvTpl/Folder/Secret CRUD/Audit)的权限检查下沉(本期承认 controller 是细粒度资源权限边界,service 是业务聚合边界)

---

## v7:List 接口按 caller 权限自动收窄可见作用域

> 本节为 v7 的设计目标与已落地项,接续 v5/v6。
>
> 核心改动:
> 1. 6 个 list 方法在 repo SQL 层加 `user_read_scopes` CTE narrowing;
> 2. controller 入口移除 `allowScope`,改为透传 `callerUserId`;
> 3. `org_admin` 绑 (org, X) 时能 list 看到 X 自身(原 allowScope 是 global-only,反而看不到);
> 4. cascade 链向下展开:parent scope 自动覆盖 children。

### 1. 单一 SQL 收窄模式

每个 list 方法在 WHERE 末尾加一段:

```sql
with user_read_scopes as (
  select distinct urb.scope_type, urb.scope_id
  from user_role_bindings urb
  join users u on u.id = urb.user_id
  join roles r on r.id = urb.role_id
  join role_permissions rp on rp.role_id = r.id
  join permissions p on p.id = rp.permission_id
  where u.external_user_id = $1
    and p.code = $2
    and (urb.expires_at is null or urb.expires_at > now())
    and urb.is_deleted = false
    and r.is_deleted = false
    and u.is_disabled = false
)
select ... from {table} t
where t.is_deleted = false
  and {现有 parent 过滤}
  and (
    exists (select 1 from user_read_scopes where scope_type = 'global')
    or {narrowing predicate}
  )
```

实现细节见 `internal/store/postgres/repository.go` 末尾的
`userReadScopeCTE` / `narrowingPredicate` / `scopeNarrowingWhere` 三个 helper。

### 2. Cascade 链

| List | permission code | narrowing 谓词 |
| --- | --- | --- |
| `ListOrganizations` | `org:read` | `t.id in (… 'organization')` |
| `ListProjects` | `project:read` | `t.id in (… 'project')` ∪ `t.org_id in (… 'organization')` |
| `ListEnvironments` | `env:read` | `t.id in (… 'environment')` ∪ `t.project_id in (… 'project')` ∪ `p.org_id in (… 'organization')`(join projects) |
| `ListEnvironmentTemplates` | `env:template:read` | `t.id in (… 'env_template')` ∪ `t.org_id in (… 'organization')` |
| `ListFolders` | `folder:read` | `t.id in (… 'folder')` ∪ `t.environment_id in (… 'environment')` ∪ `e.project_id in (… 'project')` ∪ `p.org_id in (… 'organization')`(join env+project) |
| `ListSecrets` / `SearchSecrets` | `secret:list` / `secret:search` | `s.id in (… 'secret')` ∪ `s.folder_id in (… 'folder')` ∪ `e.id in (… 'environment')` ∪ `p.id in (… 'project')` ∪ `o.id in (… 'organization')`(join 4 张表) |

`secret` / `env_template` 两种 scope_type 当前没有任何 role 会绑在这两个层级,分支恒为 false;
保留谓词以支持未来「给单个 secret / env_template 授权」的扩展。

### 3. Caller 行为矩阵

| Caller 状态 | 行为 |
| --- | --- |
| 无 JWT(中间件已 401) | 不会到达 handler |
| `externalUserId == ""`(异常路径) | CTE 返空 → 全部 list 返空(不 500、不 403) |
| `platform_admin`(绑在 global) | EXISTS 命中 → 全量 |
| `org_admin @ (organization, X)` | org 链命中 → 看到 X 及 X 下所有 project / env / folder / secret(cascading) |
| `project_viewer @ (project, Y)` | project 链命中 → 看到 Y 及 Y 下所有 env / folder / secret,看不到 X org 自身(无 `org:read`) |
| `folder_viewer @ (folder, Z)` | folder 链命中 → 看到 Z folder 下的 secret;看不到同 env 下别的 folder 的 secret |
| 无任何 binding | CTE 空 → 空 list |

### 4. 隐式空 list vs 显式 403

本轮统一**隐式空 list**(no binding → empty),不返 403。理由:

- 性能:返 403 仍要走一遍权限计算;空 list 直接 CTE 不命中,开销更小。
- 语义:「我看的就是我能看的」,和现有的"我看不到就是没有"心智模型一致。
- 安全:caller 无法通过 200/403 时延差探测存在性。

### 5. 改动清单

| 文件 | 改动 |
| --- | --- |
| `internal/store/store.go` | `ResourceRepository` 6 个 list 方法加 `callerUserId` 参数;`ListSecrets` 额外加 `action string` |
| `internal/store/postgres/repository.go` | 6 个 list 方法 SQL 追加 narrowing 子句;新增 `userReadScopeCTE` / `narrowingPredicate` / `scopeNarrowingWhere` helper |
| `internal/service/secret_service.go` | `List` / `Search` 透传 `user.UserId` 给 repo,移除 `listScope` 入口校验;`Search` 不再走 `SecretCache`(cache 不感知 user) |
| `internal/service/secret_service_test.go` | 删除 `TestListScope_FolderIdPreferred` / `TestListScope_FallsBackToEnv` / `TestListScope_EmptyFilterRejects`;新增 `TestSecretService_List_PassesCallerUserId` / `TestSecretService_List_NoUserIdRejects` / `TestSecretService_Search_PassesActionSearch` / `TestSecretService_Search_NoUserIdRejects` |
| 6 个 controller | 移除 `allowScope` 入口,`auth.UserFromContext(c).UserId` 透传给 repo |
| 文档 | `design/DESIGN.md`「List 接口按 caller 权限收窄（v7）」节 + 本节 |

### 6. 风险与权衡

- **接口 breaking change**:`ResourceRepository` 6 个 list 方法加参数,所有 caller(controller)一次性更新;`grep` 确认就 6 个 controller 调,无外部 caller。
- **行为变化**(隐式 vs 显式):`org_admin` 绑在 (org, X) 调 `ListOrganizations` 之前被 403(因 allowScope 是 global-only),现在能看到 org X(向后**放宽**,符合 cascade 目标 3)。无安全性回退。
- **性能**:CTE 子查询是 user bindings 维度(通常 0-50 行),5 个 `IN (SELECT … FROM user_read_scopes WHERE scope_type=…)` 走 `urb.scope_type` 索引;platform_admin(global 命中)走 EXISTS 短路;实测应无明显回归。
- **`SecretCache` 失效路径**:`Search` 不再走 cache,DB round-trip 一次。`Search` 本就是 keyword 模糊查询,DB 全表扫;cache 的加速对 keyword 检索本来就有限,影响可控。
- **回退**:若 SQL 收窄出错,只需把 6 个 list 方法的 SQL 改回原样,controller 重新加 `allowScope` 即可,改动局部可控。

### 7. v7 不做的项(留 todo)

- **Audit 收窄**:`ListAuditRecords` 的 `resource_type` 多态(`organization` / `project` / `environment` / `folder` / `secret` / `env_template` 每种 ancestry 不同),SQL 收窄需要 case-by-case join 资源表,工作量大;本期按"by-id 查具体资源"绕过,不收窄。
- **RBAC 管理端 list 收窄**:`ListRoles` / `ListRoleBindings` / `ListUsers` / `ListUserGrants` 是 RBAC 自身管理界面,语义和数据 list 不同。
- **GlobalSearch 收窄**:已经在 controller 层做了 per-hit allowScope(`search.go:106-155`),工作量不大但也属另一条线索。
- **`SecretCache` 是否保留**:目前 `Search` 不用,但 `ListSecretCacheRecords` 仍被 warm-up 调用;长期看 cache 只在 startup 期间用,后续可考虑移除。

---

## v3：环境归项目所有 + org 层 environment_templates

> 本节为 v3 的最终设计，已按清库重建方式落地到 `configs/schema.sql` 与代码。
> 与 v2 的"Org 共享 env"思路相反：env 不再共享，org 层只保留一份只读模板快照。
> v2 草稿位于下方 `## 完整新设计：Org 共享环境 + Per-Project Folder（v2）` 与
> `## Org 共享 env 模型下的权限设计` 两节（已标注 superseded by v3），保留作历史参考。

### 背景

v2 让 env 归属 org 并通过 `project_environments` 共享给各 project，落地过程中发现：

- folder 需额外标 project 才能切断数据互见，模型复杂。
- 业务上每个 project 的 dev/test 配置基本不互通，共享 env 没带来实际复用。
- `internal/store/postgres/rbac.go` 的 `environmentScopes` / `folderScopes` / `secretScopes`
  已经在 join `e.project_id`，但 schema 中 environments 当时没有该列，潜在 bug 未触发。

v3 把 env 直接挂到 project 下，org 层另起一份只读模板表作为"曾经出现过的 env code 名册"。

### 数据模型调整

**environments 表调整**：
- `org_id` → `project_id`（env 重新归属 project）
- 唯一索引改为 `(project_id, code) where is_deleted = false`

**删除关联表**：
- `project_environments` 整表删除

**新增模板表**：
```sql
create table if not exists environment_templates (
    id uuid primary key,
    org_id uuid not null references organizations(id),
    code text not null,
    name text not null,
    comment text not null default '',
    is_deleted boolean not null default false,
    deleted_at timestamptz,
    deleted_by text not null default '',
    created_by text not null default '',
    updated_by text not null default '',
    created_at timestamptz not null default now(),
    updated_at timestamptz not null default now(),
    constraint environment_templates_code_chk check (code ~ '^[a-z0-9]+(-[a-z0-9]+)*$')
);

create unique index if not exists environment_templates_org_code_active_uidx
    on environment_templates (org_id, code)
    where is_deleted = false;
```

模板语义：仅记录"该 org 曾经出现过的 env code 与首次写入时的 name / comment"；
后续修改或删除 env 都不会回写模板；upsert 走 `ON CONFLICT (org_id, code) WHERE is_deleted = false DO NOTHING`。

### HTTP 接口变更

- `POST /api/v1/project/create`：移除 `environmentIds`，新增 `environments: [EnvSpec]`；
  服务端在事务中创建 env、默认 folder、并对每个 env code upsert 模板。
- `POST /api/v1/env/list`：入参 `orgId` 改为 `projectId`（required）。
- `POST /api/v1/env/{create,info,update,delete}`：请求体 `parentId` 语义改为 projectId。
- `POST /api/v1/env/template/list`、`POST /api/v1/env/template/info`：新增，只读，
  按 `(orgId, ...)` 过滤。
- `POST /api/v1/secret/*`：服务端 join 链移除 `project_environments`，改为
  `join projects p on p.id = e.project_id`。

### RBAC 调整

- 新增权限码 `env:template:read`（resource_type=`env_template`，action=`template:read`），
  在 `internal/auth/rbac.go` 的 `permissionCode` 中硬编码分支。
- `internal/store/postgres/rbac.go` 的 `ResourceScopes` 增加 `env_template` 分支，
  返回 `{global, organization(orgId)}` 两层 scope。
- `org_viewer` / `org_auditor` 默认角色获得 `env:template:read`。
- `org_admin` / `project_admin` 原本就具备 `env:create/env:update/env:delete`，
  行为不变；rbac.go 的 `environmentScopes` / `folderScopes` / `secretScopes` 之前已经在
  join `e.project_id`，schema 改完后**自动**正确，无需再改 SQL。

### 当前限制

- `env_template:read` 权限码已注册但 RBAC authorizer 尚未接到 env/template 控制器，
  留待后续接入。
- 不写数据迁移脚本，按清库重建方式落地。

---

## 已完成的设计

### 业务路径 code 设计

业务路径格式：

```text
org_code.project_code.env_code.<folder_path>.KEY
```

示例：

```text
organization-a.project-a.dev.globals.DATABASE_URL
organization-a.project-a.dev.groups-secrets.payment.DATABASE_URL
```

### 字段职责

核心实体采用 `id + code + name` 三类字段：

- `id`：内部主键，UUID，用于数据库外键、RBAC、审计、逻辑删除历史等内部关联。
- `code`：业务路径标识，用户创建时指定，创建后不允许修改。
- `name`：展示名称，仅用于前端展示，不参与唯一约束。

### code 规则

适用表：organizations、projects、environments、folders

规则：
- 创建时必填，创建后不允许更新
- 只允许小写英文字母、数字、中横线
- 推荐正则：`^[a-z0-9]+(-[a-z0-9]+)*$`
- 不允许空字符串、中横线开头或结尾、连续中横线

### Secret key 规则

- 只允许大写英文字母、数字、下划线
- 推荐正则：`^[A-Z][A-Z0-9_]*$`
- 必须以大写字母开头
- 不允许包含 `.`

### Folder 多层级设计

Folder 数据库结构按树形设计，当前代码层限制最大深度为 2 层。

字段：
- `folders.environment_id`：Folder 所属环境
- `folders.parent_id`：父级 Folder，一级 Folder 的 `parent_id` 为空
- `folders.code`：当前层级下唯一的业务标识
- `folders.name`：展示名称

### 唯一约束

- `organizations.code`：全局唯一
- `projects.code`：同一个组织下唯一
- `environments.code`：同一个项目下唯一（原设计，已改为组织下唯一）
- `folders.code`：同一个父级下唯一
- `secrets.key`：同一个 Folder 下唯一

---

## 后续设计（尚未实现）

### Secret 版本历史

建议新增 `secret_versions` 表：

```sql
create table if not exists secret_versions (
    id uuid primary key,
    secret_id uuid not null references secrets(id),
    version integer not null,
    key text not null,
    value_ciphertext jsonb not null,
    comment text not null default '',
    changed_by text not null default '',
    changed_at timestamptz not null default now()
);

create unique index if not exists secret_versions_secret_version_uidx
    on secret_versions (secret_id, version);
```

### 文档更新

需要更新：
- `design/DESIGN.md` - 更新环境模型设计
- `design/api/core.yaml` - 更新环境相关接口
- `README.md` - 更新配置说明
- `configs/schema.sql` - 更新表结构

---

## 完整新设计：Org 共享环境 + Per-Project Folder（v2，superseded by v3）

> ⚠️ superseded by v3（见上方 `## v3：环境归项目所有 + org 层 environment_templates`）。
> 状态：历史设计草稿，未实现。保留作为思考过程参考。
> 所有写库变更按 v3 走"清库重建"路径。
>
> 范围：项目—环境绑定模型、Folder 2 级、Secret 路径快查。
>
> 评审重点：环境归属选择、Folder 作用域、Path 唯一性、软删级联。

### 1. 实体关系模型

```text
organization
  └─ project
       └─ project_environments  ── (对应关系，env 共享)
              └─ environment (属于 org，跨项目共享)
                    └─ folder（仅在 (project, env) 下存在，project 间不共享）
                          └─ 可选二级 folder
                                └─ secret (key 在 folder 内唯一)
```

四层关系链：

- **org**：顶层租户，全局唯一。
- **project**：org 下。
- **environment**：**org 下所有项目共享**。project ↔ env 是绑定关系，由 `project_environments` 表达。
- **folder**：**作用域 = (project, env)**。同一个 env 绑给 project A 和 B 时，A 在该 env 下有自己的 folder 树，B 在该 env 下有自己的 folder 树；两个 folder 树互不可见。
- **secret**：folder 下，key 在 folder 内唯一。

### 2. 关键设计决策

#### 2.1 code 唯一性作用域（已确认正确）

| 表 | 唯一索引 | 作用域 | 是否已支持 |
| --- | --- | --- | --- |
| `organizations` | `(code)` 全局 | org 是顶层租户，合理 | ✅ |
| `projects` | `(org_id, code)` | org 内唯一（不同 org 可同名 projecta） | ✅ |
| `environments` | `(org_id, code)` | org 内唯一 | ✅ |
| `folders` | `(project_id, environment_id, coalesce(parent_id, ''), code)` | (project, env, parent) 内唯一 | ❌ 待改 |
| `secrets` | `(folder_id, key)` | folder 内唯一（你说"KEY 是当前 folder 下唯一"） | ✅ |

> 结论：除 `folders` 唯一索引需要升级外，其他表的 code 唯一作用域已经符合设计。

#### 2.2 env 归属选择

| 方案 | A. Project 私有 env | B. Org 共享 env（推荐） |
| --- | --- | --- |
| env 表 | `environments.project_id` | `environments.org_id`（当前实现） |
| 跨项目复用 | 不可 | 可 |
| folder 作用域 | folder.env_id 已隐含项目信息，**folder 不需要 project_id** | folder 必须显式标 `project_id` |
| 运维成本 | 10 个项目要 dev/test/sim/prod = 40 个 env | 10 个项目 = 4 个 env，按需绑定 |
| 满足"其他项目也可以使用" | ❌ | ✅ |
| 满足"项目与环境只是对应关系" | ❌（父子） | ✅（对应） |
| 未来灰度发布（"某 env 仅部分项目可见"） | ❌ | ✅（改绑定即可） |

**结论：选 B。** 与"env 跨项目共享"产品需求一致；folder 多一列 `project_id` 的成本可控。

#### 2.3 Folder 作用域

- 一级 folder：`parent_id is null`
- 二级 folder：`parent_id is not null` 且 `parent.parent_id is null`（深度上限 2）
- 二级 folder 可选：代码层允许，**默认 1 级，需要时再开 2 级**
- 校验：自指和深度 3+ 写入直接 reject

#### 2.4 Secret 与 Project 的隔离

- secret 不冗余 `project_id` / `environment_id` / `org_id` 列
- 唯一路径性靠 `secrets.path` 列的**唯一 B-tree 索引**保证
- path 编码了 org / project / env / folder / key，路径访问走 `where path = $1 and is_deleted = false`，O(log n) 等值查询

### 3. 数据库表结构

#### 3.1 `project_environments`（升级现有表）

```sql
create table if not exists project_environments (
    id uuid primary key,
    project_id uuid not null references projects(id) on delete cascade,
    environment_id uuid not null references environments(id) on delete cascade,
    created_by text not null default '',
    created_at timestamptz not null default now(),
    is_deleted boolean not null default false,
    deleted_at timestamptz,
    deleted_by text not null default ''
);

create unique index if not exists project_environments_active_uidx
    on project_environments (project_id, environment_id)
    where is_deleted = false;

create index if not exists project_environments_env_active_idx
    on project_environments (environment_id)
    where is_deleted = false;
```

变更点：
- 补 FK references
- 加 `created_by` / `is_deleted` / `deleted_at` / `deleted_by` 生命周期字段

#### 3.2 `folders`（重写）

```sql
create table if not exists folders (
    id uuid primary key,
    project_id uuid not null references projects(id) on delete cascade,
    environment_id uuid not null references environments(id) on delete cascade,
    parent_id uuid references folders(id) on delete cascade,
    code text not null,
    name text not null,
    comment text not null default '',
    is_deleted boolean not null default false,
    deleted_at timestamptz,
    deleted_by text not null default '',
    created_by text not null default '',
    updated_by text not null default '',
    created_at timestamptz not null default now(),
    updated_at timestamptz not null default now(),
    constraint folders_depth_chk check (
        parent_id is null
        or (parent_id is not null and parent_id <> id)
    ),
    constraint folders_code_chk check (code ~ '^[a-z0-9]+(-[a-z0-9]+)*$')
);

create unique index if not exists folders_project_env_parent_code_active_uidx
    on folders (project_id, environment_id, coalesce(parent_id::text, ''), code)
    where is_deleted = false;

create index if not exists folders_environment_idx on folders (environment_id);
create index if not exists folders_parent_idx on folders (parent_id);
```

变更点：
- 新增 `project_id` 列（folder 作用域 = (project, env)）
- 新增 `parent_id` 列（二级 folder 支持，可空）
- 替换原 `(environment_id, code)` 唯一索引为 `(project_id, environment_id, coalesce(parent_id::text, ''), code)`
- `folders_depth_chk` 防止自指和深度失控；深度 2 上限在应用层校验（写二级 folder 时检查 `parent.parent_id is null`）

#### 3.3 `secrets`（加 path 列）

```sql
alter table secrets add column if not exists path text;

create unique index if not exists secrets_path_active_uidx
    on secrets (path)
    where is_deleted = false;
```

变更点：
- 新增 `path text` 列
- 新增 path 唯一 B-tree 索引
- 旧 `secrets_folder_key_active_uidx (folder_id, key)` 保留（folder 已是 (project, env) 私有，自然满足"key 在 folder 内唯一"）

> 不在 secrets 上冗余 `project_id` / `environment_id` / `org_id`。folder 已是 (project, env) 私有的，folder_id 已经隐含了 project 和 env。

### 4. 路径格式与维护

#### 4.1 路径格式

```text
org_code.project_code.env_code.folder_code[.folder_code].KEY
```

- 一级：`org.project.env.globals.DATABASE_URL`
- 二级：`org.project.env.groups-secrets.payment.DATABASE_URL`
- 段间分隔符：`.`（与 Redis key 风格一致；secret key 不允许 `.`，整段 path 可无歧义 parse）
- 内部 folder 段分隔：也用 `.`（而不是 `/`），跟整段 path 风格统一

#### 4.2 写时计算

`CreateSecret` / `UpdateSecret` 时根据 `folderId` 计算 path 并写入：

```go
func buildSecretPath(orgCode, projectCode, envCode, folderPath, key string) string {
    return strings.Join([]string{orgCode, projectCode, envCode, folderPath, key}, ".")
}
```

`folderPath` 形如 `"globals"` 或 `"groups-secrets.payment"`，从 `folders.code` + 父 `folders.code`（若 parent_id 非空）拼接。

写流程（简化）：

```text
1. 入参 folderId
2. folders.id = folderId  →  parent_id, code
3. parent_id != null 时：folders.id = parent_id →  parent.code
4. folder.environment_id → environments.code
5. folder.project_id → projects.code
6. project.org_id → organizations.code
7. path = org_code + "." + project_code + "." + env_code + "." + folder_path + "." + key
8. update secrets set path = $path where id = $id
```

#### 4.3 不变更 path 的边界

- `code` 不可改（已 hardcode 在 schema 的不可变规则中）→ 无 rename cascade
- `folders.environment_id` 不可改（folder 移动是禁止操作）→ 无 env 移动 cascade
- 软删 env → 同事务软删所有 `project_environments`，path 不变；查询走 `where path = ? and is_deleted = false` 仍然能定位到行，但行已软删
- 软删 folder → 同事务软删所有子 folder + secret；path 全部失效（行软删）

### 5. 接口变更

#### 5.1 行为变更（既有接口）

| 路径 | 行为变更 |
| --- | --- |
| `POST /api/v1/project/create` | `environmentIds` 改为可空数组；**不传/空数组 → 不绑任何 env**（移除 `dev/test/sim/prod` 默认） |
| `POST /api/v1/env/create` | **默认不绑任何项目**；请求体可加可选 `projectIds`，只绑指定项目 |
| `POST /api/v1/project/info` | 返回加 `environments` 字段（该项目当前绑的 env 列表） |
| `POST /api/v1/project/list` | 返回每行加 `boundEnvironmentIds` 字段 |
| `POST /api/v1/secret/create` | 服务端按 `folderId` 算 `path` 并写入 |
| `POST /api/v1/secret/update` | 改 key 时同步重算 `path` |
| `POST /api/v1/secret/list` | 接受 `orgCode / projectCode / envCode / folderPath` 中任意组合 |
| `POST /api/v1/folder/list` | 支持 `parentId` 过滤 + `depth=1/2` 过滤 |
| `POST /api/v1/folder/create` | 接受可选 `parentId`；为非空时校验父 folder 存在且 `parent.parent_id is null` |
| `POST /api/v1/folder/info` | 返回加 `projectId`、`parentId` |

#### 5.2 新增接口

| 路径 | 说明 | 请求体 |
| --- | --- | --- |
| `POST /api/v1/project/env/list` | 列出项目可绑的 env + 绑定状态；`boundOnly=true` 只看已绑 | `{projectId, boundOnly?}` |
| `POST /api/v1/project/env/attach` | 绑 env 到项目 | `{projectId, environmentIds[]}` |
| `POST /api/v1/project/env/detach` | 解绑 | `{projectId, environmentId}` |
| `POST /api/v1/secret/info-by-path` | 通过 path 查 secret（Redis 优先，未命中走 `secrets_path_active_uidx` 唯一索引） | `{path}` |

请求示例：

```json
// POST /api/v1/project/env/attach
{ "projectId": "uuid", "environmentIds": ["uuid-1", "uuid-2"] }

// POST /api/v1/project/env/detach
{ "projectId": "uuid", "environmentId": "uuid-1" }

// POST /api/v1/secret/info-by-path
{ "path": "default-org.project-a.dev.groups-secrets.payment.DATABASE_URL" }
```

### 6. 路径快查设计

#### 6.1 读取路径

```text
1. Redis: GET envvault:path:<full path>  →  secretId  (O(1))
2. 命中 → select * from secrets where id = ? and is_deleted = false
3. 未命中 → select * from secrets where path = $1 and is_deleted = false
         （走 secrets_path_active_uidx 唯一 B-tree 索引，O(log n) 等值比较）
4. 回填 Redis
```

#### 6.2 写入同步

- CreateSecret / UpdateSecret 成功 commit 后：写 Redis `pathKey(secret.Path) = secret.Id`
- DeleteSecret：删 Redis `pathKey(secret.Path)`
- 项目解绑 env / env 软删：扫描受影响的 secret，删 Redis pathKey（批处理或后台异步）

### 7. 软删级联策略

| 资源 | 软删时的级联 |
| --- | --- |
| `organizations` | 当前未实现子资源级联（DESIGN 已有说明）。本次保持原样。 |
| `projects` | 同事务软删该项目下所有 `folders`、`project_environments`、间接所有 secret |
| `environments` | 同事务软删所有 `project_environments`（该项目对该 env 的绑定） |
| `folders` | 同事务软删所有子 folder、间接所有 secret |
| `secrets` | 仅软删 secrets 行，Redis pathKey 同步失效 |

### 8. 路径访问的语义点

#### 8.1 Folder 2 级语义

- 代码层**允许 0/1/2 级**（实际就是 1 级或 2 级）
- **默认是 1 级**（`parent_id is null`），需要时通过 `parentId` 显式创建 2 级
- 校验：写 2 级 folder 时，应用层查 `parent.parent_id is null`，否则 reject

#### 8.2 env 软删时 binding 处置

- **选 (a)**：软删 env 时同事务软删所有 `project_environments` 绑定
- 理由：避免查询时 join 过滤的复杂度；让 RBAC scope 看不到幽灵 binding
- 审计：通过 `audit_records` 留下 env 删除 + binding 联动删除的痕迹

### 9. 实施步骤

1. `configs/schema.sql` —— folders / project_environments / secrets 表结构
2. `internal/store/postgres/repository.go` —— folder / project_environment / secret 增删改查重写
3. `internal/http/controller/resource_*.go` —— CreateProject 默认行为、env 绑/解绑、path 计算、info-by-path
4. `internal/store/redis/cache.go` —— 复用现有 pathKey，新增 path 反查
5. `internal/http/router.go` —— 注册新路由
6. `internal/http/controller/resource_test.go` —— 补：folder 2 级、binding 软删、path 计算、path 反查
7. `design/DESIGN.md`、`design/api/core.yaml`、`README.md` —— 同步更新

### 10. 待确认 / 开放问题

- [ ] Folder 2 级"可有可无"——确认默认 1 级、代码允许 2 级、不强求
- [ ] env 软删时 binding 处置选 (a) 同事务软删 vs (b) 保留 + 查询过滤 vs (c) 同事务软删 + 审计
- [ ] 是否需要在 `secrets` 上加 `last_accessed_at` / `access_count` 等访问统计字段（路径快查相关的可观测性）
- [ ] RBAC 范围是否需要扩展到 `project:env:bind` / `project:env:read-bindings` 权限码（与现有 `rbac:*` 解耦）

---

## Org 共享 env 模型下的权限设计（superseded by v3）

> ⚠️ superseded by v3（见上方 `## v3：环境归项目所有 + org 层 environment_templates`）。
> 状态：v2 历史设计要点，保留作为思考过程参考。
> v3 不再需要 `env:bind` / `project/env/attach` / `project/env/detach` 等权限码——env 本身归属 project。

### 1. 核心论点：env 是"标签"，folder/secret 才是"数据"

在 v2 模型里：

```text
env         → 只是个命名容器，自身不带数据
folder      → 数据归属点，作用域 = (project, env)
secret      → 数据，作用域 = folder
```

- **env 共享不共享数据**：把 env X 绑给 project A 和 B，A 和 B 在 X 下各自创建自己的 folder 树；A 看不到 B 的 folder，B 也看不到 A 的。
- **数据访问的边界永远是 project**：secret/folder 的 RBAC scope 全部 project-scoped，不变。
- **env 自身的增删/绑定是 org 级的管理动作**：env 是 org 的资产，需要新的 org-scoped 权限码。

### 2. 与"per-project env"模型的权限对比

| 维度 | Per-project env（A 方案） | Org 共享 env（B 方案） |
| --- | --- | --- |
| env 列表查询 | 必须按 project 过滤 | 可以按 org 过滤（看组织下所有 env 标签） |
| env 创建权限 | `project:env:manage`（project 维度） | `env:manage`（org 维度） |
| 跨项目 env 复用 | 不可 | 不可（因为不能跨项目复用 env 的内容）但 env 标签可以复用 |
| secret/folder 权限 scope | project（不变） | project（不变） |
| 数据泄漏面 | 局限于 project | 局限于 project（folder 是 (project, env) 私有的） |
| 新增权限码 | 0 | 3（`env:read` / `env:manage` / `env:bind`） |

**结论：权限复杂度上 B 略高（多 3 个权限码），但数据隔离面与 A 完全相同。**

### 3. 新增权限码

| 权限码 | scope | 用途 | 默认授予角色建议 |
| --- | --- | --- | --- |
| `env:read` | organization | 列出/查看 org 下的 env、查看 env 详情 | org admin、project admin |
| `env:manage` | organization | 在 org 下创建/更新/删除 env | org admin |
| `env:bind` | organization | 绑定/解绑 env 与 project | org admin |

> 说明：这里把 `env:bind` 放在 org 维度，而不是 project 维度。理由是 env 是 org 共享资产，谁有权把 env "开放"给哪些 project 应该由 org 决定，而不是由 project 自行决定。

### 4. 关键 API 的权限检查流程

#### 4.1 `POST /api/v1/env/list`（列 org 下 env）

```text
检查: user 在 org 范围内有 env:read
查询: select * from environments where org_id = $orgId and is_deleted = false
返回: env 列表（不包含 project_environments 绑定关系）
```

#### 4.2 `POST /api/v1/project/env/list`（列项目可绑的 env）

```text
检查: user 在 project 上有 project:read
      （同时 user 在 org 上有 env:read，用于过滤 org 下的 env）
查询: select e.*, pe.id as binding_id
      from environments e
      left join project_environments pe
        on pe.environment_id = e.id and pe.project_id = $projectId and pe.is_deleted = false
      where e.org_id = $orgId and e.is_deleted = false
返回: 每个 env + 是否已绑
```

#### 4.3 `POST /api/v1/project/env/attach`

```text
检查: user 在 org 上有 env:bind
校验: 所有 environmentId 都属于该 org（防止跨 org 绑定）
写入: insert into project_environments ... on conflict do nothing
审计: action = "bind_env", resource_id = project_id, metadata = environment_ids
```

#### 4.4 `POST /api/v1/project/env/detach`

```text
检查: user 在 org 上有 env:bind
校验: binding 存在
软删: update project_environments set is_deleted = true, deleted_at = now() where id = $bindingId
审计: action = "unbind_env", resource_id = project_id
```

#### 4.5 `POST /api/v1/folder/list`

```text
检查: user 在 project 上有 folder:read
查询: select * from folders where project_id = $projectId and environment_id = $envId and is_deleted = false
      （自动过滤掉其他 project 的 folder，即使 env 是共享的）
```

#### 4.6 `POST /api/v1/secret/info-by-path`

```text
1. 解析 path → (org_code, project_code, env_code, folder_path, key)
2. 解析出 project_id
3. 检查: user 在 project_id 上有 secret:read
4. 查询: select * from secrets where path = $path and is_deleted = false
5. 二次校验: 找到的 secret.folder_id 必须属于 project_id
```

### 5. 常见疑问与解答

#### Q1. 共享 env 不会泄漏其他项目的 secret 吗？

**答：不会**。folder 是 (project, env) 私有的，query 都带 `project_id` 过滤。即使 env 是共享的，A 用户查 A 项目的 secret 时只返回 A 项目 folder 下的数据。

反例（必须避免的写法）：

```sql
-- 错误：只按 env_id 过滤，不带 project_id
select s.* from secrets s
join folders f on f.id = s.folder_id
where f.environment_id = $envId
```

正确写法（必须带 project_id）：

```sql
-- 正确：始终带 project_id
select s.* from secrets s
join folders f on f.id = s.folder_id
where f.environment_id = $envId and f.project_id = $projectId
```

所有 secret/folder 查询都需要 code review 时强制验证带 `project_id` 过滤。

#### Q2. 列 org 下 env 时，会不会把不该看的 env 名字泄漏出去？

**答**：env 名字通常是公开的（"dev"/"test"/"prod"）。如果某些 env 名字本身就是敏感信息（如 "internal-payroll-system"），通过限制 `env:read` 的授权范围来控制，而不是限制 API 行为。

#### Q3. env:bind 权限下放给 project 维度可以吗？

**答：不建议**。`env:bind` 放在 org 维度才能保证 env 作为共享资产的"开放"决策不被任一项目私自篡改。如果放开到 project 维度，A 项目可以把敏感 env 绑给自己，绕过 org 的策略。

#### Q4. 跨 project 共享同一份 secret 怎么办？

**答**：v2 不支持，需要时让每个 project 在自己的 folder 下各写一份。如果将来需要"真正共享"语义，扩展方向：

```sql
alter table folders add column visibility text not null default 'private';
-- visibility ∈ {private, shared}
-- private: 仅 folders.project_id 可见
-- shared: env 下所有 project 都可见（仍需要 env:bind 才能 bind）
```

但 v2 **不做**，先用"重复写"覆盖 90% 场景。

#### Q5. 用户 A 在项目 P1，P1 绑了 env X。env X 也绑给了项目 P2（P1 没有访问权）。A 能看到 P2 在 X 下的 folder 吗？

**答：看不到**。所有 folder/secret 查询强制带 `project_id` 过滤。A 的请求 context 只有 P1，查询走 `where project_id = P1`，自然拿不到 P2 的数据。

#### Q6. 删除 env 时，绑定的 project_environments 怎么处置？

**答**：同事务软删（§ 8.2 选项 a）。理由：避免查询时 join 过滤的复杂度；让 RBAC scope 看不到幽灵 binding。审计通过 `audit_records` 留痕。

### 6. 受影响接口与权限映射表

| 接口 | 检查权限 | scope |
| --- | --- | --- |
| `POST /api/v1/org/list` | `org:read` | global |
| `POST /api/v1/org/create` | `org:manage` | global |
| `POST /api/v1/project/list` | `project:read` | organization |
| `POST /api/v1/project/create` | `project:manage` | organization |
| `POST /api/v1/project/info` | `project:read` | project |
| `POST /api/v1/project/update` | `project:manage` | project |
| `POST /api/v1/project/delete` | `project:manage` | project |
| `POST /api/v1/env/list` | `env:read` | organization |
| `POST /api/v1/env/create` | `env:manage` | organization |
| `POST /api/v1/env/info` | `env:read` | organization |
| `POST /api/v1/env/update` | `env:manage` | organization |
| `POST /api/v1/env/delete` | `env:manage` | organization |
| `POST /api/v1/project/env/list` | `project:read` + `env:read` | project × organization |
| `POST /api/v1/project/env/attach` | `env:bind` | organization |
| `POST /api/v1/project/env/detach` | `env:bind` | organization |
| `POST /api/v1/folder/list` | `folder:read` | project |
| `POST /api/v1/folder/create` | `folder:manage` | project |
| `POST /api/v1/folder/info` | `folder:read` | project |
| `POST /api/v1/folder/update` | `folder:manage` | project |
| `POST /api/v1/folder/delete` | `folder:manage` | project |
| `POST /api/v1/secret/list` | `secret:read` | project |
| `POST /api/v1/secret/search` | `secret:read` | project |
| `POST /api/v1/secret/create` | `secret:manage` | project |
| `POST /api/v1/secret/info` | `secret:read` | project |
| `POST /api/v1/secret/reveal` | `secret:reveal` | project |
| `POST /api/v1/secret/update` | `secret:manage` | project |
| `POST /api/v1/secret/delete` | `secret:manage` | project |
| `POST /api/v1/secret/info-by-path` | `secret:read`（解析 path 后） | project |

### 7. 仍需拍板的点

- [ ] `env:bind` 是否需要细分为 `env:bind:self`（只能 bind 涉及自己项目的 env）和 `env:bind:any`（能 bind 任意 project 的 env）—— 当前倾向只保留 `env:bind` 一个码
- [ ] 跨 project 共享 folder 语义（§ 5.Q4）—— v2 不做，记入未来
- [ ] env 名字本身是否敏感 —— 当前模型默认公开，由 `env:read` 授权范围控制可见性
- [ ] 是否新增系统角色 `org:env:admin`（带 `env:manage` + `env:bind`）—— 当前可以让 org admin 直接持有这两个权限码

---

## 待办清单（按优先级倒排）

> 本节集中登记尚未落地、后续要做的扩展项,每一项给出位置锚点(代码/设计文档
> 中的入口)和大致方案。具体 PR 落地时再把"已做"勾掉。

### P0 — 上线前必修

- [x] ~~**所有数据 handler 接入 RBAC**~~:v5 已完成。`/org/*`、`/project/*`、`/env/*`、`/env/template/*`、`/folder/*`、`/secret/*`、`/audit/*` 7 个文件的 ~30 个 handler 全部走 `allowScope`,`org_admin` / `project_admin` / `project_viewer` / `project_developer` 等角色真正生效。
- [ ] **Schema 版本化迁移**:把 `configs/drop_schema.sql` + `schema.sql` 替换成 `golang-migrate`(或 goose)的版本化迁移文件,迁移文件进 git,任何真实用户都不能接受"清库重建"。
- [ ] **Secret 版本历史与回滚**:新增 `secret_versions` 表;`CreateSecret` / `UpdateSecret` 在事务里同时写一行 version(单调递增,记录 ciphertext + actor + 时间);新增 `POST /api/v1/secret/versions/list`、`POST /api/v1/secret/versions/revert`。
- [ ] **加密密钥从 base64 升级到 KMS 集成**:`internal/crypto/Encryptor` 当前是单文件 base64 密钥,生产部署需对接 AWS KMS / Vault Transit / Aliyun KMS,加 `KmsEncryptor` 实现。
- [ ] **LICENSE / README / CHANGELOG / CONTRIBUTING**:`LICENSE`(Apache-2.0 跟 Vault 对齐)、`README.md`(功能/quickstart/架构图/roadmap)、`CHANGELOG.md`、`.github/ISSUE_TEMPLATE`、`.github/PULL_REQUEST_TEMPLATE`。
- [ ] **Dockerfile + docker-compose.yml**:server + postgres + redis 一键起。
- [ ] **公开 API 兼容性测试**:`design/api/core.yaml` 跟实现必须同步;用 `oapi-codegen` 或自己写的 contract test 在 CI 强制。
- [ ] **指标 (Prometheus)**:`/metrics` 端点 + `request_total{path,status}` / `request_duration_seconds{path}` / `secret_reveal_total` / `cache_hit_total{op}`。

### P1 — 三个月内补齐

- [ ] **Helm chart**:`charts/envvault/` 含 server + postgres + redis + ingress + service monitor,默认带 resource limit、HPA 钩子、PodDisruptionBudget。
- [ ] **CI / CD**:`go test ./...` + `golangci-lint run` + `govulncheck` + 镜像构建(多架构 linux/amd64 + linux/arm64)+ GHCR 发布。
- [ ] **结构化日志输出**:JSON 格式可切换(目前已经是结构化但仅文本),方便接入 Loki / ELK。
- [ ] **OpenTelemetry 分布式追踪**:`/trace` + `traceparent` header 透传;Secret Reveal / RBAC 授权 / Cache lookup 关键 span。

### P2 — 平台扩展

- [ ] **OAuth 2.0 / OIDC 身份提供者**:在 `internal/auth/` 内抽出 `IdentityProvider` 接口,先做 GitHub / Google,后做企业 Okta / AzureAD;JWT 现有实现作为 `IdentityProvider` 的一个实现。
- [ ] **SAML 2.0**:企业用户场景,放到 P2 末尾。
- [ ] **K8s 集成 (CRD + Operator)**:在 `envvault-operator` 独立 repo 中实现:
  - `EnvVaultSecret` CR:声明 secret 引用 + 注入目标
  - `EnvVaultBinding` CR:声明把某个 env 下的所有 secret 注入到 namespace 中
  - Operator 周期同步到 K8s `Secret`,应用方用 `envFrom` / `volumeMounts` 消费
- [ ] **Go SDK**:`github.com/yourorg/envvault/sdk-go` 独立 module:
  - 公开 `internal/domain/` 作为类型源
  - 公开 `service.SecretService` / `service.RBACService` 作为 Go API
  - TS SDK 从 OpenAPI 生成
- [ ] **备份 / 恢复 CLI**:`envvaultctl backup > dump.json` / `envvaultctl restore < dump.json`,含 ciphertext 字段。
- [ ] **多存储后端**:MySQL / MariaDB / SQLite (单机模式),在 `internal/store/` 已有接口,扩展成本主要是迁移 SQL 方言。

### P3 — 长期演进

- [ ] **Secret Rotation 调度器**:`POST /api/v1/secret/rotation-policy` 配 cron + 目标 plugin,周期性调出 + 回写。
- [ ] **审计日志导出**:支持把 `audit_records` 推到 S3 / Loki / SIEM。
- [ ] **多区域 / 高可用**:`SecretCache` 多 Redis 集群;Postgres 主从 + 只读副本;Raft 模式不打算做(参考 Vault Enterprise 而非 Vault OSS)。
- [ ] **FIPS 140-2 模式**:`crypto/cipher` 换 BoringCrypto,Linux 发行版切换到 `golang-fips` 镜像。
- [ ] **HCL / Terraform Provider**:`hashicorp/envvault` 给 IaC 用户使用。

### 关键文件位置(给落地时找路)

| 主题 | 入口 |
|---|---|
| 认证 / 授权 | `internal/auth/` (JWT 现状) / `internal/store/postgres/rbac.go` (PermissionStore) |
| 加密 | `internal/crypto/encryptor.go` |
| Secret 持久化 + 缓存 | `internal/store/postgres/repository.go` (Secret 部分) / `internal/store/redis/cache.go` |
| Secret 业务编排 | `internal/service/secret_service.go` |
| 路由 / DTO / 校验 | `internal/http/controller/` / `internal/http/router.go` |
| 审计 | `internal/store/postgres/repository.go` `RecordAudit` / `ListAuditRecords` |
| OpenAPI | `design/api/core.yaml` |
| 数据库 schema | `configs/schema.sql` (待替换为 migrate) |
| 配置 | `internal/config/config.go` |
