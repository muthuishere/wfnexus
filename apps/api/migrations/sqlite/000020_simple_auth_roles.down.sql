DROP TABLE IF EXISTS members;
UPDATE users SET role = CASE role
    WHEN 'superadmin'       THEN 'admin'
    WHEN 'legacy:publisher' THEN 'publisher'
    ELSE 'viewer' END;
