package legacy

import (
	"database/sql"
	"fmt"
	"unicode/utf8"

	"github.com/bropines/rdpanel/internal/crypto"
)

type peerRow struct {
	rowID                                                             int64
	id, cpu, hostname, memory, os, username, uuid, version, ip, alias string
	userID, lastOnline, groupID                                       int64
	created, updated                                                  int64 // ms; 0 when unknown
	createdOK                                                         bool
}

// peers -> devices, deduplicated by RustDesk id.
func (im *importer) importPeers() error {
	c := im.count("peers")
	groups := map[string][]*peerRow{}
	var order []string
	err := im.eachRow("peers", `SELECT row_id, COALESCE(CAST(id AS TEXT), ''), COALESCE(CAST(cpu AS TEXT), ''),
		COALESCE(CAST(hostname AS TEXT), ''), COALESCE(CAST(memory AS TEXT), ''), COALESCE(CAST(os AS TEXT), ''),
		COALESCE(CAST(username AS TEXT), ''), COALESCE(CAST(uuid AS TEXT), ''), COALESCE(CAST(version AS TEXT), ''),
		COALESCE(CAST(user_id AS INTEGER), 0), COALESCE(CAST(last_online_time AS INTEGER), 0),
		COALESCE(CAST(last_online_ip AS TEXT), ''), COALESCE(CAST(group_id AS INTEGER), 0), COALESCE(CAST(alias AS TEXT), ''),
		CAST(created_at AS TEXT), CAST(updated_at AS TEXT) FROM peers ORDER BY row_id`, func(r *sql.Rows) error {
		p := &peerRow{}
		var created, updated sql.NullString
		if err := r.Scan(&p.rowID, &p.id, &p.cpu, &p.hostname, &p.memory, &p.os, &p.username, &p.uuid, &p.version,
			&p.userID, &p.lastOnline, &p.ip, &p.groupID, &p.alias, &created, &updated); err != nil {
			return err
		}
		if p.id == "" || utf8.RuneCountInString(p.id) > 100 {
			c.Skipped++
			im.rep.warn("invalid_rustdesk_id", "peers", p.rowID, "peer id %q is empty or too long; skipped", p.id)
			return nil
		}
		p.created, p.createdOK = im.ms("peers", created)
		p.updated, _ = im.ms("peers", updated)
		if _, ok := groups[p.id]; !ok {
			order = append(order, p.id)
		}
		groups[p.id] = append(groups[p.id], p)
		return nil
	})
	if err != nil {
		return err
	}
	for _, rid := range order {
		rows := groups[rid]
		// Winner: greatest last_online_time, then updated_at, then row_id.
		best := rows[0]
		for _, p := range rows[1:] {
			if p.lastOnline > best.lastOnline ||
				(p.lastOnline == best.lastOnline && (p.updated > best.updated ||
					(p.updated == best.updated && p.rowID > best.rowID))) {
				best = p
			}
		}
		m := *best
		created, updated := int64(0), int64(0)
		for _, p := range rows {
			if p == best {
				continue
			}
			// Merge: fill fields that are empty on the winner.
			fill := func(dst *string, src string) {
				if *dst == "" {
					*dst = src
				}
			}
			fill(&m.cpu, p.cpu)
			fill(&m.hostname, p.hostname)
			fill(&m.memory, p.memory)
			fill(&m.os, p.os)
			fill(&m.username, p.username)
			fill(&m.uuid, p.uuid)
			fill(&m.version, p.version)
			fill(&m.ip, p.ip)
			fill(&m.alias, p.alias)
			if m.userID == 0 {
				m.userID = p.userID
			}
			if m.groupID == 0 {
				m.groupID = p.groupID
			}
		}
		for _, p := range rows {
			if p.createdOK && (created == 0 || p.created < created) {
				created = p.created
			}
			if p.updated > updated {
				updated = p.updated
			}
		}
		if created == 0 {
			created = updated
		}
		if created == 0 {
			created = im.nowMs
		}
		if updated == 0 {
			updated = created
		}
		if len(rows) > 1 {
			c.Deduped += len(rows) - 1
			ids := make([]int64, 0, len(rows))
			for _, p := range rows {
				ids = append(ids, p.rowID)
			}
			im.rep.warn("duplicate_peer", "peers", best.rowID, "RustDesk id %s had %d rows %v; merged into row %d", rid, len(rows), ids, best.rowID)
		}
		owner := sql.NullInt64{}
		if m.userID != 0 {
			if im.users[m.userID] != nil {
				owner = sql.NullInt64{Int64: m.userID, Valid: true}
			} else {
				c.extra("orphan_owner_cleared", 1)
				im.rep.warn("orphan_peer_owner", "peers", best.rowID, "device %s references missing user %d; owner cleared", rid, m.userID)
			}
		}
		group := sql.NullInt64{}
		if m.groupID != 0 {
			if im.deviceGroups[m.groupID] {
				group = sql.NullInt64{Int64: m.groupID, Valid: true}
			} else {
				c.extra("orphan_group_cleared", 1)
				im.rep.warn("orphan_peer_group", "peers", best.rowID, "device %s references missing device group %d; cleared", rid, m.groupID)
			}
		}
		if _, err := im.exec(`INSERT INTO devices (rustdesk_id, uuid, hostname, os, cpu, memory, os_username, client_version,
			alias, owner_user_id, device_group_id, last_seen_at, last_seen_ip, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			rid, nullStr(m.uuid), m.hostname, m.os, m.cpu, m.memory, m.username, m.version, m.alias, owner, group,
			unixSecMs(m.lastOnline), nullStr(m.ip), created, updated); err != nil {
			return err
		}
		c.Imported++
	}
	return nil
}

func legacyGUID(groupID, userID, collectionID int64) string {
	return fmt.Sprintf("%d-%d-%d", groupID, userID, collectionID)
}

// address_book_collections -> address_books (ids kept), then one personal
// book per imported user (new ids after the legacy collection sequence).
func (im *importer) importCollections() error {
	c := im.count("address_book_collections")
	err := im.eachRow("address_book_collections", `SELECT id, COALESCE(CAST(user_id AS INTEGER), 0),
		COALESCE(CAST(name AS TEXT), ''), CAST(created_at AS TEXT), CAST(updated_at AS TEXT)
		FROM address_book_collections ORDER BY id`, func(r *sql.Rows) error {
		var id, userID int64
		var name string
		var created, updated sql.NullString
		if err := r.Scan(&id, &userID, &name, &created, &updated); err != nil {
			return err
		}
		u := im.users[userID]
		if u == nil {
			c.Orphaned++
			im.rep.warn("orphan_collection", "address_book_collections", id, "collection %q references missing user %d; dropped with its entries", name, userID)
			return nil
		}
		cr, up := im.stamps("address_book_collections", created, updated)
		if _, err := im.exec(`INSERT INTO address_books (id, guid, owner_user_id, name, is_personal, legacy_collection_id,
			legacy_guid, created_at, updated_at) VALUES (?, ?, ?, ?, 0, ?, ?, ?, ?)`,
			id, legacyUUID("address_book_collections", id), userID, name, id, legacyGUID(u.legacyGroupID, userID, id), cr, up); err != nil {
			return err
		}
		im.collections[id] = id
		c.Imported++
		return nil
	})
	if err != nil {
		return err
	}
	if err := im.bumpSequence("address_books", "address_book_collections"); err != nil {
		return err
	}
	for _, uid := range sortedKeys(im.users) {
		u := im.users[uid]
		id, err := im.insertID(`INSERT INTO address_books (guid, owner_user_id, name, is_personal, legacy_collection_id,
			legacy_guid, created_at, updated_at) VALUES (?, ?, 'Personal', 1, 0, ?, ?, ?)`,
			legacyUUID("personal_address_book", uid), uid, legacyGUID(u.legacyGroupID, uid, 0), im.nowMs, im.nowMs)
		if err != nil {
			return err
		}
		im.personalBook[uid] = id
	}
	c.extra("personal_books_created", len(im.personalBook))
	return nil
}

// bookFor resolves the target address book of a legacy (user_id, collection_id).
func (im *importer) bookFor(userID, collectionID int64) (int64, bool) {
	if collectionID == 0 {
		id, ok := im.personalBook[userID]
		return id, ok
	}
	id, ok := im.collections[collectionID]
	return id, ok
}

var permissions = map[int64]string{1: "read", 2: "read_write", 3: "full"}

// address_book_collection_rules -> address_book_shares (dedupe keeps the max rule).
func (im *importer) importRules() error {
	c := im.count("address_book_collection_rules")
	type key struct {
		book, grantee int64
		group         bool
	}
	type share struct {
		key
		ruleID, rule     int64
		created, updated int64
	}
	byKey := map[key]*share{}
	var order []key
	err := im.eachRow("address_book_collection_rules", `SELECT id, COALESCE(CAST(collection_id AS INTEGER), 0),
		COALESCE(CAST(rule AS INTEGER), 0), COALESCE(CAST(type AS INTEGER), 0), COALESCE(CAST(to_id AS INTEGER), 0),
		CAST(created_at AS TEXT), CAST(updated_at AS TEXT) FROM address_book_collection_rules ORDER BY id`, func(r *sql.Rows) error {
		var id, collID, rule, typ, toID int64
		var created, updated sql.NullString
		if err := r.Scan(&id, &collID, &rule, &typ, &toID, &created, &updated); err != nil {
			return err
		}
		if permissions[rule] == "" {
			c.Skipped++
			if rule != 0 {
				im.rep.warn("unknown_rule", "address_book_collection_rules", id, "rule %d is not 1..3; dropped", rule)
			}
			return nil
		}
		book, ok := im.collections[collID]
		if !ok {
			c.Orphaned++
			return nil
		}
		k := key{book: book, grantee: toID}
		switch typ {
		case 1:
			if im.users[toID] == nil {
				c.Orphaned++
				return nil
			}
		case 2:
			if !im.groups[toID] {
				c.Orphaned++
				return nil
			}
			k.group = true
		default:
			c.Skipped++
			im.rep.warn("unknown_rule_type", "address_book_collection_rules", id, "rule type %d is not 1 (user) or 2 (group); dropped", typ)
			return nil
		}
		cr, up := im.stamps("address_book_collection_rules", created, updated)
		if prev, dup := byKey[k]; dup {
			c.Deduped++
			if rule > prev.rule {
				prev.rule, prev.ruleID, prev.updated = rule, id, up
			}
			return nil
		}
		byKey[k] = &share{key: k, ruleID: id, rule: rule, created: cr, updated: up}
		order = append(order, k)
		return nil
	})
	if err != nil {
		return err
	}
	for _, k := range order {
		s := byKey[k]
		var userID, groupID sql.NullInt64
		if k.group {
			groupID = sql.NullInt64{Int64: k.grantee, Valid: true}
		} else {
			userID = sql.NullInt64{Int64: k.grantee, Valid: true}
		}
		if _, err := im.exec(`INSERT INTO address_book_shares (address_book_id, grantee_user_id, grantee_group_id, permission,
			created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)`, k.book, userID, groupID, permissions[s.rule], s.created, s.updated); err != nil {
			return err
		}
		c.Imported++
	}
	return nil
}

// normaliseColor maps a legacy tag colour to 0..2^32-1.
func normaliseColor(v int64) (int64, bool) {
	switch {
	case v >= 0 && v <= 0xFFFFFFFF:
		return v, true
	case v < 0 && v >= -0x80000000: // int32 sign wrap
		return v & 0xFFFFFFFF, true
	default:
		return DefaultTagColor, false
	}
}

// tags -> address_book_tags, deduplicated by (book, name), latest wins.
func (im *importer) importTags() error {
	c := im.count("tags")
	type tag struct {
		id, book, color, created, updated int64
		name                              string
	}
	byKey := map[tagKey]*tag{}
	var order []tagKey
	err := im.eachRow("tags", `SELECT id, COALESCE(CAST(name AS TEXT), ''), COALESCE(CAST(user_id AS INTEGER), 0),
		COALESCE(CAST(color AS INTEGER), 0), COALESCE(CAST(collection_id AS INTEGER), 0),
		CAST(created_at AS TEXT), CAST(updated_at AS TEXT) FROM tags ORDER BY id`, func(r *sql.Rows) error {
		var id, userID, color, collID int64
		var name string
		var created, updated sql.NullString
		if err := r.Scan(&id, &name, &userID, &color, &collID, &created, &updated); err != nil {
			return err
		}
		if name == "" || utf8.RuneCountInString(name) > 128 {
			c.Skipped++
			im.rep.warn("invalid_tag_name", "tags", id, "tag name %q is empty or longer than 128 characters; skipped", name)
			return nil
		}
		book, ok := im.bookFor(userID, collID)
		if !ok {
			c.Orphaned++
			return nil
		}
		col, ok := normaliseColor(color)
		if !ok {
			im.rep.warn("invalid_tag_color", "tags", id, "tag colour %d is out of range; default used", color)
		}
		cr, up := im.stamps("tags", created, updated)
		t := &tag{id: id, book: book, color: col, created: cr, updated: up, name: name}
		k := tagKey{book, name}
		if prev, dup := byKey[k]; dup {
			c.Deduped++
			if t.updated >= prev.updated {
				byKey[k] = t
			}
			return nil
		}
		byKey[k] = t
		order = append(order, k)
		return nil
	})
	if err != nil {
		return err
	}
	for _, k := range order {
		t := byKey[k]
		id, err := im.insertID(`INSERT INTO address_book_tags (address_book_id, name, color_argb, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?)`, t.book, t.name, t.color, t.created, t.updated)
		if err != nil {
			return err
		}
		im.tagIDs[k] = id
		c.Imported++
	}
	return nil
}

// address_books -> address_book_entries (+ tags from the JSON column).
func (im *importer) importEntries() error {
	c := im.count("address_books")
	type entry struct {
		rowID, book, created, updated                                 int64
		id, username, password, hostname, alias, platform, tags, hash string
		rdpPort, rdpUser, loginName                                   string
		relay, sameServer                                             bool
	}
	type key struct {
		book int64
		rid  string
	}
	byKey := map[key]*entry{}
	var order []key
	err := im.eachRow("address_books", `SELECT row_id, COALESCE(CAST(id AS TEXT), ''), COALESCE(CAST(username AS TEXT), ''),
		COALESCE(CAST(password AS TEXT), ''), COALESCE(CAST(hostname AS TEXT), ''), COALESCE(CAST(alias AS TEXT), ''),
		COALESCE(CAST(platform AS TEXT), ''), COALESCE(CAST(tags AS TEXT), ''), COALESCE(CAST(hash AS TEXT), ''),
		COALESCE(CAST(user_id AS INTEGER), 0), CAST(force_always_relay AS TEXT), COALESCE(CAST(rdp_port AS TEXT), ''),
		COALESCE(CAST(rdp_username AS TEXT), ''), COALESCE(CAST(login_name AS TEXT), ''), CAST(same_server AS TEXT),
		COALESCE(CAST(collection_id AS INTEGER), 0), CAST(created_at AS TEXT), CAST(updated_at AS TEXT)
		FROM address_books ORDER BY row_id`, func(r *sql.Rows) error {
		e := &entry{}
		var userID, collID int64
		var relay, same, created, updated sql.NullString
		if err := r.Scan(&e.rowID, &e.id, &e.username, &e.password, &e.hostname, &e.alias, &e.platform, &e.tags, &e.hash,
			&userID, &relay, &e.rdpPort, &e.rdpUser, &e.loginName, &same, &collID, &created, &updated); err != nil {
			return err
		}
		book, ok := im.bookFor(userID, collID)
		if !ok {
			c.Orphaned++
			im.rep.warn("orphan_ab_entry", "address_books", e.rowID,
				"entry %s references missing user %d / collection %d; dropped", e.id, userID, collID)
			return nil
		}
		if e.id == "" || utf8.RuneCountInString(e.id) > 100 {
			c.Skipped++
			im.rep.warn("invalid_rustdesk_id", "address_books", e.rowID, "entry id %q is empty or too long; skipped", e.id)
			return nil
		}
		e.book = book
		e.relay, e.sameServer = parseBool(relay), parseBool(same)
		e.created, e.updated = im.stamps("address_books", created, updated)
		k := key{book, e.id}
		if prev, dup := byKey[k]; dup {
			c.Deduped++
			im.rep.warn("duplicate_ab_entry", "address_books", e.rowID, "entry %s duplicates row %d in the same book; the most recently updated row was kept", e.id, prev.rowID)
			if e.updated >= prev.updated {
				byKey[k] = e
			}
			return nil
		}
		byKey[k] = e
		order = append(order, k)
		return nil
	})
	if err != nil {
		return err
	}
	tagsCreated, invalidTags := 0, 0
	for _, k := range order {
		e := byKey[k]
		id, err := im.insertID(`INSERT INTO address_book_entries (address_book_id, rustdesk_id, alias, os_username, hostname,
			platform, login_name, force_always_relay, rdp_port, rdp_username, same_server, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			e.book, e.id, e.alias, e.username, e.hostname, e.platform, e.loginName, boolInt(e.relay), e.rdpPort, e.rdpUser,
			boolInt(e.sameServer), e.created, e.updated)
		if err != nil {
			return err
		}
		// Secrets: AEAD over the exact legacy bytes; AD binds them to this entry id.
		var pwEnc, hashEnc []byte
		if e.password != "" {
			if pwEnc, err = im.opts.AEAD.SealString(e.password, crypto.ABEntryPasswordAD(id)); err != nil {
				return err
			}
		}
		if e.hash != "" {
			if hashEnc, err = im.opts.AEAD.SealString(e.hash, crypto.ABEntryHashAD(id)); err != nil {
				return err
			}
		}
		if pwEnc != nil || hashEnc != nil {
			if _, err := im.exec(`UPDATE address_book_entries SET password_enc = ?, hash_enc = ? WHERE id = ?`, pwEnc, hashEnc, id); err != nil {
				return err
			}
		}
		names, ok := parseTagNames(e.tags)
		if !ok {
			invalidTags++
			im.rep.warn("invalid_tags_json", "address_books", e.rowID, "tags column is not a JSON array (%q); imported without tags", truncate(e.tags, 80))
		}
		for _, name := range names {
			if utf8.RuneCountInString(name) > 128 {
				im.rep.warn("invalid_tag_name", "address_books", e.rowID, "tag name longer than 128 characters dropped")
				continue
			}
			tk := tagKey{e.book, name}
			tagID, exists := im.tagIDs[tk]
			if !exists {
				if tagID, err = im.insertID(`INSERT INTO address_book_tags (address_book_id, name, color_argb, created_at, updated_at)
					VALUES (?, ?, ?, ?, ?)`, e.book, name, DefaultTagColor, e.created, e.created); err != nil {
					return err
				}
				im.tagIDs[tk] = tagID
				tagsCreated++
			}
			if _, err := im.exec(`INSERT INTO address_book_entry_tags (entry_id, tag_id) VALUES (?, ?)`, id, tagID); err != nil {
				return err
			}
		}
		c.Imported++
	}
	if tagsCreated > 0 {
		c.extra("tags_created_from_entries", tagsCreated)
	}
	if invalidTags > 0 {
		c.extra("invalid_tags_json", invalidTags)
	}
	return nil
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
