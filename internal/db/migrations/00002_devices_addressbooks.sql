-- Schema v1, part 2: devices, address books, share links.

-- +goose Up

CREATE TABLE device_groups (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    name       TEXT    NOT NULL CHECK (length(name) BETWEEN 1 AND 128),
    note       TEXT    NOT NULL DEFAULT '',
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
);
CREATE UNIQUE INDEX ux_device_groups_name ON device_groups (name COLLATE NOCASE);

-- One row per RustDesk ID (legacy "peers", deduplicated).
CREATE TABLE devices (
    id                 INTEGER PRIMARY KEY AUTOINCREMENT,
    rustdesk_id        TEXT    NOT NULL CHECK (length(rustdesk_id) BETWEEN 1 AND 100),
    -- Client machine uuid (base64). Changes are restricted (see SPEC section 3).
    uuid               TEXT    NULL,
    hostname           TEXT    NOT NULL DEFAULT '',
    os                 TEXT    NOT NULL DEFAULT '',
    cpu                TEXT    NOT NULL DEFAULT '',
    memory             TEXT    NOT NULL DEFAULT '',
    os_username        TEXT    NOT NULL DEFAULT '',
    client_version     TEXT    NOT NULL DEFAULT '',
    alias              TEXT    NOT NULL DEFAULT '',
    note               TEXT    NOT NULL DEFAULT '',
    owner_user_id      INTEGER NULL REFERENCES users (id) ON DELETE SET NULL,
    device_group_id    INTEGER NULL REFERENCES device_groups (id) ON DELETE SET NULL,
    -- Last observation by any signal (hbbs presence or heartbeat), throttled writes.
    last_seen_at       INTEGER NULL,
    last_seen_ip       TEXT    NULL,
    last_heartbeat_at  INTEGER NULL,
    -- Last sysinfo upload.
    sysinfo_at         INTEGER NULL,
    -- Set when the server wants a sysinfo re-upload; the heartbeat answers with
    -- the "sysinfo" key while sysinfo_at is older than this.
    sysinfo_requested_at INTEGER NULL,
    created_at         INTEGER NOT NULL,
    updated_at         INTEGER NOT NULL
);
CREATE UNIQUE INDEX ux_devices_rustdesk_id ON devices (rustdesk_id);
CREATE INDEX ix_devices_uuid ON devices (uuid) WHERE uuid IS NOT NULL;
CREATE INDEX ix_devices_owner_user_id ON devices (owner_user_id);
CREATE INDEX ix_devices_device_group_id ON devices (device_group_id);
CREATE INDEX ix_devices_last_seen_at ON devices (last_seen_at);

CREATE TABLE address_books (
    id                   INTEGER PRIMARY KEY AUTOINCREMENT,
    -- Stable public id used by clients (?ab=<guid>); never embeds a group id.
    guid                 TEXT    NOT NULL CHECK (length(guid) BETWEEN 1 AND 64),
    owner_user_id        INTEGER NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    name                 TEXT    NOT NULL DEFAULT '',
    note                 TEXT    NOT NULL DEFAULT '',
    is_personal          INTEGER NOT NULL DEFAULT 0 CHECK (is_personal IN (0, 1)),
    -- Legacy collection id (0 = legacy personal book); NULL for new books.
    legacy_collection_id INTEGER NULL,
    -- Legacy "<group_id>-<user_id>-<collection_id>" guid, still accepted.
    legacy_guid          TEXT    NULL,
    -- Shared books: default connection password advertised in shared/profiles
    -- ("info.password"); AEAD("address_book.password:<id>").
    default_password_enc BLOB    NULL,
    created_at           INTEGER NOT NULL,
    updated_at           INTEGER NOT NULL
);
CREATE UNIQUE INDEX ux_address_books_guid ON address_books (guid);
CREATE UNIQUE INDEX ux_address_books_legacy_guid ON address_books (legacy_guid) WHERE legacy_guid IS NOT NULL;
CREATE UNIQUE INDEX ux_address_books_personal ON address_books (owner_user_id) WHERE is_personal = 1;
CREATE INDEX ix_address_books_owner ON address_books (owner_user_id);

-- Sharing rules: exactly one grantee (user or user group) per row.
CREATE TABLE address_book_shares (
    id               INTEGER PRIMARY KEY AUTOINCREMENT,
    address_book_id  INTEGER NOT NULL REFERENCES address_books (id) ON DELETE CASCADE,
    grantee_user_id  INTEGER NULL REFERENCES users (id) ON DELETE CASCADE,
    grantee_group_id INTEGER NULL REFERENCES user_groups (id) ON DELETE CASCADE,
    permission       TEXT    NOT NULL CHECK (permission IN ('read', 'read_write', 'full')),
    created_at       INTEGER NOT NULL,
    updated_at       INTEGER NOT NULL,
    CHECK ((grantee_user_id IS NULL) <> (grantee_group_id IS NULL))
);
CREATE UNIQUE INDEX ux_ab_shares_user ON address_book_shares (address_book_id, grantee_user_id) WHERE grantee_user_id IS NOT NULL;
CREATE UNIQUE INDEX ux_ab_shares_group ON address_book_shares (address_book_id, grantee_group_id) WHERE grantee_group_id IS NOT NULL;
CREATE INDEX ix_ab_shares_grantee_user ON address_book_shares (grantee_user_id) WHERE grantee_user_id IS NOT NULL;
CREATE INDEX ix_ab_shares_grantee_group ON address_book_shares (grantee_group_id) WHERE grantee_group_id IS NOT NULL;

CREATE TABLE address_book_entries (
    id                 INTEGER PRIMARY KEY AUTOINCREMENT,
    address_book_id    INTEGER NOT NULL REFERENCES address_books (id) ON DELETE CASCADE,
    rustdesk_id        TEXT    NOT NULL CHECK (length(rustdesk_id) BETWEEN 1 AND 100),
    alias              TEXT    NOT NULL DEFAULT '',
    os_username        TEXT    NOT NULL DEFAULT '',
    hostname           TEXT    NOT NULL DEFAULT '',
    platform           TEXT    NOT NULL DEFAULT '',
    login_name         TEXT    NOT NULL DEFAULT '',
    note               TEXT    NULL,
    -- AEAD("ab_entry.password:<id>") / AEAD("ab_entry.hash:<id>"); returned byte-for-byte.
    password_enc       BLOB    NULL,
    hash_enc           BLOB    NULL,
    force_always_relay INTEGER NOT NULL DEFAULT 0 CHECK (force_always_relay IN (0, 1)),
    rdp_port           TEXT    NOT NULL DEFAULT '',
    rdp_username       TEXT    NOT NULL DEFAULT '',
    same_server        INTEGER NOT NULL DEFAULT 0 CHECK (same_server IN (0, 1)),
    created_at         INTEGER NOT NULL,
    updated_at         INTEGER NOT NULL
);
CREATE UNIQUE INDEX ux_ab_entries_book_rustdesk_id ON address_book_entries (address_book_id, rustdesk_id);
CREATE INDEX ix_ab_entries_rustdesk_id ON address_book_entries (rustdesk_id);

CREATE TABLE address_book_tags (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    address_book_id INTEGER NOT NULL REFERENCES address_books (id) ON DELETE CASCADE,
    name            TEXT    NOT NULL CHECK (length(name) BETWEEN 1 AND 128),
    -- Flutter ARGB uint32 (e.g. 4288585374 = 0xFF9E9E9E).
    color_argb      INTEGER NOT NULL DEFAULT 0 CHECK (color_argb BETWEEN 0 AND 4294967295),
    created_at      INTEGER NOT NULL,
    updated_at      INTEGER NOT NULL
);
CREATE UNIQUE INDEX ux_ab_tags_book_name ON address_book_tags (address_book_id, name);

CREATE TABLE address_book_entry_tags (
    entry_id INTEGER NOT NULL REFERENCES address_book_entries (id) ON DELETE CASCADE,
    tag_id   INTEGER NOT NULL REFERENCES address_book_tags (id) ON DELETE CASCADE,
    PRIMARY KEY (entry_id, tag_id)
) WITHOUT ROWID;
CREATE INDEX ix_ab_entry_tags_tag_id ON address_book_entry_tags (tag_id);

-- A tag may only be attached to entries of the same address book.
-- +goose StatementBegin
CREATE TRIGGER trg_ab_entry_tags_same_book
BEFORE INSERT ON address_book_entry_tags
FOR EACH ROW
WHEN (SELECT address_book_id FROM address_book_entries WHERE id = NEW.entry_id)
     IS NOT (SELECT address_book_id FROM address_book_tags WHERE id = NEW.tag_id)
BEGIN
    SELECT RAISE(ABORT, 'address_book_entry_tags: entry and tag belong to different address books');
END;
-- +goose StatementEnd

-- Web-client share links (bearer URL); token stored hashed.
CREATE TABLE share_links (
    id                 INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id            INTEGER NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    device_rustdesk_id TEXT    NOT NULL,
    token_sha256       BLOB    NOT NULL CHECK (length(token_sha256) = 32),
    password_mode      TEXT    NOT NULL CHECK (password_mode IN ('once', 'fixed')),
    password_enc       BLOB    NULL,
    expires_at         INTEGER NULL,
    used_at            INTEGER NULL,
    created_at         INTEGER NOT NULL,
    updated_at         INTEGER NOT NULL
);
CREATE UNIQUE INDEX ux_share_links_token_sha256 ON share_links (token_sha256);
CREATE INDEX ix_share_links_user_id ON share_links (user_id);

-- +goose Down
DROP TABLE share_links;
DROP TRIGGER trg_ab_entry_tags_same_book;
DROP TABLE address_book_entry_tags;
DROP TABLE address_book_tags;
DROP TABLE address_book_entries;
DROP TABLE address_book_shares;
DROP TABLE address_books;
DROP TABLE devices;
DROP TABLE device_groups;
