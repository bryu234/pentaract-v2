CREATE TABLE IF NOT EXISTS schema_migrations (
    version integer PRIMARY KEY,
    applied_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS users (
    id uuid PRIMARY KEY,
    email text NOT NULL UNIQUE,
    password_hash text NOT NULL,
    totp_secret_enc bytea NOT NULL,
    totp_enabled boolean NOT NULL DEFAULT false,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS recovery_codes (
    id uuid PRIMARY KEY,
    user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    code_hash bytea NOT NULL,
    used_at timestamptz
);

CREATE TABLE IF NOT EXISTS sessions (
    id uuid PRIMARY KEY,
    user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    token_hash bytea NOT NULL UNIQUE,
    csrf_token text NOT NULL,
    expires_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS telegram_settings (
    id smallint PRIMARY KEY DEFAULT 1 CHECK (id = 1),
    storage_name text NOT NULL,
    chat_id bigint NOT NULL,
    bot_user_id bigint NOT NULL,
    bot_username text NOT NULL,
    bot_token_enc bytea NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TYPE node_kind AS ENUM ('file', 'folder');
CREATE TYPE file_state AS ENUM ('uploading', 'queued', 'processing', 'ready', 'failed');

CREATE TABLE IF NOT EXISTS nodes (
    id uuid PRIMARY KEY,
    parent_id uuid REFERENCES nodes(id) ON DELETE CASCADE,
    name text NOT NULL,
    kind node_kind NOT NULL,
    size bigint NOT NULL DEFAULT 0,
    state file_state,
    wrapped_key bytea,
    plain_hash bytea,
    error_message text,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    deleted_at timestamptz,
    purge_after timestamptz,
    CHECK ((kind = 'folder' AND state IS NULL) OR (kind = 'file' AND state IS NOT NULL))
);

CREATE UNIQUE INDEX IF NOT EXISTS nodes_active_name_unique
ON nodes (COALESCE(parent_id, '00000000-0000-0000-0000-000000000000'::uuid), lower(name))
WHERE deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS nodes_parent_idx ON nodes(parent_id) WHERE deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS nodes_trash_idx ON nodes(purge_after) WHERE deleted_at IS NOT NULL;

CREATE TABLE IF NOT EXISTS upload_sessions (
    id uuid PRIMARY KEY,
    node_id uuid NOT NULL UNIQUE REFERENCES nodes(id) ON DELETE CASCADE,
    temp_path text NOT NULL,
    expected_size bigint NOT NULL,
    received_size bigint NOT NULL DEFAULT 0,
    expires_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS file_chunks (
    id uuid PRIMARY KEY,
    node_id uuid NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
    position integer NOT NULL,
    plain_offset bigint NOT NULL,
    plain_size bigint NOT NULL,
    cipher_size bigint NOT NULL,
    nonce_prefix bytea NOT NULL,
    plain_hash bytea NOT NULL,
    cipher_hash bytea NOT NULL,
    telegram_file_id text NOT NULL,
    telegram_file_unique_id text NOT NULL,
    telegram_message_id bigint NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE(node_id, position)
);

