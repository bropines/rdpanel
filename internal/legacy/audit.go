package legacy

import (
	"database/sql"
	"strings"
	"unicode/utf8"

	"github.com/bropines/rdpanel/internal/crypto"
)

// login_logs -> login_events. user_token_id holds a user id (legacy bug), so
// token_id is always NULL. Also sets users.last_login_at.
func (im *importer) importLoginLogs() error {
	c := im.count("login_logs")
	lastLogin := map[int64]int64{}
	unknownType, orphanUser := 0, 0
	err := im.eachRow("login_logs", `SELECT id, COALESCE(CAST(user_id AS INTEGER), 0), COALESCE(CAST(client AS TEXT), ''),
		COALESCE(CAST(device_id AS TEXT), ''), COALESCE(CAST(uuid AS TEXT), ''), COALESCE(CAST(ip AS TEXT), ''),
		COALESCE(CAST(type AS TEXT), ''), COALESCE(CAST(platform AS TEXT), ''), COALESCE(CAST(is_deleted AS INTEGER), 0),
		CAST(created_at AS TEXT), CAST(updated_at AS TEXT) FROM login_logs ORDER BY id`, func(r *sql.Rows) error {
		var id, userID, deleted int64
		var client, deviceID, uuid, ip, typ, platform string
		var created, updated sql.NullString
		if err := r.Scan(&id, &userID, &client, &deviceID, &uuid, &ip, &typ, &platform, &deleted, &created, &updated); err != nil {
			return err
		}
		method := "password"
		switch strings.ToLower(strings.TrimSpace(typ)) {
		case "account":
		case "oauth":
			method = "oidc" // webauth logins were also logged as "oauth"
		default:
			unknownType++
		}
		user := sql.NullInt64{}
		if userID != 0 && im.users[userID] != nil {
			user = sql.NullInt64{Int64: userID, Valid: true}
		} else if userID != 0 {
			orphanUser++
		}
		cr, _ := im.stamps("login_logs", created, updated)
		if _, err := im.exec(`INSERT INTO login_events (user_id, token_id, method, result, client_kind, device_rustdesk_id,
			device_uuid, platform, ip, hidden_by_user, created_at) VALUES (?, NULL, ?, 'success', ?, ?, ?, ?, ?, ?, ?)`,
			user, method, nullStr(client), nullStr(deviceID), nullStr(uuid), nullStr(platform), nullStr(ip),
			boolInt(deleted != 0), cr); err != nil {
			return err
		}
		if user.Valid && cr > lastLogin[userID] {
			lastLogin[userID] = cr
		}
		c.Imported++
		return nil
	})
	if err != nil {
		return err
	}
	if unknownType > 0 {
		c.extra("unknown_type_as_password", unknownType)
		im.rep.warn("unknown_login_type", "login_logs", nil, "%d row(s) with a type other than account/oauth imported as method 'password'", unknownType)
	}
	if orphanUser > 0 {
		c.extra("user_cleared", orphanUser)
		im.rep.warn("orphan_login_user", "login_logs", nil, "%d row(s) reference deleted users; kept with user_id NULL", orphanUser)
	}
	for _, uid := range sortedKeys(lastLogin) {
		if _, err := im.exec(`UPDATE users SET last_login_at = ? WHERE id = ?`, lastLogin[uid], uid); err != nil {
			return err
		}
	}
	return nil
}

// audit_conns -> connection_audits. guid = UUIDv5 of the legacy id (stable
// across re-runs); session_id copied as exact text.
func (im *importer) importAuditConns() error {
	c := im.count("audit_conns")
	return im.eachRow("audit_conns", `SELECT id, COALESCE(CAST(conn_id AS INTEGER), 0), COALESCE(CAST(peer_id AS TEXT), ''),
		COALESCE(CAST(from_peer AS TEXT), ''), COALESCE(CAST(from_name AS TEXT), ''), COALESCE(CAST(ip AS TEXT), ''),
		COALESCE(CAST(session_id AS TEXT), ''), COALESCE(CAST(type AS INTEGER), 0), COALESCE(CAST(uuid AS TEXT), ''),
		COALESCE(CAST(close_time AS INTEGER), 0), CAST(created_at AS TEXT), CAST(updated_at AS TEXT)
		FROM audit_conns ORDER BY id`, func(r *sql.Rows) error {
		var id, connID, typ, closeTime int64
		var peerID, fromPeer, fromName, ip, sessionID, uuid string
		var created, updated sql.NullString
		if err := r.Scan(&id, &connID, &peerID, &fromPeer, &fromName, &ip, &sessionID, &typ, &uuid, &closeTime,
			&created, &updated); err != nil {
			return err
		}
		cr, up := im.stamps("audit_conns", created, updated)
		if _, err := im.exec(`INSERT INTO connection_audits (guid, device_rustdesk_id, device_uuid, conn_id, session_id,
			conn_type, controller_rustdesk_id, controller_name, controller_ip, started_at, ended_at, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			legacyUUID("audit_conns", id), peerID, nullStr(uuid), connID, sessionID, typ, fromPeer, fromName, ip,
			cr, unixSecMs(closeTime), cr, up); err != nil {
			return err
		}
		c.Imported++
		return nil
	})
}

// audit_files -> file_transfer_audits.
func (im *importer) importAuditFiles() error {
	c := im.count("audit_files")
	wrapped := 0
	err := im.eachRow("audit_files", `SELECT id, COALESCE(CAST(from_peer AS TEXT), ''), COALESCE(CAST(info AS TEXT), ''),
		CAST(is_file AS TEXT), COALESCE(CAST(path AS TEXT), ''), COALESCE(CAST(peer_id AS TEXT), ''),
		COALESCE(CAST(type AS INTEGER), 0), COALESCE(CAST(uuid AS TEXT), ''), COALESCE(CAST(ip AS TEXT), ''),
		COALESCE(CAST(num AS INTEGER), 0), COALESCE(CAST(from_name AS TEXT), ''), CAST(created_at AS TEXT),
		CAST(updated_at AS TEXT) FROM audit_files ORDER BY id`, func(r *sql.Rows) error {
		var id, typ, num int64
		var fromPeer, info, path, peerID, uuid, ip, fromName string
		var isFile, created, updated sql.NullString
		if err := r.Scan(&id, &fromPeer, &info, &isFile, &path, &peerID, &typ, &uuid, &ip, &num, &fromName,
			&created, &updated); err != nil {
			return err
		}
		infoJSON, ok := normaliseAuditInfo(info)
		if !ok {
			wrapped++
		}
		cr, _ := im.stamps("audit_files", created, updated)
		if _, err := im.exec(`INSERT INTO file_transfer_audits (device_rustdesk_id, device_uuid, controller_rustdesk_id,
			controller_name, controller_ip, direction, is_file, path, file_count, info, created_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			peerID, nullStr(uuid), fromPeer, fromName, ip, typ, boolInt(parseBool(isFile)), path, num, infoJSON, cr); err != nil {
			return err
		}
		c.Imported++
		return nil
	})
	if err != nil {
		return err
	}
	if wrapped > 0 {
		c.extra("info_wrapped_raw", wrapped)
		im.rep.warn("invalid_audit_info", "audit_files", nil, "%d row(s) had non-JSON info; stored as {\"raw\": ...}", wrapped)
	}
	return nil
}

// share_records -> share_links (token hashed, password encrypted).
func (im *importer) importShareRecords() error {
	c := im.count("share_records")
	seen := map[string]bool{}
	return im.eachRow("share_records", `SELECT id, COALESCE(CAST(user_id AS INTEGER), 0), COALESCE(CAST(peer_id AS TEXT), ''),
		COALESCE(CAST(share_token AS TEXT), ''), COALESCE(CAST(password_type AS TEXT), ''), COALESCE(CAST(password AS TEXT), ''),
		COALESCE(CAST(expire AS INTEGER), 0), CAST(created_at AS TEXT), CAST(updated_at AS TEXT)
		FROM share_records ORDER BY id`, func(r *sql.Rows) error {
		var id, userID, expire int64
		var peerID, token, pwType, password string
		var created, updated sql.NullString
		if err := r.Scan(&id, &userID, &peerID, &token, &pwType, &password, &expire, &created, &updated); err != nil {
			return err
		}
		if im.users[userID] == nil {
			c.Orphaned++
			return nil
		}
		mode := strings.ToLower(strings.TrimSpace(pwType))
		if token == "" || (mode != "once" && mode != "fixed") {
			c.Skipped++
			im.rep.warn("invalid_share_record", "share_records", id, "share record has an empty token or password type %q; skipped", pwType)
			return nil
		}
		if seen[token] {
			c.Deduped++
			return nil
		}
		seen[token] = true
		cr, up := im.stamps("share_records", created, updated)
		expires := sql.NullInt64{}
		if expire > 0 {
			expires = sql.NullInt64{Int64: cr + expire*1000, Valid: true}
			if expires.Int64 <= im.nowMs {
				c.Skipped++
				c.extra("expired", 1)
				return nil
			}
		}
		id2, err := im.insertID(`INSERT INTO share_links (user_id, device_rustdesk_id, token_sha256, password_mode, expires_at,
			created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
			userID, peerID, crypto.HashToken(token), mode, expires, cr, up)
		if err != nil {
			return err
		}
		if password != "" {
			enc, err := im.opts.AEAD.SealString(password, crypto.ShareLinkPasswordAD(id2))
			if err != nil {
				return err
			}
			if _, err := im.exec(`UPDATE share_links SET password_enc = ? WHERE id = ?`, enc, id2); err != nil {
				return err
			}
		}
		c.Imported++
		return nil
	})
}

// server_cmds -> server_commands (target "21115" hbbs, "21117" hbbr).
func (im *importer) importServerCmds() error {
	c := im.count("server_cmds")
	type key struct{ target, cmd string }
	seen := map[key]bool{}
	return im.eachRow("server_cmds", `SELECT id, COALESCE(CAST(cmd AS TEXT), ''), COALESCE(CAST(alias AS TEXT), ''),
		COALESCE(CAST("option" AS TEXT), ''), COALESCE(CAST("explain" AS TEXT), ''), COALESCE(CAST(target AS TEXT), ''),
		CAST(created_at AS TEXT), CAST(updated_at AS TEXT) FROM server_cmds ORDER BY id`, func(r *sql.Rows) error {
		var id int64
		var cmd, alias, option, explain, target string
		var created, updated sql.NullString
		if err := r.Scan(&id, &cmd, &alias, &option, &explain, &target, &created, &updated); err != nil {
			return err
		}
		var t string
		switch strings.TrimSpace(target) {
		case "21115", "hbbs":
			t = "hbbs"
		case "21117", "hbbr":
			t = "hbbr"
		default:
			c.Skipped++
			im.rep.warn("unknown_cmd_target", "server_cmds", id, "command %q has unknown target %q; skipped", cmd, target)
			return nil
		}
		if cmd == "" || utf8.RuneCountInString(cmd) > 64 {
			c.Skipped++
			im.rep.warn("invalid_cmd", "server_cmds", id, "command %q is empty or longer than 64 characters; skipped", cmd)
			return nil
		}
		k := key{t, cmd}
		if seen[k] {
			c.Deduped++
			return nil
		}
		seen[k] = true
		cr, up := im.stamps("server_cmds", created, updated)
		if _, err := im.exec(`INSERT INTO server_commands (target, cmd, alias, option_hint, description, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?)`, t, cmd, alias, option, explain, cr, up); err != nil {
			return err
		}
		c.Imported++
		return nil
	})
}
