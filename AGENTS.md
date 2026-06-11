# AGENTS.md

## 项目概览

envVault 是一个全新的 Go 项目，目标是实现一个类似 Infisical 的轻量级、支持私有化部署的密钥管理平台。

`README.md` 中描述的产品层级模型为：

- 组织
- 项目
- 环境：`dev`、`test`、`sim`、`prod`,支持用户自建
- Folder 目录：当前计划只支持一级目录
- 密钥键值对：`key/value`

项目预期的核心能力：

- 基于 JWT 的统一认证与授权中间件。
- 支持认证拦截和必要的权限放行能力。
- 支持全局检索 `key`、`value` 内容，也支持按项目检索。
- 支持修改记录追溯和审计。
- 支持服务端加密存储密钥。
- 提供默认加密实现，同时暴露加密接口，允许用户自定义加密方式。
- 加密主密钥从环境变量中读取。
- 通过 HTTP API 对外提供操作能力。
- HTTP 框架使用 Gin。
- 全局查询需要支持高并发。

## 当前状态

当前仓库仍是一个初始脚手架：

- `go.mod` 声明模块名为 `envVault`。
- `main.go` 仍是 GoLand 默认生成的示例代码。
- 目前还没有稳定的架构分层、路由结构、数据库层或测试套件。
- 当前目录尚未初始化为 git 仓库。

后续开始实现真实功能时，应替换 `main.go` 中的示例逻辑，不要在示例输出代码上继续扩展业务。

## 开发命令

在仓库根目录执行：

```bash
go test ./...
go run .
go fmt ./...
```

如果后续引入新的工具、构建步骤或运行方式，请同步更新 `README.md`，并保持本文件中的说明一致。

## Go 代码约定

- 使用 `go fmt` 保持代码格式统一。
- 在架构尚未稳定前，优先保持简单清晰的包边界。
- 尽量不要把业务逻辑堆在 HTTP handler 中。
- service 层应返回结构化错误，并在 HTTP 边界转换为响应。
- 请求生命周期、存储访问、加密操作和搜索路径都应使用 `context.Context`。
- 不要记录原始密钥值、加密后的密钥值、JWT token 或加密密钥。

## 推荐架构方向

当项目开始增长时，可以优先参考如下包结构：

- `cmd/envvault`：应用入口。
- `internal/config`：环境变量与配置加载。
- `internal/http`：Gin 路由、中间件、请求和响应 DTO。
- `internal/auth`：JWT 解析、校验和授权策略。
- `internal/domain`：核心领域模型与校验规则。
- `internal/service`：业务用例和流程编排。
- `internal/store`：持久化接口与具体实现。
- `internal/crypto`：加密接口与默认加密实现。
- `internal/audit`：变更历史与审计记录。
- `internal/search`：全局检索和项目内检索逻辑。

不要提前一次性创建所有目录。只有当有真实业务逻辑需要承载时，再引入对应包。

## HTTP API 规范

遵循 `README.md` 中的约定：

- 无参数查询统一使用 `GET`。
- `GET` 默认不承载 request body，也不承载业务 query 参数。
- 有请求数据的查询或变更统一使用 `POST`，请求数据放在 JSON body 中。
- 分页、过滤条件、资源 ID、搜索关键字都视为请求数据，应使用 `POST`。
- 特殊场景下，例如分享链接这类带参数但适合链接访问的流程，可以使用 `GET + query params`。
- HTTP API 的请求字段和响应字段统一使用 camelCase，例如 `parentId`、`folderId`、`scopeType`、`externalUserId`。数据库表字段、SQL 列名和索引名可以继续使用 snake_case。
- 分页请求统一复用 `PageRequest`，字段为 `pageNum` 和 `pageSize`；分页响应统一为 `{ "pageNum": 页码, "pageSize": 每页数量, "total": 总条数, "list": 数据列表 }`,**空数据时退化为 `{ "total": 0, "list": [] }`,省略 `pageNum` / `pageSize`**(由 `omitempty` 控制,见 `design/DESIGN.md`「分页响应 - 空数据形态」节)。
- HTTP 状态码只表示 HTTP 传输层结果，响应体中的 `code` 是业务状态码，二者不能混用。
- 成功响应业务码统一为 `0`，通用失败业务码统一为 `-1`；明确可区分的特殊错误码使用 `1000` 以上，例如参数错误、未认证、无权限、资源不存在、服务不可用。
- 成功响应优先使用 `response.OK`，需要自定义成功消息时使用 `response.OkWithMsg`；通用失败优先使用 `response.FailWithMsg`，特殊错误使用 `response.Fail` 并传入明确业务码。
- HTTP 框架使用 Gin。
- 请求和响应结构应保持明确，并为后续版本演进留出空间。
- 列表和搜索接口默认不应返回密钥明文，除非该接口明确用于查看密钥，并且已经完成授权校验。

## 安全要求

安全相关行为应作为核心业务逻辑处理，而不是附带实现。

- 密钥必须加密后再持久化。
- 加密主密钥必须从环境变量读取。
- 提供小而稳定的加密接口，方便注入自定义实现。
- 默认加密实现应选择成熟、通用、带认证能力的加密方案。
- 明文密钥只应在必要操作中短暂存在，避免长期保留。
- 不要在日志、panic 信息、指标标签或审计元数据中暴露明文密钥。
- 不要在日志、panic 信息、指标标签或审计元数据中暴露加密后的密钥值、JWT token 或加密主密钥。
- JWT 校验应通过中间件统一执行。
- 授权检查应尽量靠近 service 操作本身，不应只依赖路由注册时的控制。
- 密钥创建、更新、删除以及关键元数据变化都应记录变更历史。

## 认证 & 用户（v9）

v9 起，EnvVault 支持本地 email+password 自助注册/登录，端点定义在 `design/api/core.yaml`。
实现位置：`internal/auth/password.go`（argon2id）、`internal/auth/ratelimit/`（Redis 频控）、
`internal/auth/tokens_cache.go`（强制登出锚点缓存）、`internal/service/auth_service.go`（业务编排）、
`internal/store/postgres/auth.go`（`AuthStore` 落库）、`internal/http/controller/auth.go`（HTTP 层）。

- 密码哈希使用 argon2id（`m=64MiB, t=3, p=2`，PHC 字符串格式），**禁止**写入 bcrypt / 明文 / SHA。
- 登录密码与 12 位最小长度（配置 `auth.passwordMinLength`，默认 12）由 `AuthService.Login` 校验。
- 登录频控：同一 IP 在 `window`（默认 60s）内 `maxAttempts`（默认 5）次失败后锁 `lockoutPeriod`（默认 15min）。
  Redis 未启用时降级为 noop（开发态）。详见 `internal/auth/ratelimit/ratelimit.go`。
- 强制登出：`users.tokens_valid_after` 字段 + 进程内 `TokensCache`（refresher 周期 1min）。
  Logout/ChangePassword 必 bump；JWT iat < tokens_valid_after 的请求 1401。
- 任何 password 都不应出现在日志、panic 信息、错误响应中。
- 错误响应统一返回 `401 1401`（不区分「邮箱不存在」与「密码错误」）以避免枚举攻击。
- 自注册写 `login_attempts` 审计行，不论成功失败，保留 IP 与 email 索引。
- 单元测试应覆盖：argon2id roundtrip、ratelimit 阈值/锁定/重置、TokensCache refresher 失败降级、
  `AuthService` 4 个方法的 happy/拒绝路径、`AuthStore` 首个 password 用户自动 platform_admin 的并发安全。

## 搜索与并发

全局搜索需要支持高并发。

- 搜索 API 应支持 `context` 取消。
- 避免长时间持有全局锁。
- 随着实现成熟，优先考虑不可变快照、索引存储或数据库原生搜索能力。
- 全局搜索和项目内搜索的行为边界要清晰。
- 注意不要跨组织、项目或环境泄露密钥内容。

## 测试建议

每次实现有意义的功能时，都应补充对应测试。

优先覆盖：

- 加密和解密的往返测试。
- 自定义加密实现的注入能力。
- JWT 中间件行为。
- 权限校验。
- 组织、项目、环境、Folder 的作用域隔离。
- 审计记录创建。
- 搜索结果正确性与授权过滤。
- 搜索实现完成后，补充并发搜索行为测试。

对于校验、授权和 handler 行为，优先使用表驱动测试，让用例保持清晰。

## 文档要求

- `README.md` 应与真实的安装、配置和运行方式保持一致。
- 必须记录必要的环境变量，尤其是加密和 JWT 相关配置。
- 完整 HTTP 端点目录(path / request / response / RBAC)以 OpenAPI 3.0.3 维护在
  `design/api/core.yaml`(核心资源)+ `design/api/rbac.yaml`(权限管理)。
  任何新增/修改端点都要同步更新对应的 yaml;Redocly / Swagger UI 都能直接渲染。
  实现代码与 yaml 字段必须保持一致(默认值、枚举值、状态码、错误码)。

## 主要 HTTP 端点索引

完整契约见 `design/api/core.yaml`,这里只列导航,方便定位。

| 端点 | 用途 | 关键字段 |
|------|------|---------|
| `POST /api/v1/auth/dev/token` | 开发态 JWT(仅 `DevTokenEnabled=true` 时暴露) | — |
| `POST /api/v1/auth/register` / `login` / `logout` / `changePassword` | v9 邮箱+密码认证 | email / password |
| `POST /api/v1/me` | 当前用户信息 | — |
| `POST /api/v1/org/{list,create,info,update,delete}` | 组织 CRUD | code / name |
| `POST /api/v1/project/{list,create,info,update,delete}` | 项目 CRUD | orgId / code / envs[] |
| `POST /api/v1/env/{list,create,info,update,delete}` | 环境 CRUD | projectId / code |
| `POST /api/v1/env/template/{list,info}` | org 层 env 模板只读快照 | orgId / code |
| `POST /api/v1/folder/{list,create,info,update,delete}` | folder CRUD(2 级) | level=1: 无父; level=2: `parentCode`; `envList` 必填(env id 列表) |
| `POST /api/v1/folder/listByProject` | 按 `projectId` 拉所有 folder(按 code 聚合,带 envList + subFolders) | `projectId` |
| `POST /api/v1/secret/{list,search,create,info,reveal,update,delete}` | 密钥 CRUD + 全文搜索 | folderId / key / value |
| `POST /api/v1/secret/path/{info,reveal,batchReveal}` | 路径式访问 `org.proj.env.folder[.KEY]` | path / keys[] |
| `POST /api/v1/audit/list` | 审计记录 | resourceType / resourceId |
| `POST /api/v1/rbac/{permission,role,binding,user}/*` | RBAC 管理 | code / scopeType / scopeId |
| `POST /api/v1/search/global` | 跨 5 类资源关键字搜索(Redis 缓存) | keyword / types[] |
| `POST /api/v1/tree/get` | **完整资源分级树**(org→proj→env→folder-l1→folder-l2) | `maxDepth` / `includeOrphans` |

新增/修改端点时:
1. 在 controller 写实现
2. 在 `design/api/core.yaml` 或 `rbac.yaml` 补 schema 与示例(状态码、错误码、camelCase 字段名)
3. 在本表加一行导航
- API endpoint 实现后，应补充调用示例。
- 当项目约定发生变化时，同步更新本文件。

## 代理工作规则

- 做架构改动前，先阅读 `README.md`、`go.mod` 和相关包文件。
- 保持改动聚焦，除非明确要求，不要进行大范围脚手架搭建。
- 不要随意引入依赖，尤其是加密、认证和持久化相关依赖。
- 如果确实需要新增依赖，优先选择成熟、维护活跃的库，并在变更说明中解释原因。
- 代码变更后，在可行时运行 `go test ./...`。
- 不要提交生成的二进制文件、本地 IDE 配置、环境文件或任何密钥材料。

## Folder 批量跨环境创建(`POST /api/v1/folder/create`)

**纯批量接口**,不保留单条创建路径。`envList` 必填且非空,每项是 env 的 **id
(UUID)**,不是 env code。空 / 缺省 `envList` 直接返业务码 `-1`。

### 请求体

**level=1**(在多个 env 下创建同名顶层 folder):

```json
{
  "level": 1,
  "code": "globals",
  "name": "Globals",
  "comment": "默认全局",
  "envList": [
    "11111111-1111-1111-1111-111111111111",
    "22222222-2222-2222-2222-222222222222",
    "33333333-3333-3333-3333-333333333333"
  ]
}
```

**level=2**(在多个 env 下同名父 folder 下挂子 folder):

```json
{
  "level": 2,
  "code": "child-folder-aaaa",
  "name": "child-folder aaaa",
  "comment": "测试子folder aaa",
  "parentCode": "payment",
  "envList": [
    "11111111-1111-1111-1111-111111111111",
    "22222222-2222-2222-2222-222222222222",
    "33333333-3333-3333-3333-333333333333"
  ]
}
```

### 行为

1. **level=1**:`envList` 每项 → 在该 env 下创建顶层 folder(`parent_id=NULL`,`level=1`)。
2. **level=2**:`parentCode` 是参考父 folder 的 code;后端在 `envList` 每个 env 下用
   `parentCode` 反查同 code 的 `level=1` sibling parent,挂子 folder 于此。
3. 整批在 1 个事务里,任一 env 缺失 / `level=2` 的 sibling parent 缺失 / 目标 code 已
   存在 → 整体回滚。
4. 缓存同步:每个新建 folder 走 `GetFolderContext` 拿全量上下文后 `UpsertFolder`。
5. 响应:`data` 直接是 `[Entity, ...]`,按 `envList` 顺序排列(创建接口响应规范:批量
   create 直接把列表放在 `data`,不再用 `created`/`items` 等中间字段包装,详见
   `design/DESIGN.md` 「创建接口响应规范」)。

### 错误码

| HTTP | 业务码 | 触发条件 |
|------|--------|---------|
| 200 | -1 | `envList` 缺省 / 空 |
| 400 | 1002 | `level` 不在 {1,2};code 不符合正则;envList 项不是 UUID;level=2 缺 `parentCode` |
| 404 | 1404 | 任意 envId 不存在 / 已软删;level=2 任一 env 下找不到 `parentCode` 对应的 level=1 folder |
| 409 | 1409 | 目标 code 在某 env 下已存在(整批回滚) |

### 关键文件

- `internal/store/postgres/repository.go` — `CreateFoldersAcrossEnvs`(level=2)+ `CreateTopLevelFoldersInEnvs`(level=1)
- `internal/http/controller/resource_folder.go` — `createFolderRequest` 字段定义 + `CreateFolder` 入口 + `validateEnvIdsForCreate` UUID 校验
- `internal/http/controller/resource_folder_test.go` — envList 校验 + JSON 解析单测
- `design/api/core.yaml` — `/api/v1/folder/create` 完整 OpenAPI 定义

## Folder 按 project 聚合列表(`POST /api/v1/folder/listByProject`)

按 `projectId` 一次性返回该 project 下所有 folder 结构(level=1 + level=2),
按 `code` 聚合,每组带 `envList`(在哪些 env 下存在)与 `subFolders`(子层)。

### 请求体

```json
{ "projectId": "11111111-1111-1111-1111-111111111111" }
```

### 响应

```json
{
  "folderList": [
    {
      "id": "22222222-2222-2222-2222-222222222222",
      "code": "globals",
      "name": "Globals",
      "comment": "默认全局",
      "envList": [
        "33333333-3333-3333-3333-333333333331",
        "33333333-3333-3333-3333-333333333332",
        "33333333-3333-3333-3333-333333333333"
      ],
      "subFolders": []
    },
    {
      "id": "44444444-4444-4444-4444-444444444441",
      "code": "payment",
      "name": "Payment Providers",
      "envList": ["33333333-...", "33333333-..."],
      "subFolders": [
        {
          "id": "55555555-5555-5555-5555-555555555551",
          "code": "stripe",
          "name": "Stripe",
          "envList": ["33333333-...", "33333333-..."]
        }
      ]
    }
  ]
}
```

### 关键契约

- **子目录跟随父目录**:父 group 出现时,所有 RBAC 可见的 `subFolders` 一定存在
  (SQL 一次性拉同 project 下 level=1+2,service 层按 `(父 code, 子 code)` 聚合)
- **反向保证**:父被 RBAC 收窄时,其所有子实例被整体丢弃,不会出现孤儿
- **id/name/comment**:从该 code 在第一个 env 中的实例取(同名 folder 通常一致)
- **排序**:`folderList` 按 `code` 升序,每组 `subFolders` 也按 `code` 升序
- **空 project**:`folderList` 返 `[]`(非 `null`)
- **RBAC**:服务端 SQL narrowing 在 (folder, env, project, org) 4 层链 + service 层
  per-folder `authorizer.Allow("folder:read")` 二次收窄

### 关键文件

- `internal/store/postgres/repository_tree.go` — `ListFoldersInProject` 实现
- `internal/service/tree_service.go` — `GetProjectFolderTree` 业务编排(按 code 聚合)
- `internal/http/controller/resource_folder.go` — `ListFoldersByProject` handler
- `internal/service/tree_service_test.go` — 6 个聚合逻辑单测
- `design/api/core.yaml` — `/api/v1/folder/listByProject` 完整 OpenAPI

## 资源分级树(tree)接口

新增 `POST /api/v1/tree/get`,一次性返回 org → project → env → folder(l1) → folder(l2) 的完整树,供前端做分级筛选。

### 数据流

- **读**:TreeService.GetTree 优先读 Redis(4 类散装 HASH),cache miss 或 cache 不可用时 fallback 到 DB,通过 `ListAll*ForTree` 4 个不带分页的 repo 方法拉全集,顺手异步 `WarmTree` 回填 cache。
- **写**:16 个 CRUD 端点(org/project/env/folder 的 create/update/delete)在 handler 调完 repo 后,统一通过 `Controller.cacheUpsert` / `cacheDelete` 集中 helper 同步维护 cache。级联软删返回 `domain.CascadeScope`,handler 遍历 `Controller.cacheInvalidateCascade` 逐类 delete。
- **字段约定**:tree 节点用 `TreeNode`(id/type/parentId/code/name/comment/level/children),`level` 只对 folder 有意义(1 或 2),`children` 强制输出 `[]` 而非 `null`。

### 权限

不新增 `tree:read` 权限码。入口仅要求 JWT,可见范围由 `org:read` / `project:read` / `env:read` / `folder:read` 4 个权限码在 SQL narrowing + service 二次收窄处自然收敛,与 ListXxx 行为对齐。

### 孤儿处理

"父不可见但子可见"是合法态(caller 拿到 folder 直接 grant,无 env grant)。`includeOrphans=true`(默认)时挂到虚拟根 `Id="__orphans__"`,`Stats.Orphans` 记录数量;`includeOrphans=false` 时丢弃但 `Orphans` 仍记数。

### 关键文件

- `internal/domain/tree.go` — TreeNode / ResourceTree / TreeStats / CascadeScope / TreeRequest / FolderTreeEntry
- `internal/store/postgres/repository_tree.go` — 4 个 ListAll*ForTree
- `internal/store/redis/cache.go` — TreeWarmSnapshot / WarmTree / ListAllMeta
- `internal/service/tree_service.go` — TreeService 业务编排 + RBAC 收窄 + 组装
- `internal/http/controller/tree.go` — GetResourceTree handler
- `internal/http/router.go` — `POST /api/v1/tree/get` 路由
