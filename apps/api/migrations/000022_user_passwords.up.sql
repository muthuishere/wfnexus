-- PASSWORD SIGN-IN (ADR 0025). A user may hold an argon2id hash of a
-- password; the value itself is never stored. must_change forces the next
-- password sign-in to set a new one (first admin, admin resets). `disabled`
-- already exists (000010_identity).
alter table users add column if not exists password_hash text not null default '';
alter table users add column if not exists must_change boolean not null default false;
