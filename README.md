# 秘钥管理平台

---

---

- 背景：
    - 这个项目是一个类似infisical的轻量级秘钥管理平台
    - 支持私有化部署
  
- 预期功能
    - 管理层级支持层级如下
      - 组织
        - 项目
          - 环境：dev/test/sim/prod，支持自建其他环境，比如poc
            - folder目录，目前只考虑一级目录，
              - 秘钥key ：value
    - 支持统一的jwt认证，认证拦截，权限放行
    - 支持全局检索key、value内容，支持按照项目检索
    - 支持修改记录追溯
    - 支持服务端加密，选取一下目前用的最多的密码秘钥加密方式，要求暴露出来接口，在提供默认加密的同时，可让用户自己实现加密方式
      - 加密的秘钥存放在环境变量里面
    - 对外提供http方式进行操作
      - http接口规范
        - 无参数查询统一使用get；特殊情况如分享的链接，带有参数的可以使用get请求
        - 有参数查询统一使用post
      - http框架采用gin
    - 全局查询的时候要支持高并发

## 当前基础架构

- 应用入口：
  - `main.go`：保留 `go run .` 的默认入口
  - `cmd/envvault/main.go`：后续发布二进制时的推荐入口
- 基础包：
  - `internal/app`：应用启动与 HTTP server 组装
  - `internal/config`：环境变量配置
  - `internal/http`：Gin 路由和 HTTP handler
  - `internal/auth`：JWT 中间件
  - `internal/crypto`：可替换加密接口与 AES-256-GCM 默认实现
  - `internal/domain`：核心领域模型
  - `internal/service`：业务服务接口边界
  - `internal/store/postgres`：PostgreSQL 连接初始化

## 配置管理

项目使用 `configs/config.yaml` 作为默认配置文件，并通过 `internal/config` 统一加载。

可以通过 `ENVVAULT_CONFIG_PATH` 指定其他配置文件：

```bash
ENVVAULT_CONFIG_PATH=./configs/config.yaml go run .
```

配置项支持通过环境变量覆盖，命名规则为 `ENVVAULT_` + 配置路径大写，例如：

```bash
ENVVAULT_DATABASE_PASSWORD=123456 go run .
ENVVAULT_HTTP_ADDR=:9090 go run .
```

当前 PostgreSQL 本地开发配置：

```yaml
database:
  host: "127.0.0.1"
  port: 5432
  user: "admin"
  password: "123456"
  name: "envvault"
  ssl_mode: "disable"
```

生产环境不要把真实密码、JWT secret、加密主密钥提交到仓库，优先使用环境变量或独立部署配置。

## 本地运行

先启动本地 PostgreSQL：

```bash
docker compose up -d postgres
```

初始化表结构：

```bash
psql "postgres://admin:123456@127.0.0.1:5432/envvault?sslmode=disable" -f configs/schema.sql
```

然后启动应用：

```bash
go run .
```

默认监听地址为 `:8080`，可以通过环境变量覆盖：

```bash
ENVVAULT_HTTP_ADDR=:8080 go run .
```

健康检查：

```bash
curl http://localhost:8080/healthz
curl http://localhost:8080/api/v1/readyz
```

## 环境变量

- `ENVVAULT_CONFIG_PATH`：配置文件路径
- `ENVVAULT_HTTP_ADDR`：HTTP 服务监听地址，默认 `:8080`
- `ENVVAULT_AUTH_JWT_SECRET`：JWT HMAC 校验密钥
- `ENVVAULT_SECURITY_ENCRYPTION_KEY`：服务端加密主密钥，默认 AES-256-GCM 实现要求 base64 编码后的 32 字节密钥
- `ENVVAULT_DATABASE_HOST`：PostgreSQL 地址
- `ENVVAULT_DATABASE_PORT`：PostgreSQL 端口
- `ENVVAULT_DATABASE_USER`：PostgreSQL 用户名
- `ENVVAULT_DATABASE_PASSWORD`：PostgreSQL 密码
- `ENVVAULT_DATABASE_NAME`：PostgreSQL 数据库名
- `ENVVAULT_DATABASE_SSL_MODE`：PostgreSQL SSL 模式，本地开发通常为 `disable`

## 统一响应格式

所有业务接口统一返回：

```json
{
  "code": 0,
  "msg": "success",
  "data": {}
}
```

- `code = 0` 表示成功。
- `code != 0` 表示失败。
- `msg` 成功时默认为 `success`，失败时返回具体错误信息。
- `data` 为实际业务数据对象。

## 认证设计

envVault 只校验外部系统签发的 JWT，不负责登录、用户管理和 token 签发。

服务端通过 `ENVVAULT_AUTH_JWT_SECRET` 或配置文件中的 `auth.jwt_secret` 配置 JWT HMAC 校验密钥。业务接口统一使用：

```http
Authorization: Bearer <jwt>
```

当前解析的用户字段：

```json
{
  "staffUserId": "string",
  "gxjId": "string",
  "staffNo": "string",
  "name": "string",
  "jwt": "string",
  "cookie": "string"
}
```

## 权限设计（RBAC 预留）

权限暂不正式启用，当前代码预留 `Authorizer` 接口，默认实现为全部放行。

后续 RBAC 建议模型：

- `users`：外部 JWT 用户在本系统中的映射。
- `roles`：角色，如 `owner`、`admin`、`editor`、`viewer`。
- `permissions`：权限点，如 `org:create`、`project:update`、`secret:read`、`secret:write`。
- `role_permissions`：角色和权限点的绑定。
- `user_roles`：用户在某个资源范围内拥有的角色。
- `resource_scope`：权限作用域，可为 `global`、`organization`、`project`、`environment`、`folder`。

建议权限判断输入：

```text
用户信息 + 动作 action + 资源类型 resource_type + 资源 ID resource_id
```

例如：

- `secret:read`：查看密钥。
- `secret:write`：创建或更新密钥。
- `secret:delete`：删除密钥。
- `project:manage`：管理项目和项目下环境。
- `org:manage`：管理组织。

全局搜索、项目搜索、密钥明文查看都应经过 RBAC 过滤。当前先预留接口，后续补权限表和具体策略。

## 删除设计

当前采用“主表逻辑删除 + 删除历史表快照”的组合方案。

主表字段：

- `is_deleted`：是否已删除。
- `deleted_at`：删除时间。
- `deleted_by`：删除人。

删除历史表：`deleted_records`

- `resource_type`：资源类型，如 `organization`、`project`、`environment`、`folder`、`secret`。
- `resource_id`：资源 ID。
- `resource_key`：资源查询键，当前格式为 `<resource_type>:<resource_id>`。
- `snapshot`：删除时的完整 JSON 快照。
- `deleted_by`：删除人。
- `deleted_at`：删除时间。

这样既能保持主表查询简单，又能记录同一资源历史上的多次删除快照。后续如果需要恢复能力，可以基于 `deleted_records.snapshot` 做恢复接口。

## 版本管理方案

当前密钥表已保留 `version` 字段，每次更新 secret 时版本号递增。

后续完整版本管理建议新增 `secret_versions` 表：

- `id`
- `secret_id`
- `version`
- `key`
- `value_ciphertext`
- `comment`
- `changed_by`
- `changed_at`

写入策略：

- 创建 secret 时写入 version `1`。
- 更新 secret 前，将旧版本写入 `secret_versions`。
- 更新成功后，主表 `secrets.version + 1`。
- 回滚时，将指定历史版本重新写回主表，并生成新版本。

审计记录中可以保存加密后的值，但不要保存明文值。

## 加密与密钥轮换设计

当前默认加密方式为 `AES-256-GCM`，加密主密钥从配置读取：

```yaml
security:
  encryption_key: "<base64 encoded 32-byte key>"
```

当前暂不实现密钥轮换。后续如果要支持轮换，建议：

- 新增 `encryption_keys` 表，记录 `key_id`、`algorithm`、`status`、`created_at`。
- `secrets.value_ciphertext` 中保存 `key_id`、`algorithm`、`nonce`、`data`。
- 新写入数据使用 active key。
- 旧数据读取时按 `key_id` 找对应 key 解密。
- 提供后台 re-encrypt 任务逐步重加密历史 secret。

## HTTP API 规范

接口采用动作风格：

- 无参数请求统一使用 `GET`。
- 有参数请求统一使用 `POST`，参数放在 body。
- 分享链接等必须通过 URL 传参的特殊场景可以使用 `GET + params`。

当前公共接口：

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| GET | `/healthz` | 存活检查 |
| GET | `/api/v1/readyz` | 就绪检查，包含数据库状态 |

当前认证接口：

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| GET | `/api/v1/me` | 查看当前 JWT 解析出的用户信息 |

### 组织接口

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| GET | `/api/v1/org/list` | 组织列表 |
| POST | `/api/v1/org/create` | 创建组织 |
| POST | `/api/v1/org/info` | 组织详情 |
| POST | `/api/v1/org/update` | 更新组织 |
| POST | `/api/v1/org/delete` | 删除组织 |

创建组织：

```json
{
  "name": "default-org",
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
  "org_id": "uuid"
}
```

创建项目：

```json
{
  "parent_id": "org uuid",
  "name": "project-a",
  "comment": "项目说明"
}
```

创建项目时会自动创建默认环境：`dev`、`test`、`sim`、`prod`，并为每个环境创建默认 Folder：`globals`、`groups-secrets`。

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
  "project_id": "uuid"
}
```

创建环境：

```json
{
  "parent_id": "project uuid",
  "name": "poc",
  "comment": "自定义环境"
}
```

创建环境时会自动创建默认 Folder：`globals`、`groups-secrets`。

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
  "environment_id": "uuid"
}
```

创建 Folder：

```json
{
  "parent_id": "environment uuid",
  "name": "custom-folder",
  "comment": "一级目录"
}
```

Folder 只支持一级目录。同一个环境下 Folder 名称唯一。

### Secret 接口

单个 Secret 内容：

```json
{
  "key": "DATABASE_URL",
  "value": "postgres://...",
  "comment": "数据库连接串"
}
```

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| POST | `/api/v1/secret/list` | Secret 列表 |
| POST | `/api/v1/secret/search` | 按 key 搜索 Secret |
| POST | `/api/v1/secret/create` | 创建 Secret |
| POST | `/api/v1/secret/info` | Secret 详情 |
| POST | `/api/v1/secret/update` | 更新 Secret |
| POST | `/api/v1/secret/delete` | 删除 Secret |

Secret 列表：

```json
{
  "org_id": "uuid",
  "project_id": "uuid",
  "environment_id": "uuid",
  "folder_id": "uuid"
}
```

创建 Secret：

```json
{
  "folder_id": "uuid",
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
  "org_id": "uuid",
  "project_id": "uuid",
  "environment_id": "uuid",
  "folder_id": "uuid",
  "keyword": "DATABASE"
}
```

当前只实现 key 搜索。value 搜索暂时预留，后续需要在安全性和搜索性能之间确认方案。

Secret 唯一约束：同一个 Folder 下 `key` 唯一；不同 Folder 可以存在相同 `key`。

### 审计接口

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| POST | `/api/v1/audit/list` | 查询修改记录 |

查询修改记录：

```json
{
  "resource_type": "secret",
  "resource_id": "uuid"
}
```

`resource_type` 和 `resource_id` 都可以为空。为空时按权限范围返回可见审计记录；当前权限尚未启用，后续接入 RBAC 后需要补充过滤。
