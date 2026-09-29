-- What a model costs, per MILLION tokens in USD (ADR 0020).
--
-- Keyed by model FAMILY: a prefix of the model id's last path segment, the
-- longest matching row wins (`claude-sonnet` prices `anthropic/claude-sonnet-4.5`).
-- The row `*` is the fallback for a paid model no family matches.
--
-- Seeded on boot from the approximate table embedded in the binary
-- (internal/catalog/prices.json), inserting only families that are missing, so
-- an operator's edit is never overwritten. `seeded` says the row still holds
-- the shipped number rather than one a person set.
create table if not exists model_prices (
    model      text             primary key,
    price_in   double precision not null,
    price_out  double precision not null,
    seeded     boolean          not null default true,
    updated_at timestamptz      not null default now()
);
