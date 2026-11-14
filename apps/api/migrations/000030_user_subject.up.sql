-- A user who signs in through an external provider is matched on the
-- provider's stable id for the person — (auth_provider, auth_subject) — never
-- on a name or an email.
--
-- link_state:
--   ''        nothing pending
--   'legacy'  created by an external sign-in before this change: it is bound
--             to the subject that next signs in with its name, once
--   'confirm' bound, but it held roles: it signs in only after an admin
--             confirms the link (`wfx-server link confirm NAME`)
alter table users add column if not exists auth_provider text not null default '';
alter table users add column if not exists auth_subject  text not null default '';
alter table users add column if not exists link_state    text not null default '';
create unique index if not exists users_auth_subject_key on users (auth_provider, auth_subject) where auth_subject <> '';
update users set link_state = 'legacy' where password_hash = '' and position('@' in name) > 0 and auth_subject = '';
