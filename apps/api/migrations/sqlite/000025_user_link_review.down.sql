DROP INDEX IF EXISTS users_auth_subject_lower_idx;
DROP INDEX IF EXISTS users_lower_name_idx;
DROP TABLE IF EXISTS link_rejections;
ALTER TABLE users DROP COLUMN auth_claim;
