package relays

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/bropines/rdpanel/internal/store"
)

// PrefixRule represents a CIDR prefix to relay mapping for hbbs.
type PrefixRule struct {
	ID        int64     `json:"id"`
	CIDR      string    `json:"cidr"`
	RelayHost string    `json:"relay_host"`
	Comment   string    `json:"comment"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Validate checks that CIDR and RelayHost are well-formed.
func (r *PrefixRule) Validate() error {
	cidr := strings.TrimSpace(r.CIDR)
	if cidr == "" {
		return errors.New("cidr is required")
	}
	prefix, err := netip.ParsePrefix(cidr)
	if err != nil {
		// Also allow bare IP treated as /32 or /128
		addr, aerr := netip.ParseAddr(cidr)
		if aerr != nil {
			return fmt.Errorf("invalid CIDR %q: %w", cidr, err)
		}
		prefix = netip.PrefixFrom(addr, addr.BitLen())
	}
	r.CIDR = prefix.String()

	host := strings.TrimSpace(r.RelayHost)
	if host == "" {
		return errors.New("relay_host is required")
	}
	r.RelayHost = host
	r.Comment = strings.TrimSpace(r.Comment)
	return nil
}

// ListPrefixRules returns all prefix routing rules ordered by id.
func (s *Store) ListPrefixRules(ctx context.Context) ([]PrefixRule, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT id, cidr, relay_host, comment, created_at, updated_at
		FROM relay_prefix_rules ORDER BY id ASC`)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()

	var out []PrefixRule
	for rows.Next() {
		var (
			r       PrefixRule
			created int64
			updated int64
		)
		if err := rows.Scan(&r.ID, &r.CIDR, &r.RelayHost, &r.Comment, &created, &updated); err != nil {
			return nil, mapErr(err)
		}
		r.CreatedAt = time.UnixMilli(created).UTC()
		r.UpdatedAt = time.UnixMilli(updated).UTC()
		out = append(out, r)
	}
	return out, mapErr(rows.Err())
}

// CreatePrefixRule validates and inserts a new prefix rule.
func (s *Store) CreatePrefixRule(ctx context.Context, r *PrefixRule) error {
	if err := r.Validate(); err != nil {
		return err
	}
	now := time.Now().UTC()
	r.CreatedAt = now
	r.UpdatedAt = now

	res, err := s.DB.ExecContext(ctx, `INSERT INTO relay_prefix_rules (cidr, relay_host, comment, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?)`,
		r.CIDR, r.RelayHost, r.Comment, now.UnixMilli(), now.UnixMilli())
	if err != nil {
		return mapErr(err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return mapErr(err)
	}
	r.ID = id
	return nil
}

// DeletePrefixRule deletes a prefix rule by id.
func (s *Store) DeletePrefixRule(ctx context.Context, id int64) error {
	res, err := s.DB.ExecContext(ctx, `DELETE FROM relay_prefix_rules WHERE id = ?`, id)
	if err != nil {
		return mapErr(err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return mapErr(err)
	}
	if n == 0 {
		return store.ErrNotFound
	}
	return nil
}

// FormatRelayMap formats prefix rules into the relay_map.txt line format (<cidr> <relay_host>).
func FormatRelayMap(rules []PrefixRule) string {
	var b strings.Builder
	for _, r := range rules {
		cidr := strings.TrimSpace(r.CIDR)
		host := strings.TrimSpace(r.RelayHost)
		if cidr == "" || host == "" {
			continue
		}
		b.WriteString(cidr)
		b.WriteByte(' ')
		b.WriteString(host)
		b.WriteByte('\n')
	}
	return b.String()
}

// WriteRelayMapFile writes relay_map.txt atomically to dir.
func WriteRelayMapFile(dir string, content string) error {
	if dir == "" {
		return nil
	}
	path := filepath.Join(dir, "relay_map.txt")
	data := []byte(content)
	if old, err := os.ReadFile(path); err == nil && bytes.Equal(old, data) {
		return nil
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
