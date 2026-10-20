-- +goose Up

CREATE TABLE ldap_settings (
    id                      BIGINT  PRIMARY KEY CHECK (id = 1),
    enabled                 BIGINT  NOT NULL DEFAULT 0 CHECK (enabled IN (0, 1)),
    server_url              TEXT    NOT NULL DEFAULT '',
    start_tls               BIGINT  NOT NULL DEFAULT 0 CHECK (start_tls IN (0, 1)),
    insecure_skip_verify    BIGINT  NOT NULL DEFAULT 0 CHECK (insecure_skip_verify IN (0, 1)),
    bind_dn                 TEXT    NOT NULL DEFAULT '',
    bind_password_enc       BYTEA   NULL,
    base_dn                 TEXT    NOT NULL DEFAULT '',
    user_search_base        TEXT    NOT NULL DEFAULT '',
    user_filter             TEXT    NOT NULL DEFAULT '(&(objectClass=user)(sAMAccountName=%s))',
    username_attribute      TEXT    NOT NULL DEFAULT 'sAMAccountName',
    display_name_attribute  TEXT    NOT NULL DEFAULT 'displayName',
    email_attribute         TEXT    NOT NULL DEFAULT 'mail',
    group_attribute         TEXT    NOT NULL DEFAULT 'memberOf',
    group_mappings          TEXT    NOT NULL DEFAULT '{}',
    default_user_group_id   BIGINT  NULL REFERENCES user_groups (id) ON DELETE SET NULL,
    auto_register           BIGINT  NOT NULL DEFAULT 1 CHECK (auto_register IN (0, 1)),
    sync_interval_mins      BIGINT  NOT NULL DEFAULT 0,
    last_sync_at            BIGINT  NULL,
    last_sync_status        TEXT    NULL,
    last_sync_count         BIGINT  NOT NULL DEFAULT 0,
    updated_at              BIGINT  NOT NULL DEFAULT 0
);

INSERT INTO ldap_settings (
    id, enabled, server_url, start_tls, insecure_skip_verify,
    bind_dn, base_dn, user_search_base, user_filter,
    username_attribute, display_name_attribute, email_attribute, group_attribute,
    group_mappings, auto_register, sync_interval_mins, updated_at
) VALUES (
    1, 0, '', 0, 0,
    '', '', '', '(&(objectClass=user)(sAMAccountName=%s))',
    'sAMAccountName', 'displayName', 'mail', 'memberOf',
    '{}', 1, 0, 0
);

ALTER TABLE users ADD COLUMN is_ldap BIGINT NOT NULL DEFAULT 0 CHECK (is_ldap IN (0, 1));
ALTER TABLE users ADD COLUMN ldap_dn TEXT NULL;
CREATE INDEX ix_users_ldap_dn ON users (ldap_dn) WHERE ldap_dn IS NOT NULL;

-- +goose Down
DROP INDEX IF EXISTS ix_users_ldap_dn;
ALTER TABLE users DROP COLUMN ldap_dn;
ALTER TABLE users DROP COLUMN is_ldap;
DROP TABLE IF EXISTS ldap_settings;
