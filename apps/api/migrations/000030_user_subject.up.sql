-- A user who signs in through an external provider is matched on the
-- provider's stable id for the person — (auth_provider, auth_subject) — never
-- on a name or an email.
--
-- link_state:
--   ''        nothing pending
--   'legacy'  existed before this change, named like an external account
--             (<user>@<provider>, no password). What it becomes is decided at
--             sign-in by the provider whose name it carries: a redirect
--             provider (oidc, google, github) binds it once to the subject that
--             signs in with that name; an LDAP provider binds it from the LDAP
--             username, as it always matched; no provider of that name, never.
--   'confirm' bound, but it held roles: it signs in only after an admin
--             confirms the link (`wfx-server link confirm NAME`)
alter table users add column if not exists auth_provider text not null default '';
alter table users add column if not exists auth_subject  text not null default '';
alter table users add column if not exists link_state    text not null default '';
create unique index if not exists users_auth_subject_key on users (auth_provider, auth_subject) where auth_subject <> '';
update users set link_state = 'legacy' where password_hash = '' and position('@' in name) > 0 and auth_subject = '';
-- Their sessions, refresh tokens and API tokens end here: each signs in
-- again, through the binding above.
delete from user_tokens where user_id in (select id from users where link_state = 'legacy');
