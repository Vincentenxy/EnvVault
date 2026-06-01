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
    constraint projects_code_chk check (code ~ '^[a-z0-9]+(-[a-z0-9]+)*$')
);

create unique index if not exists projects_org_code_active_uidx
    on projects (org_id, code)
    where is_deleted = false;

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

create table if not exists folders (
    id uuid primary key,
    environment_id uuid not null references environments(id),
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
    constraint folders_code_chk check (code ~ '^[a-z0-9]+(-[a-z0-9]+)*$')
);

create unique index if not exists folders_environment_code_active_uidx
    on folders (environment_id, code)
    where is_deleted = false;

create table if not exists secrets (
    id uuid primary key,
    folder_id uuid not null references folders(id),
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
    org_id uuid references organizations(id),
    project_id uuid references projects(id),
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
    role_id uuid not null references roles(id),
    permission_id uuid not null references permissions(id),
    created_at timestamptz not null default now(),
    primary key (role_id, permission_id)
);

create index if not exists role_permissions_permission_id_idx
    on role_permissions (permission_id);

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
