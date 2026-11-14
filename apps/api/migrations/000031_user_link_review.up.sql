-- For reviewing accounts bound on their first sign-in after the subject
-- change: the name the person claimed (username or email) when the account
-- was bound, and a subject an admin rejected for it, which may not bind it
-- again. link_state gains 'linked': bound with no roles, signing in, still
-- open to review (`wfx-server link list|confirm|reject`).
alter table users add column if not exists auth_claim    text not null default '';
alter table users add column if not exists link_rejected text not null default '';
