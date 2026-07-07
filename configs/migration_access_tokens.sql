-- Personal Access Token (PAT) 表
-- 类似 GitLab Personal Access Token,允许用户生成 API token 用于程序化访问。
-- token 明文仅在创建时返回一次,DB 只存 SHA256 hash。
-- 软删除通过 deleted_at 标记,保留审计轨迹。

create table if not exists access_tokens (
    id uuid primary key,
    user_id uuid not null references users(id),
    name text not null,
    token_hash text not null,
    token_prefix text not null,
    expires_at timestamptz,
    last_used_at timestamptz,
    created_at timestamptz not null default now(),
    deleted_at timestamptz
);

create unique index if not exists access_tokens_token_hash_uidx
    on access_tokens (token_hash)
    where deleted_at is null;

create index if not exists access_tokens_user_id_idx
    on access_tokens (user_id)
    where deleted_at is null;
