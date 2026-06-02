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