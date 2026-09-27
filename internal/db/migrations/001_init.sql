CREATE TABLE users (
    id            bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    email         text        NOT NULL UNIQUE, -- stored lowercased
    password_hash text        NOT NULL,        -- argon2id PHC string
    created_at    timestamptz NOT NULL DEFAULT now()
);

-- Only SHA-256 hashes of refresh tokens are stored. Tokens rotate on every use;
-- presenting a used token revokes its whole family.
CREATE TABLE refresh_tokens (
    token_hash bytea       PRIMARY KEY,
    user_id    bigint      NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    client_id  text        NOT NULL,
    family_id  uuid        NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    expires_at timestamptz NOT NULL,
    used_at    timestamptz,
    revoked_at timestamptz
);
CREATE INDEX refresh_tokens_family_idx ON refresh_tokens (family_id);
CREATE INDEX refresh_tokens_expires_idx ON refresh_tokens (expires_at);

-- state is the packed game state (see internal/game/codec.go). seq is kept as a
-- column so answers can be applied with an optimistic "WHERE seq = expected".
CREATE TABLE kids (
    id             bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    user_id        bigint      NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    name           text        NOT NULL,
    seq            integer     NOT NULL DEFAULT 0,
    last_answer_at timestamptz,
    state          bytea       NOT NULL,
    created_at     timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX kids_user_idx ON kids (user_id);

-- One row per kid per UTC day; events is the packed answer log
-- (see internal/game/events.go).
CREATE TABLE answer_log (
    kid_id     bigint      NOT NULL REFERENCES kids (id) ON DELETE CASCADE,
    day        date        NOT NULL,
    first_seq  integer     NOT NULL,
    started_at timestamptz NOT NULL,
    events     bytea       NOT NULL,
    PRIMARY KEY (kid_id, day)
);
