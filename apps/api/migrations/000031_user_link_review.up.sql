-- For reviewing accounts bound on their first sign-in after the subject
-- change: the name the person claimed (username or email) when the account
-- was bound, and every subject an admin rejected for an account, none of
-- which may bind it again. link_state gains 'linked': bound with no roles,
-- signing in, still open to review (`wfx-server link list|confirm|reject`).
alter table users add column if not exists auth_claim text not null default '';
create table if not exists link_rejections (
    user_id    uuid        not null references users(id) on delete cascade,
    provider   text        not null,
    subject    text        not null,
    created_at timestamptz not null default now(),
    primary key (user_id, provider, subject)
);
