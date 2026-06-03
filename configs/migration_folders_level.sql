-- migration_folders_level.sql
-- 把 folders 表升级为支持 2 级 folder(level + parent_id),无需 drop 重建。
-- 兼容已有数据:存量 folder 都视为 level=1(默认值),parent_id=NULL,所有约束自动满足。
-- 唯一索引同步从 (environment_id, code) 升级为 (environment_id, coalesce(parent_id::text, ''), code)。
-- 这两条索引对存量行等价(存量 parent_id 全部 NULL,coalesce 出来都是空串),不会因为创建失败。

-- 1. 加列(带默认值,存量行 level=1,parent_id=NULL)
alter table folders
    add column if not exists level int not null default 1;

alter table folders
    add column if not exists parent_id uuid references folders(id) on delete cascade;

-- 2. 加 check 约束(用 IF NOT EXISTS 模式;PG 9.6+ 原生支持)
do $$
begin
    if not exists (
        select 1 from pg_constraint where conname = 'folders_level_chk'
    ) then
        alter table folders
            add constraint folders_level_chk check (level in (1, 2));
    end if;
end$$;

do $$
begin
    if not exists (
        select 1 from pg_constraint where conname = 'folders_level_parent_chk'
    ) then
        alter table folders
            add constraint folders_level_parent_chk check (
                (level = 1 and parent_id is null)
                or (level = 2 and parent_id is not null)
            );
    end if;
end$$;

-- 3. 索引:删旧 + 建新。
--    生产建议拆成两步走,先 drop 再 CREATE INDEX CONCURRENTLY 建新索引,
--    避免长时间持锁。本文件给出"安全但持锁"版本,业务低峰期跑即可。
drop index if exists folders_environment_code_active_uidx;

create unique index if not exists folders_env_parent_code_active_uidx
    on folders (environment_id, coalesce(parent_id::text, ''), code)
    where is_deleted = false;

-- 4. 二级 folder 的 parent 查找索引
create index if not exists folders_parent_idx
    on folders (parent_id)
    where is_deleted = false;

-- 5. 自检:确认新列/索引已就位
select
    (select count(*) from information_schema.columns
       where table_name = 'folders' and column_name = 'level') as level_col_ok,
    (select count(*) from information_schema.columns
       where table_name = 'folders' and column_name = 'parent_id') as parent_id_col_ok,
    (select count(*) from pg_indexes
       where indexname = 'folders_env_parent_code_active_uidx') as new_uidx_ok,
    (select count(*) from pg_indexes
       where indexname = 'folders_parent_idx') as parent_idx_ok,
    (select count(*) from folders) as total_folders,
    (select count(*) from folders where parent_id is not null) as folders_with_parent;
