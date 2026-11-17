drop index if exists users_auth_subject_key;
alter table users drop column if exists link_state;
alter table users drop column if exists auth_subject;
alter table users drop column if exists auth_provider;
