begin;

alter table environments
    add column if not exists sort_order integer not null default 100;

update environments
set sort_order = case code
    when 'dev' then 10
    when 'test' then 20
    when 'sim' then 30
    when 'prod' then 40
    else 100
end
where sort_order = 100
  and code in ('dev', 'test', 'sim', 'prod');

create index if not exists environments_project_sort_idx
    on environments (project_id, sort_order, created_at)
    where is_deleted = false;

commit;
