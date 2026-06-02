# EnvVault 后续设计待办

状态：本文件中的业务路径 `code` 设计已按清库重建方式落地到 `configs/schema.sql`、核心 CRUD 接口、Redis Secret 缓存和 `design/api/core.yaml`。后续如果已有生产数据，需要另写迁移脚本，本次不包含历史数据迁移。

---

## 新设计：环境归组织所有，项目按需关联

### 背景

原设计：环境属于项目，每个项目创建时自动创建 dev/test/sim/prod 四个环境。

新设计：环境属于组织，所有项目共享组织下的环境列表。项目创建时默认关联 dev/test/sim/prod 四个环境，也可选择关联其他已存在环境或创建新环境。

### 数据模型调整

**environments 表调整**：
- `project_id` → `org_id`（环境属于组织）

**新增关联表**：
```sql
create table if not exists project_environments (
    id uuid primary key,
    project_id uuid not null,
    environment_id uuid not null,
    created_at timestamptz not null default now(),
    constraint project_environments_unique unique (project_id, environment_id)
);
```

### 查询逻辑

**场景1：查询项目可用的环境**
```sql
-- 如果项目有关联的 project_environments 记录，使用关联的环境
SELECT e.* FROM environments e
JOIN project_environments pe ON pe.environment_id = e.id
WHERE pe.project_id = 'project-uuid' AND e.is_deleted = false;

-- 如果项目没有关联记录（初始状态），默认使用组织下所有环境
SELECT e.* FROM environments e
WHERE e.org_id = 'org-uuid' AND e.is_deleted = false;
```

**场景2：查询组织下所有环境（供项目选择）**
```sql
SELECT e.* FROM environments e
WHERE e.org_id = 'org-uuid' AND e.is_deleted = false;
```

### 创建项目时的行为

1. 不再自动创建环境
2. 默认关联组织下已有的 dev/test/sim/prod 四个环境
3. 用户可以：
   - 选择只关联部分环境（如只关联 dev/test）
   - 选择创建新环境并关联

### 创建环境时的行为

1. 环境创建在组织下，不是项目下
2. 自动关联到所有已有项目（或仅关联到创建时指定的项目，取决于业务决策）

### 待办事项

#### 数据库改动
- [ ] `environments` 表：`project_id` 改为 `org_id`
- [ ] 新增 `project_environments` 关联表
- [ ] 更新 `schema.sql`
- [ ] 更新外键约束

#### 代码改动
- [ ] `repository.go`：
  - [ ] `CreateProject` 不再自动创建环境，改为关联组织下 dev/test/sim/prod
  - [ ] `CreateEnvironment` 从 `project_id` 改为 `org_id`
  - [ ] `ListEnvironments` 支持按 `org_id` 查询
  - [ ] 新增 `ListProjectEnvironments` 查询项目关联的环境
  - [ ] 新增 `AssociateEnvironmentsToProject` 关联环境到项目
- [ ] `controller/resource.go`：
  - [ ] `createEntityRequest` 可能需要调整
  - [ ] 创建项目接口增加 `environmentIds` 可选参数，允许用户选择关联哪些环境
- [ ] `schema.sql` 更新

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

## 完整新设计：Org 共享环境 + Per-Project Folder（v2）

> 状态：设计待评审，未实现。所有写库变更走"清库重建"路径，不包含历史数据迁移。
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

## Org 共享 env 模型下的权限设计

> 状态：v2 设计评审要点，需在 schema 改动前敲定。

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