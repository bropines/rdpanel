package store

import (
	"database/sql"
	"errors"

	"github.com/bropines/rustdesk-panel/internal/db"
)

// Repository errors. Wrap-checked with errors.Is.
var (
	// ErrNotFound means the requested row does not exist (or is not active).
	ErrNotFound = errors.New("store: not found")
	// ErrConflict means a uniqueness constraint was violated.
	ErrConflict = errors.New("store: conflict")
	// ErrReferenced means a foreign key constraint was violated (missing
	// parent or a row still referenced with ON DELETE RESTRICT).
	ErrReferenced = errors.New("store: foreign key violation")
	// ErrInvalid means a CHECK constraint rejected the values.
	ErrInvalid = errors.New("store: invalid value")
)

// mapErr converts driver errors into repository errors, preserving the cause.
func mapErr(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, sql.ErrNoRows):
		return ErrNotFound
	case db.IsUniqueViolation(err):
		return errors.Join(ErrConflict, err)
	case db.IsForeignKeyViolation(err):
		return errors.Join(ErrReferenced, err)
	case db.IsCheckViolation(err):
		return errors.Join(ErrInvalid, err)
	default:
		return err
	}
}

// expectOne returns ErrNotFound when an UPDATE/DELETE touched no rows.
func expectOne(res sql.Result, err error) error {
	if err != nil {
		return mapErr(err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}
