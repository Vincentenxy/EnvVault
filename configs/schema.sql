-- Drop statements (in reverse dependency order)
drop table if exists role_binding_audit_records;
drop table if exists user_role_bindings;
drop table if exists role_permissions;
drop table if exists roles;
drop table if exists permissions;
drop table if exists users;
drop table if exists secrets;
drop table if exists folders;
drop table if exists environment_templates;
drop table if exists environments;
drop table if exists projects;
drop table if exists audit_records;
drop table if exists deleted_records;
drop table if exists organizations;

-- Create statements
create extension if not exists pg_trgm;

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
    updated_at timestamptz not null default now(),
    constraint organizations_code_chk check (code ~ '^[a-z0-9]+(-[a-z0-9]+)*$')
);

create unique index if not exists organizations_code_active_uidx
    on organizations (code)
    where is_deleted = false;

create table if not exists projects (
    id uuid primary key,
    org_id uuid not null,
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
    constraint projects_code_chk check (code ~ '^[a-z0-9]+(-[a-z0-9]+)*$')
);

create unique index if not exists projects_org_code_active_uidx
    on projects (org_id, code)
    where is_deleted = false;

-- v3: env 归属 project;org 下不再直接挂 env
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
    updated_at timestamptz not null default now(),
    constraint environments_code_chk check (code ~ '^[a-z0-9]+(-[a-z0-9]+)*$')
);

create unique index if not exists environments_project_code_active_uidx
    on environments (project_id, code)
    where is_deleted = false;

create index if not exists environments_project_idx
    on environments (project_id)
    where is_deleted = false;

-- v3: org 层 env 模板汇总,只读快照;以首次写入为准
create table if not exists environment_templates (
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
    updated_at timestamptz not null default now(),
    constraint environment_templates_code_chk check (code ~ '^[a-z0-9]+(-[a-z0-9]+)*$')
);

create unique index if not exists environment_templates_org_code_active_uidx
    on environment_templates (org_id, code)
    where is_deleted = false;

create index if not exists environment_templates_org_idx
    on environment_templates (org_id)
    where is_deleted = false;

-- folders:支持 2 级层级(顶级 / 子级)。
--
-- 【字段语义:environment_id vs parent_id 不能互相替代,看似冗余其实不然】
--
--   environment_id  答"这个 folder 属于哪个 env"   ← 顶层 + 子级都必填
--   parent_id       答"这个 folder 的父 folder 是谁" ← 只有子级(level=2)填
--   level           答"它是顶级还是子级"             ← 1 = 顶层,2 = 子级
--
-- 顶层(level=1)folder 的 parent_id 永远是 NULL(父是 env 而不是 folder),
-- 所以必须靠 environment_id 才能定位到 env——environment_id 不是冗余的。
-- 子级(level=2)folder 的 environment_id 确实可以从 parent.environment_id 推出,
-- 但保留它(反范式)换来 O(1) 的 env 范围查询,代价仅 16 字节/行,值得。
--
-- 历史上有人尝试把 environment_id 砍掉、把 parent_id 改成"多态"(level=1 指向 env、
-- level=2 指向 folder),会同时丢掉:
--   - FK 约束(parent_id 不知道指向 env 还是 folder)
--   - 简单索引(只能上递归 CTE)
--   - 应用层"我手里这个 uuid 到底是不是 env"的判别负担
-- 故维持两列各司其职,不要合并。
create table if not exists folders (
    id uuid primary key,
    environment_id uuid not null,
    parent_id uuid references folders(id) on delete cascade,
    level int not null default 1,
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
    constraint folders_code_chk check (code ~ '^[a-z0-9]+(-[a-z0-9]+)*$'),
    constraint folders_level_chk check (level in (1, 2)),
    -- 1 级 folder 不能有 parent,2 级 folder 必须有 parent(且 parent 必须是 1 级)
    constraint folders_level_parent_chk check (
        (level = 1 and parent_id is null)
        or (level = 2 and parent_id is not null)
    )
);

-- 唯一索引按 (env, parent 域, code) 取域。
-- 对 level=1,parent 域 = 空串;对 level=2,parent 域 = 父 folder id。
-- 这样同一 env 下顶层 code="globals" 和子级 code="globals" 不冲突。
create unique index if not exists folders_env_parent_code_active_uidx
    on folders (environment_id, coalesce(parent_id::text, ''), code)
    where is_deleted = false;

create index if not exists folders_parent_idx
    on folders (parent_id)
    where is_deleted = false;

create table if not exists secrets (
    id uuid primary key,
    folder_id uuid not null,
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
    updated_at timestamptz not null default now(),
    constraint secrets_key_chk check (key ~ '^[A-Z][A-Z0-9_]*$')
);

create unique index if not exists secrets_folder_key_active_uidx
    on secrets (folder_id, key)
    where is_deleted = false;

create index if not exists secrets_key_search_idx
    on secrets using gin (key gin_trgm_ops)
    where is_deleted = false;

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

create table if not exists roles (
    id uuid primary key,
    code text not null,
    name text not null,
    description text not null default '',
    scope_type text not null,
    org_id uuid,
    project_id uuid,
    is_system boolean not null default false,
    is_deleted boolean not null default false,
    deleted_at timestamptz,
    deleted_by text not null default '',
    created_by text not null default '',
    updated_by text not null default '',
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

create table if not exists role_permissions (
    role_id uuid not null,
    permission_id uuid not null,
    created_at timestamptz not null default now(),
    primary key (role_id, permission_id)
);

create index if not exists role_permissions_permission_id_idx
    on role_permissions (permission_id);

create table if not exists user_role_bindings (
    id uuid primary key,
    user_id uuid not null,
    role_id uuid not null,
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

create table if not exists role_binding_audit_records (
    id uuid primary key,
    actor text not null default '',
    action text not null,
    target_user_id uuid,
    role_id uuid,
    scope_type text not null,
    scope_id uuid,
    snapshot jsonb,
    created_at timestamptz not null default now()
);

create index if not exists role_binding_audit_records_target_idx
    on role_binding_audit_records (target_user_id, created_at desc);

create index if not exists role_binding_audit_records_scope_idx
    on role_binding_audit_records (scope_type, scope_id, created_at desc);
