# EnvVault 搜索设计

## 背景

EnvVault 的核心数据层级为：

```text
organization / project / environment / folder / key:value
```

当前数据持久化在 PostgreSQL 中，Secret `value` 使用服务端加密后保存。Redis 已作为缓存引入，当前保存 Secret 的层级信息、key 和 `value_ciphertext`，用于快速访问和搜索预热。

新的搜索需求：

- 搜索 organization、project、environment、folder、secret key、secret value、description/comment。
- 支持关键字搜索和正则表达式搜索。
- 支持大小写敏感和大小写不敏感。
- value 是加密存储的，搜索 value 时需要取出密文并解密后匹配。
- 搜索结果需要展示完整配置路径和命中信息。
- 必须结合 RBAC，用户只能搜索自己有权限的数据；没有权限的数据不允许被访问、命中或通过数量侧信道暴露。
- 速度要求尽可能快。

## 现有设计约束

`design/DESIGN.md` 当前说明：

- Redis 用于 Secret 查询缓存。
- Redis 保存 `key` 和加密后的 `value_ciphertext`，不保存明文 value。
- 当前只实现 key 搜索。
- value 搜索需要在安全性和搜索性能之间继续设计。

`design/rbac_degisn.md` 当前说明：

- 搜索必须走 `secret:search` 权限。
- 列表和搜索不能先查全部再在内存中过滤，必须优先把可见作用域下推到 SQL 或缓存查询条件中。
- `secret:read` 和 `secret:reveal` 必须拆开。
- 搜索结果默认不返回明文 value。

## 关键结论

加密 value 与任意正则搜索之间存在天然冲突：

- PostgreSQL 只能索引密文，无法对明文 value 做全文、模糊或正则索引。
- Redis 只保存密文时，也无法直接匹配明文 value。
- 如果每次搜索都从数据库或 Redis 拉取候选密文再逐条解密，安全性较好，但在全局搜索和高并发下性能较差。
- 如果要求任意正则表达式搜索明文 value 且速度尽可能快，服务端必须在运行时持有可搜索的明文材料，至少是短生命周期的内存快照。

因此推荐方案为：

```text
PostgreSQL 持久化密文
Redis 缓存密文和层级元数据
应用内 search index 保存不可持久化的解密搜索快照
RBAC 先生成可见范围，再执行搜索
```

明文 value 不落 PostgreSQL、不落 Redis、不落日志、不落审计，只在应用进程内存中用于搜索。

## 总体架构

```text
HTTP /api/v1/search
  |
  v
SearchService
  |
  |-- RBAC: 计算用户可搜索 scope
  |
  |-- MetadataIndex: 搜索 org/project/env/folder/key/description
  |
  |-- SecretValueIndex: 在授权候选内搜索解密后的 value
  |
  v
ResultMerger: 合并、排序、脱敏、分页
```

推荐新增包：

```text
internal/search
```

核心职责：

- 编译和限制搜索表达式。
- 根据 RBAC 生成可见 scope filter。
- 维护搜索快照。
- 执行元数据搜索和 value 搜索。
- 合并并裁剪结果。

## 搜索对象

搜索文档统一抽象为 `SearchDocument`。

```text
documentId
resourceType: organization | project | environment | folder | secret
resourceId
orgId
projectId
environmentId
folderId
secretId
path
fields
updatedAt
version
```

`fields` 包含：

| 字段 | 来源 | 是否明文持久化 | 是否参与搜索 |
| --- | --- | --- | --- |
| `organization.name` | organizations.name | 是 | 是 |
| `organization.description` | organizations.comment | 是 | 是 |
| `project.name` | projects.name | 是 | 是 |
| `project.description` | projects.comment | 是 | 是 |
| `environment.name` | environments.name | 是 | 是 |
| `environment.description` | environments.comment | 是 | 是 |
| `folder.name` | folders.name | 是 | 是 |
| `folder.description` | folders.comment | 是 | 是 |
| `secret.key` | secrets.key | 是 | 是 |
| `secret.description` | secrets.comment | 是 | 是 |
| `secret.value` | secrets.value_ciphertext 解密后得到 | 否 | 是 |

说明：

- 当前表里字段名是 `comment`，产品语义上可以在 API 层命名为 `description`。
- value 明文只存在于应用内搜索快照。

## 搜索模式

### 普通关键字搜索

普通关键字搜索用于大多数场景。

参数：

```json
{
  "keyword": "database",
  "caseSensitive": false
}
```

匹配规则：

- `caseSensitive = false` 时统一按 Unicode lower-case 后匹配。
- `caseSensitive = true` 时按原文匹配。
- 普通关键字不按正则解释。

### 正则表达式搜索

正则搜索用于高级场景。

参数：

```json
{
  "pattern": "DATABASE_(URL|HOST)",
  "mode": "regex",
  "caseSensitive": true
}
```

实现要求：

- Go 端使用标准库 `regexp`，即 RE2 语义，避免灾难性回溯。
- 限制 pattern 长度，例如最大 512 字符。
- 限制单次搜索超时时间，例如默认 2 秒。
- 限制最大返回数量，例如默认 100，最大 1000。
- 编译失败返回 400。
- 禁止把 pattern 和命中的 value 明文写入日志。

大小写不敏感有两种实现方式：

- 关键字模式：搜索前对 pattern 和目标文本做 lower-case。
- 正则模式：编译时自动加 `(?i)` 前缀，或对目标文本 lower-case 后匹配 lower-case pattern。

推荐正则模式使用 `(?i)`，保留用户 pattern 语义。

## 权限模型

搜索涉及两个权限：

| 权限点 | 说明 |
| --- | --- |
| `secret:search` | 允许搜索 Secret 元数据和 value，但默认不返回明文 value |
| `secret:reveal` | 允许在搜索结果中展示明文 value |

元数据层级资源也需要对应 read 权限：

| 资源 | 权限 |
| --- | --- |
| organization | `org:read` |
| project | `project:read` |
| environment | `env:read` |
| folder | `folder:read` |

### 授权顺序

搜索必须先授权，再匹配。

流程：

1. 从 JWT 解析用户。
2. RBAC 计算用户可见 scope 集合。
3. 根据请求中的 `orgId`、`projectId`、`environmentId`、`folderId` 收窄 scope。
4. 如果请求 scope 不在用户可见范围内，返回空结果或 403。
5. MetadataIndex 只搜索可见元数据。
6. SecretValueIndex 只搜索用户拥有 `secret:search` 的 Secret 候选。
7. 结果返回时，只有拥有 `secret:reveal` 的 Secret 才能包含明文 value。

推荐行为：

- 显式指定无权限 scope 时返回 403。
- 未指定 scope 的全局搜索只返回有权限的结果，不能暴露无权限结果数量。

## 搜索结果展示

搜索结果需要展示完整路径和命中字段。

响应示例：

```json
{
  "code": 0,
  "msg": "success",
  "data": {
    "items": [
      {
        "resourceType": "secret",
        "resourceId": "uuid",
        "path": {
          "organization": {
            "id": "org uuid",
            "name": "default-org"
          },
          "project": {
            "id": "project uuid",
            "name": "billing"
          },
          "environment": {
            "id": "env uuid",
            "name": "prod"
          },
          "folder": {
            "id": "folder uuid",
            "name": "globals"
          }
        },
        "secret": {
          "id": "secret uuid",
          "key": "DATABASE_URL",
          "value": "",
          "valueRevealed": false,
          "comment": "database connection"
        },
        "matches": [
          {
            "field": "secret.key",
            "preview": "DATABASE_URL"
          },
          {
            "field": "secret.value",
            "preview": "***"
          }
        ]
      }
    ],
    "nextCursor": ""
  }
}
```

说明：

- `valueRevealed = false` 时，`value` 为空或省略。
- 如果用户同时拥有 `secret:reveal`，可以返回明文 value，但仍不写日志。
- `matches.preview` 对 value 默认使用 `***`。如果产品要求展示命中片段，也必须要求 `secret:reveal`。

## 推荐实现方案

### 方案 A：授权优先的运行时解密索引

这是推荐方案，兼顾速度、正则能力和安全边界。

#### 数据来源

启动时：

1. 从 PostgreSQL 加载 active organizations、projects、environments、folders、secrets。
2. 对 Secret 读取 `value_ciphertext`。
3. 使用 Encryptor 解密 value。
4. 构建内存中的不可变 SearchSnapshot。
5. Redis 同步保存密文和元数据，用于快速恢复和增量更新。

变更时：

- organization/project/environment/folder 的 name/comment 更新后，更新 metadata document。
- secret 创建/更新后，解密最新密文并更新 secret document。
- secret 删除后，从 snapshot 中移除。
- Redis 更新仍只保存密文。

#### 内存快照结构

```text
SearchSnapshot
  version
  built_at
  docs_by_id
  docs_by_org
  docs_by_project
  docs_by_environment
  docs_by_folder
```

文档内容：

```text
SearchDocument
  ids and path names
  searchable fields
  normalized searchable fields
  permissions scope metadata
```

更新策略：

- 使用 copy-on-write 或不可变快照。
- 搜索请求只读当前快照，不加全局大锁。
- 更新时构建小范围新快照，然后原子替换指针。

Go 实现方向：

```text
atomic.Pointer[SearchSnapshot]
```

优点：

- 搜索路径没有数据库 IO。
- 正则搜索可以直接在内存文本上执行。
- 可支持高并发。
- Redis/PostgreSQL 仍不保存明文 value。

缺点：

- 应用内存会持有明文 value。
- 应用重启需要重新解密构建索引。
- 多副本部署时每个副本都需要自己的内存索引或事件同步机制。

### 方案 B：按请求解密扫描

流程：

1. RBAC 先计算授权 scope。
2. 从 Redis 或 PostgreSQL 拉取授权候选 Secret 密文。
3. 当前请求内逐条解密 value。
4. 执行匹配。
5. 返回结果。

优点：

- 明文只在请求生命周期存在。
- 实现简单。

缺点：

- 全局搜索性能差。
- 高并发下重复解密开销大。
- 正则搜索无法使用索引。

适用：

- 数据量很小。
- 初期 MVP。
- 对明文驻留内存极度敏感。

### 方案 C：外部搜索引擎

例如 OpenSearch、Meilisearch、Bleve。

如果索引明文 value，搜索速度最好，但明文会落到外部索引存储，不满足当前“value 只加密存储”的安全目标。

如果索引加密 value，无法实现明文正则搜索。

因此第一阶段不推荐。

## Redis 设计

Redis 不保存明文 value。

推荐 key：

```text
envvault:secret:ids
envvault:secret:<secret_id>
envvault:search:snapshot:version
envvault:search:dirty
```

`envvault:secret:<secret_id>` hash 字段：

```text
id
org_id
org_name
org_comment
project_id
project_name
project_comment
environment_id
environment_name
environment_comment
folder_id
folder_name
folder_comment
key
comment
value_ciphertext
version
updated_at
```

说明：

- `value_ciphertext` 继续 base64 保存 JSON 密文。
- name/comment 存入 Redis 可以减少构建 SearchSnapshot 时的 PostgreSQL join。
- Redis 中的 comment/description 是明文元数据，若未来这些描述也被视为敏感字段，应一起纳入加密或脱敏策略。

## PostgreSQL 设计

当前 `secrets_key_search_idx` 只能支持 key 的 trigram 搜索。

推荐补充两个方向。

### 元数据搜索 SQL

对非加密元数据使用 PostgreSQL trigram 索引：

```sql
create index if not exists organizations_name_search_idx
    on organizations using gin (name gin_trgm_ops)
    where is_deleted = false;

create index if not exists organizations_comment_search_idx
    on organizations using gin (comment gin_trgm_ops)
    where is_deleted = false;

create index if not exists projects_name_search_idx
    on projects using gin (name gin_trgm_ops)
    where is_deleted = false;

create index if not exists projects_comment_search_idx
    on projects using gin (comment gin_trgm_ops)
    where is_deleted = false;

create index if not exists environments_name_search_idx
    on environments using gin (name gin_trgm_ops)
    where is_deleted = false;

create index if not exists environments_comment_search_idx
    on environments using gin (comment gin_trgm_ops)
    where is_deleted = false;

create index if not exists folders_name_search_idx
    on folders using gin (name gin_trgm_ops)
    where is_deleted = false;

create index if not exists folders_comment_search_idx
    on folders using gin (comment gin_trgm_ops)
    where is_deleted = false;

create index if not exists secrets_comment_search_idx
    on secrets using gin (comment gin_trgm_ops)
    where is_deleted = false;
```

### 搜索快照状态表

用于多副本和增量同步：

```sql
create table if not exists search_index_events (
    id bigserial primary key,
    resource_type text not null,
    resource_id uuid not null,
    action text not null,
    created_at timestamptz not null default now()
);

create index if not exists search_index_events_created_idx
    on search_index_events (id);
```

资源变更事务提交时写入事件：

```text
organization update/delete
project update/delete
environment update/delete
folder update/delete
secret create/update/delete
```

搜索服务定期拉取 event，更新内存快照。第一阶段也可以在写接口成功后直接调用 index updater。

## 搜索执行流程

### 请求参数

```json
{
  "query": "database",
  "mode": "keyword",
  "caseSensitive": false,
  "fields": [
    "organization.name",
    "organization.description",
    "project.name",
    "project.description",
    "environment.name",
    "environment.description",
    "folder.name",
    "folder.description",
    "secret.key",
    "secret.description",
    "secret.value"
  ],
  "orgId": "",
  "projectId": "",
  "environmentId": "",
  "folderId": "",
  "limit": 100,
  "cursor": ""
}
```

字段说明：

| 字段 | 说明 |
| --- | --- |
| `query` | 搜索关键字或正则表达式 |
| `mode` | `keyword` 或 `regex` |
| `caseSensitive` | 是否大小写敏感 |
| `fields` | 搜索字段，空表示全部字段 |
| `orgId` | 限定组织 |
| `projectId` | 限定项目 |
| `environmentId` | 限定环境 |
| `folderId` | 限定 Folder |
| `limit` | 返回数量 |
| `cursor` | 分页游标 |

### 流程

```text
1. 校验 query、mode、limit、fields
2. 编译 matcher
3. 从 RBAC 获取 user visible scopes
4. 将请求 scope 与 visible scopes 求交集
5. 如果交集为空，返回空列表或 403
6. 从 SearchSnapshot 获取候选 docs
7. 在候选 docs 内执行字段匹配
8. 按匹配分数和路径排序
9. 根据 secret:reveal 决定是否返回 value
10. 返回分页结果
```

## 性能设计

### 候选集裁剪

搜索前必须先裁剪候选集：

- 指定 `folderId` 时，只扫描该 Folder 下 Secret 和相关元数据。
- 指定 `environmentId` 时，只扫描该环境。
- 指定 `projectId` 时，只扫描该项目。
- 指定 `orgId` 时，只扫描该组织。
- 未指定时，只扫描用户可见范围。

这样可以避免全局扫描所有明文 value。

### 快照与并发

推荐：

- 每次搜索读取一个不可变快照。
- 更新索引时不阻塞搜索。
- 搜索请求使用 `context.Context` 支持取消。
- 对正则搜索设置超时和最大扫描文档数。
- 对结果做 limit，避免大响应。

### 字段预处理

为提高关键字搜索速度：

- 每个字段保存原文和 lower-case 版本。
- 可选：为 key/name/comment 建立内存 token/trigram 倒排索引。
- value 因需要正则搜索，仍保留全文字符串用于扫描。

第一阶段建议：

- key/name/comment 用简单 contains 或 PostgreSQL trigram 兜底。
- value 用内存快照扫描。

第二阶段再加：

- 内存 trigram 倒排索引用于 value 关键字搜索。
- 正则搜索先从 pattern 提取 literal，再用倒排索引缩小候选。

## 安全设计

### 明文 value 生命周期

推荐方案会在应用内存中持有明文 value。必须遵守：

- 不写日志。
- 不写 Redis。
- 不写 PostgreSQL。
- 不写审计表。
- 不放入 panic 信息。
- 不作为 metrics label。
- 搜索结果默认不返回明文 value。

Go 中 string 不易清零，因此解密后的 value 如果放入索引，就应视为进程内敏感数据。部署上需要：

- 限制应用容器的调试权限。
- 禁止不必要的 heap dump。
- 生产环境关闭 pprof 或加严格认证。
- 限制 Pod exec 权限。

### 权限侧信道

搜索不能暴露无权限数据：

- 无权限资源不参与候选集。
- total count 只统计有权限结果。
- 不返回“有匹配但无权限”的提示。
- 显式搜索无权限 scope 时建议返回 403。

### value 命中展示

如果命中字段是 `secret.value`：

- 无 `secret:reveal`：只返回 `field = secret.value` 和 `preview = ***`。
- 有 `secret:reveal`：可以返回明文 value 或命中片段。

推荐即使拥有 `secret:reveal`，也默认只返回命中片段，由前端再调用 `/secret/reveal` 查看完整明文。这样可以减少明文在响应中扩散。

## HTTP API 设计

### 全局搜索

| 方法 | 路径 | 权限 |
| --- | --- | --- |
| POST | `/api/v1/search` | 按资源类型分别检查 read/search 权限 |

请求：

```json
{
  "query": "DATABASE",
  "mode": "keyword",
  "caseSensitive": false,
  "fields": [],
  "orgId": "",
  "projectId": "",
  "environmentId": "",
  "folderId": "",
  "limit": 100,
  "cursor": ""
}
```

### Secret 搜索

保留当前接口，但扩展语义：

| 方法 | 路径 | 权限 |
| --- | --- | --- |
| POST | `/api/v1/secret/search` | `secret:search` |

请求：

```json
{
  "query": "DATABASE",
  "mode": "regex",
  "caseSensitive": true,
  "fields": [
    "secret.key",
    "secret.description",
    "secret.value"
  ],
  "orgId": "uuid",
  "projectId": "",
  "environmentId": "",
  "folderId": "",
  "limit": 100,
  "cursor": ""
}
```

响应：

```json
{
  "code": 0,
  "msg": "success",
  "data": {
    "items": [],
    "nextCursor": "",
    "snapshotVersion": 12
  }
}
```

## 排序与打分

第一阶段使用简单排序：

1. 精确 key/name 命中。
2. key/name 包含命中。
3. description/comment 命中。
4. value 命中。
5. 按 path 字典序。

第二阶段可以引入分数：

```text
score = field_weight + exact_bonus + prefix_bonus + recent_bonus
```

字段权重建议：

| 字段 | 权重 |
| --- | --- |
| secret.key | 100 |
| folder.name | 80 |
| environment.name | 70 |
| project.name | 70 |
| organization.name | 70 |
| description/comment | 40 |
| secret.value | 30 |

value 权重较低，避免因为密钥值里的长文本产生太多噪声。

## 数据一致性

搜索允许最终一致。

建议目标：

- 写入成功后 1 秒内搜索可见。
- 删除成功后应尽快从搜索快照移除。
- 删除事件失败时，下一次全量重建必须修复。

实现方式：

- 写接口成功后同步更新 Redis 和本地 SearchSnapshot。
- 同时写 `search_index_events`。
- 后台 worker 定期拉取遗漏事件。
- 应用启动时全量重建一次。

## 配置项建议

```yaml
search:
  enabled: true
  value_search_enabled: true
  index_plaintext_values_in_memory: true
  max_query_length: 512
  default_limit: 100
  max_limit: 1000
  regex_timeout: "2s"
  rebuild_on_start: true
  refresh_interval: "1s"
```

环境变量：

```text
ENVVAULT_SEARCH_ENABLED
ENVVAULT_SEARCH_VALUE_SEARCH_ENABLED
ENVVAULT_SEARCH_INDEX_PLAINTEXT_VALUES_IN_MEMORY
ENVVAULT_SEARCH_MAX_QUERY_LENGTH
ENVVAULT_SEARCH_DEFAULT_LIMIT
ENVVAULT_SEARCH_MAX_LIMIT
ENVVAULT_SEARCH_REGEX_TIMEOUT
ENVVAULT_SEARCH_REBUILD_ON_START
ENVVAULT_SEARCH_REFRESH_INTERVAL
```

如果 `index_plaintext_values_in_memory = false`，则 value 搜索降级为方案 B：按请求解密扫描。

## 测试建议

必须覆盖：

- key/name/comment 普通关键字搜索。
- value 解密后搜索。
- regex 搜索。
- 大小写敏感和大小写不敏感。
- 无权限数据不出现在结果中。
- 显式请求无权限 scope 返回 403。
- `secret:search` 可命中 value 但不返回明文。
- `secret:reveal` 才返回明文或明文片段。
- Secret 更新后搜索快照更新。
- Secret 删除后搜索结果消失。
- 高并发搜索无数据竞争。
- context 取消和 regex timeout。

## 分阶段落地建议

### 第一阶段：安全可用

- 建立 `internal/search`。
- 实现 RBAC scope prefilter。
- 实现应用内不可变 SearchSnapshot。
- 启动时全量重建。
- Secret 创建、更新、删除后更新快照。
- 支持 keyword、regex、caseSensitive。
- 搜索结果默认不返回 value 明文。

### 第二阶段：性能增强

- 对 key/name/comment 建立内存倒排索引。
- 对 value 关键字搜索建立 trigram 倒排索引。
- regex 搜索从 pattern 提取 literal 来缩小候选。
- 支持多副本通过 `search_index_events` 做增量同步。

### 第三阶段：运维增强

- 搜索索引健康检查。
- 快照版本和重建耗时指标。
- 手动重建索引接口。
- 搜索慢查询日志，但日志中不能包含 Secret value。

## 不推荐方案

### 不推荐把明文 value 存入 Redis

原因：

- Redis 持久化、备份、复制链路都可能扩散明文。
- 与当前安全要求冲突。

### 不推荐把明文 value 存入 PostgreSQL 搜索表

原因：

- 破坏“密钥必须加密后再持久化”的安全要求。
- 审计和备份风险高。

### 不推荐先全量搜索再做权限过滤

原因：

- 无权限数据进入搜索路径，容易产生侧信道。
- 高并发下浪费大量 CPU 解密和匹配。

## 待确认问题

1. 搜索结果是否允许在拥有 `secret:reveal` 时直接返回完整 value，还是只返回命中片段。
2. description/comment 是否视为敏感字段。如果是，元数据也需要加密和解密搜索。
3. 全局搜索未指定 scope 时，无权限 scope 是返回空结果还是 403。推荐返回用户可见结果。
4. 是否允许通过配置关闭 value 搜索。推荐允许关闭。
5. 多副本部署时，是否接受每个副本各自维护内存快照。推荐第一阶段接受，第二阶段再优化同步。
