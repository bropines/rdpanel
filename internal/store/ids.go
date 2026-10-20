package store

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"strings"

	"github.com/bropines/rdpanel/internal/db"
)

// ErrNoSecrets is returned by repositories that store returnable secrets when
// the Store was built without WithSecrets.
var ErrNoSecrets = errors.New("store: secrets-at-rest key not configured")

// NewUUID returns a random RFC 4122 version-4 UUID in canonical form. It is the
// default generator for public ids (address-book guids, audit guids).
func NewUUID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("store: crypto/rand failed: " + err.Error())
	}
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	var out [36]byte
	hex.Encode(out[0:8], b[0:4])
	out[8] = '-'
	hex.Encode(out[9:13], b[4:6])
	out[13] = '-'
	hex.Encode(out[14:18], b[6:8])
	out[18] = '-'
	hex.Encode(out[19:23], b[8:10])
	out[23] = '-'
	hex.Encode(out[24:], b[10:])
	return string(out[:])
}

// insertWithID runs "INSERT INTO table (cols) VALUES (args) RETURNING id" and
// stores the new id in *id. A preset *id (legacy import) is inserted as is
// and the table's id sequence is moved past it; a zero *id leaves the column
// out so the database assigns one (PostgreSQL rejects an explicit NULL for an
// identity column, SQLite treats both forms alike).
func insertWithID(ctx context.Context, q db.Querier, table, cols string, id *int64, args ...any) error {
	preset := *id != 0
	if preset {
		cols = "id, " + cols
		args = append([]any{*id}, args...)
	}
	values := strings.TrimSuffix(strings.Repeat("?, ", len(args)), ", ")
	err := q.QueryRowContext(ctx, `INSERT INTO `+table+` (`+cols+`) VALUES (`+values+`) RETURNING id`, args...).Scan(id)
	if err != nil {
		return mapErr(err)
	}
	if preset {
		return db.SyncIDSequence(ctx, q, table)
	}
	return nil
}
