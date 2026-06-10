begin;

update roles
set name = seed.name,
    description = seed.description,
    scope_type = 'environment',
    is_system = true,
    is_deleted = false,
    deleted_at = null,
    deleted_by = '',
    updated_by = 'system',
    updated_at = now()
from (
    values
        ('environment_admin', 'Environment Admin', 'Manage folders, secrets, members, and audit records in one environment'),
        ('environment_developer', 'Environment Developer', 'Read, reveal, create, and update secrets in one environment'),
        ('environment_viewer', 'Environment Viewer', 'Read environment metadata and secret keys without revealing values'),
        ('environment_auditor', 'Environment Auditor', 'Read environment metadata, secret keys, and audit records')
) as seed(code, name, description)
where roles.code = seed.code
  and roles.is_system = true;

insert into roles (
    id,
    code,
    name,
    description,
    scope_type,
    is_system,
    created_by,
    updated_by
)
select
    seed.id::uuid,
    seed.code,
    seed.name,
    seed.description,
    'environment',
    true,
    'system',
    'system'
from (
    values
        ('a84b11ec-263c-4d8b-a1b1-000000000001', 'environment_admin', 'Environment Admin', 'Manage folders, secrets, members, and audit records in one environment'),
        ('a84b11ec-263c-4d8b-a1b1-000000000002', 'environment_developer', 'Environment Developer', 'Read, reveal, create, and update secrets in one environment'),
        ('a84b11ec-263c-4d8b-a1b1-000000000003', 'environment_viewer', 'Environment Viewer', 'Read environment metadata and secret keys without revealing values'),
        ('a84b11ec-263c-4d8b-a1b1-000000000004', 'environment_auditor', 'Environment Auditor', 'Read environment metadata, secret keys, and audit records')
) as seed(id, code, name, description)
where not exists (
    select 1
    from roles r
    where r.code = seed.code
      and r.is_system = true
);

delete from role_permissions rp
using roles r
where rp.role_id = r.id
  and r.is_system = true
  and r.code in (
      'environment_admin',
      'environment_developer',
      'environment_viewer',
      'environment_auditor'
  );

insert into role_permissions (role_id, permission_id)
select r.id, p.id
from (
    values
        ('environment_admin', 'env:read'),
        ('environment_admin', 'env:update'),
        ('environment_admin', 'folder:create'),
        ('environment_admin', 'folder:read'),
        ('environment_admin', 'folder:update'),
        ('environment_admin', 'folder:delete'),
        ('environment_admin', 'secret:list'),
        ('environment_admin', 'secret:search'),
        ('environment_admin', 'secret:read'),
        ('environment_admin', 'secret:reveal'),
        ('environment_admin', 'secret:create'),
        ('environment_admin', 'secret:update'),
        ('environment_admin', 'secret:delete'),
        ('environment_admin', 'rbac:binding:read'),
        ('environment_admin', 'rbac:binding:manage'),
        ('environment_admin', 'audit:read'),

        ('environment_developer', 'env:read'),
        ('environment_developer', 'folder:read'),
        ('environment_developer', 'secret:list'),
        ('environment_developer', 'secret:search'),
        ('environment_developer', 'secret:read'),
        ('environment_developer', 'secret:reveal'),
        ('environment_developer', 'secret:create'),
        ('environment_developer', 'secret:update'),

        ('environment_viewer', 'env:read'),
        ('environment_viewer', 'folder:read'),
        ('environment_viewer', 'secret:list'),
        ('environment_viewer', 'secret:search'),
        ('environment_viewer', 'secret:read'),

        ('environment_auditor', 'env:read'),
        ('environment_auditor', 'folder:read'),
        ('environment_auditor', 'secret:list'),
        ('environment_auditor', 'secret:read'),
        ('environment_auditor', 'audit:read')
) as mapping(role_code, permission_code)
join roles r
  on r.code = mapping.role_code
 and r.is_system = true
 and r.is_deleted = false
join permissions p
  on p.code = mapping.permission_code
 and p.is_system = true
on conflict do nothing;

do $$
declare
    missing_count integer;
begin
    select count(*)
    into missing_count
    from (
        values
            ('environment_admin', 16),
            ('environment_developer', 8),
            ('environment_viewer', 5),
            ('environment_auditor', 5)
    ) as expected(role_code, permission_count)
    left join (
        select r.code, count(*)::integer as permission_count
        from roles r
        join role_permissions rp on rp.role_id = r.id
        where r.code in (
            'environment_admin',
            'environment_developer',
            'environment_viewer',
            'environment_auditor'
        )
          and r.is_system = true
          and r.is_deleted = false
        group by r.code
    ) actual
      on actual.code = expected.role_code
     and actual.permission_count = expected.permission_count
    where actual.code is null;

    if missing_count > 0 then
        raise exception 'environment role import failed: required permissions are missing';
    end if;
end
$$;

commit;

select
    r.code,
    r.scope_type,
    array_agg(p.code order by p.code) as permissions
from roles r
join role_permissions rp on rp.role_id = r.id
join permissions p on p.id = rp.permission_id
where r.code in (
    'environment_admin',
    'environment_developer',
    'environment_viewer',
    'environment_auditor'
)
  and r.is_system = true
  and r.is_deleted = false
group by r.code, r.scope_type
order by r.code;
