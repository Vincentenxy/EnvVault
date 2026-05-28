create extension if not exists pg_trgm;

create table if not exists organizations (
    id uuid primary key,
    name text not null,
    comment text not null default '',
    is_deleted boolean not null default false,
    deleted_at timestamptz,
    deleted_by text not null default '',
    created_at timestamptz not null default now(),
    updated_at timestamptz not null default now()
);

create unique index if not exists organizations_name_active_uidx
    on organizations (name)
    where is_deleted = false;

create table if not exists projects (
    id uuid primary key,
    org_id uuid not null references organizations(id),
    name text not null,
    comment text not null default '',
    is_deleted boolean not null default false,
    deleted_at timestamptz,
    deleted_by text not null default '',
    created_at timestamptz not null default now(),
    updated_at timestamptz not null default now()
);

create unique index if not exists projects_org_name_active_uidx
    on projects (org_id, name)
    where is_deleted = false;

create table if not exists environments (
    id uuid primary key,
    project_id uuid not null references projects(id),
    name text not null,
    comment text not null default '',
    is_deleted boolean not null default false,
    deleted_at timestamptz,
    deleted_by text not null default '',
    created_at timestamptz not null default now(),
    updated_at timestamptz not null default now()
);

create unique index if not exists environments_project_name_active_uidx
    on environments (project_id, name)
    where is_deleted = false;

create table if not exists folders (
    id uuid primary key,
    environment_id uuid not null references environments(id),
    name text not null,
    comment text not null default '',
    is_deleted boolean not null default false,
    deleted_at timestamptz,
    deleted_by text not null default '',
    created_at timestamptz not null default now(),
    updated_at timestamptz not null default now()
);

create unique index if not exists folders_environment_name_active_uidx
    on folders (environment_id, name)
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
    created_at timestamptz not null default now(),
    updated_at timestamptz not null default now()
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
