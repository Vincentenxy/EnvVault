# EnvVault 后续设计待办

## 业务路径 code 设计

目标是在保留 UUID 内部主键的前提下，为组织、项目、环境、Folder、Secret 提供稳定、可读、可拼接的业务路径。

业务路径格式：

```text
org_code.project_code.env_code.folder_code.KEY
```

示例：

```text
organization-a.project-a.dev.globals.DATABASE_URL
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
- 不允许包含 `.`，避免和 `org_code.project_code.env_code.folder_code.KEY` 路径分隔符冲突。

## 唯一约束

`name` 不需要唯一，只作为展示名称。

`code` 按层级唯一：

- `organizations.code`：全局唯一。
- `projects.code`：同一个组织下唯一，不同组织下可以相同。
- `environments.code`：同一个项目下唯一，不同项目下可以相同。
- `folders.code`：同一个环境下唯一，不同环境下可以相同。
- `secrets.key`：同一个 Folder 下唯一，不同 Folder 下可以相同。

所有唯一约束只针对未删除数据生效，即 `is_deleted = false`。

## 数据库改动待办

需要调整 `configs/schema.sql`：

- `organizations` 增加 `code text not null`。
- `projects` 增加 `code text not null`。
- `environments` 增加 `code text not null`。
- `folders` 增加 `code text not null`。
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

create unique index folders_environment_code_active_uidx
    on folders (environment_id, code)
    where is_deleted = false;
```

已建库升级时需要：

- 给相关表新增 `code` 字段。
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

Secret 创建和更新需要校验 `key` 格式。

## Go 代码改动待办

需要调整：

- `Entity` 增加 `Code` 字段。
- `createEntityRequest` 增加 `Code` 字段。
- `createEntityTx` 写入 `code`。
- `listEntities`、`getEntity`、`updateEntity` 查询并返回 `code`。
- `updateEntity` 不更新 `code`。
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
- `folder_code`
- `path`

建议完整路径：

```text
org_code.project_code.env_code.folder_code.KEY
```

建议增加路径到 Secret ID 的索引：

```text
envvault:secret:path:<org_code.project_code.env_code.folder_code.KEY> -> secret_id
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
