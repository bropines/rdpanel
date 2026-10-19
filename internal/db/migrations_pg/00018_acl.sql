-- +goose Up

CREATE TABLE control_roles (
    id                   BIGSERIAL PRIMARY KEY,
    name                 TEXT    NOT NULL UNIQUE CHECK (length(name) BETWEEN 1 AND 64),
    note                 TEXT    NOT NULL DEFAULT '',
    allow_keyboard_mouse INTEGER NOT NULL DEFAULT 1 CHECK (allow_keyboard_mouse IN (0, 1)),
    allow_clipboard      INTEGER NOT NULL DEFAULT 1 CHECK (allow_clipboard IN (0, 1)),
    allow_file_transfer  INTEGER NOT NULL DEFAULT 1 CHECK (allow_file_transfer IN (0, 1)),
    allow_audio          INTEGER NOT NULL DEFAULT 1 CHECK (allow_audio IN (0, 1)),
    allow_tcp_tunneling  INTEGER NOT NULL DEFAULT 1 CHECK (allow_tcp_tunneling IN (0, 1)),
    allow_terminal       INTEGER NOT NULL DEFAULT 1 CHECK (allow_terminal IN (0, 1)),
    allow_restart        INTEGER NOT NULL DEFAULT 1 CHECK (allow_restart IN (0, 1)),
    is_default           INTEGER NOT NULL DEFAULT 0 CHECK (is_default IN (0, 1)),
    created_at BIGINT NOT NULL,
    updated_at BIGINT NOT NULL
);

-- Seed default built-in control roles:
INSERT INTO control_roles (name, note, allow_keyboard_mouse, allow_clipboard, allow_file_transfer, allow_audio, allow_tcp_tunneling, allow_terminal, allow_restart, is_default, created_at, updated_at)
VALUES (
    'Full Control',
    'Unrestricted session control with all capabilities enabled.',
    1, 1, 1, 1, 1, 1, 1,
    1,
    1729000000000,
    1729000000000
);

INSERT INTO control_roles (name, note, allow_keyboard_mouse, allow_clipboard, allow_file_transfer, allow_audio, allow_tcp_tunneling, allow_terminal, allow_restart, is_default, created_at, updated_at)
VALUES (
    'View Only',
    'View-only observation mode; remote keyboard, mouse, and file operations are blocked.',
    0, 0, 0, 1, 0, 0, 0,
    0,
    1729000000000,
    1729000000000
);

INSERT INTO control_roles (name, note, allow_keyboard_mouse, allow_clipboard, allow_file_transfer, allow_audio, allow_tcp_tunneling, allow_terminal, allow_restart, is_default, created_at, updated_at)
VALUES (
    'Support (No Files)',
    'Interactive troubleshooting without file transfer or terminal capabilities.',
    1, 1, 0, 1, 0, 0, 1,
    0,
    1729000000000,
    1729000000000
);

CREATE TABLE connection_acls (
    id              BIGSERIAL PRIMARY KEY,
    name            TEXT    NOT NULL CHECK (length(name) BETWEEN 1 AND 128),
    note            TEXT    NOT NULL DEFAULT '',
    user_id BIGINT NULL REFERENCES users (id) ON DELETE CASCADE,
    user_group_id BIGINT NULL REFERENCES user_groups (id) ON DELETE CASCADE,
    device_id BIGINT NULL REFERENCES devices (id) ON DELETE CASCADE,
    device_group_id BIGINT NULL REFERENCES device_groups (id) ON DELETE CASCADE,
    control_role_id BIGINT NULL REFERENCES control_roles (id) ON DELETE SET NULL,
    action          TEXT    NOT NULL CHECK (action IN ('allow', 'deny')),
    priority        INTEGER NOT NULL DEFAULT 100,
    enabled         INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0, 1)),
    created_at BIGINT NOT NULL,
    updated_at BIGINT NOT NULL
);

CREATE INDEX ix_connection_acls_priority ON connection_acls (priority, id);
CREATE INDEX ix_connection_acls_user_id ON connection_acls (user_id);
CREATE INDEX ix_connection_acls_user_group_id ON connection_acls (user_group_id);
CREATE INDEX ix_connection_acls_device_id ON connection_acls (device_id);
CREATE INDEX ix_connection_acls_device_group_id ON connection_acls (device_group_id);
CREATE INDEX ix_connection_acls_control_role_id ON connection_acls (control_role_id);

-- +goose Down
DROP INDEX IF EXISTS ix_connection_acls_control_role_id;
DROP INDEX IF EXISTS ix_connection_acls_device_group_id;
DROP INDEX IF EXISTS ix_connection_acls_device_id;
DROP INDEX IF EXISTS ix_connection_acls_user_group_id;
DROP INDEX IF EXISTS ix_connection_acls_user_id;
DROP INDEX IF EXISTS ix_connection_acls_priority;
DROP TABLE IF EXISTS connection_acls;
DROP TABLE IF EXISTS control_roles;
