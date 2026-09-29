-- See migrations/000015_model_prices.up.sql for what this table is.
CREATE TABLE IF NOT EXISTS model_prices (
    model      text    PRIMARY KEY,
    price_in   real    NOT NULL,
    price_out  real    NOT NULL,
    seeded     boolean NOT NULL DEFAULT 1,
    updated_at timestamp NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M:%f', 'now'))
);
