-- envVault 测试账号种子 SQL
-- ----------------------------------------------------------------------------
-- 用法:
--   1) 确认已跑过 cmd/seed(或手工至少有 org-01 / proj-01 / proj-02)
--   2) psql -U <user> -d <envvault> -f design/test_users_seed.sql
--   3) 全部 INSERT 用 ON CONFLICT DO NOTHING,重跑安全
--
-- 测试账号(6 个,密码 argon2id):
--
--   | email                 | 密码           | 角色                  | 作用域                |
--   |-----------------------|----------------|-----------------------|-----------------------|
--   | alice@envvault.local  | Test@1234      | platform_admin        | global                |
--   | erin@envvault.local   | Owner@1234     | org_owner             | org-01 (Alibaba)      |
--   | frank@envvault.local  | Admin@1234     | org_admin             | org-01 (Alibaba)      |
--   | bob@envvault.local    | Viewer@1234    | org_viewer            | org-01 (Alibaba)      |
--   | dave@envvault.local   | Auditor@1234   | project_auditor       | proj-01 (UserCenter)  |
--   | carol@envvault.local  | Dev@1234       | project_developer     | proj-02 (OrderSystem) |
--
-- 角色权限说明(来自 internal/store/postgres/rbac.go:1074 的 defaultRoles):
--   platform_admin  : 全部 30 个 permission
--   org_owner       : 全部 30 个 permission(作用域=org)
--   org_admin       : 缺 org:create / org:delete / org:force_delete,其余全有
--   org_viewer      : org:read / project:read / env:read / folder:read
--                      secret:list / secret:search / secret:read / env:template:read
--   project_auditor : project:read / env:read / folder:read / secret:list
--                      secret:read / audit:read
--   project_developer: project:read / env:read / folder:read
--                      secret:list / secret:search / secret:read / secret:reveal
--                      secret:create / secret:update
-- ----------------------------------------------------------------------------

BEGIN;

-- 0) 兜底:确保 system roles 已存在(若 EnsureSystemData 未跑过,这一段会建好)
--    正常情况下服务启动时已经跑过,这里用 ON CONFLICT 跳过
INSERT INTO roles (id, code, name, description, scope_type, is_system, created_by, updated_by)
SELECT gen_random_uuid(), v.code, v.name, v.description, v.scope_type, true, 'sql-seed', 'sql-seed'
FROM (VALUES
    ('platform_admin',     'Platform Admin',     'Global super admin',                'global'),
    ('org_owner',          'Organization Owner', 'Full control within one org',       'organization'),
    ('org_admin',          'Organization Admin', 'Manage org content (no create org)','organization'),
    ('org_viewer',         'Organization Viewer','Read-only within one org',         'organization'),
    ('org_auditor',        'Organization Auditor','Read + audit within one org',      'organization'),
    ('project_admin',      'Project Admin',      'Full control within one project',   'project'),
    ('project_developer',  'Project Developer',  'Read/write secrets in one project', 'project'),
    ('project_viewer',     'Project Viewer',     'Read-only within one project',      'project'),
    ('project_auditor',    'Project Auditor',    'Read + audit within one project',   'project'),
    ('folder_admin',       'Folder Admin',       'Full control within one folder',    'folder'),
    ('folder_editor',      'Folder Editor',      'Read/write secrets in one folder',  'folder'),
    ('folder_viewer',      'Folder Viewer',      'Read-only within one folder',       'folder')
) AS v(code, name, description, scope_type)
WHERE NOT EXISTS (
    SELECT 1 FROM roles r WHERE r.code = v.code AND r.is_system = true
);

-- 1) 创建 6 个 password 用户
--    external_user_id 形如 "email:<lower-case>",与 AuthStore.CreatePasswordUser 约定一致
INSERT INTO users (id, external_user_id, name, email, source, password_hash, password_algo, last_seen_at, created_at, updated_at)
VALUES
    (gen_random_uuid(), 'email:alice@envvault.local', 'Alice (Platform Admin)',   'alice@envvault.local', 'password',
        '$argon2id$v=19$m=65536,t=3,p=2$GD6q3nKsdNbaLz+gLqKIIg$zggqY8DQ6flAWwwSFYzWwEB9me+6SY2UDfoqUhBwAfo',
        'argon2id', now(), now(), now()),
    (gen_random_uuid(), 'email:erin@envvault.local',  'Erin (Org Owner)',         'erin@envvault.local',  'password',
        '$argon2id$v=19$m=65536,t=3,p=2$0rzuk6mQkfFkm38B1kbMdA$jGb+QK3n6FI4JFpFXQ0bqYHI6Ao3FSD215RoC7B2tOw',
        'argon2id', now(), now(), now()),
    (gen_random_uuid(), 'email:frank@envvault.local', 'Frank (Org Admin)',        'frank@envvault.local', 'password',
        '$argon2id$v=19$m=65536,t=3,p=2$RamaQYIOnPZewPjf8I73Pw$p7iU6FMKss/SjPrMjXMOUTCoghoh4K6x+uDJs1CVcrs',
        'argon2id', now(), now(), now()),
    (gen_random_uuid(), 'email:bob@envvault.local',   'Bob (Org Viewer)',         'bob@envvault.local',   'password',
        '$argon2id$v=19$m=65536,t=3,p=2$QmorF9hLX4fkxJHNcki5Fg$KIHbGppLOgtGmDjnCxr2jXz7vDMxDjZlRTyaEOVi5jc',
        'argon2id', now(), now(), now()),
    (gen_random_uuid(), 'email:dave@envvault.local',  'Dave (Project Auditor)',   'dave@envvault.local',  'password',
        '$argon2id$v=19$m=65536,t=3,p=2$RMRh126Fm903gL1YIp8ZdQ$c6Vm8K7fIC0iOrZo0DTCKTuSwPWE0X0kBnSk3AQ0/hc',
        'argon2id', now(), now(), now()),
    (gen_random_uuid(), 'email:carol@envvault.local', 'Carol (Project Developer)','carol@envvault.local', 'password',
        '$argon2id$v=19$m=65536,t=3,p=2$tgZht1+gvJj0IsGwAAWeUQ$AqvbkTnwiQBLVvg1KrvxBFozfOLQ98liHfAyQwwnhTk',
        'argon2id', now(), now(), now())
ON CONFLICT (external_user_id) DO NOTHING;

-- 2) 角色绑定 —— scope 信息从 organizations / projects 现查,避免硬编码 uuid
--    用 CTE 一次算好,后面 6 个 INSERT 都引用

WITH
    org01 AS (SELECT id AS org_id FROM organizations WHERE code = 'org-01' AND is_deleted = false),
    proj01 AS (SELECT id AS project_id FROM projects WHERE code = 'proj-01' AND is_deleted = false),
    proj02 AS (SELECT id AS project_id FROM projects WHERE code = 'proj-02' AND is_deleted = false),
    -- role_id 解析
    r_admin AS (SELECT id AS role_id FROM roles WHERE code = 'platform_admin' AND is_deleted = false),
    r_owner AS (SELECT id AS role_id FROM roles WHERE code = 'org_owner' AND is_deleted = false),
    r_orgadmin AS (SELECT id AS role_id FROM roles WHERE code = 'org_admin' AND is_deleted = false),
    r_orgviewer AS (SELECT id AS role_id FROM roles WHERE code = 'org_viewer' AND is_deleted = false),
    r_projauditor AS (SELECT id AS role_id FROM roles WHERE code = 'project_auditor' AND is_deleted = false),
    r_projdev AS (SELECT id AS role_id FROM roles WHERE code = 'project_developer' AND is_deleted = false),
    -- user_id 解析
    u_alice AS (SELECT id AS user_id FROM users WHERE external_user_id = 'email:alice@envvault.local'),
    u_erin  AS (SELECT id AS user_id FROM users WHERE external_user_id = 'email:erin@envvault.local'),
    u_frank AS (SELECT id AS user_id FROM users WHERE external_user_id = 'email:frank@envvault.local'),
    u_bob   AS (SELECT id AS user_id FROM users WHERE external_user_id = 'email:bob@envvault.local'),
    u_dave  AS (SELECT id AS user_id FROM users WHERE external_user_id = 'email:dave@envvault.local'),
    u_carol AS (SELECT id AS user_id FROM users WHERE external_user_id = 'email:carol@envvault.local')
INSERT INTO user_role_bindings (id, user_id, role_id, scope_type, scope_id, granted_by, created_at, updated_at)
SELECT gen_random_uuid(), src.user_id, src.role_id, src.scope_type, src.scope_id, 'sql-seed', now(), now()
FROM (VALUES
    -- alice: platform_admin (global)
    ((SELECT user_id FROM u_alice), (SELECT role_id FROM r_admin),     'global',       NULL::uuid),
    -- erin:  org_owner -> org-01
    ((SELECT user_id FROM u_erin),  (SELECT role_id FROM r_owner),     'organization', (SELECT org_id FROM org01)),
    -- frank: org_admin -> org-01
    ((SELECT user_id FROM u_frank), (SELECT role_id FROM r_orgadmin),  'organization', (SELECT org_id FROM org01)),
    -- bob:   org_viewer -> org-01
    ((SELECT user_id FROM u_bob),   (SELECT role_id FROM r_orgviewer), 'organization', (SELECT org_id FROM org01)),
    -- dave:  project_auditor -> proj-01
    ((SELECT user_id FROM u_dave),  (SELECT role_id FROM r_projauditor), 'project',   (SELECT project_id FROM proj01)),
    -- carol: project_developer -> proj-02
    ((SELECT user_id FROM u_carol), (SELECT role_id FROM r_projdev),   'project',      (SELECT project_id FROM proj02))
) AS src(user_id, role_id, scope_type, scope_id)
WHERE src.user_id IS NOT NULL
  AND src.role_id IS NOT NULL
  AND (src.scope_id IS NOT NULL OR src.scope_type = 'global')
  -- 幂等:已存在同 (user, role, scope_type, scope_id) 的 active binding 就跳过
  AND NOT EXISTS (
      SELECT 1 FROM user_role_bindings urb
      WHERE urb.user_id = src.user_id
        AND urb.role_id = src.role_id
        AND urb.scope_type = src.scope_type
        AND (
            (src.scope_id IS NULL AND urb.scope_id IS NULL) OR
            urb.scope_id = src.scope_id
        )
        AND urb.is_deleted = false
  );

COMMIT;

-- ----------------------------------------------------------------------------
-- 验收查询(执行后跑一下确认):
--
--   -- 看所有测试账号和 active role
--   SELECT u.email, r.code AS role, urb.scope_type, urb.scope_id
--   FROM user_role_bindings urb
--   JOIN users u ON u.id = urb.user_id
--   JOIN roles r ON r.id = urb.role_id
--   WHERE u.email LIKE '%@envvault.local'
--     AND urb.is_deleted = false
--   ORDER BY u.email, r.code;
--
--   -- 看 alice 在 global scope 的有效权限
--   SELECT p.code
--   FROM user_role_bindings urb
--   JOIN users u ON u.id = urb.user_id
--   JOIN roles r ON r.id = urb.role_id
--   JOIN role_permissions rp ON rp.role_id = r.id
--   JOIN permissions p ON p.id = rp.permission_id
--   WHERE u.email = 'alice@envvault.local'
--     AND urb.is_deleted = false
--     AND urb.scope_type = 'global';
--
--   -- 撤销某个授权(以 bob 为例)
--   UPDATE user_role_bindings
--   SET is_deleted = true, deleted_at = now(), deleted_by = 'sql-undo', updated_at = now()
--   WHERE user_id = (SELECT id FROM users WHERE email = 'bob@envvault.local')
--     AND is_deleted = false;
-- ----------------------------------------------------------------------------
