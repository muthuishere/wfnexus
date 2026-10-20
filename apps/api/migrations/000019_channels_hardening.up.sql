-- Channels, hardened after review (ADR 0023).
--
-- pause_codes is rebuilt rather than altered: a code is now scoped to the
-- CHANNEL it was sent on, expires, and is retired when its pause resolves or
-- the step pauses again — so a replayed code can never answer a newer pause.
-- Codes are ephemeral by design, so dropping the old rows loses nothing a
-- person still needs: a pause announced before this migration is answered in
-- the UI or with `wfx approve`.
--
-- channel_messages.handled is false from the moment a message is stored until
-- it has been acted on, so a crash in between re-handles it on the next poll
-- instead of losing a "#code yes".
drop table if exists pause_codes;
create table pause_codes (
    channel     text        not null,
    code        text        not null,
    run_id      text        not null,
    step_id     text        not null,
    kind        text        not null default '',
    created_at  timestamptz not null,
    expires_at  timestamptz not null,
    retired_at  timestamptz,
    primary key (channel, code)
);
create index if not exists pause_codes_step_idx on pause_codes (run_id, step_id);

alter table channel_messages add column if not exists handled boolean not null default true;
create index if not exists channel_messages_pending_idx on channel_messages (channel, handled);
