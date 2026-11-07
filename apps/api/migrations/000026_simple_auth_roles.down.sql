drop table if exists members;
update users set role = case role
    when 'superadmin'       then 'admin'
    when 'legacy:publisher' then 'publisher'
    else 'viewer' end;
