-- organizations.code 只在活动记录(is_deleted = false)中唯一。
--
-- 适用场景:
--   旧数据库曾使用 UNIQUE(code)，导致软删除组织仍阻塞同 code 新组织创建。
--
-- 执行前建议先备份数据库。脚本可重复执行。

begin;

-- 删除 organizations(code) 上由 UNIQUE constraint 创建的全局唯一约束。
do $$
declare
    item record;
begin
    for item in
        select c.conname
        from pg_constraint c
        join pg_class t on t.oid = c.conrelid
        join pg_namespace n on n.oid = t.relnamespace
        where n.nspname = current_schema()
          and t.relname = 'organizations'
          and c.contype = 'u'
          and pg_get_constraintdef(c.oid) ~* '^UNIQUE \(code\)'
    loop
        execute format(
            'alter table %I.%I drop constraint %I',
            current_schema(),
            'organizations',
            item.conname
        );
    end loop;
end
$$;

-- 删除不是 constraint 创建、但同样作用于 organizations(code) 的全局唯一索引。
do $$
declare
    item record;
begin
    for item in
        select ni.nspname as schema_name, idx.relname as index_name
        from pg_index i
        join pg_class t on t.oid = i.indrelid
        join pg_namespace nt on nt.oid = t.relnamespace
        join pg_class idx on idx.oid = i.indexrelid
        join pg_namespace ni on ni.oid = idx.relnamespace
        join pg_attribute a
          on a.attrelid = t.oid
         and a.attnum = i.indkey[0]
        where nt.nspname = current_schema()
          and t.relname = 'organizations'
          and i.indisunique = true
          and i.indisprimary = false
          and i.indpred is null
          and i.indnatts = 1
          and a.attname = 'code'
    loop
        execute format(
            'drop index %I.%I',
            item.schema_name,
            item.index_name
        );
    end loop;
end
$$;

create unique index if not exists organizations_code_active_uidx
    on organizations (code)
    where is_deleted = false;

commit;
