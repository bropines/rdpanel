-- Schema v1, part 3: audit logs, hbbs/hbbr control, settings, migration runs.

-- +goose Up

CREATE TABLE login_events (
    id                 BIGSERIAL PRIMARY KEY,
    -- NULL for failed attempts against unknown usernames (or deleted users).
    user_id BIGINT NULL REFERENCES users (id) ON DELETE SET NULL,
    username_attempted TEXT    NULL,
    token_id BIGINT NULL REFERENCES auth_tokens (id) ON DELETE SET NULL,
    method             TEXT    NOT NULL CHECK (method IN ('password', 'oidc', 'webauth')),
    result             TEXT    NOT NULL DEFAULT 'success' CHECK (result IN ('success', 'failure')),
    failure_reason     TEXT    NULL,
    -- Legacy "client": webadmin | client | webclient | RustDesk deviceInfo.type.
    client_kind        TEXT    NULL,
    device_rustdesk_id TEXT    NULL,
    device_uuid        TEXT    NULL,
    platform           TEXT    NULL,
    ip                 TEXT    NULL,
    user_agent         TEXT    NULL,
    hidden_by_user     INTEGER NOT NULL DEFAULT 0 CHECK (hidden_by_user IN (0, 1)),
    created_at BIGINT NOT NULL
);
CREATE INDEX ix_login_events_created_at ON login_events (created_at);
CREATE INDEX ix_login_events_user_created ON login_events (user_id, created_at);

-- Posted by the controlled client to /api/audit/conn.
CREATE TABLE connection_audits (
    id                     BIGSERIAL PRIMARY KEY,
    -- Opaque id returned to the controller by GET /api/audit/conn/active and
    -- echoed by PUT /api/audit (session note).
    guid                   TEXT    NOT NULL CHECK (length(guid) BETWEEN 1 AND 64),
    device_rustdesk_id     TEXT    NOT NULL,
    device_uuid            TEXT    NULL,
    -- Client-local connection counter (not globally unique).
    conn_id                INTEGER NOT NULL DEFAULT 0,
    -- u64 kept as text (legacy values above 2^53 are already lossy).
    session_id             TEXT    NOT NULL DEFAULT '',
    -- 0 remote desktop, 1 file transfer, 2 port forward, 3 view camera, 4 terminal.
    conn_type              INTEGER NOT NULL DEFAULT 0,
    -- 1 click-to-accept, 2 temporary password, 3 permanent password, 4 switch sides.
    primary_auth           INTEGER NULL,
    -- 1 TOTP, 2 trusted device.
    two_factor             INTEGER NULL,
    -- Opaque hbbs-supplied reference (ControlledContext), when present.
    conn_audit_ref         TEXT    NULL,
    controller_rustdesk_id TEXT    NOT NULL DEFAULT '',
    controller_name        TEXT    NOT NULL DEFAULT '',
    controller_ip          TEXT    NOT NULL DEFAULT '',
    note                   TEXT    NOT NULL DEFAULT '',
    started_at BIGINT NOT NULL,
    ended_at BIGINT NULL,
    created_at BIGINT NOT NULL,
    updated_at BIGINT NOT NULL
);
CREATE UNIQUE INDEX ux_connection_audits_guid ON connection_audits (guid);
CREATE INDEX ix_connection_audits_device_conn ON connection_audits (device_rustdesk_id, conn_id);
CREATE INDEX ix_connection_audits_device_session ON connection_audits (device_rustdesk_id, session_id);
CREATE INDEX ix_connection_audits_started_at ON connection_audits (started_at);
CREATE INDEX ix_connection_audits_controller ON connection_audits (controller_rustdesk_id);

-- Posted by the controlled client to /api/audit/file.
CREATE TABLE file_transfer_audits (
    id                     BIGSERIAL PRIMARY KEY,
    device_rustdesk_id     TEXT    NOT NULL,
    device_uuid            TEXT    NULL,
    controller_rustdesk_id TEXT    NOT NULL DEFAULT '',
    controller_name        TEXT    NOT NULL DEFAULT '',
    controller_ip          TEXT    NOT NULL DEFAULT '',
    -- 0 = remote->local, 1 = local->remote (RustDesk FileAuditType).
    direction              INTEGER NOT NULL DEFAULT 0,
    is_file                INTEGER NOT NULL DEFAULT 0 CHECK (is_file IN (0, 1)),
    path                   TEXT    NOT NULL DEFAULT '',
    file_count             INTEGER NOT NULL DEFAULT 0,
    -- Raw client JSON ({ip,name,num,files:[...]}); non-JSON legacy rows wrapped as {"raw": "..."}.
    info                   TEXT    NOT NULL DEFAULT '{}' ,
    created_at BIGINT NOT NULL
);
CREATE INDEX ix_file_transfer_audits_created_at ON file_transfer_audits (created_at);
CREATE INDEX ix_file_transfer_audits_device ON file_transfer_audits (device_rustdesk_id);

-- Posted by the controlled client to /api/audit/alarm (client >= 1.5.0).
CREATE TABLE alarm_audits (
    id                 BIGSERIAL PRIMARY KEY,
    device_rustdesk_id TEXT    NOT NULL,
    device_uuid        TEXT    NULL,
    -- 0 IP whitelist block, 1 >30 failed attempts, 2 6 attempts/min, 6 IPv6
    -- prefix attempts, 7/8 terminal OS-login limits, 9 scope violation, 10 ID whitelist block.
    alarm_type         INTEGER NOT NULL,
    conn_id            INTEGER NULL,
    conn_audit_ref     TEXT    NULL,
    info               TEXT    NOT NULL DEFAULT '{}' ,
    created_at BIGINT NOT NULL
);
CREATE INDEX ix_alarm_audits_created_at ON alarm_audits (created_at);
CREATE INDEX ix_alarm_audits_device ON alarm_audits (device_rustdesk_id);

-- Client audit retry de-duplication (clients >= 1.5.0 send a UUID nonce and
-- expect the server to remember it for >= 5 minutes).
CREATE TABLE audit_nonces (
    nonce      TEXT    PRIMARY KEY CHECK (length(nonce) BETWEEN 1 AND 64),
    created_at BIGINT NOT NULL
) WITHOUT ROWID;
CREATE INDEX ix_audit_nonces_created_at ON audit_nonces (created_at);

-- Every admin mutation (panel or CLI).
CREATE TABLE admin_audit (
    id             BIGSERIAL PRIMARY KEY,
    -- NULL for CLI/system actions or deleted users; actor_name is a snapshot.
    actor_user_id  INTEGER NULL REFERENCES users (id) ON DELETE SET NULL,
    actor_name     TEXT    NOT NULL DEFAULT '',
    actor_ip       TEXT    NULL,
    -- Dotted verb, e.g. "user.create", "device.delete", "server.command".
    action         TEXT    NOT NULL CHECK (length(action) BETWEEN 1 AND 64),
    target_type    TEXT    NULL,
    target_id      TEXT    NULL,
    -- Structured details; never contains secrets.
    details        TEXT    NOT NULL DEFAULT '{}' ,
    created_at BIGINT NOT NULL
);
CREATE INDEX ix_admin_audit_created_at ON admin_audit (created_at);
CREATE INDEX ix_admin_audit_actor ON admin_audit (actor_user_id, created_at);
CREATE INDEX ix_admin_audit_target ON admin_audit (target_type, target_id);

-- Saved custom hbbs/hbbr console commands (legacy server_cmds).
CREATE TABLE server_commands (
    id          BIGSERIAL PRIMARY KEY,
    target      TEXT    NOT NULL CHECK (target IN ('hbbs', 'hbbr')),
    cmd         TEXT    NOT NULL CHECK (length(cmd) BETWEEN 1 AND 64),
    alias       TEXT    NOT NULL DEFAULT '',
    option_hint TEXT    NOT NULL DEFAULT '',
    description TEXT    NOT NULL DEFAULT '',
    created_at BIGINT NOT NULL,
    updated_at BIGINT NOT NULL
);
CREATE UNIQUE INDEX ux_server_commands_target_cmd ON server_commands (target, cmd);

-- Desired hbbs/hbbr runtime state, re-applied by the reconciler.
CREATE TABLE server_settings (
    target          TEXT    NOT NULL CHECK (target IN ('hbbs', 'hbbr')),
    -- e.g. relay_servers, always_use_relay, must_login, blacklist, blocklist,
    -- limit_speed, total_bandwidth, single_bandwidth, downgrade_threshold.
    key             TEXT    NOT NULL CHECK (length(key) BETWEEN 1 AND 64),
    value           TEXT    NOT NULL ,
    updated_by BIGINT NULL REFERENCES users (id) ON DELETE SET NULL,
    updated_at BIGINT NOT NULL,
    applied_at BIGINT NULL,
    last_error      TEXT    NULL,
    PRIMARY KEY (target, key)
) WITHOUT ROWID;

-- Panel settings (branding, retention, rate limits, ...). Values are JSON.
CREATE TABLE settings (
    key        TEXT    PRIMARY KEY CHECK (length(key) BETWEEN 1 AND 128),
    value      TEXT    NOT NULL ,
    updated_by BIGINT NULL REFERENCES users (id) ON DELETE SET NULL,
    updated_at BIGINT NOT NULL
) WITHOUT ROWID;

-- Legacy import bookkeeping (rdpanel migrate-legacy).
CREATE TABLE migration_runs (
    id             BIGSERIAL PRIMARY KEY,
    source         TEXT    NOT NULL,
    source_path    TEXT    NOT NULL DEFAULT '',
    source_version INTEGER NULL,
    dry_run        INTEGER NOT NULL DEFAULT 0 CHECK (dry_run IN (0, 1)),
    status         TEXT    NOT NULL CHECK (status IN ('running', 'succeeded', 'failed')),
    report         TEXT    NOT NULL DEFAULT '{}' ,
    started_at BIGINT NOT NULL,
    finished_at BIGINT NULL
);

-- +goose Down
DROP TABLE migration_runs;
DROP TABLE settings;
DROP TABLE server_settings;
DROP TABLE server_commands;
DROP TABLE admin_audit;
DROP TABLE audit_nonces;
DROP TABLE alarm_audits;
DROP TABLE file_transfer_audits;
DROP TABLE connection_audits;
DROP TABLE login_events;
