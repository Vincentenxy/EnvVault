# EnvVault 设计文档

## 背景

EnvVault 是一个类似 Infisical 的轻量级、支持私有化部署的密钥管理平台。系统通过 HTTP API 对外提供组织、项目、环境、Folder 和 Secret 的管理能力，并围绕密钥存储安全、权限控制、搜索、审计和历史追溯展开设计。

核心产品层级（v3）：

```text
organization
  project
    environment
      └─ folder
          └─ secret key:value
organization
  environment_templates    （只读模板/汇总）
```

> v3 起，env 直接归属 project，不再使用 `project_environments` 关联表。
> org 层额外维护一份 `environment_templates` 汇总，记录该 org 下"曾经创建过哪些 env code"，
> 其 `name` / `comment` 永远是该 code 首次进入 org 时的快照，供前端在新建 project 时参考。

默认环境：

```text
dev / test / sim / prod
```

默认 Folder：

```text
globals / groups-secrets
```

当前约束：

- Folder 暂只支持一级目录。
- Secret 由 `key`、`value`、`comment` 组成。
- Secret value 必须加密后持久化。
- JWT 只负责认证，RBAC 负责授权。
- 列表、搜索接口默认不返回 Secret 明文 value。
- 删除采用逻辑删除，并保留删除快照。
- 变更需要写审计记录。

## 当前基础架构

应用入口：

- `main.go`：保留 `go run .` 的默认入口。
- `cmd/envvault/main.go`：发布二进制时的推荐入口。

基础包：

- `internal/app`：应用启动与 HTTP server 组装。
- `internal/config`：配置和环境变量加载。
- `internal/http`：Gin 路由、controller、响应 DTO。
- `internal/auth`：JWT 中间件、RBAC 接口。
- `internal/crypto`：加密接口与 AES-256-GCM 默认实现。
- `internal/domain`：核心领域模型。
- `internal/service`：业务服务边界，后续授权、审计、加密流程应收敛到这里。
- `internal/store/postgres`：PostgreSQL 仓储实现。
- `internal/store/redis`：Redis 搜索缓存。
- `internal/logging`：请求 ID、访问日志、panic recovery 和脱敏日志。

详细扩展设计：

- RBAC 设计：[rbac_degisn.md](rbac_degisn.md)
- 搜索设计：[search.md](search.md)

接口文档统一放在：

```text
design/api
```

当前接口文档：

- Core OpenAPI：[api/core.yaml](api/core.yaml)
- RBAC OpenAPI：[api/rbac.yaml](api/rbac.yaml)

## 配置管理

默认配置文件：

```text
configs/config.yaml
```

可通过环境变量指定配置文件：

```bash
ENVVAULT_CONFIG_PATH=./configs/config.yaml go run .
```

配置项支持环境变量覆盖，命名规则为 `ENVVAULT_` + 配置路径大写。例如：

```bash
ENVVAULT_DATABASE_PASSWORD=123456 go run .
ENVVAULT_HTTP_ADDR=:9090 go run .
```

常用环境变量：

- `ENVVAULT_CONFIG_PATH`：配置文件路径。
- `ENVVAULT_HTTP_ADDR`：HTTP 服务监听地址，默认 `:8080`。
- `ENVVAULT_HTTP_REQUEST_ID_HEADER`：请求 ID 请求头名称，默认 `x-request-id`。
- `ENVVAULT_AUTH_ENABLED`：是否启用 JWT 认证，默认 `true`。
- `ENVVAULT_AUTH_PUBLIC_KEY`：JWT 验签公钥，支持 PEM 格式 RSA/ECDSA/Ed25519 public key。
- `ENVVAULT_AUTH_DEV_TOKEN_ENABLED`：是否开启本地测试 JWT 签发接口，默认 `false`，生产环境不要开启。
- `ENVVAULT_AUTH_DEV_PRIVATE_KEY`：本地测试 JWT 签发私钥，支持 PEM 格式 RSA/ECDSA/Ed25519 private key。
- `ENVVAULT_AUTH_DEV_USER_ID`：关闭认证时注入的开发用户 ID，必须是 `users.id` UUID，默认 `00000000-0000-4000-8000-000000000001`。
- `ENVVAULT_AUTH_DEV_USER_NAME`：关闭认证时注入的开发用户名，默认 `Dev User`。
- `ENVVAULT_SECURITY_ENCRYPTION_KEY`：AES-256-GCM 主密钥，要求 base64 编码后的 32 字节密钥。
- `ENVVAULT_DATABASE_HOST`：PostgreSQL 地址。
- `ENVVAULT_DATABASE_PORT`：PostgreSQL 端口。
- `ENVVAULT_DATABASE_USER`：PostgreSQL 用户名。
- `ENVVAULT_DATABASE_PASSWORD`：PostgreSQL 密码。
- `ENVVAULT_DATABASE_NAME`：PostgreSQL 数据库名。
- `ENVVAULT_DATABASE_SSL_MODE`：PostgreSQL SSL 模式。
- `ENVVAULT_REDIS_ENABLED`：是否启用 Redis。
- `ENVVAULT_REDIS_MODE`：Redis 模式，支持 `single` 和 `cluster`。
- `ENVVAULT_REDIS_ADDRS`：Redis 地址列表。
- `ENVVAULT_REDIS_PASSWORD`：Redis 密码。
- `ENVVAULT_REDIS_DB`：Redis DB，仅单节点模式有效。
- `ENVVAULT_REDIS_KEY_PREFIX`：Redis key 前缀，默认 `envvault`。
- `ENVVAULT_REDIS_WARM_UP_ON_START`：启动时是否预热 Secret 查询缓存。

生产环境不要提交真实数据库密码、JWT secret、加密主密钥或其他密钥材料。

## 核心数据模型

### Organization

组织是 EnvVault 的最高业务隔离单元。组织下可以包含多个项目。

核心字段：

- `id`
- `code`
- `name`
- `comment`
- 删除元数据
- 创建和更新时间

约束：

- 未删除组织的 `code` 全局唯一。
- `code` 创建时必填，创建后不可修改，只允许小写字母、数字、中横线，格式为 `^[a-z0-9]+(-[a-z0-9]+)*$`。
- `name` 是展示名称，不参与唯一约束。

### Project

项目属于一个组织。v3 起，env 直接归属 project，project 创建时若请求体 `environments[]` 携带环境列表，会一并创建这些 env、默认 folder `globals` / `groups-secrets`，并对每个 env code 在 `environment_templates` 中 upsert；不传则 project 下不建任何 env，需要后续调用 `/api/v1/env/create` 补建。

核心字段：

- `id`
- `org_id`
- `code`
- `name`
- `comment`
- 删除元数据
- 创建和更新时间

约束：

- 同一个组织下未删除项目的 `code` 唯一。
- `name` 是展示名称，不参与唯一约束。

### Environment

环境属于一个 project，每个 project 拥有独立的环境列表。默认环境为 `dev`、`test`、`sim`、`prod`，同时支持自定义环境（如 `poc`）。

核心字段：

- `id`
- `project_id`
- `code`
- `name`
- `comment`
- 删除元数据
- 创建和更新时间

约束：

- 同一个 project 下未删除环境的 `code` 唯一。
- `name` 是展示名称，不参与唯一约束。
- 创建环境时自动在该环境上创建默认 Folder：`globals` 和 `groups-secrets`。
- 创建 project 时若在请求体 `environments[]` 中声明环境，会一次性创建这些 env、默认 folder 并 upsert org 层模板；不传则 project 下不建任何 env，后续通过 `/api/v1/env/create` 补建。
- 创建 env 时（包括 project 内联创建与独立 create 接口）会 upsert `(org_id, code)` 在 `environment_templates` 中的模板行；模板已存在时 `name` / `comment` 保持首次写入快照不变。

### Folder

Folder 属于一个环境，支持最多两级目录（顶级 + 一层子级）。默认 Folder 为 `globals` 和 `groups-secrets`，由调用方按需显式创建（不再随 env 自动建）。

核心字段：

- `id`
- `environment_id`  答"属于哪个 env",level=1 与 level=2 都必填
- `parent_id`       答"父 folder 是谁",仅 level=2 填写,level=1 必为 NULL
- `level`           1=顶级,2=子级
- `code`
- `name`
- `comment`
- 删除元数据
- 创建和更新时间

**字段语义不重叠**：`environment_id` 和 `parent_id` 看似冗余,实际回答的是不同问题:

- `environment_id`:这个 folder 属于哪个 env(level=1 时父是 env 不是 folder,只能靠它定位)
- `parent_id`:这个 folder 的父 folder 是谁(仅 level=2 有)

历史上有人想合并两列让 `parent_id` 多态(level=1 指向 env、level=2 指向 folder),会同时丢失 FK 约束、简单索引,得不偿失。当前两列各司其职,**不要合并**。

约束：

- 同一个环境下未删除 Folder 的 `(parent 域, code)` 唯一,即:
  - level=1:同一 env 下顶层 `code` 唯一
  - level=2:同一 env 下同一父 folder 下 `code` 唯一
  - 顶层和子级可以同名（如 env 下有顶层 `globals`,子级下也可以有 `globals`,不冲突)
- `name` 是展示名称，不参与唯一约束。

### Secret

Secret 属于一个 Folder。Secret value 必须加密后存储。

核心字段：

- `id`
- `folder_id`
- `key`
- `value_ciphertext`
- `comment`
- `version`
- 删除元数据
- 创建和更新时间

约束：

- 同一个 Folder 下未删除 Secret 的 `key` 唯一。
- `key` 必须符合 `.env` key 风格：`^[A-Z][A-Z0-9_]*$`。
- Secret 完整业务路径为 `org_code.project_code.env_code.folder_code.KEY`。
- `value_ciphertext` 存储加密后的 JSON 结构。
- `version` 创建时为 1，每次更新递增。

当前密文结构：

```json
{
  "algorithm": "AES-256-GCM",
  "nonce": "bytes",
  "data": "bytes"
}
```

## 数据存放设计

### PostgreSQL

PostgreSQL 是唯一权威持久化存储。

保存内容：

- 组织、项目、环境、Folder、Secret 元数据。
- Secret 加密值 `value_ciphertext`。
- 删除快照 `deleted_records`。
- 审计记录 `audit_records`。
- 后续 RBAC 表、搜索事件表、版本历史表也应放在 PostgreSQL。

不保存内容：

- Secret 明文 value。
- JWT token。
- 加密主密钥。

查询原则：

- 所有正常业务查询都默认过滤 `is_deleted = false`。
- 需要追溯删除记录时查询 `deleted_records`。
- 需要追溯变更行为时查询 `audit_records`。

### Redis

Redis 是缓存和搜索加速组件，不作为权威存储。

当前保存内容：

- Secret ID 集合。
- Secret 路径相关 ID。
- Secret key。
- Secret comment。
- Secret version。
- Secret 加密值 `value_ciphertext`。
- 创建和更新时间。

不保存内容：

- Secret 明文 value。
- JWT token。
- 加密主密钥。

Redis 数据可以从 PostgreSQL 重新构建。应用启动时如果 `redis.warm_up_on_start = true`，会从 PostgreSQL 加载 active secrets 预热 Redis。

### 应用内存

应用内存用于请求生命周期中的临时数据：

- JWT claims。
- 已解密 Secret value。
- 搜索设计中可选的运行时解密搜索快照。

明文 value 只应在必要路径短暂存在，不能写日志、panic、审计元数据或指标标签。

## 数据访问实现策略

项目采用 GORM v2 与原生 SQL 混合使用的方式访问 PostgreSQL。

### 为什么引入 GORM

EnvVault 中存在大量标准 CRUD 和管理类查询，例如：

- RBAC 权限点列表。
- 角色列表、角色详情。
- 用户同步和用户查询。
- 角色删除、普通状态更新。
- 后续组织、项目、环境、Folder 的基础 CRUD。

这些逻辑如果全部使用 `database/sql` 手写，会产生大量重复代码：

- 手写 `QueryContext`。
- 手写 `rows.Next()`。
- 手写 `Scan`。
- 手写简单条件拼接。

GORM v2 可以降低这类样板代码，提高开发效率和可维护性。项目不追求极致 ORM 性能，基础管理接口使用 GORM 是可接受的。

当前连接层通过 GORM 打开 PostgreSQL，同时取出底层 `*sql.DB`：

```text
*gorm.DB  -> 简单 CRUD、模型映射、普通更新
*sql.DB   -> 已有仓储、复杂 SQL、健康检查、特殊 PostgreSQL 能力
```

### GORM 使用边界

推荐使用 GORM 的场景：

- 单表或少量 join 的列表查询。
- 按主键或唯一字段查询。
- 普通 create、update、delete。
- 管理类接口的 DTO 映射。
- RBAC 中权限点、角色、用户等基础 CRUD。

不推荐完全交给 GORM 的场景：

- 资源祖先链查询。
- 用户有效权限推导。
- 搜索和高并发查询。
- 删除快照 `to_jsonb(t)`。
- 批量缓存预热。
- 需要精确控制 SQL、锁、索引和查询计划的路径。

这些场景继续使用原生 SQL 或 GORM `Raw`。原因是：

- SQL 行为更透明，便于审计。
- 权限过滤和搜索候选裁剪需要精确控制。
- PostgreSQL 特性如 `to_jsonb`、`gin_trgm_ops`、复杂 join 用原生 SQL 更清晰。
- 密钥系统的安全边界不能被 ORM 隐式行为遮住。

### 迁移与建表策略

项目暂不使用 GORM `AutoMigrate` 作为生产建表方式。

原因：

- 表结构需要可审计、可 review。
- 软删除字段采用项目自定义的 `is_deleted`、`deleted_at`、`deleted_by`，不是 GORM 默认 soft delete。
- 加密字段、审计字段、索引和部分唯一索引需要精确控制。

当前仍以 `configs/schema.sql` 作为基础 schema 来源。后续如果引入迁移工具，也应生成显式 migration SQL，而不是在生产环境依赖自动迁移。

### 安全约束

使用 GORM 时仍必须遵守：

- 所有数据库调用使用 `WithContext(ctx)`。
- 不启用会打印敏感参数的 SQL 日志。
- 不把 Secret 明文 value 放入 GORM model 长期持有。
- 涉及 Secret 写入、审计、缓存更新的流程必须显式事务化。
- 复杂权限过滤不得先查出全量数据再在内存中过滤。

## 数据库表结构设计

当前基础表由 `configs/schema.sql` 初始化。后续如果引入迁移工具，应保持本节与迁移文件一致。

首次初始化时，需要先连接 PostgreSQL 默认库，例如 `postgres`，创建 EnvVault 业务库：

```sql
create database envvault
    with owner admin
    encoding 'UTF8';
```

PostgreSQL 没有 MySQL 的 `use database` 语法。数据库创建完成后，需要在客户端里切换连接到 `envvault`，或者使用连接串直接连到 `envvault`。

本地测试如果需要重建全量库表，可以先手动执行：

```bash
psql "postgres://admin:123456@127.0.0.1:5432/envvault?sslmode=disable" -f configs/drop_schema.sql
psql "postgres://admin:123456@127.0.0.1:5432/envvault?sslmode=disable" -f configs/schema.sql
```

`configs/drop_schema.sql` 只用于本地测试或明确确认要清空库表的场景，生产环境不能直接执行。

### PostgreSQL 扩展

```sql
create extension if not exists pg_trgm;
```

`pg_trgm` 用于 key、name、comment 等明文字段的模糊搜索索引。Secret value 因加密存储，不能直接使用 PostgreSQL 明文索引搜索。

### organizations

```sql
create table if not exists organizations (
    id uuid primary key,
    code text not null,
    name text not null,
    comment text not null default '',
    is_deleted boolean not null default false,
    deleted_at timestamptz,
    deleted_by text not null default '',
    created_by text not null default '',
    updated_by text not null default '',
    created_at timestamptz not null default now(),
    updated_at timestamptz not null default now()
);

create unique index if not exists organizations_code_active_uidx
    on organizations (code)
    where is_deleted = false;
```

`organizations.code` 采用“活动记录唯一”语义：

- 相同 `code` 且 `is_deleted = false` 时，创建返回 `409 Conflict`。
- 只有相同 `code` 的软删除历史记录时，允许创建新组织并生成新的 UUID。
- 创建操作不会自动恢复、覆盖或删除历史组织。
- 恢复历史组织时，如果已有相同 `code` 的活动组织，同样返回 `409 Conflict`。
- 旧数据库如果残留全局 `UNIQUE(code)`，执行
  `configs/migration_organizations_active_code_unique.sql` 修复索引。

### projects

```sql
create table if not exists projects (
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
    updated_at timestamptz not null default now()
);

create unique index if not exists projects_org_code_active_uidx
    on projects (org_id, code)
    where is_deleted = false;
```

### environments

```sql
create table if not exists environments (
    id uuid primary key,
    project_id uuid not null references projects(id),
    code text not null,
    name text not null,
    comment text not null default '',
    sort_order integer not null default 100,
    is_deleted boolean not null default false,
    deleted_at timestamptz,
    deleted_by text not null default '',
    created_by text not null default '',
    updated_by text not null default '',
    created_at timestamptz not null default now(),
    updated_at timestamptz not null default now(),
    constraint environments_code_chk check (code ~ '^[a-z0-9]+(-[a-z0-9]+)*$')
);

create unique index if not exists environments_project_code_active_uidx
    on environments (project_id, code)
    where is_deleted = false;

create index if not exists environments_project_idx
    on environments (project_id)
    where is_deleted = false;

create index if not exists environments_project_sort_idx
    on environments (project_id, sort_order, created_at)
    where is_deleted = false;
```

`sort_order` 用于表达环境的业务展示顺序。默认环境顺序为 `dev=10`、`test=20`、`sim=30`、`prod=40`，其他自定义环境默认 `100`。环境列表统一按 `sort_order asc, created_at asc` 返回，前端按后端返回顺序展示，不需要自行硬编码排序。

### environment_templates

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

create index if not exists environment_templates_org_idx
    on environment_templates (org_id)
    where is_deleted = false;
```

说明：

- 模板表是 org 层的只读快照，仅记录"该 org 曾经出现过的 env code 与首次写入时的 name/comment"。
- 创建 env / 创建 project 时通过 `INSERT ... ON CONFLICT (org_id, code) WHERE is_deleted = false DO NOTHING` 写入。
- 后续修改 env 的 `name` / `comment` **不会**回写模板；删除 env **不会**清理模板行。
- 前端可在新建 project 时通过 `/api/v1/env/template/list` 拉取该 org 的模板，给用户预填环境名。

### folders

```sql
create table if not exists folders (
    id uuid primary key,
    environment_id uuid not null references environments(id),
    code text not null,
    name text not null,
    comment text not null default '',
    is_deleted boolean not null default false,
    deleted_at timestamptz,
    deleted_by text not null default '',
    created_by text not null default '',
    updated_by text not null default '',
    created_at timestamptz not null default now(),
    updated_at timestamptz not null default now()
);

create unique index if not exists folders_environment_code_active_uidx
    on folders (environment_id, code)
    where is_deleted = false;
```

### secrets

```sql
create table if not exists secrets (
    id uuid primary key,
    folder_id uuid not null references folders(id),
    key text not null,
    value_ciphertext jsonb not null,
    comment text not null default '',
    version integer not null default 1,
    is_deleted boolean not null default false,
    deleted_at timestamptz,
    deleted_by text not null default '',
    created_by text not null default '',
    updated_by text not null default '',
    created_at timestamptz not null default now(),
    updated_at timestamptz not null default now()
);

create unique index if not exists secrets_folder_key_active_uidx
    on secrets (folder_id, key)
    where is_deleted = false;

create index if not exists secrets_key_search_idx
    on secrets using gin (key gin_trgm_ops)
    where is_deleted = false;
```

说明：

- `value_ciphertext` 是 JSONB，保存算法、nonce 和密文数据。
- 当前只对 `key` 建立 trigram 搜索索引。
- value 搜索详见 [search.md](search.md)。

### 创建人与更新人字段

组织、项目、环境、Folder、Secret 等核心业务主表统一维护：

- `created_by`：创建人用户 ID，来源于当前 JWT 解析出的 `userId`。
- `updated_by`：最后更新人用户 ID，来源于当前 JWT 解析出的 `userId`。
- `deleted_by`：删除人用户 ID，来源于当前 JWT 解析出的 `userId`。

这些字段表示资源当前状态的责任人，便于列表页、详情页和排障场景直接展示。完整历史行为仍以 `audit_records` 为准。

查询返回给前端时，除了用户 ID，还需要返回 label 字段。label 不在资源查询 SQL 中连表计算，而是在 Go 服务内维护用户基础信息内存缓存，通过数据库字段 `created_by` / `updated_by` 到缓存中查找展示名，HTTP 响应字段使用 camelCase：

- `createdByLabel`：创建人展示名，优先取用户缓存中的姓名，如果缓存没有记录或姓名为空，则回退为 `createdBy`。
- `updatedByLabel`：最后更新人展示名，优先取用户缓存中的姓名，如果缓存没有记录或姓名为空，则回退为 `updatedBy`。
- 用户缓存启动时从 `users` 表加载；JWT 用户同步、授权导入用户、bootstrap 管理员创建时同步刷新缓存；普通请求读取当前用户信息时，也可以用 JWT 中的 `userId` 和 `name` 刷新当前进程缓存。
- 资源列表、详情、Secret Redis 预热等查询禁止为了 label 额外 `join users`，避免核心资源查询和用户展示信息强耦合。

接口响应示例：

```json
{
  "id": "uuid",
  "name": "default-org",
  "createdBy": "dev-user",
  "createdByLabel": "Dev User",
  "updatedBy": "dev-user",
  "updatedByLabel": "Dev User",
  "createdAt": "2026-06-01T10:00:00Z",
  "updatedAt": "2026-06-01T10:00:00Z"
}
```

### deleted_records

```sql
create table if not exists deleted_records (
    id uuid primary key,
    resource_type text not null,
    resource_id uuid not null,
    resource_key text not null,
    snapshot jsonb not null,
    deleted_by text not null default '',
    deleted_at timestamptz not null default now()
);

create index if not exists deleted_records_resource_key_idx
    on deleted_records (resource_key, deleted_at desc);
```

说明：

- 保存删除前快照。
- `snapshot` 对 Secret 来说包含的是加密后的 `value_ciphertext`，不能保存明文 value。
- `resource_key` 当前格式为 `<resource_type>:<resource_id>`。

### audit_records

```sql
create table if not exists audit_records (
    id uuid primary key,
    actor text not null default '',
    resource_type text not null,
    resource_id uuid not null,
    action text not null,
    encrypted_value jsonb,
    created_at timestamptz not null default now()
);

create index if not exists audit_records_resource_idx
    on audit_records (resource_type, resource_id, created_at desc);
```

说明：

- 记录资源创建、更新、删除等行为。
- Secret 创建和更新时可以记录加密后的值，但不能记录明文 value。
- 后续建议增加 `request_id`、`metadata`、`diff` 字段，便于审计分析。

### 后续建议：secret_versions

当前 `secrets.version` 已记录版本号，但尚未保存完整历史版本。后续建议新增：

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

create index if not exists secret_versions_secret_changed_idx
    on secret_versions (secret_id, changed_at desc);
```

写入策略：

- 创建 Secret 时主表版本为 `1`，可以选择同步写入 `secret_versions` 的版本 1。
- 更新 Secret 前，将旧版本写入 `secret_versions`。
- 更新成功后，主表 `version + 1`。
- 回滚时，将指定历史版本重新写回主表，并生成一个新版本。

## 删除设计

删除采用“主表逻辑删除 + 删除快照”的组合方案。

主表通用删除字段：

- `is_deleted`：是否删除。
- `deleted_at`：删除时间。
- `deleted_by`：删除人。

删除流程：

1. 开启数据库事务。
2. 根据资源 ID 查询未删除记录。
3. 使用 `to_jsonb(t)` 生成删除前快照。
4. 写入 `deleted_records`。
5. 更新主表 `is_deleted = true`、`deleted_at = now()`、`deleted_by = actor`。
6. 写入 `audit_records`，action 为 `delete`。
7. 提交事务。
8. 如果删除的是 Secret，同步删除 Redis 缓存。

删除行为约束：

- 删除接口必须幂等地处理不存在或已删除资源，当前实现返回 `record not found`。
- 子资源级联删除当前尚未实现。删除上级资源时，后续需要明确是禁止删除非空资源，还是级联逻辑删除。
- 对 Secret 的删除快照只能包含密文，不能包含明文 value。
- 删除记录不应被普通列表接口返回。

恢复设计：

- 当前不实现恢复接口。
- 未来可基于 `deleted_records.snapshot` 做恢复。
- 恢复时必须重新检查唯一约束，例如同名组织、同 Folder 下同 key 是否已被重新创建。

## 历史数据与审计设计

EnvVault 需要区分三类历史数据：

| 类型 | 表 | 目的 |
| --- | --- | --- |
| 删除快照 | `deleted_records` | 记录资源删除前完整状态，支持追溯和未来恢复 |
| 审计记录 | `audit_records` | 记录谁在什么时候对什么资源做了什么动作 |
| Secret 版本 | `secret_versions` | 记录 Secret 每次内容变化，支持 diff、回滚和历史查看 |

### 删除快照

删除快照关注“资源删除前是什么样”。适用于组织、项目、环境、Folder、Secret。

快照内容：

- 主表完整字段。
- Secret 快照包含 `value_ciphertext`，不包含明文。

### 审计记录

审计记录关注“谁做了什么操作”。

建议 action 枚举：

```text
create
update
delete
reveal
restore
grant_role
revoke_role
```

当前已有 action：

```text
create / update / delete
```

后续建议：

- Secret 明文查看 `/secret/reveal` 必须记录 `reveal` 审计。
- RBAC 授权和撤权必须记录 `grant_role`、`revoke_role`。
- 审计记录不能保存 Secret 明文 value、JWT token、加密主密钥。

### Secret 版本历史

Secret 更新历史应独立于审计记录。审计记录描述行为，版本表保存可回滚的数据。

版本历史建议：

- 更新 Secret 时先保存旧版本。
- 主表只保存最新版本。
- 历史版本中的 `value_ciphertext` 仍是密文。
- 查看历史版本需要 `secret:read`。
- 查看历史版本明文需要 `secret:reveal`，并写审计记录。

## 加密设计

默认加密方式为 `AES-256-GCM`，具备认证加密能力。

加密接口：

```go
type Encryptor interface {
    Encrypt(ctx context.Context, plaintext []byte) (Ciphertext, error)
    Decrypt(ctx context.Context, ciphertext Ciphertext) ([]byte, error)
}
```

主密钥来源：

```yaml
security:
  encryption_key: "<base64 encoded 32-byte key>"
```

安全要求：

- Secret value 入库前必须加密。
- 明文 value 不写日志、不写审计、不写 Redis、不写 PostgreSQL。
- 加密主密钥不写日志、不进入响应、不提交仓库。
- 解密操作必须接收 `context.Context`。

密钥轮换后续设计：

- 新增 `encryption_keys` 表记录 `key_id`、`algorithm`、`status`、`created_at`。
- `value_ciphertext` 中保存 `key_id`。
- 新写入数据使用 active key。
- 旧数据读取时按 `key_id` 解密。
- 后台任务逐步 re-encrypt 历史数据。

## 搜索设计

当前实现：

- `/api/v1/secret/search` 只按 Secret key 搜索。
- Redis 优先，失败回退 PostgreSQL。
- Redis 不保存明文 value。

后续完整搜索设计见 [search.md](search.md)。

核心原则：

- 搜索必须先做 RBAC scope 过滤。
- 无权限数据不能进入搜索候选集。
- value 搜索需要解密后匹配，但明文不能持久化。
- 正则搜索需要限制 pattern 长度、超时和返回数量。
- 搜索结果默认不返回 Secret 明文 value。

## 权限设计

当前代码状态：

- JWT 中间件已实现认证。
- JWT 认证可通过配置 `auth.enabled` 开关控制，默认开启。
- 当 `auth.enabled = false` 时，服务端跳过 JWT 校验，并使用 `auth.dev_user_id`、`auth.dev_user_name` 注入开发用户，便于本地测试。
- `Authorizer` 接口已实现，默认装载为 `RBACAuthorizer`（在 `internal/app/app.go` 中通过 `auth.NewRBACAuthorizer(rbacStore)` 注入），不再使用 `AllowAllAuthorizer`。
- `AllowAllAuthorizer` 仍保留为测试与本地放行的可选实现。
- v5 起所有数据 handler 全部接入 `allowScope`,`org_admin` / `project_admin` / `project_viewer` / `project_developer` 等角色真正生效。
- v6 起所有权限检查调用点从 controller 下沉到 service;`SecretService` 9 个方法 + `RBACService` 11 个方法入口第一行 `s.authorizer.Allow`。controller 只做认证拦截(JWT 解析、bind、write),不做 authz。
- v6 起所有 delete 操作按"父 → 子"级联软删,避免孤儿行;`org` 级删除需 `force=true` 触发 4 级级联,需要额外 `org:force_delete` 权限。
- v7 起 list 接口按 caller 的 `user_role_bindings` 自动收窄可见作用域(`ListOrganizations` / `ListProjects` / `ListEnvironments` / `ListEnvironmentTemplates` / `ListFolders` / `ListSecrets`);controller 入口不再做 `allowScope`,repo SQL 自身完成 cascade narrowing;无 binding 的 user 拿到空 list(隐式空 list,不返 403)。
- v8 起新增 `POST /api/v1/secret/path/batchReveal` 批量 reveal 接口:按 `org.proj.env.folder` 4 段路径 + 可选 keys 列表,**一次性**返回所有命中 key 的明文 + 元数据。**不分页**、**无上限**(用户决策);复用 v7 cascade narrowing(`secret:reveal` 权限自动从 secret / folder / env / project / org 任何一层 binding 向下展开);**整批 1 条 audit**(`action="reveal_batch"`,`resource_type="folder"`,`encrypted_value=jsonb([keys...])`)。
- v9 起新增认证 & 用户管理:4 个端点(`/auth/register` `/auth/login` `/auth/logout` `/auth/changePassword`),argon2id 密码哈希 + Redis sliding window 频控 + 进程内 `TokensCache` 强制登出。
- v10 起 `POST /api/v1/folder/list` 在 `environmentId` 模式下支持 `includeSubfolders: true` 开关:响应里每个父 folder 多带 `subfolders: [Entity, ...]`,一次性拉完两级目录,消除 N+1 round-trip。`folderParentId` 模式不支持(只到 level=2),传了 true 直接 400。

完整 RBAC 设计、实现细节和权限矩阵以 [rbac_degisn.md](rbac_degisn.md) 为准。

关键原则：

- JWT 只证明用户身份，不直接代表权限。
- 授权检查应靠近 service 操作本身。
- 列表、搜索、审计查询必须按权限过滤。
- `secret:read` 和 `secret:reveal` 必须拆开。

### RBAC 层级与 Environment 角色

正式授权作用域统一为：

```text
global → organization → project → environment → folder
```

Secret 是受控资源，不作为角色绑定作用域；Secret 操作权限从 Folder、Environment、Project、Organization 或 Global 继承。系统内置 `environment_admin`、`environment_developer`、`environment_viewer`、`environment_auditor` 四个 Environment 角色。

权限计算只做 allow 叠加，不做 deny：上级作用域角色自动作用到下级资源；用户可以在某个更具体的下级作用域额外绑定角色，补充权限。角色绑定必须与角色自身作用域一致，例如 `environment_admin` 只能绑定到 Environment，不能绑定到 Project 或 Folder。

### 字段命名：`user_id` / `role_type` / `resource_id`

domain 层和 HTTP API 层把内部实现术语映射为 SDK 友好的"用户-角色-资源"三段式。数据库 schema 与 `permissionCode` / `defaultPermissions` / `defaultRoles` 不动,SDK 不直接面对 `scope_type` / `scope_id` / `role_code` 等内部命名。

| 内部表/字段 | domain / API 字段 | 说明 |
| --- | --- | --- |
| `users.id` | `userId` | 数据库用户 UUID,RBAC 授权与绑定统一使用它做用户唯一标识 |
| `roles.code` | `roleType` | 角色码,例如 `org_admin` |
| `user_role_bindings.scope_type` | `resourceType` | `global` / `organization` / `project` / `environment` / `folder` / `secret` / `env_template` |
| `user_role_bindings.scope_id` | `resourceId` | global 时为空 |
| `user_role_bindings.expires_at` | `expiresAt` | RFC3339 |
| `user_role_bindings.granted_by` | `grantedBy` | 授权人 users.id |
| `user_role_bindings.created_at` | `grantedAt` | 授权时间 |

`internal/domain/rbac.go` 新增 `RoleGrant` 类型作为 `RoleBinding` 的语义别名,`RoleBinding` 字段保持不变以兼容 store/service 层。新类型仅在 HTTP API 响应中用,经 `(RoleBinding).ToGrant()` 转换。

请求体 `roleGrantRequest` / `userLookupRequest` / `pagedUserLookupRequest` 新增 alias 字段:

| 旧字段 | 新 alias | 解析优先级 |
| --- | --- | --- |
| `externalUserId` | `userId` | `externalUserId` 仅兼容旧客户端,当前也按 users.id(UUID) 解析;`userId` 非空时优先 |
| `roleCode` | `roleType` | 同上 |
| `scopeType` | `resourceType` | 同上 |
| `scopeId` | `resourceId` | 同上 |

旧字段仍然兼容,绑定逻辑取 alias 优先;但 RBAC 不再使用 `external_user_id` 做授权主身份。响应(`ListRoleBindings` / `ListUserGrants` / `GrantRole`)字段从 `RoleBinding` 整结构转成 `RoleGrant`,SDK 看到的是三段式。

### 授权检查放在 service 层(v6)

v5 把 `allowScope` 放在 controller;v6 起,**所有权限判定走 `auth.Authorizer.Allow` 统一接口,调用点下沉到 service**。controller 只做认证拦截(JWT 解析、bind、write),不做 authz。

#### 调用矩阵

见 `design/todo.md` v6 章节 §2。

#### 错误传播

service 返 `auth.ErrPermissionDenied` → `controller.write` 映射 1403 Forbidden;`domain.ErrNotFound` → 1404;`domain.ErrConflict` → 1409。链路见 `internal/http/controller/resource_common.go:111-131` 的 `write` 实现。

#### 不下沉的部分(本期承认)

- 7 个数据资源(Org/Project/Env/EnvTpl/Folder/Secret CRUD/Audit)的权限检查**继续在 controller 层**。理由:这些是简单 CRUD,无业务编排;把它们下沉需要新建 6 个 service,违反 v4 瘦身原则(controller 是细粒度资源权限边界,service 是业务聚合边界)。
- `Bootstrap` / `EnsureBootstrapAdmin` 是应用启动调用,非用户请求,不需要 user context。

#### 设计权衡

- 优点:绕过 HTTP 直接调 service 的代码(SDK、worker、cron)也会被 RBAC 拦截。
- 代价:service 方法签名加 `user auth.UserInfo` 是 breaking change,所有调用方需同步更新。本次只 controller 一处调用,无第三方影响。
- 后续:若需让 service 在新调用方(SDK / K8s Operator)也能被直接调用,现在改完就免去重做。

## GitLab 风格父级覆盖 + 自授拦截（v9）

v6 完成了「单点操作鉴权」:GrantRole / RevokeRole / CreateRole / UpdateRole
入口会用 `authorizer.Allow("rbac:binding:manage")` 校验 caller 在目标
scope 上持有对应 manage 权限。但**光有 manage 权限不够**:若 caller
本身不持有被授权 / 被创建角色所声明的权限码,他就能「以小欺大」—
把一个自己不具备的能力授给 target user,或创建一个自己用不了的角色。

v9 引入**父级覆盖 (parent coverage)** 校验。语义对齐 GitLab 的角色
等级:Owner > Maintainer > Developer > Reporter > Guest,父级角色
天然涵盖下级角色。

### 覆盖规则（action 维度）

| 父级 action | 覆盖子级 action | 备注 |
| --- | --- | --- |
| `X` | `X` | 同 action 覆盖自身 |
| `update` | `read` | GitLab write ≥ read |
| `resource:manage` | `resource:read` | maintainer includes reporter;resource 前缀必须相同 |

其他 action(create / delete / force_delete / reveal / search / list /
template:read 等)只覆盖自身,不交叉。

### 父级链

校验沿资源父级链 `folder → environment → project → organization → global`
从最具体到最一般,只要**任意一层**满足覆盖即放行;都不满足 → 403。
这是 store 层的 `ResourceScopes` 接口语义,RBAC service 直接复用。

跨 resource 覆盖不参与 resource 前缀匹配(例如 `org:update` 同样能
覆盖子级 `secret:read`),这是 GitLab 「org owner 可以管理组织内所有
资源」的语义具体实现。

### 影响的入口

| 方法 | 新增检查 |
| --- | --- |
| `GrantRole` | `checkCallerParentCoverage(role.Permissions)` + `rejectSelfGrant` |
| `RevokeRole` | `checkCallerParentCoverage(role.Permissions)` + `rejectSelfGrant` |
| `CreateRole` | `checkCallerParentCoverage(permissions)` |
| `UpdateRole` | `checkCallerParentCoverage(permissions)` |
| `ListRoleBindings` | 走 cascade store 查询,返回 (scopeType, scopeId) 及所有下级 binding |
| `ListUserGrants` | 走 global `rbac:binding:read` 校验 |

### 自授 / 自撤

`rejectSelfGrant` 是固定边界:即便 caller 是 `platform_admin`,也不允许
把角色授给自己或撤销自己(`userId == callerUserId` → 403)。

### cascade 收窄

`ListRoleBindings` 走 store 层新增的 `ListRoleBindingsCascading` 接口,
内部用递归 CTE 沿 scope 层级收集 binding,一次性返回 caller 在该
scope 上「应该看见的全集」。

## List 接口按 caller 权限收窄（v7）

v5/v6 完成了「单点操作鉴权」：所有写操作与单点 read/reveal 都在 `SecretService` / `RBACService` 入口
用 `authorizer.Allow` 拦截。但 list 接口始终返**全量**：通过 `allowScope` 入口校验后，caller 能看到
该 parent 下所有记录，而不管他在更广 scope 上是否还有 binding。

v7 把 list 的"入口"判定从 controller 下沉到 repo SQL，按 caller 的 `user_role_bindings` 自动收窄可见
作用域，caller 看到的就是他能看的。

### 单一 SQL 收窄模式

数据列表分为两类语义：

- **操作权限列表**：例如 Secret 列表、Folder 列表，按目标操作权限码收窄。
- **导航父级列表**：例如 Organization / Project / Environment 级联选择器，需要从下级授权反推上级可见节点。

导航父级列表只代表「前端可以看见并继续下钻」，不代表 caller 自动获得上级资源的完整 `read/update/delete` 权限。单点详情和写操作仍然通过 `authorizer.Allow` 严格校验。

操作权限列表的 WHERE 末尾追加一段 narrowing 谓词，核心是一个 CTE 把「caller 持有 `permissionCode`
的所有 (scope_type, scope_id) 对」算出来，然后 OR 进 list 的 WHERE：

```sql
with user_read_scopes as (
  select distinct urb.scope_type, urb.scope_id
  from user_role_bindings urb
  join users u on u.id = urb.user_id
  join roles r on r.id = urb.role_id
  join role_permissions rp on rp.role_id = r.id
  join permissions p on p.id = rp.permission_id
  where u.id = $1
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
    or {narrowing predicate,见下表}
  )
```

### Cascade 链

| List | permission code | narrowing 谓词 |
| --- | --- | --- |
| `ListOrganizations` | navigation | `org/project/env/folder/secret` 任一下级资源权限均可反推出所属 organization 可见 |
| `ListProjects` | navigation | `project/env/folder/secret` 任一下级资源权限均可反推出所属 project 可见；organization scope 持有下级资源权限时可见其下 project |
| `ListEnvironments` | navigation | `env/folder/secret` 任一下级资源权限均可反推出所属 environment 可见；project / organization scope 持有下级资源权限时可见其下 environment |
| `ListEnvironmentTemplates` | `env:template:read` | `t.id in (… 'env_template')` ∪ `t.org_id in (… 'organization')` |
| `ListFolders` | `folder:read` | `t.id in (… 'folder')` ∪ `t.parent_id in (… 'folder')` ∪ `t.environment_id in (… 'environment')` ∪ `e.project_id in (… 'project')` ∪ `p.org_id in (… 'organization')`(需 join env+project) |
| `ListSecrets` / `SearchSecrets` / `BatchRevealSecretsByPath` | `secret:list` / `secret:search` / `secret:reveal` | `s.id in (… 'secret')` ∪ `s.folder_id in (… 'folder')` ∪ `f.parent_id in (… 'folder')` ∪ `e.id in (… 'environment')` ∪ `p.id in (… 'project')` ∪ `o.id in (… 'organization')`(需 join 4 张表) |

`secret` / `env_template` 两种 scope_type 当前没有任何 role 会绑在这两个层级，分支恒为 false；
保留谓词以支持未来「给单个 secret / env_template 授权」的扩展。

### Caller 行为矩阵

| Caller 状态 | 行为 |
| --- | --- |
| 无 JWT（中间件已 401） | 不会到达 handler |
| `userId == ""`（异常路径） | CTE 返空 → 全部 list 返空（不 500、不 403） |
| `platform_admin`（绑在 global） | EXISTS 命中 → 全量 |
| `org_admin @ (organization, X)` | org 链命中 → 看到 X 及 X 下所有 project / env / folder / secret（cascading） |
| `project_viewer @ (project, Y)` | project 链命中 → 导航列表能看到 Y 所属 organization、Y project 以及 Y 下 env / folder / secret；但不自动获得 organization 的详情/更新/删除权限 |
| `folder_viewer @ (folder, Z)` | folder 链命中 → 看到 Z、Z 的直接子 Folder及其中 Secret 的 key/元数据；不包含 `secret:reveal`，也看不到同 env 下其他 Folder 树 |
| 无任何 binding | CTE 空 → 空 list |

### 隐式空 list vs 显式 403

无 binding 的 user 拿到空 list，**不返 403**。理由：

- 性能：返 403 仍要走一遍权限计算；空 list 直接 CTE 不命中，开销更小。
- 语义：「我看的就是我能看的」，和现有的"我看不到就是没有"心智模型一致。
- 安全：caller 无法通过 200/403 时延差探测存在性。

### 不在本轮范围

- **Audit 收窄**：`ListAuditRecords` 的 `resource_type` 多态（每种 ancestry 不同），SQL 收窄需 case-by-case join；本期按"by-id 查具体资源"绕过，不收窄。
- **RBAC 管理端 list 收窄**：`ListRoles` / `ListRoleBindings` / `ListUsers` / `ListUserGrants` 是 RBAC 自身管理界面，语义和数据 list 不同。
- **GlobalSearch 收窄**：已在 controller 层做 per-hit allowScope（`search.go:106-155`），也属另一条线索，留下一轮。

### 影响范围

| 文件 | 改动 |
| --- | --- |
| `internal/store/store.go` | `ResourceRepository` 6 个 list 方法加 `callerUserId` 参数；`ListSecrets` 额外加 `action string` |
| `internal/store/postgres/repository.go` | 6 个 list 方法 SQL 追加 narrowing 子句；新增 `userReadScopeCTE` / `narrowingPredicate` / `scopeNarrowingWhere` helper |
| `internal/service/secret_service.go` | `List` / `Search` 透传 `user.UserId` 给 repo，移除 `listScope` 入口校验；`Search` 不再走 `SecretCache` |
| 6 个 controller | 移除 `allowScope` 入口，调用方从 `auth.UserFromContext(c).UserId` 拿 caller |
| 测试 | 删除 `TestListScope_*`；新增 `TestSecretService_List_PassesCallerUserId` 等 4 个 |
| 文档 | `design/DESIGN.md` 本节 + `design/todo.md` v7 状态行 |

---

## Secret 路径访问

v5 起 Secret 支持路径访问,SDK / K8s 集成无需先调 4 个 lookup 接口再调 reveal。

### 路径格式

```text
org_code.project_code.env_code.folder_code.KEY
```

示例:`o1.p1.dev.globals.DATABASE_URL`。

### 单 SQL 5 表 join

`internal/store/postgres/repository.go::GetSecretByPath` 一次 round-trip 解析 4 级 code + key:

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

执行计划为 4 步 index-nested-loop,每步命中一个 `(parent_id, code) where is_deleted = false` 唯一索引;任意一段 code 找不到 → 0 rows → `ErrNotFound`。`Path` 字段由 `buildSecretPath` 自动拼接。

### 接口

| 路径 | 用途 | 权限 |
| --- | --- | --- |
| `POST /api/v1/secret/path/info` | 路径查询 secret metadata,不返回明文 value | `secret:read` |
| `POST /api/v1/secret/path/reveal` | 路径查询 secret 明文 value,走加密 + reveal 审计 | `secret:reveal` |

请求体:

```json
{ "path": "o1.p1.dev.globals.DATABASE_URL" }
```

`RevealByPath` 走现有 `Reveal` 加密链路:`GetByPath` 拿 id → `Reveal(id)` 解密 → 写 reveal 审计。RBAC 在 `GetByPath` 成功后做(`secret:read` / `secret:reveal`),无权限用户拿到 403。本次不隐藏侧信道,后续若需严格防探测,把 `allowScope` 失败时的 403 改 404 即可。

### 效率与权衡

- 4 级 code 解析 + 最终 secret 查找共 5 步 index-nested-loop,全走唯一索引,实测单次 < 5ms(本地 PG,常规数据量);
- 相对"先 4 个 lookup 接口再 reveal"方案减少 4 次网络 round-trip,SDK 端代码从 5 次请求降为 1 次;
- 5 段解析(`parseSecretPath`)在 service 层做,任一段为空或段数 != 5 → `invalid secret path`,早失败。

### 批量 reveal（v8）

`/secret/reveal` 与 `/secret/path/reveal` 都是单条 reveal,SDK / K8s 集成方常需要
一次拉多个 key(例如同时拉 `DATABASE_URL` / `API_KEY` / `REDIS_URL`),串行 N 次调用
网络往返 + 鉴权 + audit 重复成本高。v8 新增 `POST /api/v1/secret/path/batchReveal`
接口,接收 folder 路径 + 可选 key 列表,一次性返回所有命中 key 的明文 + 元数据。

#### 路径格式

```text
org_code.project_code.env_code.folder_code
```

只到 folder,不含 KEY。`parseFolderPath` 校验同 `parseSecretPath`(4 段,非空,
trim,全段必填);段数错或空段返 400。

#### 行为

- `keys` 缺省 / 空数组 = 该 folder 下所有 secret。
- **无分页、无上限**;`list` 按 `key` ASC 排,便于 diff。
- 复用 v7 `userReadScopeCTE` + `scopeNarrowingWhere` 做 cascade narrowing,
  `secret:reveal` 权限从 (secret, folder, env, project, org) 任一层 binding 自动展开。
- 无 binding 的 caller 拿到空 `list`(隐式空 list,不 403,与 list/search 行为一致)。
- 整批解密失败 → 整批 5xx,与单条 `Reveal` 行为一致。

#### notFound 语义

- request `keys` 非空时:`notFound = request keys ∖ 命中 keys`(按 request 顺序)。
- request `keys` 为空时:**不返回** `notFound` 字段(无对照,避免误导调用方)。

#### 整批 audit 设计

`audit_records.resource_id` 是单 uuid,无法在一行内同时记录 N 个 secret id;
v8 决策:整批 1 条 audit,`resource_type="folder"`,`resource_id=<folder.id>`,
`action="reveal_batch"`,`encrypted_value=jsonb(["KEY1", "KEY2", ...])` 记录
「实际被 reveal 的 keys 列表」:

- `keys` 非空请求:填 request keys 列表(可包含「请求了但未命中」的 key,审计能反查
  caller 尝试读了什么);
- `keys` 空请求:填实际命中的 keys 列表;
- 无命中(空 list):不写 audit(无意义)。

`action="reveal_batch"` 字符串无 schema 约束;前端按 action 区分展示。
`resource_id` 关联 folder `info` 接口可反查 folder 完整路径。

#### 与 list/search 的对比

| 维度 | list / search | batchReveal |
| --- | --- | --- |
| 输入 | folder / env + 过滤 + 分页 | folder path + key 列表 |
| 输出 | 列表(无明文) + total / pagination | 列表(带明文) + notFound,**无 total / pagination** |
| 权限 | `secret:list` / `secret:search` | `secret:reveal` |
| audit | 不写 | **整批 1 条** |
| 缓存 | Search 走 cache(已废弃) | 不走 cache(同 Reveal) |
| 数量上限 | pageSize (200) | **无上限**(用户决策) |

### Folder list 嵌套子 folder（v10）

`POST /api/v1/folder/list` 在 `environmentId` 模式(level=1 父列表)下,前端通常还要
对每个父 folder 再调一次 list(传 `folderParentId`)才能拼出两级树。在 env 下 folder
数量不大时,这 N+1 次往返既慢又费 audit/audit-quota。v10 给 `environmentId` 模式
加一个 `includeSubfolders` 开关,触发后响应里每个父 folder 多带
`subfolders: [Entity, ...]`,一次性把两级目录拉完。

#### 请求

```json
{
  "pageNum": 1,
  "pageSize": 20,
  "environmentId": "uuid",
  "folderParentId": "",
  "includeSubfolders": true
}
```

校验:
- `environmentId` 与 `folderParentId` 互斥(已有)。
- `includeSubfolders=true` 时必须传 `environmentId`,否则
  `400 1002 includeSubfolders only valid with environmentId`。

#### 响应

触发 `includeSubfolders=true` 时,`data.list` 的元素从 `Entity` 升级为
`FolderWithSubfolders`(用 `allOf: [Entity, { subfolders: [Entity] }]` 表达;
无子 folder 时 `subfolders: []`,与 `tree.get` 同款约定,前端不用判 null):

```json
{
  "code": 0,
  "data": {
    "pageNum": 1,
    "pageSize": 20,
    "total": 3,
    "list": [
      {
        "id": "folder-1", "parentId": "env-id", "code": "globals", "name": "Globals",
        "comment": "", "createdBy": "...", "createdByLabel": "...", "updatedBy": "...",
        "createdAt": "...", "updatedAt": "...",
        "subfolders": [
          { "id": "folder-1-1", "parentId": "folder-1", "code": "aws", "name": "AWS", ... },
          { "id": "folder-1-2", "parentId": "folder-1", "code": "db",  "name": "DB",  ... }
        ]
      },
      {
        "id": "folder-2", "parentId": "env-id", "code": "groups-secrets", "name": "Group Secrets",
        "subfolders": []
      }
    ]
  }
}
```

`data.total` 仍只计父 folder 数量;`subfolders` 不参与分页(env 下子 folder 数量
天然受父 folder 数量约束,无分页必要)。不传 `includeSubfolders` 或 `false` 时,
响应 list 项完全等同现有 `Entity`,**不带** `subfolders` 字段(omitempty),
**与历史 100% 兼容**。

#### 权限

子 folder 走与父 folder 同样的 `folder:read` narrowing,复用 v7 已有的
`userReadScopeCTE` / `narrowingPredicate` / `scopeNarrowingWhere`;
narrowing chain 与 `ListFolders` 一致:`(folder, environment, project, organization)`。
一级 Folder binding 通过 `t.parent_id` 覆盖其二级 Folder；二级 Folder 也可以通过
自身 `t.id` 独立授权。父与子均未命中时，子不出现在 `subfolders`。

#### 实现要点

- 新增 `ResourceRepository.ListFolderChildren(ctx, callerUserId, parentIds) (map[string][]Entity, error)`:
  - 复用 `entityReadColumns("parent_id")` 让 level=2 folder 的 `Entity.ParentId` 字段
    自动取 `parent_id` 列(父 folder id);
  - narrowing entries `[(folder, t.id), (folder, t.parent_id), (environment, t.environment_id), (project, p.id), (organization, p.org_id)]`;
  - 单 SQL `WHERE t.parent_id = ANY($3::uuid[])`;
  - `ORDER BY t.parent_id ASC, t.name ASC` 便于 Go 端按 `t.parent_id` 分组装 `map`;
  - 空 `parentIds` 直接返回空 map(不发 SQL);返回 map 始终非 nil。
- Handler:ListFolders 在 `includeSubfolders=true` 时,先调 `ListFolders` 拿父列表,再
  用父列表的 `id` 集合调 `ListFolderChildren`,把结果按父 id 拼到响应里。空子 folder
  兜底为 `[]Entity{}`,JSON 出 `[]` 而非 `null`。

#### 风险与权衡

- **响应体膨胀**:env 下 50 个 folder,每个 10 个子 folder → 500 行 Entity。属可接受范围;
  未来若发现过度,可加 per-parent 上限。
- **隐式授权泄露面**:子 folder 走 narrowing,但父 folder 已经过 narrowing,所以没有
  额外泄露面;反过来若父不通过,子不会出现在 `subfolders`(整个父都不在 list 里)。
- **DTO 命名**:用 `Subfolders`(复数)避开与 `Entity.ParentId` 的命名;
  `Entity` 自身没有 `subfolders` 字段(纯 entity,无 children 概念),不会冲突。
- **回退**:DTO 加字段对现有调用方透明;完全独立方法 `ListFolderChildren` 不影响
  `ListFolders` 行为。
- **审计**:不新增 audit 事件(list 本身不写 audit)。

### Secret 批量创建（v11）

`POST /api/v1/secret/create` 是单条创建。当用户要在一个 project 下多个 env
里给同一个 key（如 `DATABASE_URL`）设置不同 value 时，需要 N 次 round-trip、
N 次权限校验 + N 条 audit。v11 新增 `POST /api/v1/secrets/batchCreate`（注意：
复数 `secrets`，与单条 `/secret/*` 区分，**单独路由组**）：**一次请求**由
客户端**显式指定每条 (env, folder, value) 三元组**，**单事务**完成 N 条 INSERT
+ 1 条 batch audit，全成功或全 rollback。

> v12 调整：原 4 个硬编码 env 字段 (`dev` / `test` / `sim` / `prod`) 替换为
> **`envList: [{envCode, folderId, value}, ...]`** 数组形式。env 不再限定
> 4 个标准名，项目下任意 env code 都可作为入参；envList 长度 = 该 key 要创建的
> secret 数。详见下节。

#### 业务语义

- **无 template folder 机制**：v11 不再有"通过 folderId.code 跨 env 找同名 folder"
  的服务端推断；客户端在每个 entry 里**显式**指定 `envCode` + `folderId` + `value`。
  理由：每个 env 的目标 folder 是客户端已知的业务事实；让客户端显式表达减少
  服务端的隐式查找和失败面。
- **envList 数组形式**：每个 item 用 `envList: [{envCode, folderId, value}, ...]`
  列出要创建的 secret。`envCode` 是项目自定义的 env code（dev / test / sim / prod
  是常见值，但**不限**于这 4 个）。envList 长度 = 该 key 要创建的 secret 数。
- **顺序由 client 控制**：service 不再硬编码 env 顺序；envList 顺序由请求体
  决定，service 端做 trim + 去重 + 校验后顺序透传，audit 与 cache 都按此顺序处理。
- **同 item 内唯一性**：
  - `envCode` 在同一 item 内必须唯一（同 env 下建两条同 key 的 secret 视为语义错）；
  - `folderId` 在同一 item 内必须唯一（避免 DB `(folder_id, key)` 唯一约束冲突，
    错误信息比「secret 已存在」更精准，client 可立刻定位）。
- **每条 item 至少要指定一个 env**：envList 为空视为该 item 无效，整条拒绝。
- **整批原子**：任一阶段失败（入参校验 / 权限 / 冲突 / 内部错误）即整批拒绝，
  不会部分写入。

#### 请求

```json
{
  "secretList": [
    {
      "key": "DATABASE_URL",
      "comment": "数据库url",
      "envList": [
        { "envCode": "dev",  "folderId": "f-dev-uuid",  "value": "postgres://dev.local/db"  },
        { "envCode": "test", "folderId": "f-test-uuid", "value": "postgres://test.local/db" },
        { "envCode": "sim",  "folderId": "f-sim-uuid",  "value": "postgres://sim.local/db"  },
        { "envCode": "prod", "folderId": "f-prod-uuid", "value": "postgres://prod.local/db" }
      ]
    }
  ]
}
```

只指定部分 env（例：只 `dev` + `prod`）也合法；envList 留空则该 item 被拒绝。
`envList` 内的顺序由 client 决定，service 透传处理。

#### 响应（成功，HTTP 200）

```json
{
  "code": 0,
  "msg": "success",
  "data": null
}
```

成功响应极简：HTTP 200 + `code: 0` + `data: null`。客户端按需用 `secretList` /
`secret/path/info` 反查 metadata。

#### 失败响应（**统一 HTTP 200 + code=-1**）

```json
{
  "code": -1,
  "msg": "创建失败，secret 已存在",
  "data": null
}
```

| 失败场景 | `msg` 格式 | `code` |
| --- | --- | --- |
| 入参校验失败（空 secretList / 缺 key / 缺 envCode / 缺 folderId / key 格式错 / 重复 envCode / 重复 folderId） | `入参校验，<错误描述>` | `-1` |
| 权限不足（任一 target folder 缺 `secret:create`） | `创建失败，权限不足` | `-1` |
| 目标 folder 不存在 | `创建失败，目标 folder 不存在` | `-1` |
| (folder, key) 冲突（unique violation） | `创建失败，secret 已存在` | `-1` |
| 加密 / DB / 缓存等其他内部错误 | `创建失败，<err.Error()>` | `-1` |

> **关键设计决策**：本端点**所有失败统一 code=-1**。这与「业务错也 HTTP 200」
> 的 v11 起点一致，把所有"入参错 / 权限 / 冲突 / 内部错误"统统走同一条出口，
> 前端只需判定 `body.code != 0` 即失败，无需区分 -1 / 1403 / 1409。
> `msg` 中文描述提供足够信息供前端展示。`auth.ErrPermissionDenied` /
> `domain.ErrNotFound` / `domain.ErrConflict` 在 controller 端用 `errors.Is` 翻译
> 成固定文案；其他 err 走 `err.Error()` 原文。

#### 事务与 audit

与 v8 `batchReveal` 同款「单事务 N 条 INSERT + 1 条 batch audit」范式：

- `repo.BatchCreateSecrets(items)`：`BeginTx` → 循环 N 条
  `INSERT INTO secrets RETURNING id` → 1 条
  `recordAuditTx(action="create_batch", resource_type="folder",
  resource_id=第一条 item 的 folder_id,
  encrypted_value=jsonb([{envCode, key, secretId}, ...]))` → `Commit`。
- 任一 INSERT 失败（PG `23505` unique violation 等）→ `defer tx.Rollback()` 触发，
  err 透传，service 层 `errors.Is(err, domain.ErrConflict)` 检测（`translatePgErr`
  已做 unique violation → `domain.ErrConflict` 翻译）。
- 成功 commit 后**不**走 `r.GetSecret` 二次查表（节省 N 次 round-trip）：
  客户端如需 metadata 调 `secretList` / `secret/path/info` 即可。
- commit 后逐条 `s.cacheUpsert(ctx, secret, ciphertext)` 同步到 Redis SecretCache，
  顺序与 `targets` 一致。
- 单条 audit 的 `resource_id` 选第一条 item 的 folder_id（共享同一个 folder 的
  item 聚合时无歧义；多 folder 跨 project 场景罕见，且 audit 通过
  `encrypted_value` 数组保留了完整 env×key 映射）。

#### 权限

对每个 (envCode, folderId, value) target **单独** `secret:create` 权限 check，
走 v7 cascade narrowing `(folder → environment → project → organization)`。
任一 target 拒绝即整批失败、err 透传为 `auth.ErrPermissionDenied`，controller
翻译为 `msg: "创建失败，权限不足"`。

#### 关键设计决策

| 维度 | 决策 | 备注 |
| --- | --- | --- |
| 端点路径 | `/api/v1/secrets/batchCreate`（**复数** secrets，独立路由组） | 与单条 `/secret/*` 区分 |
| env 字段 | **`envList: [{envCode, folderId, value}, ...]`** 数组 | v12 替换原 4 硬编码字段；env 不再限于标准 4 个 |
| env 顺序 | 由 client 入参控制 | service 透传，不重排 |
| 目标 folder 指定 | 客户端显式（每个 entry 自带 folderId） | 无服务端跨 env 推断 |
| 入参校验 | secretList 非空 / key 合法 / envList 非空 / 每 entry.envCode+folderId 非空 / envCode 唯一 / folderId 唯一 | controller 端 + service 端双重防御 |
| 失败统一出口 | HTTP 200 + body code=-1 + msg 中文 | 不再分 1403/1409/1404/1500 |
| 原子性 | **单事务**：N 条 INSERT + 1 条 batch audit | v8 batchReveal 范式 |
| 业务码常量 | `CodeBatchCreateError = -1` | — |
| 权限 | 每个 target folder 单独 `secret:create` check | v7 cascade narrowing |
| Audit | 1 条 `action="create_batch"`，`resource_type="folder"`，`resource_id=第一条 item folder_id` | 整批 1 条 |
| Cache | 同步 upsert 每条到 Redis SecretCache | 同 CreateSecret |

#### 不支持的场景

- `folderParentId` 嵌套 / `includeSubfolders` 嵌套（与单条 Create 保持一致）。
- env entry `envCode` / `folderId` 为空字符串（整条 item 拒绝）。
- 同 item 内重复 `envCode` 或重复 `folderId`（视为入参错，整条拒绝）。
- 跨 project 批量创建（每条 item 各自的 folderId 必须在 caller 有 `secret:create`
  权限；无 project 级批量语义，caller 需在每个目标 folder 上分别有权限）。

#### 风险与权衡

- **HTTP 200 + 业务错 vs 4xx**：本端点选择 200 + body code 是用户原始诉求
  （「业务错也 HTTP 200」）。代价是：传统「HTTP status 判定」失效，前端 SDK
  必须解析 body.code；服务端需在中间件/网关放行本端点的 200。后续如果需要
  「HTTP 4xx 风格」，需要新加 `strict` query 参数或新端点。
- **统一 code=-1 与多错误码的权衡**：本端点选择统一 `-1`（用户明确要求）
  而不是分 1403/1409/1404。代价：前端拿不到精确错误类型；获益：实现简单、
  失败面收窄、SDK 判定逻辑一致。`msg` 文案提供 enough info 给前端展示。
- **整批 1 条 audit**：与 v8 batchReveal 一致；丢失的是「单条 secret 的精确
  失败原因」——例如 4 条 INSERT 中第 3 条 23505，整批 rollback，但 audit 里只有
  attempt 4 条的列表，没有"哪条冲突"。这与单条 `CreateSecret` 的"1 条 1 audit"
  行为相比是损失，但换取 N 倍 round-trip + N 倍 audit 写入的节约。
- **无 success response data**：成功时 `data: null`（不返 created[] 列表）。
  客户端需要 metadata 走 `secretList` / `secret/path/info` 二次查询。节省
  响应体大小 + 避免 N 个 secret 的 ciphertext 误传出（虽然 service 不填
  `Secret.Value`，但 metadata 本身也含 `comment` / `createdBy` / `updatedBy`
  等冗余信息）。
- **envList 数组 vs 4 硬编码字段**：v12 改用数组后，env 不再限于 4 标准名，
  任何项目自定义 env code 都可作为入参；前端 schema 统一用 `envList`，后端
  service 层对 env 存在性 / 权限的判定更灵活。代价是 client 端要做 envList
  构造（之前 4 字段自动展开），但换来配置自由度的提升。
- **回退**：若端点行为不被接受，handler / writer 集中在
  `internal/http/controller/resource_secret_batch.go`，删一个文件 + 路由 + service
  一个方法即可回退到 v10 状态。

### Secret 跨 env 列表（v12）

`POST /api/v1/secret/list` / `/secret/search` / `/secret/path/batchReveal` 等
已有端点存在以下使用痛点：

- `/secret/list` 与 `/secret/search` 一次只能查一个 env（或 folder 维度）；
- `/secret/path/batchReveal` 一次只能 reveal 一个 folder 下所有 key；
- `/secret/search` 在 project 维度会聚合整个项目的所有 (folder, key) 组合返
  group 列表，**无法精确指定某一个 key 在某几个 env 下的值**。

实际场景：前端做"配置对比"页时，需要针对某一个 key，展示「dev / test / sim /
prod 下对应的值分别是多少」。这种"按 (project, folderCode, key) 维度精确查若干 env"
的场景，现有接口要么 over-fetch（search 返整个项目全量），要么 under-fetch
（batchReveal 只能看一个 env）。

v12 新增 `POST /api/v1/secrets/list`（与 batchCreate 共用 `secrets` 路由组，
复数）：**精确查 (project, folderCode, key) 在上送 envList 命中 env 下的值**，
跨 env 一次性 reveal。

> 关键扩展：**`keyList` 为可选**。keyList 非空时精确查 (folderCode, keyList 中
> 每个 key) 在 envList 命中 env 下的值；keyList 为空时返回项目下所有
> (folderCode, key) 跨 envList 命中 env 下的值（覆盖「配置对比」整页浏览场景）。

#### 业务语义

- **`keyList` 必填 / 可选二态**：
  - `keyList` 非空 → 精确查 (folderCode, keyList 中每个 key) 跨 envList；
    folderCode 必填；service 端做 trim + 去重 + 空过滤 + `^[A-Z][A-Z0-9_]*$`
    正则校验，长度上限 32；
  - `keyList` 为空 → 列出 (folderCode, key) 跨 envList；folderCode 是**独立的
    过滤维度**：空时返项目下所有 folder，非空时 SQL 直接限定到该 folder。
- **envList 必填**：1..32 项，service 端做 trim + 去重 + 空过滤。
- **未命中 env → JSON null**：上送 envList 中的 env 在该项目下没有 secret
  时，响应里该 env 字段为 `null`（用 `*EnvSecretValue` 指针 + 自定义
  `MarshalJSON` 兜底），前端按固定下标位访问，不会出现字段缺失。
- **无命中时返空数组（key 为空）或 1 元素占位（key 非空）**：保证响应 shape
  一致，前端无需做特判。
- **不走 secret:list 收窄的 SQL 模式**：用两个 repo 方法
  `ListSecretsByProjectFolderKey`（keyList 非空时精确查 keys 数组）/
  `ListSecretsInProjectByEnvs`（keyList 为空，folderCode 独立过滤），
  内部走 v7 cascade narrowing 链
  `(secret → folder → env → project → org)` 收窄；本端点的 action 用
  `secret:read`（v12 起由 `secret:reveal` 放宽为 `secret:read`）。
  权限语义：持有 `secret:read` 即有资格看到 secret 的明文——SQL
  层把无 read 的 secret 直接收窄掉（不出现在 result 里），service
  不再单独校验 `secret:reveal`。这把"是否能看到明文"从细粒度
  reveal 降级为粗粒度 read，符合批量浏览场景（持有 read 即可直
  接看到 plaintext）；单点 `/secret/reveal` 接口仍走 `secret:reveal`
  保持细粒度控制。
- **整批 1 条 audit**（action=`reveal_batch`、resource_type=`project`、
  resource_id=projectId）：有命中才写，与 `BatchRevealByPath` 一致；payload
  区分 keyList 非空（`{folderCode, keys:[...], envList, hits}`）与 keyList 为空
  （`{keys:[], envList, keyCount, totalHits, items:[{folderCode, key}, ...]}`）
  两种形态。

#### 请求

**单 key 精确查**：
```json
{
  "projectId":  "uuid-of-project",
  "folderCode": "ana-svc",
  "keyList":    ["DATABASE_URL"],
  "envList":    ["dev", "test", "sim", "prod"]
}
```

**多 key 精确查**：
```json
{
  "projectId":  "uuid-of-project",
  "folderCode": "ana-svc",
  "keyList":    ["DATABASE_URL", "REDIS_URL", "API_KEY"],
  "envList":    ["dev", "test", "sim", "prod"]
}
```

**全量查（keyList 为空）**：
```json
{
  "projectId":  "uuid-of-project",
  "envList":    ["dev", "test", "sim", "prod"]
}
```

| 字段 | 必填 | 说明 |
| --- | --- | --- |
| `projectId` | 是 | project uuid |
| `folderCode` | keyList 非空时必填；keyList 为空时**也是有效过滤条件**(空字符串 = 跨所有 folder) | folder code（跨 env 稳定标识） |
| `keyList` | 否 | secret key 数组；空时返所有 key；非空时 service 端 trim + 去重 + 空过滤 + `^[A-Z][A-Z0-9_]*$` 正则校验，长度 1 ≤ len ≤ 32 |
| `envList` | 是 | 目标 env code 数组，1 ≤ len ≤ 32，service 端 trim+去重+空过滤 |

#### 响应

**单 key 精确查**（`data` 是 1 元素数组）：
```json
{
  "code": 0,
  "msg": "ok",
  "data": [
    {
      "projectCode": "p1",
      "key":         "DATABASE_URL",
      "comment":     "数据库url(取自第一个命中env)",
      "dev":  { "value": "postgres://...", "version": 1, "comment": "...", "updatedAt": "2024-01-01T00:00:00Z" },
      "test": { "value": "postgres://...", "version": 1, "comment": "...", "updatedAt": "2024-01-02T00:00:00Z" },
      "sim":  null,
      "prod": null
    }
  ]
}
```

**多 key 精确查**（`data` 长度 = cleanedKeys 长度，每条对应一个 key，未命中的 key 用占位元素兜底）：
```json
{
  "code": 0,
  "msg": "ok",
  "data": [
    {
      "projectCode": "p1",
      "key":         "DATABASE_URL",
      "dev":  { "value": "postgres://...", "version": 1, "updatedAt": "..." },
      "test": { "value": "postgres://...", "version": 1, "updatedAt": "..." },
      "sim":  null,
      "prod": null
    },
    {
      "projectCode": "p1",
      "key":         "REDIS_URL",
      "dev":  { "value": "redis://...", "version": 2, "updatedAt": "..." },
      "test": null,
      "sim":  null,
      "prod": null
    },
    {
      "projectCode": "p1",
      "key":         "API_KEY",
      "dev":  null,
      "test": null,
      "sim":  null,
      "prod": null
    }
  ]
}
```

**全量查**（`data` 是 N 元素数组，每条对应一个 (folder, key)）：
```json
{
  "code": 0,
  "msg": "ok",
  "data": [
    {
      "projectCode": "p1",
      "key":         "DATABASE_URL",
      "dev": { "value": "...", "version": 1, "updatedAt": "..." },
      "test": { "value": "...", "version": 1, "updatedAt": "..." },
      "sim": null,
      "prod": null
    },
    {
      "projectCode": "p1",
      "key":         "API_KEY",
      "dev": { "value": "...", "version": 3, "updatedAt": "..." },
      "test": null,
      "sim": null,
      "prod": { "value": "...", "version": 1, "updatedAt": "..." }
    }
  ]
}
```

**无命中**：
- `keyList` 非空：按 cleanedKeys 长度返 N 元素占位数组（顶层 `key` 字段为
  cleanedKeys 中对应 key，env 字段全 `null`）；保证响应 shape 与请求 keyList
  长度一致，前端按固定下标位访问不会出现错位。
- `keyList` 为空：返空数组 `[]`。

> 响应里**不**回显请求参数：`projectId` / `folderCode` 是请求入参，不再放进
> 响应（避免冗余）；`projectCode` 是从 secret 派生出来的标识，保留供前端展示。
> env 字段（dev / test / sim / prod 等）走自定义 `MarshalJSON` 展平到顶层，
> 未命中的 env 序列化为 `null`。

#### 关键设计决策

| 维度 | 决策 | 备注 |
| --- | --- | --- |
| 端点路径 | `/api/v1/secrets/list`（**复数** secrets，与 batchCreate 同路由组） | 与单条 `/secret/*` 区分 |
| keyList 是否必填 | **可选** | keyList 空时返项目下所有 (folder, key) |
| folderCode 语义 | keyList 非空时必填；**keyList 空时也是有效过滤条件**(非空限定到该 folder) | 二态共享同一 SQL 兜底分支 |
| envList 必填 | 是 | 1..32 项；不传 400 |
| 未命中 env | 序列化为 `null` | `*EnvSecretValue` 指针 + 自定义 `MarshalJSON` |
| 响应形态 | 永远是数组 | keyList 长度=1 → 1 元素；keyList 长度=N → N 元素（含占位）；keyList 为空 → 0..N 元素；统一 shape |
| 权限 | `secret:read`（v12 起） | v7 cascade narrowing 自动收窄；持有 read 即可看明文（无 read 在 SQL 层被收窄掉，不出现 env 字段）；单点 `/secret/reveal` 仍走 `secret:reveal` 保持细粒度 |
| Audit | 整批 1 条 | `reveal_batch` / `resource_type=project` / `resource_id=projectId`；无命中不写 |
| 分页 | 无 | 最多命中 envList 长度条，keyList 模式最多 32×32 = 1024 条 |
| Repo SQL | 复用 `ListSecretsByProjectFolderKey`（接收 `keys []string`）+ `ListSecretsInProjectByEnvs(folderCode?)` | 共用 v7 cascade narrowing；folderCode 用 `$5::text = '' or f.code = $5` 兜底；keys 用 `cardinality($5::text[]) > 0 and s.key = any($5::text[])` 过滤 |

#### 不支持的场景

- 跨 project 查询（每条 `projectId` 限定单 project）。
- 分页（keyList 模式最多 32 × 32 = 1024 条，全量模式理论无上限；当前未发现
  需要分页的使用场景，若项目下 (folder, key) 组合过多可后续按 folderCode
  二次收敛）。
- 同 (folder, key) 出现多次的「全量」场景去重（service 端按 (folder, key) 分组，
  每组一个 SecretAcrossEnvs）。

#### 风险与权衡

- **复用两条 SQL**：`ListSecretsByProjectFolderKey`（keyList 非空分支，接收
  `keys []string` 用 `s.key = any($5::text[])` 精确匹配）+
  `ListSecretsInProjectByEnvs`（keyList 为空分支，folderCode 独立过滤）。
  两条 SQL 都复用 v7 cascade narrowing，与 `BatchRevealSecretsByPath` 同链路，
  权限收窄统一。
- **env code 顺序由 Go map 决定**：`SecretAcrossEnvs.Envs` 是 `map[string]*EnvSecretValue`，
  JSON 序列化时字段顺序随机；前端按 key 访问不受影响，但若依赖字段顺序需
  后续改为有序 slice。
- **顶层 comment 取自第一个命中 env**：`topComment = secrets[0].Comment` 语义
  不够严谨（不同 env 的 comment 可能不同），但符合"一份 key 一份 comment"的
  实际场景；若需要"全 env 共享 comment"可后续扩展为 per-env comment 字段。
- **keyList 长度上限 32**：service 端在 trim+去重+空过滤后仍超过 32 → 直接
  返回业务错误；与 envList 上限对齐，保持接口对称。
- **回退**：handler / writer / repo 方法集中在
  `internal/http/controller/resource_secret.go` +
  `internal/service/secret_service.go` +
  `internal/store/postgres/repository.go`，删对应方法 + 路由 + DTO 即可回退。

## 日志与链路追踪

HTTP 请求进入后读取请求头中的 request id，并写回响应头。默认请求头名称为：

```text
x-request-id
```

如果请求未携带 request id，服务端自动生成 UUID。

日志格式：

```text
时间 级别 x-request-id=<request-id> method=<方法名> msg=<信息内容> key=value
```

脱敏规则：

- 字段名包含 `value`、`password`、`secret`、`token`、`cookie`、`jwt` 时，日志值统一打印为 `***`。
- Secret key 可以打印。
- Secret value 不能打印明文。后续代码应避免把 value 传入日志字段，即使 logger 会脱敏。

## HTTP API 规范

统一前缀：

```text
/api/v1
```

接口风格：

- 无请求数据的查询使用 `GET`。
- `GET` 默认不承载 request body，也不承载业务 query 参数。
- 有请求数据的查询或变更使用 `POST`，请求数据统一放在 JSON body 中。
- 分页、过滤条件、资源 ID、搜索关键字都视为请求数据，应使用 `POST`。
- 只有分享链接、跳转链接等天然需要 URL 表达的场景，允许使用 `GET + query params`。
- 请求体使用 JSON。
- 响应体使用统一结构。
- HTTP API 的请求字段和响应字段统一使用 camelCase，例如 `parentId`、`folderId`、`scopeType`、`userId`。
- 数据库表字段、SQL 列名、索引名继续使用 snake_case，不受 HTTP API 字段命名约束影响。

统一响应：

```json
{
  "code": 0,
  "msg": "success",
  "data": {}
}
```

分页请求统一使用 `PageRequest`：

```json
{
  "pageNum": 1,
  "pageSize": 20
}
```

分页请求规则：

- `pageNum`：页码，从 `1` 开始；为空或小于 `1` 时按 `1` 处理。
- `pageSize`：每页数量；为空或小于 `1` 时按 `20` 处理，最大 `100`。
- 所有分页查询接口必须复用 `PageRequest`，业务过滤字段通过组合或嵌入方式扩展。

分页响应统一使用 `PageResp`，放在统一响应的 `data` 中：

```json
{
  "code": 0,
  "msg": "success",
  "data": {
    "pageNum": 1,
    "pageSize": 20,
    "total": 100,
    "list": []
  }
}
```

分页响应规则：

- `total`：符合查询条件的总条数。
- `list`：当前页数据列表。
- **空数据形态**：当 `list` 为空时(本次查询无数据返回),响应退化为
  ```json
  { "total": 0, "list": [] }
  ```
  `pageNum` / `pageSize` 字段**不出现**;服务端在 `pageData` 把这两个 int 字段设为 0
  并由 `omitempty` 省略。这样前端「空数据」分支不用做 "pageNum undefined?" 的防御。
- 非空时(`list` 长度 ≥ 1)仍按 `PageResp` 完整四字段返回
  ```json
  { "pageNum": 1, "pageSize": 20, "total": N, "list": [...] }
  ```
  便于调用方确认服务端归一化后的分页上下文。
- `pageData` 用反射把 nil slice 转换为同类型的空 slice,保证 `json.Marshal` 出 `[]`
  而非 `null`(Go 的 `encoding/json` 对 nil slice 输出 `null`)。
- 不允许再使用 `organizations`、`projects`、`secrets` 等按资源类型命名的列表字段，所有分页列表统一使用 `list`。

创建接口响应规范：

- **单条创建**(产生 1 个实体)直接把实体对象放在 `data` 中，不再用 `created` / `item` 等中间字段包装：

  ```json
  {
    "code": 0,
    "msg": "success",
    "data": {
      "id": "...",
      "code": "...",
      "name": "..."
    }
  }
  ```

- **批量创建**(产生 N 个实体的列表)直接把数组放在 `data` 中，不再用 `created` / `items` / `results` 等中间字段包装；元素顺序与请求里对应的输入列表(如 `envList`)顺序一致：

  ```json
  {
    "code": 0,
    "msg": "success",
    "data": [
      { "id": "...", "code": "...", "name": "..." },
      { "id": "...", "code": "...", "name": "..." }
    ]
  }
  ```

- 历史接口里出现的 `data.created` / `data.items` 等额外包装层一律视为遗留写法，新接口禁止再引入；已有接口在重构窗口中按本规范对齐。
- 该规范只适用于「创建语义」端点(`/create`、`/batchCreate` 等)。其他端点继续遵循各自的契约:`/list` 走 `PageResp`(`data.list`);`/delete` 走 `data.deleted = true`;`/get` / `/update` 直接 `data` 是单实体。
- Go 端实现:`response.OK(c, item)` 传单实体或切片即可,不再用 `gin.H{"created": ...}` 二次包装。客户端 SDK 反序列化时,`data` 字段直接对应实体类型或实体切片。

错误响应：

```json
{
  "code": 1404,
  "msg": "record not found",
  "data": null
}
```

建议错误码：

| HTTP 状态码 | code | 场景 |
| --- | --- | --- |
| 200 | -1 | 通用业务失败，无法明确归类时使用 |
| 400 | 1002 | 请求参数错误 |
| 401 | 1401 | 未认证或 JWT 无效 |
| 403 | 1403 | 无权限 |
| 404 | 1404 | 资源不存在 |
| 409 | 1409 | 唯一约束或业务冲突 |
| 500 | 1500 | 服务端错误 |
| 503 | 1503 | 依赖服务不可用或服务未配置 |

响应体中的 `code` 是业务状态码，不允许直接复用 HTTP 状态码。通用成功使用 `0`，通用失败使用 `-1`，特殊错误使用 `1000` 以上错误码。代码中成功响应优先使用 `response.OK`，需要自定义成功消息时使用 `response.OkWithMsg`；通用失败优先使用 `response.FailWithMsg`，特殊错误使用 `response.Fail` 并传入明确业务码。

## HTTP API 路径设计

### 公共接口

| 方法 | 路径 | 认证 | 说明 |
| --- | --- | --- | --- |
| GET | `/healthz` | 否 | 存活检查 |
| GET | `/api/v1/readyz` | 否 | 就绪检查，包含数据库状态 |
| POST | `/api/v1/auth/dev/token` | 否 | 本地测试 JWT 签发接口，仅在 `auth.dev_token_enabled=true` 时注册 |

测试 JWT 签发请求：

```json
{
  "userId": "00000000-0000-4000-8000-000000000001",
  "name": "Dev User",
  "expiresInSeconds": 3600
}
```

响应：

```json
{
  "code": 0,
  "msg": "success",
  "data": {
    "tokenType": "Bearer",
    "token": "jwt token",
    "expiresAt": "2026-05-31T12:00:00Z"
  }
}
```

### 当前用户接口

| 方法 | 路径 | 认证 | 说明 |
| --- | --- | --- | --- |
| GET | `/api/v1/me` | 是 | 查看当前 JWT 解析出的用户信息 |

### 组织接口

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| POST | `/api/v1/org/list` | 组织列表 |
| POST | `/api/v1/org/create` | 创建组织 |
| POST | `/api/v1/org/info` | 组织详情 |
| POST | `/api/v1/org/update` | 更新组织 |
| POST | `/api/v1/org/delete` | 删除组织 |

组织列表：

```json
{
  "pageNum": 1,
  "pageSize": 20
}
```

创建组织：

```json
{
  "code": "default-org",
  "name": "默认组织",
  "comment": "默认组织"
}
```

详情/删除组织：

```json
{
  "id": "uuid"
}
```

更新组织：

```json
{
  "id": "uuid",
  "name": "new-org-name",
  "comment": "说明"
}
```

### 项目接口

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| POST | `/api/v1/project/list` | 项目列表 |
| POST | `/api/v1/project/create` | 创建项目 |
| POST | `/api/v1/project/info` | 项目详情 |
| POST | `/api/v1/project/update` | 更新项目 |
| POST | `/api/v1/project/delete` | 删除项目 |

项目列表：

```json
{
  "orgId": "uuid",
  "pageNum": 1,
  "pageSize": 20
}
```

创建项目：

```json
{
  "parentId": "org uuid",
  "code": "project-a",
  "name": "项目 A",
  "comment": "项目说明",
  "environments": [
    {"code": "dev",  "name": "Development"},
    {"code": "test", "name": "Testing"},
    {"code": "prod", "name": "Production"}
  ]
}
```

`environments` 为可选字段：

- 不传或传空数组：project 下不会创建任何环境，后续通过 `/api/v1/env/create` 补建。
- 传入 `EnvSpec` 列表：在事务中创建 env，并对每个 code 在 `environment_templates` 中 upsert（已存在则保持首次快照）。`EnvSpec.sortOrder` 可选；不传时按 `dev/test/sim/prod` 规则写入默认排序。
- v3 起不再支持"项目级环境关联修改接口"，因为 env 本身归属 project 已是最终模型。

### 环境接口

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| POST | `/api/v1/env/list` | 环境列表（按 projectId 过滤） |
| POST | `/api/v1/env/create` | 创建环境 |
| POST | `/api/v1/env/info` | 环境详情 |
| POST | `/api/v1/env/update` | 更新环境 |
| POST | `/api/v1/env/delete` | 删除环境 |

环境列表返回顺序由后端保证：`sortOrder asc, createdAt asc`。创建单个环境时不需要传排序字段，服务端按 code 自动写入默认排序；自定义 code 默认排在标准环境之后。
| POST | `/api/v1/env/template/list` | org 层 env 模板列表（只读） |
| POST | `/api/v1/env/template/info` | org 层 env 模板详情（只读） |

环境列表：

```json
{
  "projectId": "uuid",
  "pageNum": 1,
  "pageSize": 20
}
```

创建环境：

```json
{
  "parentId": "project uuid",
  "code": "poc",
  "name": "poc",
  "comment": "自定义环境"
}
```

创建环境成功后自动在该环境下创建默认 Folder `globals` 和 `groups-secrets`，并对 org 层模板执行 `ON CONFLICT DO NOTHING` upsert。

环境模板列表（org 层只读）：

```json
{
  "orgId": "uuid",
  "pageNum": 1,
  "pageSize": 20
}
```

环境模板详情：`{ "id": "..." }` 或 `{ "parentId": "org uuid", "code": "dev" }` 二选一。

### Folder 接口

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| POST | `/api/v1/folder/list` | Folder 列表 |
| POST | `/api/v1/folder/create` | 创建 Folder |
| POST | `/api/v1/folder/info` | Folder 详情 |
| POST | `/api/v1/folder/update` | 更新 Folder |
| POST | `/api/v1/folder/delete` | 删除 Folder |

Folder 列表：

```json
{
  "environmentId": "uuid",
  "pageNum": 1,
  "pageSize": 20
}
```

创建 Folder：

```json
{
  "parentId": "environment uuid",
  "code": "custom-folder",
  "name": "自定义目录",
  "comment": "一级目录"
}
```

### Secret 接口

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| POST | `/api/v1/secret/list` | Secret 列表，不返回明文 value |
| POST | `/api/v1/secret/search` | Secret 搜索，不返回明文 value |
| POST | `/api/v1/secret/create` | 创建 Secret |
| POST | `/api/v1/secret/info` | Secret 详情，不返回明文 value |
| POST | `/api/v1/secret/reveal` | 查看 Secret 明文 value，并记录 reveal 审计 |
| POST | `/api/v1/secret/update` | 更新 Secret |
| POST | `/api/v1/secret/delete` | 删除 Secret |
| POST | `/api/v1/secret/path/info` | 按 4 级 code path 查 secret 详情 |
| POST | `/api/v1/secret/path/reveal` | 按 4 级 code path 看明文 |
| POST | `/api/v1/secret/path/batchReveal` | 按 4 级 code path 批量 reveal（folder 下所有/指定 keys） |
| POST | `/api/v1/secret/code/batchReveal` | 按结构化 code 批量 reveal folder 下所有 secret |
| POST | `/api/v1/secrets/batchCreate` | **复数** secrets 路由组；envList 数组形式批量创建（v11+） |
| POST | `/api/v1/secrets/list` | **复数** secrets 路由组；按 (project, [folderCode], [key]) 跨 envList 一次性 reveal（v12） |

Secret 列表：

```json
{
  "orgId": "uuid",
  "projectId": "uuid",
  "environmentId": "uuid",
  "folderId": "uuid"
}
```

创建 Secret：

```json
{
  "folderId": "uuid",
  "key": "DATABASE_URL",
  "value": "postgres://...",
  "comment": "数据库连接串"
}
```

更新 Secret：

```json
{
  "id": "uuid",
  "key": "DATABASE_URL",
  "value": "postgres://...",
  "comment": "数据库连接串"
}
```

搜索 Secret：

```json
{
  "orgId": "uuid",
  "projectId": "uuid",
  "environmentId": "uuid",
  "folderId": "uuid",
  "keyword": "DATABASE"
}
```

查看 Secret 明文：

```json
{
  "id": "uuid"
}
```

说明：

- `/secret/info` 和 `/secret/list` 默认不返回明文 value。
- `/secret/reveal` 需要单独权限 `secret:reveal`，并记录审计。

### 审计接口

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| POST | `/api/v1/audit/list` | 查询审计记录 |

查询审计记录：

```json
{
  "resourceType": "secret",
  "resourceId": "uuid"
}
```

说明：

- `resourceType` 和 `resourceId` 可以为空。
- 接入 RBAC 后，空条件查询必须按用户权限范围过滤。

### 历史数据接口建议

当前尚未实现，后续建议：

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| POST | `/api/v1/deleted/list` | 查询删除记录 |
| POST | `/api/v1/deleted/info` | 查询删除快照详情 |
| POST | `/api/v1/secret/version/list` | 查询 Secret 版本列表 |
| POST | `/api/v1/secret/version/info` | 查询 Secret 指定版本元数据 |
| POST | `/api/v1/secret/version/reveal` | 查看 Secret 历史版本明文 |
| POST | `/api/v1/secret/version/rollback` | 回滚 Secret 到指定版本 |

删除记录查询：

```json
{
  "resourceType": "secret",
  "resourceId": "uuid"
}
```

Secret 版本列表：

```json
{
  "secretId": "uuid"
}
```

Secret 版本回滚：

```json
{
  "secretId": "uuid",
  "version": 3
}
```

## 本地运行

启动 PostgreSQL 和 Redis：

```bash
docker compose up -d postgres redis
```

初始化表结构：

```bash
psql "postgres://admin:123456@127.0.0.1:5432/envvault?sslmode=disable" -f configs/schema.sql
```

启动应用：

```bash
go run .
```

健康检查：

```bash
curl http://localhost:8080/healthz
curl http://localhost:8080/api/v1/readyz
```

## 测试建议

重点覆盖：

- AES-GCM 加密和解密往返测试。
- JWT 中间件行为。
- RBAC 权限允许和拒绝。
- 组织、项目、环境、Folder 作用域隔离。
- Secret 创建、更新、删除时只落密文。
- 删除快照写入。
- 审计记录写入。
- Secret 版本历史写入和回滚。
- Redis 缓存同步。
- 搜索结果正确性和授权过滤。
- 并发搜索和 context 取消。

## 当前待完善事项

- 当前 controller 仍直接调用 repository，后续应将加密、授权、审计、缓存同步收敛到 service 层。
- 当前删除上级资源时未定义是否级联删除子资源，需要补业务规则。
- 当前 `audit_records` 只保存基础字段，后续建议补充 request id、metadata 和 diff。
- 当前 `secret_versions` 尚未实现，主表只保留最新 version 字段，缺少历史回滚能力。
- 当前 `env_template:read` 权限码已注册但 RBAC authorizer 尚未接到 env/template 控制器，后续接入。
- 当前 value 搜索尚未实现，详见 [search.md](search.md)。
- 当前 tokens_valid_after 缓存为进程内,多实例部署下跨进程传播最迟 1min,极端场景(用户改密后立即跨进程访问)有窗口;后续可引入 Redis 共享层。

## 认证 & 用户（v9）

### 目标

envVault 之前没有用户管理入口。`users` 表只由三条隐式路径写入:
- `EnsureBootstrapAdmin`(通过 `ENVVAULT_BOOTSTRAP_ADMIN_USER_ID` 环境变量拉起第一个 admin)
- `SyncUser`(JWT 用户首次调 `/rbac/user/me` 时 lazy 创建)
- `GrantRole`(管理员授权时若 user 不存在则 upsert)

前端无注册页,运维只能手工 bootstrap。本轮加 4 个端点,提供完整自注册 / 登录 / 强制登出 / 改密能力。

### 端点

| 路径 | 方法 | 鉴权 | 说明 |
| --- | --- | --- | --- |
| `/api/v1/auth/register` | POST | 匿名 | 自助注册;**首用户自动 platform_admin(global)** |
| `/api/v1/auth/login` | POST | 匿名 | 邮箱 + 密码登录;**统一 `bad credentials`**(防 user enumeration) |
| `/api/v1/auth/logout` | POST | JWT | 强制登出;旧 token 立即失效(本进程)+ 1min 内全集群同步 |
| `/api/v1/auth/changePassword` | POST | JWT | 改密;成功后旧 token 全部失效 |

### 用户身份模型

- 统一使用 `users.id`(数据库 UUID)作为「JWT subject / JWT userId」和「RBAC authorizer 入口」。`user_role_bindings.user_id` 也只绑定这个 UUID。
- `users.external_user_id` 暂时保留为兼容字段,不参与 RBAC 授权判断。通过 JWT 自动同步用户时,如果只传入 `userId`,服务端会把 `external_user_id` 同步为同一个 UUID,满足当前表结构的非空/唯一约束。
- 自注册用户仍会生成 `users.id` UUID;JWT 返回和后续授权都使用这个 UUID。`external_user_id = "email:<email>"` 仅作为兼容标识保留,不作为授权主键。
- `users` 表新增 3 列:`password_hash` / `password_algo` / `tokens_valid_after`(默认 `epoch`)。
- `email` 加 partial unique index:`where email <> ''`(JWT 占位 user email 为空,不影响唯一约束)。

### 密码

- 算法:**argon2id**,参数 `m=64 MiB` / `t=3` / `p=2`(OWASP 2023 推荐),salt 16 byte,key 32 byte。
- 输出 PHC 字符串:`$argon2id$v=19$m=65536,t=3,p=2$<salt-b64>$<hash-b64>`,复用 `golang.org/x/crypto/argon2`。
- 最小长度:12 字符(可配 `auth.password_min_length`)。无复杂度要求(OWASP 2023 已撤销复杂度建议)。
- 校验走 `crypto/subtle.ConstantTimeCompare`,防 timing attack。

### 强制登出策略

JWT 本身无状态,服务端无法直接 revoke。本轮用 **「JWT 嵌入 + 进程内缓存比对」** 双层机制:

1. **DB 侧**:`users.tokens_valid_after` 列,默认 `epoch`(永不拒绝);`Logout` / `ChangePassword` 流程 `UPDATE ... = NOW()`。
2. **JWT 侧**:不需要额外 claim。middleware 用 `claims.IssuedAt`(标准 iat)即可。
3. **校验**:`cache.Get(userId)` 拿到 `tokens_valid_after`,若 `iat < tokens_valid_after` → 401 `ErrTokenRevoked`。
4. **进程内缓存**:`internal/auth.TokensCache`,key=userId,value=time;全量 loader + per-user loader,后台 goroutine 周期灌(默认 1min);Logout / ChangePassword 主动 Set,让本进程下一次请求立即生效。
5. **跨实例**:各实例独立维护 cache,极端情况(用户在 A 实例 logout,B 实例还有 1min 旧 cache)最迟 1min 内通过 refresher 同步。
6. **可接受 trade-off**:v9 锁定 web 单设备场景(用户决策),无多设备登出需求。若以后跨设备,需引入 Redis 共享层,改 `TokensCache` 后端实现。

### 登录频控

- Redis sliding window(sorted set)+ hard lockout key。
- 参数(可配):5 次/1min/IP,触发后 15min lockout。
- 实现位于 `internal/auth/ratelimit`,接口 `Limiter { Check, Record }`;fake 实现便于单测,生产用 `NewRedisLimiter(client, opts)` 包装 go-redis。
- Check:先 EXISTS lockout key;再 ZCARD 失败计数;超阈值时 SetNX lockout(防止并发竞争)。
- Record:成功 → DEL failures + lockout;失败 → ZADD 唯一 member(`<ts-ns>-<rand-hex>`,防同 ms 覆盖)+ ZREMRANGEBYSCORE 清过期。

### 业务码

| 错误 | HTTP | 业务码 | 说明 |
| --- | --- | --- | --- |
| `service.ErrInvalidArgument` | 400 | 1002 | 邮箱格式错 / 密码短 / name 空 |
| `service.ErrEmailAlreadyExists` | 409 | 1409 | email 已被注册 |
| `service.ErrBadCredentials` | 401 | 1401 | 邮箱不存在 / 密码错 / 用户被禁用 |
| `ratelimit.ErrRateLimited` | 429 | 1429 | IP 频控锁 |

`response.CodeRateLimited = 1429` 为本轮新增。

### 配置(`config.AuthConfig`)

| 字段 | 默认 | 说明 |
| --- | --- | --- |
| `enabled` | true | JWT 中间件是否启用 |
| `public_key` | — | JWT 验签公钥(PEM,RSA/ECDSA/Ed25519) |
| `private_key` | — | **本轮新增**:生产用私钥;Register / Login 签 token |
| `register_enabled` | true | 关闭后 `/auth/register` 返 403 |
| `password_min_length` | 12 | 密码最小长度 |
| `login_rate_limit` | 5 | 窗口内允许的失败次数 |
| `login_rate_limit_window` | 1min | 频控窗口 |
| `lockout_duration` | 15min | 触发后封禁时长 |
| `tokens_cache_refresh` | 1min | 后台灌全量周期 |
| `token_ttl` | 24h | JWT 有效期 |

### 关键文件

| 文件 | 改动 |
| --- | --- |
| `configs/schema.sql` | users +3 列,login_attempts 表,email partial unique |
| `internal/domain/rbac.go` | User +3 字段 |
| `internal/store/store.go` | AuthRepository 接口 |
| `internal/store/postgres/auth.go` | AuthStore 实现 |
| `internal/auth/password.go` | argon2id Hasher |
| `internal/auth/ratelimit/ratelimit.go` | Redis sliding window Limiter |
| `internal/auth/tokens_cache.go` | 进程内 cache + Refresher |
| `internal/auth/jwt.go` | Claims + IsRevokedBy + JWTRegisteredClaimsAt + middleware 接入 cache |
| `internal/service/auth_service.go` | AuthService 业务编排 |
| `internal/http/controller/auth.go` | 4 个 handler + 错误码映射 |
| `internal/http/router.go` | 4 个端点(2 匿名 + 2 JWT) |
| `internal/app/app.go` | AuthStore / AuthService / TokensCache / 后台 Refresher / 真实 ratelimit 装配 |
| `internal/http/response/response.go` | +CodeRateLimited = 1429 |
| `internal/config/config.go` | AuthConfig +8 字段 |
| `internal/store/redis/cache.go` | Client() accessor(给 ratelimit 复用) |
