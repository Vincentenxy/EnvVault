# EnvVault 设计文档

## 背景

EnvVault 是一个类似 Infisical 的轻量级、支持私有化部署的密钥管理平台。系统通过 HTTP API 对外提供组织、项目、环境、Folder 和 Secret 的管理能力，并围绕密钥存储安全、权限控制、搜索、审计和历史追溯展开设计。

核心产品层级：

```text
organization
  project
    environment
      folder
        secret key:value
```

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
- `ENVVAULT_AUTH_DEV_USER_ID`：关闭认证时注入的开发用户 ID，默认 `dev-user`。
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

项目属于一个组织。创建项目时自动创建默认环境和默认 Folder。

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

环境属于一个项目。默认环境为 `dev`、`test`、`sim`、`prod`，同时支持自定义环境。

核心字段：

- `id`
- `project_id`
- `code`
- `name`
- `comment`
- 删除元数据
- 创建和更新时间

约束：

- 同一个项目下未删除环境的 `code` 唯一。
- `name` 是展示名称，不参与唯一约束。

### Folder

Folder 属于一个环境，当前只支持一级目录。默认 Folder 为 `globals` 和 `groups-secrets`。

核心字段：

- `id`
- `environment_id`
- `code`
- `name`
- `comment`
- 删除元数据
- 创建和更新时间

约束：

- 同一个环境下未删除 Folder 的 `code` 唯一。
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
    is_deleted boolean not null default false,
    deleted_at timestamptz,
    deleted_by text not null default '',
    created_by text not null default '',
    updated_by text not null default '',
    created_at timestamptz not null default now(),
    updated_at timestamptz not null default now()
);

create unique index if not exists environments_project_code_active_uidx
    on environments (project_id, code)
    where is_deleted = false;
```

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
- `Authorizer` 接口已预留。
- 默认实现为 `AllowAllAuthorizer`，即全部放行。

后续 RBAC 以 [rbac_degisn.md](rbac_degisn.md) 为准。

关键原则：

- JWT 只证明用户身份，不直接代表权限。
- 授权检查应靠近 service 操作本身。
- 列表、搜索、审计查询必须按权限过滤。
- `secret:read` 和 `secret:reveal` 必须拆开。

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
- HTTP API 的请求字段和响应字段统一使用 camelCase，例如 `parentId`、`folderId`、`scopeType`、`externalUserId`。
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
- 分页响应必须返回 `pageNum`、`pageSize`，便于调用方确认服务端归一化后的分页上下文。
- 不允许再使用 `organizations`、`projects`、`secrets` 等按资源类型命名的列表字段，所有分页列表统一使用 `list`。

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
  "userId": "dev-user",
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
  "comment": "项目说明"
}
```

创建项目成功后自动创建默认环境和默认 Folder。

### 环境接口

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| POST | `/api/v1/env/list` | 环境列表 |
| POST | `/api/v1/env/create` | 创建环境 |
| POST | `/api/v1/env/info` | 环境详情 |
| POST | `/api/v1/env/update` | 更新环境 |
| POST | `/api/v1/env/delete` | 删除环境 |

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

创建环境成功后自动创建默认 Folder。

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
- 当前 `secret_versions` 尚未实现。
- 当前 RBAC 尚未正式启用，仍是 `AllowAllAuthorizer`。
- 当前 value 搜索尚未实现，详见 [search.md](search.md)。
