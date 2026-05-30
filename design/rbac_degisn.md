# EnvVault RBAC 设计

## 背景

EnvVault 当前只完成 JWT 认证入口，`internal/auth.Authorizer` 仍是 `AllowAllAuthorizer`。后续需要在认证之后增加标准 RBAC 授权能力，确保组织、项目、环境、Folder、Secret、审计记录和搜索接口都不会跨作用域泄露数据。

本设计只覆盖授权模型、数据库表和 HTTP API，不包含具体代码实现。待方案确认后再进入开发。

RBAC OpenAPI 接口定义放在：[api/rbac.yaml](api/rbac.yaml)。

## 设计目标

- 遵循标准 RBAC：用户、角色、权限点、角色权限绑定、用户角色绑定。
- 支持资源作用域：全局、组织、项目、环境、Folder。
- 支持权限继承：上层作用域角色默认作用于下层资源。
- 认证与授权解耦：JWT 只证明用户身份，RBAC 负责判断能否操作资源。
- service 层必须执行授权检查，不能只依赖路由层。
- 列表、搜索、审计查询需要按权限范围过滤结果。
- 默认角色足够覆盖早期使用场景，同时允许后续扩展自定义角色。
- 不在权限日志、审计元数据中记录 JWT、密钥明文、密文值或加密主密钥。

## 核心概念

### 用户

EnvVault 不负责登录和密码管理，用户来源于外部 JWT。服务端首次看到合法 JWT 用户时，可以把用户映射到本地 `users` 表。

用户唯一标识使用 JWT 中的 `userId`，显示名使用 JWT 中的 `name`。

### 权限点

权限点是最小授权单元，格式为：

```text
<resource>:<action>
```

示例：

```text
secret:read
secret:create
secret:update
secret:delete
project:manage
audit:read
```

权限点不直接分配给用户，只绑定到角色。

### 角色

角色是一组权限点的集合。

角色分两类：

- 系统角色：内置角色，固定 code，不允许删除。
- 自定义角色：后续扩展能力，允许在组织或项目内创建。

第一阶段建议只实现系统角色，自定义角色可以预留表结构。

### 作用域

用户角色绑定必须指定作用域。作用域决定角色在哪里生效。

支持的作用域：

| scope_type | scope_id | 说明 |
| --- | --- | --- |
| `global` | 空字符串 | 全局作用域，平台级管理 |
| `organization` | organization id | 组织内生效 |
| `project` | project id | 项目内生效 |
| `environment` | environment id | 环境内生效 |
| `folder` | folder id | Folder 内生效 |

Secret 不作为角色绑定作用域。Secret 权限通过其所在 Folder、Environment、Project、Organization 继承获得。

## 作用域继承规则

资源层级为：

```text
global
  organization
    project
      environment
        folder
          secret
```

判断某个资源的权限时，需要解析出该资源的完整祖先链。例如一个 Secret 的祖先链为：

```text
global
organization:<org_id>
project:<project_id>
environment:<environment_id>
folder:<folder_id>
secret:<secret_id>
```

用户只要在资源自身或任意祖先作用域拥有包含目标权限点的角色，即视为允许。

示例：

- 用户在 `organization:A` 上拥有 `org_admin`，则可以管理组织 A 下的项目、环境、Folder 和 Secret。
- 用户在 `project:B` 上拥有 `project_developer`，则只能操作项目 B 下的环境、Folder 和 Secret。
- 用户在 `folder:C` 上拥有 `folder_viewer`，则只能查看 Folder C 下的 Secret 列表和详情。

## 权限叠加与有效权限

用户可以在多个作用域拥有多个角色。权限计算采用“允许权限叠加”的模型：

- 同一个用户可以是多个组织的管理员。
- 同一个用户可以是某几个项目的管理员。
- 同一个用户可以在组织 A 是 `org_admin`，在组织 B 只是 `org_viewer`。
- 同一个用户可以在项目 P1 是 `project_admin`，在项目 P2 是 `project_viewer`。
- 如果用户已经是某项目的管理员，又被授予该项目所属组织的 `org_admin`，则该用户在该组织下获得更大的有效权限。

权限只做 allow 叠加，第一阶段不设计 deny 权限。也就是说，只要用户在目标资源自身或任意祖先作用域拥有包含目标权限点的角色，就允许访问。系统不会因为用户在某个下级作用域是 viewer，就抵消上级作用域的 admin 权限。

示例：

```text
user-1
  project_admin on project:P1
  org_admin on organization:O1
```

如果 `P1` 属于 `O1`，则 `project_admin on project:P1` 只是一个更小范围的授权；`org_admin on organization:O1` 会让 `user-1` 对组织 O1 下所有项目获得组织管理员权限。

如果业务希望某人只管理某一个或某几个项目，不应授予 `org_admin`，而应为这些项目分别写入 `project_admin` 绑定。

当前用户被授予了什么权限，由 `user_role_bindings` 记录“直接授权”，由 `roles` 和 `role_permissions` 计算“有效权限”。

直接授权示例：

```text
user-1 -> project_admin -> project:P1
user-1 -> project_admin -> project:P2
user-1 -> org_viewer -> organization:O2
```

有效权限查询时，需要把这些直接授权展开为：

- 用户在哪些作用域有角色。
- 每个角色包含哪些权限点。
- 上级作用域权限可以作用到哪些下级资源。

因此接口上需要同时提供两类查询：

- 授权清单：这个用户被直接加了哪些角色。
- 有效权限：这个用户在某个资源或作用域上最终拥有哪些权限点。

## 默认角色

### 平台角色

| 角色 code | 作用域 | 说明 |
| --- | --- | --- |
| `platform_admin` | `global` | 平台管理员，可管理所有资源和 RBAC |

### 组织角色

| 角色 code | 作用域 | 说明 |
| --- | --- | --- |
| `org_owner` | `organization` | 组织所有者，可管理组织、成员、角色和所有下级资源 |
| `org_admin` | `organization` | 组织管理员，可管理组织下项目、环境、Folder、Secret |
| `org_viewer` | `organization` | 组织只读成员，可查看组织下资源元数据和 Secret key |
| `org_auditor` | `organization` | 审计员，可查看组织内审计记录和资源元数据 |

### 项目角色

| 角色 code | 作用域 | 说明 |
| --- | --- | --- |
| `project_admin` | `project` | 项目管理员，可管理项目下环境、Folder、Secret 和项目成员 |
| `project_developer` | `project` | 开发者，可创建、更新、查看 Secret |
| `project_viewer` | `project` | 项目只读成员，可查看项目下资源元数据和 Secret key |
| `project_auditor` | `project` | 项目审计员，可查看项目内审计记录 |

### Folder 角色

| 角色 code | 作用域 | 说明 |
| --- | --- | --- |
| `folder_admin` | `folder` | Folder 管理员，可管理该 Folder 下 Secret |
| `folder_editor` | `folder` | 可创建、更新、查看该 Folder 下 Secret |
| `folder_viewer` | `folder` | 只读查看该 Folder 下 Secret key |

## 权限点设计

### 资源管理权限

| 权限点 | 说明 |
| --- | --- |
| `org:create` | 创建组织 |
| `org:read` | 查看组织 |
| `org:update` | 更新组织 |
| `org:delete` | 删除组织 |
| `project:create` | 创建项目 |
| `project:read` | 查看项目 |
| `project:update` | 更新项目 |
| `project:delete` | 删除项目 |
| `env:create` | 创建环境 |
| `env:read` | 查看环境 |
| `env:update` | 更新环境 |
| `env:delete` | 删除环境 |
| `folder:create` | 创建 Folder |
| `folder:read` | 查看 Folder |
| `folder:update` | 更新 Folder |
| `folder:delete` | 删除 Folder |

### Secret 权限

| 权限点 | 说明 |
| --- | --- |
| `secret:list` | 查看 Secret 列表，不返回明文 value |
| `secret:search` | 搜索 Secret，不返回明文 value |
| `secret:read` | 查看 Secret 元数据，不返回明文 value |
| `secret:reveal` | 查看 Secret 明文 value |
| `secret:create` | 创建 Secret |
| `secret:update` | 更新 Secret |
| `secret:delete` | 删除 Secret |

说明：

- `secret:read` 和 `secret:reveal` 必须拆开。列表、搜索和普通详情默认不返回明文。
- 当前代码的 `/secret/info` 只返回元数据，后续如果提供明文查看，应新增 `/secret/reveal`，并单独检查 `secret:reveal`。

### 审计与权限管理权限

| 权限点 | 说明 |
| --- | --- |
| `audit:read` | 查看审计记录 |
| `rbac:role:read` | 查看角色 |
| `rbac:role:manage` | 创建、更新、删除自定义角色 |
| `rbac:binding:read` | 查看成员角色绑定 |
| `rbac:binding:manage` | 授予或撤销成员角色 |

## 默认角色权限矩阵

| 角色 | 主要权限 |
| --- | --- |
| `platform_admin` | 全部权限 |
| `org_owner` | 组织内全部权限，包含 `rbac:*` 和 `audit:read` |
| `org_admin` | 组织内资源管理和 Secret 管理，不包含组织删除和 RBAC 角色管理 |
| `org_viewer` | `org:read`、`project:read`、`env:read`、`folder:read`、`secret:list`、`secret:search`、`secret:read` |
| `org_auditor` | 组织内资源只读、`audit:read` |
| `project_admin` | 项目内环境、Folder、Secret 管理，项目内成员绑定管理 |
| `project_developer` | 项目内 `secret:list`、`secret:search`、`secret:read`、`secret:reveal`、`secret:create`、`secret:update` |
| `project_viewer` | 项目内资源只读、Secret 元数据只读 |
| `project_auditor` | 项目内资源只读、`audit:read` |
| `folder_admin` | Folder 内 Secret 全部管理 |
| `folder_editor` | Folder 内 Secret 查看、创建、更新 |
| `folder_viewer` | Folder 内 Secret 元数据只读 |

第一阶段建议 `secret:reveal` 只分配给 `org_owner`、`org_admin`、`project_admin`、`project_developer`、`folder_admin`、`folder_editor`。审计员和 viewer 不允许查看明文。

## 授权判断流程

### 单资源操作

输入：

```text
user_id
permission code
resource_type
resource_id
```

流程：

1. 从 JWT 中解析 `userId`。
2. 将 JWT 用户同步到 `users` 表。
3. 根据 `resource_type` 和 `resource_id` 查询资源祖先链。
4. 查询用户在祖先链上拥有的 active role bindings。
5. 查询这些角色包含的 active permissions。
6. 如果包含目标 permission，则允许。
7. 否则返回 403。

### 创建资源操作

创建资源时目标资源还不存在，应检查父级资源权限。

| 接口 | 检查权限 | 检查作用域 |
| --- | --- | --- |
| 创建组织 | `org:create` | `global` |
| 创建项目 | `project:create` | parent organization |
| 创建环境 | `env:create` | parent project |
| 创建 Folder | `folder:create` | parent environment |
| 创建 Secret | `secret:create` | parent folder |

### 列表与搜索

列表和搜索不能先查全部再在内存中过滤。应优先把可见作用域下推到 SQL 或缓存查询条件中。

第一阶段建议：

- 用户显式传入 `org_id`、`project_id`、`environment_id`、`folder_id` 时，先检查对应作用域的 `read/list/search` 权限。
- 如果查询条件为空，必须根据用户可见组织或项目范围生成过滤条件。
- Redis 搜索缓存也必须接收授权后的 scope filter，不能返回未授权数据。

## 数据库表设计

以下为建议新增表。命名保持复数表名，与当前 `organizations`、`projects` 风格一致。

### users

保存外部 JWT 用户在 EnvVault 内的映射。

```sql
create table if not exists users (
    id uuid primary key,
    external_user_id text not null,
    name text not null default '',
    email text not null default '',
    source text not null default 'jwt',
    is_disabled boolean not null default false,
    last_seen_at timestamptz,
    created_at timestamptz not null default now(),
    updated_at timestamptz not null default now()
);

create unique index if not exists users_external_user_id_uidx
    on users (external_user_id);
```

### permissions

保存权限点定义。

```sql
create table if not exists permissions (
    id uuid primary key,
    code text not null,
    resource_type text not null,
    action text not null,
    description text not null default '',
    is_system boolean not null default true,
    created_at timestamptz not null default now(),
    updated_at timestamptz not null default now()
);

create unique index if not exists permissions_code_uidx
    on permissions (code);
```

### roles

保存角色定义。

```sql
create table if not exists roles (
    id uuid primary key,
    code text not null,
    name text not null,
    description text not null default '',
    scope_type text not null,
    org_id uuid references organizations(id),
    project_id uuid references projects(id),
    is_system boolean not null default false,
    is_deleted boolean not null default false,
    deleted_at timestamptz,
    deleted_by text not null default '',
    created_by text not null default '',
    created_at timestamptz not null default now(),
    updated_at timestamptz not null default now(),
    constraint roles_scope_type_chk check (
        scope_type in ('global', 'organization', 'project', 'environment', 'folder')
    )
);

create unique index if not exists roles_system_code_uidx
    on roles (code)
    where is_system = true and is_deleted = false;

create unique index if not exists roles_org_code_uidx
    on roles (org_id, code)
    where org_id is not null and is_deleted = false;

create unique index if not exists roles_project_code_uidx
    on roles (project_id, code)
    where project_id is not null and is_deleted = false;
```

说明：

- 系统角色 `is_system = true`，`code` 全局唯一。
- 自定义角色可以限制在组织或项目内，第一阶段可不开放创建。

### role_permissions

保存角色和权限点的绑定。

```sql
create table if not exists role_permissions (
    role_id uuid not null references roles(id),
    permission_id uuid not null references permissions(id),
    created_at timestamptz not null default now(),
    primary key (role_id, permission_id)
);

create index if not exists role_permissions_permission_id_idx
    on role_permissions (permission_id);
```

### user_role_bindings

保存用户在某个作用域下拥有的角色。

```sql
create table if not exists user_role_bindings (
    id uuid primary key,
    user_id uuid not null references users(id),
    role_id uuid not null references roles(id),
    scope_type text not null,
    scope_id uuid,
    granted_by text not null default '',
    expires_at timestamptz,
    is_deleted boolean not null default false,
    deleted_at timestamptz,
    deleted_by text not null default '',
    created_at timestamptz not null default now(),
    updated_at timestamptz not null default now(),
    constraint user_role_bindings_scope_type_chk check (
        scope_type in ('global', 'organization', 'project', 'environment', 'folder')
    ),
    constraint user_role_bindings_scope_id_chk check (
        (scope_type = 'global' and scope_id is null)
        or (scope_type <> 'global' and scope_id is not null)
    )
);

create unique index if not exists user_role_bindings_active_uidx
    on user_role_bindings (user_id, role_id, scope_type, scope_id)
    where is_deleted = false;

create index if not exists user_role_bindings_user_scope_idx
    on user_role_bindings (user_id, scope_type, scope_id)
    where is_deleted = false;

create index if not exists user_role_bindings_scope_idx
    on user_role_bindings (scope_type, scope_id)
    where is_deleted = false;
```

说明：

- 撤销角色使用逻辑删除，方便审计。
- `expires_at` 预留临时授权能力。

### role_binding_audit_records

RBAC 变更应独立审计，避免和 Secret 变更记录混在一起难以查询。

```sql
create table if not exists role_binding_audit_records (
    id uuid primary key,
    actor text not null default '',
    action text not null,
    target_user_id uuid references users(id),
    role_id uuid references roles(id),
    scope_type text not null,
    scope_id uuid,
    snapshot jsonb,
    created_at timestamptz not null default now()
);

create index if not exists role_binding_audit_records_target_idx
    on role_binding_audit_records (target_user_id, created_at desc);

create index if not exists role_binding_audit_records_scope_idx
    on role_binding_audit_records (scope_type, scope_id, created_at desc);
```

## 初始化数据

系统启动或迁移时应写入内置权限和角色。

初始化必须幂等：

- `permissions.code` 使用 upsert。
- 系统角色 `roles.code` 使用 upsert。
- `role_permissions` 使用 `on conflict do nothing`。

首个管理员可以通过环境变量指定：

```text
ENVVAULT_BOOTSTRAP_ADMIN_USER_ID
```

如果该变量存在，应用启动时为该用户创建本地用户记录，并授予 `platform_admin` 的 `global` 绑定。生产环境完成初始化后应移除该变量，避免误授权。

## HTTP API 设计

接口保持当前动作式风格：无参数查询使用 `GET`，有参数请求使用 `POST`。

列表查询统一支持分页：

- `pageNum`：页码，从 `1` 开始，默认 `1`。
- `pageSize`：每页数量，默认 `20`，最大 `100`。
- GET 列表接口使用 query string，例如 `/api/v1/rbac/permission/list?pageNum=1&pageSize=20`。
- POST 列表接口将 `pageNum`、`pageSize` 放在 JSON body 中。
- 响应中统一返回分页元信息：`pageNum`、`pageSize`、`total`。

### 权限查询

| 方法 | 路径 | 权限 | 说明 |
| --- | --- | --- | --- |
| GET | `/api/v1/rbac/permission/list` | 已认证 | 查看系统权限点 |
| POST | `/api/v1/rbac/me/permissions` | 已认证 | 查看当前用户在某作用域下拥有的权限 |

查询当前用户权限：

```json
{
  "scope_type": "project",
  "scope_id": "uuid"
}
```

响应：

```json
{
  "code": 0,
  "msg": "success",
  "data": {
    "permissions": [
      "project:read",
      "secret:list",
      "secret:read"
    ]
  }
}
```

### 角色接口

| 方法 | 路径 | 权限 | 说明 |
| --- | --- | --- | --- |
| POST | `/api/v1/rbac/role/list` | `rbac:role:read` | 查看角色列表 |
| POST | `/api/v1/rbac/role/info` | `rbac:role:read` | 查看角色详情 |
| POST | `/api/v1/rbac/role/create` | `rbac:role:manage` | 创建自定义角色 |
| POST | `/api/v1/rbac/role/update` | `rbac:role:manage` | 更新自定义角色 |
| POST | `/api/v1/rbac/role/delete` | `rbac:role:manage` | 删除自定义角色 |

角色列表：

```json
{
  "scope_type": "organization",
  "scope_id": "uuid",
  "pageNum": 1,
  "pageSize": 20
}
```

创建角色：

```json
{
  "scope_type": "organization",
  "scope_id": "uuid",
  "code": "release_operator",
  "name": "Release Operator",
  "description": "发布人员",
  "permissions": [
    "secret:list",
    "secret:read",
    "secret:reveal"
  ]
}
```

限制：

- 系统角色不允许通过 API 更新或删除。
- 自定义角色的权限点不能超出操作者在该作用域拥有的权限集合。

### 用户角色绑定接口

| 方法 | 路径 | 权限 | 说明 |
| --- | --- | --- | --- |
| POST | `/api/v1/rbac/binding/list` | `rbac:binding:read` | 查看某作用域成员角色绑定 |
| POST | `/api/v1/rbac/binding/grant` | `rbac:binding:manage` | 授予用户角色 |
| POST | `/api/v1/rbac/binding/revoke` | `rbac:binding:manage` | 撤销用户角色 |

绑定列表：

```json
{
  "scope_type": "project",
  "scope_id": "uuid",
  "pageNum": 1,
  "pageSize": 20
}
```

授权：

```json
{
  "external_user_id": "user-123",
  "name": "Alice",
  "role_code": "project_developer",
  "scope_type": "project",
  "scope_id": "uuid",
  "expires_at": ""
}
```

撤销：

```json
{
  "external_user_id": "user-123",
  "role_code": "project_developer",
  "scope_type": "project",
  "scope_id": "uuid"
}
```

限制：

- 非 `platform_admin` 不允许授予 `platform_admin`。
- 操作者不能授予自己不拥有的权限集合。
- 最后一个 `org_owner` 不允许被撤销，避免组织无人管理。
- 角色绑定变更必须写入审计记录。

### 用户接口

| 方法 | 路径 | 权限 | 说明 |
| --- | --- | --- | --- |
| GET | `/api/v1/rbac/user/me` | 已认证 | 查看当前本地用户和角色摘要 |
| POST | `/api/v1/rbac/user/list` | `rbac:binding:read` | 查看某作用域可见成员 |
| POST | `/api/v1/rbac/user/grants` | `rbac:binding:read` | 查看某用户被直接授予的角色清单 |
| POST | `/api/v1/rbac/user/permissions` | `rbac:binding:read` | 查看某用户在某作用域下的有效权限 |

用户列表：

```json
{
  "scope_type": "organization",
  "scope_id": "uuid",
  "pageNum": 1,
  "pageSize": 20
}
```

用户直接授权清单：

```json
{
  "external_user_id": "user-123",
  "pageNum": 1,
  "pageSize": 20
}
```

响应示例：

```json
{
  "code": 0,
  "msg": "success",
  "data": {
    "grants": [
      {
        "role_code": "project_admin",
        "scope_type": "project",
        "scope_id": "project uuid",
        "granted_by": "admin-user",
        "expires_at": ""
      },
      {
        "role_code": "org_viewer",
        "scope_type": "organization",
        "scope_id": "organization uuid",
        "granted_by": "admin-user",
        "expires_at": ""
      }
    ],
    "pageNum": 1,
    "pageSize": 20,
    "total": 2
  }
}
```

用户有效权限查询：

```json
{
  "external_user_id": "user-123",
  "scope_type": "project",
  "scope_id": "project uuid"
}
```

响应示例：

```json
{
  "code": 0,
  "msg": "success",
  "data": {
    "permissions": [
      "project:read",
      "project:update",
      "env:create",
      "folder:create",
      "secret:list",
      "secret:search",
      "secret:read",
      "secret:reveal",
      "secret:create",
      "secret:update",
      "secret:delete"
    ],
    "source_grants": [
      {
        "role_code": "org_admin",
        "scope_type": "organization",
        "scope_id": "organization uuid"
      },
      {
        "role_code": "project_admin",
        "scope_type": "project",
        "scope_id": "project uuid"
      }
    ]
  }
}
```

## 现有业务接口权限映射

| 接口 | 所需权限 |
| --- | --- |
| `GET /api/v1/org/list` | `org:read`，按可见组织过滤 |
| `POST /api/v1/org/create` | `org:create` on `global` |
| `POST /api/v1/org/info` | `org:read` on organization |
| `POST /api/v1/org/update` | `org:update` on organization |
| `POST /api/v1/org/delete` | `org:delete` on organization |
| `POST /api/v1/project/list` | `project:read` on organization/project |
| `POST /api/v1/project/create` | `project:create` on organization |
| `POST /api/v1/project/info` | `project:read` on project |
| `POST /api/v1/project/update` | `project:update` on project |
| `POST /api/v1/project/delete` | `project:delete` on project |
| `POST /api/v1/env/list` | `env:read` on project/environment |
| `POST /api/v1/env/create` | `env:create` on project |
| `POST /api/v1/env/info` | `env:read` on environment |
| `POST /api/v1/env/update` | `env:update` on environment |
| `POST /api/v1/env/delete` | `env:delete` on environment |
| `POST /api/v1/folder/list` | `folder:read` on environment/folder |
| `POST /api/v1/folder/create` | `folder:create` on environment |
| `POST /api/v1/folder/info` | `folder:read` on folder |
| `POST /api/v1/folder/update` | `folder:update` on folder |
| `POST /api/v1/folder/delete` | `folder:delete` on folder |
| `POST /api/v1/secret/list` | `secret:list` on requested scope |
| `POST /api/v1/secret/search` | `secret:search` on requested scope |
| `POST /api/v1/secret/create` | `secret:create` on folder |
| `POST /api/v1/secret/info` | `secret:read` on secret/folder |
| `POST /api/v1/secret/reveal` | `secret:reveal` on secret/folder |
| `POST /api/v1/secret/update` | `secret:update` on secret/folder |
| `POST /api/v1/secret/delete` | `secret:delete` on secret/folder |
| `POST /api/v1/audit/list` | `audit:read` on requested scope |

## 错误码建议

| HTTP 状态码 | code | 场景 |
| --- | --- | --- |
| 401 | 1401 | 未认证或 JWT 无效 |
| 403 | 1403 | 已认证但无权限 |
| 404 | 1404 | 资源不存在，或不希望泄露存在性时可对无权限资源返回 404 |
| 409 | 1409 | 角色绑定冲突、删除最后一个 owner |
| 500 | 1500 | 未预期错误 |

对 Secret、审计记录这类敏感资源，如果资源存在但用户无权限，可以按场景返回 403 或 404。对外部用户不应通过错误信息推断其他组织或项目的资源存在。

## 实现落地建议

第一阶段：

1. 新增 RBAC 表和初始化数据。
2. 实现 `Authorizer` 的数据库版本。
3. 在 service 层接入单资源授权检查。
4. 为列表和搜索增加授权过滤。
5. 新增角色绑定接口，先不开放自定义角色管理。
6. 补充 JWT 用户同步和 bootstrap admin。
7. 添加权限单元测试和 handler 表驱动测试。

第二阶段：

1. 支持自定义角色。
2. 支持临时授权过期清理。
3. 支持权限缓存和失效机制。
4. 支持更细粒度的审计查询过滤。

## 安全注意事项

- 不允许在日志中打印 JWT、Secret 明文、Secret 密文、加密主密钥。
- 授权失败日志只记录用户 ID、权限点、资源类型和资源 ID，不记录 Secret value。
- 搜索结果默认不返回明文 value。
- `secret:reveal` 必须单独授权、单独审计。
- RBAC 变更必须审计，至少记录操作者、目标用户、角色、作用域、动作和时间。
- 初始化管理员只能通过受控环境变量创建，生产环境初始化后应移除该变量。
