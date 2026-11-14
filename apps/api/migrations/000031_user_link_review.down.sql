drop index if exists users_auth_subject_lower_idx;
drop index if exists users_lower_name_idx;
drop table if exists link_rejections;
alter table users drop column if exists auth_claim;
