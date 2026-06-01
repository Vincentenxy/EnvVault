# EnvVault 后续设计待办

状态：本文件中的业务路径 `code` 设计已按清库重建方式落地到 `configs/schema.sql`、核心 CRUD 接口、Redis Secret 缓存和 `design/api/core.yaml`。后续如果已有生产数据，需要另写迁移脚本，本次不包含历史数据迁移。

## 业务路径 code 设计

目标是在保留 UUID 内部主键的前提下，为组织、项目、环境、Folder、Secret 提供稳定、可读、可拼接的业务路径。

业务路径格式需要支持 Folder 多层级，不再固定为单个 `folder_code`：

```text
org_code.project_code.env_code.<folder_path>.KEY
```

其中 `folder_path` 由一个或多个 `folder_code` 使用 `.` 拼接。

示例：

```text
organization-a.project-a.dev.globals.DATABASE_URL
organization-a.project-a.dev.groups-secrets.payment.DATABASE_URL
```

## 字段职责

核心实体后续采用 `id + code + name` 三类字段：

- `id`：内部主键，继续使用 UUID，用于数据库外键、RBAC、审计、逻辑删除历史等内部关联。
- `code`：业务路径标识，用户创建时指定，创建后不允许修改。
- `name`：展示名称，仅用于前端展示，可以写中文，不参与唯一约束。

Secret 不新增 `code` 字段，继续使用 `key` 作为路径最后一段。

## code 规则

适用表：

- `organizations`
- `projects`
- `environments`
- `folders`

规则：

- 创建时必填。
- 创建后不允许更新。
- 只允许小写英文字母、数字、中横线。
- 推荐正则：`^[a-z0-9]+(-[a-z0-9]+)*$`
- 不允许空字符串。
- 不允许以中横线开头或结尾。
- 不允许连续中横线。

## Secret key 规则

Secret 的 `key` 按标准 `.env` key 风格约束：

- 只允许大写英文字母、数字、下划线。
- 推荐正则：`^[A-Z][A-Z0-9_]*$`
- 不允许空字符串。
- 建议必须以大写字母开头，避免纯数字 key。
- 不允许包含 `.`，避免和 `org_code.project_code.env_code.<folder_path>.KEY` 路径分隔符冲突。

## Folder 多层级设计

Folder 数据库结构按树形设计，代码层暂时限制最大深度为 2 层，后续可以扩展到 3 层或更多层。

数据库字段：

- `folders.environment_id`：Folder 所属环境。
- `folders.parent_id`：父级 Folder，一级 Folder 的 `parent_id` 为空。
- `folders.code`：当前层级下唯一的业务标识。
- `folders.name`：展示名称，不参与唯一约束。

层级规则：

- 当前代码层最大深度暂定为 2。
- 一级 Folder：`parent_id is null`。
- 二级 Folder：`parent_id` 指向一级 Folder。
- 暂时不允许创建三级 Folder。
- 数据库使用树形结构保留扩展能力，后续需要支持三级时，只调整代码层深度限制和接口校验。
- 创建子 Folder 时，必须校验父 Folder 存在、未删除，并且属于同一个 `environment_id`。

路径解析规则：

- 第 1 段：`org_code`。
- 第 2 段：`project_code`。
- 第 3 段：`env_code`。
- 最后一段：Secret `KEY`。
- 中间剩余段：`folder_path`，按顺序逐级匹配 Folder。

示例：

```text
organization-a.project-a.dev.globals.DATABASE_URL
```

解析为：

- `org_code = organization-a`
- `project_code = project-a`
- `env_code = dev`
- `folder_path = globals`
- `key = DATABASE_URL`

```text
organization-a.project-a.dev.groups-secrets.payment.DATABASE_URL
```

解析为：

- `org_code = organization-a`
- `project_code = project-a`
- `env_code = dev`
- `folder_path = groups-secrets.payment`
- `key = DATABASE_URL`

## 唯一约束

`name` 不需要唯一，只作为展示名称。

`code` 按层级唯一：

- `organizations.code`：全局唯一。
- `projects.code`：同一个组织下唯一，不同组织下可以相同。
- `environments.code`：同一个项目下唯一，不同项目下可以相同。
- `folders.code`：同一个父级下唯一，不同父级下可以相同。
  - 一级 Folder 在同一个环境下唯一。
  - 二级 Folder 在同一个父 Folder 下唯一。
- `secrets.key`：同一个 Folder 下唯一，不同 Folder 下可以相同。

所有唯一约束只针对未删除数据生效，即 `is_deleted = false`。

## 数据库改动待办

需要调整 `configs/schema.sql`：

- `organizations` 增加 `code text not null`。
- `projects` 增加 `code text not null`。
- `environments` 增加 `code text not null`。
- `folders` 增加 `code text not null`。
- `folders` 增加 `parent_id uuid references folders(id)`。
- 删除或停止使用 `name` 唯一索引。
- 增加 `code` 层级唯一索引。
- 保留 `secrets(folder_id, key)` 唯一索引。

建议唯一索引：

```sql
create unique index organizations_code_active_uidx
    on organizations (code)
    where is_deleted = false;

create unique index projects_org_code_active_uidx
    on projects (org_id, code)
    where is_deleted = false;

create unique index environments_project_code_active_uidx
    on environments (project_id, code)
    where is_deleted = false;

create unique index folders_environment_root_code_active_uidx
    on folders (environment_id, code)
    where parent_id is null and is_deleted = false;

create unique index folders_parent_code_active_uidx
    on folders (parent_id, code)
    where parent_id is not null and is_deleted = false;
```

Folder 树形结构建议增加外键：

```sql
alter table folders
    add column parent_id uuid references folders(id);
```

如果后续希望在数据库层防止跨环境挂载子 Folder，可以额外增加约束或通过代码事务校验。当前优先在代码层校验：

```text
child.environment_id == parent.environment_id
```

如果 PostgreSQL 需要更强约束，可以考虑增加 `(id, environment_id)` 唯一约束，再让 `(parent_id, environment_id)` 做复合外键。

```sql
create unique index folders_id_environment_uidx
    on folders (id, environment_id);
```

然后使用复合外键约束子 Folder 与父 Folder 必须属于同一个环境。该方案后续开发时再决定是否落地，当前先记录为增强项。

旧的单层 Folder 唯一索引需要替换：

```sql
drop index if exists folders_environment_code_active_uidx;
```

改为：

```sql
create unique index folders_environment_root_code_active_uidx
    on folders (environment_id, code)
    where parent_id is null and is_deleted = false;

create unique index folders_parent_code_active_uidx
    on folders (parent_id, code)
    where is_deleted = false;
```

已建库升级时需要：

- 给相关表新增 `code` 字段。
- 给 `folders` 新增 `parent_id` 字段。
- 使用现有 `name` 或人工指定值回填 `code`。
- 校验历史数据在同级下没有重复 code。
- 删除旧的 `name` 唯一索引。
- 创建新的 `code` 唯一索引。

## 接口改动待办

创建接口需要增加 `code`：

- `/api/v1/org/create`
- `/api/v1/project/create`
- `/api/v1/env/create`
- `/api/v1/folder/create`

更新接口不允许更新 `code`，只允许更新：

- `name`
- `comment`

列表、详情、创建、更新返回需要增加：

- `code`

Folder 创建接口需要增加：

- `parent_id`：可选。为空表示创建一级 Folder；不为空表示创建子 Folder。

Folder 列表接口需要支持：

- 按 `environment_id` 查询当前环境下的 Folder。
- 可选按 `parent_id` 查询某个父 Folder 下的直接子 Folder。
- 后续如果前端需要树形展示，可以增加独立树形接口。

Secret 创建和更新需要校验 `key` 格式。

## Go 代码改动待办

需要调整：

- `Entity` 增加 `Code` 字段。
- `createEntityRequest` 增加 `Code` 字段。
- Folder 创建请求增加 `ParentID` 和 `FolderParentID` 的语义区分，避免和环境 ID 混淆；或单独定义 Folder 请求结构。
- `createEntityTx` 写入 `code`。
- `listEntities`、`getEntity`、`updateEntity` 查询并返回 `code`。
- `updateEntity` 不更新 `code`。
- Folder 创建时校验最大深度，目前最大深度为 2。
- Folder 创建时校验父 Folder 与当前环境一致。
- Folder 路径查询时按 `folder_path` 逐级解析。
- 自动创建默认环境和 Folder 时，`code` 和 `name` 可以默认相同：
  - env: `dev`、`test`、`sim`、`prod`
  - folder: `globals`、`groups-secrets`
- 增加 code 格式校验。
- 增加 Secret key 格式校验。

## Redis 改动待办

Secret 缓存需要补充业务路径字段：

- `org_code`
- `project_code`
- `environment_code`
- `folder_path`
- `path`

建议完整路径：

```text
org_code.project_code.env_code.<folder_path>.KEY
```

建议增加路径到 Secret ID 的索引：

```text
envvault:secret:path:<org_code.project_code.env_code.<folder_path>.KEY> -> secret_id
```

现有按 Secret UUID 存 hash 的方式保留。

## 文档改动待办

需要更新：

- `design/DESIGN.md`
- `design/api/core.yaml`
- `README.md`
- `configs/schema.sql`
- 本地已建库升级 SQL 说明

文档里需要明确：

- UUID `id` 是内部稳定主键。
- `code` 是业务路径标识。
- `name` 是展示名称。
- `code` 创建后不可变。
- `.env` key 只能使用大写字母、数字、下划线。
