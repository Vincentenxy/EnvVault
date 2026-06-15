-- =====================================================================
-- 多用户 + 多权限 测试数据 SQL 脚本
-- =====================================================================
--
-- 背景:目前系统里只有 dev-user 一个用户(platform_admin 全权),
--      无法测试 search 接口在不同 RBAC scope 下的行为。
-- 本脚本创建 7 个用户,分别绑定到不同层级的不同 role,
-- 便于用 curl 拿 dev token 后逐个打 /api/v1/secrets/search 接口,
-- 观察不同用户看到的 list / aggregate 结果。
--
-- 假设前置数据已存在(由 cmd/seed 写入):
--   - org "org-01"
--   - project "proj-09" 在 org-01 下
--   - env "dev" / "test" / "sim" / "prod" 在 proj-09 下
--   - folder "ana-svc" (level=1) 在 4 个 env 下各一份
--
-- 角色定义见 internal/store/postgres/rbac.go 的 defaultRoles():
--   - project_admin:       全权(包含 secret:reveal)
--   - project_developer:   read+reveal+create+update(无 delete/audit:read)
--   - project_viewer:      只读 metadata(无 reveal)
--   - folder_editor:       单 folder 下全权(无 list/search 之外)
--   - folder_viewer:       单 folder 下只读 metadata
--   - org_viewer:          全 org 只读 metadata
--
-- 用法:
--   psql $DATABASE_URL -f configs/test_users.sql
-- 验证最后一节的 SELECT 即可看到 7 个 user 的 binding 总览。
--
-- 跑完后再用 dev token 拿各 user 的 JWT,比如:
--   curl -sX POST http://localhost:8880/api/v1/auth/dev/token \
--     -H 'Content-Type: application/json' \
--     -d '{"userId":"alice","name":"Alice"}' | jq -r .data.token
--
-- 兼容性:本脚本用 gen_random_uuid()(PostgreSQL 13+ 内置;更早版本需
--   create extension pgcrypto)。envVault 自身用 Go crypto/rand 生成 UUID,
--   所以 DB 端 gen_random_uuid() 只用于本脚本的 users / bindings 行。
-- =====================================================================

BEGIN;

-- 1. 创建用户(idempotent: external_user_id 是 unique key)
INSERT INTO users (id, external_user_id, name, email, source)
VALUES
    (gen_random_uuid(), 'alice',  'Alice (project_admin)',       'alice@example.com',  'jwt'),
    (gen_random_uuid(), 'bob',    'Bob (project_developer)',     'bob@example.com',    'jwt'),
    (gen_random_uuid(), 'carol',  'Carol (project_viewer)',      'carol@example.com',  'jwt'),
    (gen_random_uuid(), 'dave',   'Dave (folder_editor/dev)',    'dave@example.com',   'jwt'),
    (gen_random_uuid(), 'eve',    'Eve (folder_viewer/dev)',     'eve@example.com',    'jwt'),
    (gen_random_uuid(), 'frank',  'Frank (org_viewer)',          'frank@example.com',  'jwt'),
    (gen_random_uuid(), 'grace',  'Grace (no binding,空查询)',  'grace@example.com',  'jwt')
ON CONFLICT (external_user_id) DO UPDATE
SET name       = EXCLUDED.name,
    email      = EXCLUDED.email,
    updated_at = now();

-- 2. 清掉旧的测试 binding(便于重跑,真正的 user_id 通过 sub-select 锁定)
DELETE FROM user_role_bindings
WHERE user_id IN (
    SELECT id FROM users
    WHERE external_user_id IN ('alice', 'bob', 'carol', 'dave', 'eve', 'frank', 'grace')
);

-- 3. 创建 user_role_bindings(scope_id 全部通过 sub-select 实时查,
--    避免硬编码 UUID;若 org/project/env/folder 被重建,也能正确跟随)

-- 3.1 alice: project_admin @ proj-09(project 维度全权,可 reveal 明文)
INSERT INTO user_role_bindings (id, user_id, role_id, scope_type, scope_id, granted_by)
SELECT gen_random_uuid(),
       (SELECT id FROM users          WHERE external_user_id = 'alice'),
       (SELECT id FROM roles          WHERE code = 'project_admin'),
       'project',
       (SELECT id FROM projects       WHERE code = 'proj-09'
            AND org_id = (SELECT id FROM organizations WHERE code = 'org-01')),
       'manual-script';

-- 3.2 bob: project_developer @ proj-09(可 read+reveal+create+update,无 delete)
INSERT INTO user_role_bindings (id, user_id, role_id, scope_type, scope_id, granted_by)
SELECT gen_random_uuid(),
       (SELECT id FROM users          WHERE external_user_id = 'bob'),
       (SELECT id FROM roles          WHERE code = 'project_developer'),
       'project',
       (SELECT id FROM projects       WHERE code = 'proj-09'
            AND org_id = (SELECT id FROM organizations WHERE code = 'org-01')),
       'manual-script';

-- 3.3 carol: project_viewer @ proj-09(只能读 metadata,无 secret:reveal)
--     → search 走 project 维度时,聚合 group 仍会出现,但 Envs[*].values 永远为空
INSERT INTO user_role_bindings (id, user_id, role_id, scope_type, scope_id, granted_by)
SELECT gen_random_uuid(),
       (SELECT id FROM users          WHERE external_user_id = 'carol'),
       (SELECT id FROM roles          WHERE code = 'project_viewer'),
       'project',
       (SELECT id FROM projects       WHERE code = 'proj-09'
            AND org_id = (SELECT id FROM organizations WHERE code = 'org-01')),
       'manual-script';

-- 3.4 dave: folder_editor @ folder "ana-svc" in env "dev"(只这一个 folder)
--     → search 走 project 维度时,4 个 env 的 ana-svc folder 仅有 dev 的可见
INSERT INTO user_role_bindings (id, user_id, role_id, scope_type, scope_id, granted_by)
SELECT gen_random_uuid(),
       (SELECT id FROM users          WHERE external_user_id = 'dave'),
       (SELECT id FROM roles          WHERE code = 'folder_editor'),
       'folder',
       (SELECT f.id
          FROM folders f
          JOIN environments e ON e.id = f.environment_id
          JOIN projects p      ON p.id = e.project_id
          JOIN organizations o ON o.id = p.org_id
         WHERE f.code = 'ana-svc' AND f.level = 1 AND e.code = 'dev'
           AND p.code = 'proj-09' AND o.code = 'org-01'),
       'manual-script';

-- 3.5 eve: folder_viewer @ folder "ana-svc" in env "dev"(同 dave,但只读)
INSERT INTO user_role_bindings (id, user_id, role_id, scope_type, scope_id, granted_by)
SELECT gen_random_uuid(),
       (SELECT id FROM users          WHERE external_user_id = 'eve'),
       (SELECT id FROM roles          WHERE code = 'folder_viewer'),
       'folder',
       (SELECT f.id
          FROM folders f
          JOIN environments e ON e.id = f.environment_id
          JOIN projects p      ON p.id = e.project_id
          JOIN organizations o ON o.id = p.org_id
         WHERE f.code = 'ana-svc' AND f.level = 1 AND e.code = 'dev'
           AND p.code = 'proj-09' AND o.code = 'org-01'),
       'manual-script';

-- 3.6 frank: org_viewer @ org-01(整 org 只读 metadata)
--     → search 走 project 维度时,proj-09 下所有 folder/secret 都能 list/search,
--       但 Envs[*].values 因无 secret:reveal 全部为空
INSERT INTO user_role_bindings (id, user_id, role_id, scope_type, scope_id, granted_by)
SELECT gen_random_uuid(),
       (SELECT id FROM users          WHERE external_user_id = 'frank'),
       (SELECT id FROM roles          WHERE code = 'org_viewer'),
       'organization',
       (SELECT id FROM organizations  WHERE code = 'org-01'),
       'manual-script';

-- 3.7 grace: 故意不给任何 binding
--     → 任何 list / search / 读接口都返空 list(走 RBAC narrowing 后无可见 scope)

COMMIT;

-- =====================================================================
-- 验证:列出 7 个 user 的 binding 总览(scope_name 反查 code 便于人读)
-- =====================================================================
SELECT
    u.external_user_id                                AS user_id,
    u.name                                            AS user_name,
    r.code                                            AS role_code,
    urb.scope_type                                    AS scope_type,
    CASE urb.scope_type
        WHEN 'global'        THEN 'all'
        WHEN 'organization' THEN (SELECT code FROM organizations WHERE id = urb.scope_id)
        WHEN 'project'       THEN (SELECT code FROM projects       WHERE id = urb.scope_id)
        WHEN 'environment'   THEN (SELECT code FROM environments   WHERE id = urb.scope_id)
        WHEN 'folder'        THEN (SELECT code FROM folders        WHERE id = urb.scope_id)
        ELSE ''
    END                                               AS scope_code
FROM user_role_bindings urb
JOIN users u ON u.id = urb.user_id
JOIN roles r ON r.id = urb.role_id
WHERE u.external_user_id IN ('alice', 'bob', 'carol', 'dave', 'eve', 'frank', 'grace')
  AND urb.is_deleted = false
ORDER BY u.external_user_id, urb.scope_type, r.code;

-- =====================================================================
-- 拿到 7 个 user 的 dev token(逐个调用 /auth/dev/token,
-- body 用对应 userId 模拟「该用户登录」;Response.data.token 即 JWT)
-- =====================================================================
--
-- TOKEN_ALICE=$(curl -sX POST http://localhost:8880/api/v1/auth/dev/token \
--   -H 'Content-Type: application/json' \
--   -d '{"userId":"alice","name":"Alice"}' | jq -r .data.token)
-- TOKEN_BOB=$(curl -sX POST http://localhost:8880/api/v1/auth/dev/token \
--   -H 'Content-Type: application/json' \
--   -d '{"userId":"bob","name":"Bob"}' | jq -r .data.token)
-- TOKEN_CAROL=$(curl -sX POST http://localhost:8880/api/v1/auth/dev/token \
--   -H 'Content-Type: application/json' \
--   -d '{"userId":"carol","name":"Carol"}' | jq -r .data.token)
-- TOKEN_DAVE=$(curl -sX POST http://localhost:8880/api/v1/auth/dev/token \
--   -H 'Content-Type: application/json' \
--   -d '{"userId":"dave","name":"Dave"}' | jq -r .data.token)
-- TOKEN_EVE=$(curl -sX POST http://localhost:8880/api/v1/auth/dev/token \
--   -H 'Content-Type: application/json' \
--   -d '{"userId":"eve","name":"Eve"}' | jq -r .data.token)
-- TOKEN_FRANK=$(curl -sX POST http://localhost:8880/api/v1/auth/dev/token \
--   -H 'Content-Type: application/json' \
--   -d '{"userId":"frank","name":"Frank"}' | jq -r .data.token)
-- TOKEN_GRACE=$(curl -sX POST http://localhost:8880/api/v1/auth/dev/token \
--   -H 'Content-Type: application/json' \
--   -d '{"userId":"grace","name":"Grace"}' | jq -r .data.token)
--
-- === 用法示例:project 维度 search(只有 alice/bob/carol/frank 能拿到非空 list) ===
-- curl -sX POST http://localhost:8880/api/v1/secrets/search \
--   -H "Authorization: Bearer $TOKEN_ALICE" \
--   -H 'Content-Type: application/json' \
--   -d '{"pageNum":1,"pageSize":50,"projectId":"<proj-09-uuid>","keyword":"OB_USER_11111111111"}' | jq
--
-- === env 维度 search(alice/bob/carol/frank 都能用,dave/eve 仅有 dev/ana-svc 一条命中) ===
-- curl -sX POST http://localhost:8880/api/v1/secrets/search \
--   -H "Authorization: Bearer $TOKEN_DAVE" \
--   -H 'Content-Type: application/json' \
--   -d '{"pageNum":1,"pageSize":50,"environmentId":"<dev-uuid>","keyword":"OB_USER"}' | jq
