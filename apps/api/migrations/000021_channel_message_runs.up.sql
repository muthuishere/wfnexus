-- A message starts a given workflow AT MOST ONCE (ADR 0023).
--
-- Handling a received message is "start the run, then mark the message
-- handled". A crash between the two left the message pending, and the next
-- poll started the workflow again. The claim row is taken BEFORE the run is
-- created, in one INSERT that only one caller can win; a re-handled message
-- finds it and starts nothing. The cost is the other direction — a crash
-- after the claim and before the run means the run never starts — and that is
-- the right way round for a message that may say "deploy".
create table if not exists channel_message_runs (
    channel    text        not null,
    msg_id     text        not null,
    workflow   text        not null,
    run_id     text        not null default '',
    created_at timestamptz not null default now(),
    primary key (channel, msg_id, workflow)
);
