package relays

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	"github.com/bropines/rdpanel/internal/hbbs/control"
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

// Validate checks and normalizes a rule the way hbbs reads relay_map.txt
// (one "<cidr> <host>[,<host>...]" line per rule): the CIDR is stored masked
// ("10.1.2.3/8" -> "10.0.0.0/8", a bare IP becomes /32 or /128) and every
// relay host must be a single host[:port] without whitespace, so a rule can
// never smuggle extra lines into the file.
func (r *PrefixRule) Validate() error {
	cidr := strings.TrimSpace(r.CIDR)
	if cidr == "" {
		return fmt.Errorf("%w: cidr is required", store.ErrInvalid)
	}
	prefix, err := netip.ParsePrefix(cidr)
	if err != nil {
		// Also allow bare IP treated as /32 or /128
		addr, aerr := netip.ParseAddr(cidr)
		if aerr != nil || addr.Zone() != "" {
			return fmt.Errorf("%w: invalid CIDR %q", store.ErrInvalid, cidr)
		}
		prefix = netip.PrefixFrom(addr, addr.BitLen())
	}
	r.CIDR = prefix.Masked().String()

	host := strings.TrimSpace(r.RelayHost)
	if host == "" {
		return fmt.Errorf("%w: relay_host is required", store.ErrInvalid)
	}
	hosts, err := control.NormalizeRelayServers(strings.Split(host, ","))
	if err != nil || len(hosts) == 0 || strings.ContainsFunc(host, unicode.IsControl) {
		return fmt.Errorf("%w: relay_host must be host[:port] (several separated by commas, no spaces)", store.ErrInvalid)
	}
	r.RelayHost = strings.Join(hosts, ",")
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
	return s.CreatePrefixRules(ctx, []*PrefixRule{r})
}

// CreatePrefixRules validates and inserts rules in one transaction (all or
// nothing).
func (s *Store) CreatePrefixRules(ctx context.Context, rules []*PrefixRule) error {
	for _, r := range rules {
		if err := r.Validate(); err != nil {
			return err
		}
	}
	now := time.Now().UTC()
	return mapErr(s.DB.WithTx(ctx, func(tx *sql.Tx) error {
		q := s.DB.Wrap(tx)
		for _, r := range rules {
			r.CreatedAt, r.UpdatedAt = now, now
			if err := q.QueryRowContext(ctx, `INSERT INTO relay_prefix_rules (cidr, relay_host, comment, created_at, updated_at)
				VALUES (?, ?, ?, ?, ?) RETURNING id`,
				r.CIDR, r.RelayHost, r.Comment, now.UnixMilli(), now.UnixMilli()).Scan(&r.ID); err != nil {
				return err
			}
		}
		return nil
	}))
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

// FormatRelayMap formats prefix rules into the relay_map.txt line format
// (<cidr> <relay_host>). Rules that do not validate (e.g. rows stored before
// validation was tightened) are skipped rather than written verbatim.
func FormatRelayMap(rules []PrefixRule) string {
	var b strings.Builder
	for _, r := range rules {
		if err := r.Validate(); err != nil {
			continue
		}
		b.WriteString(r.CIDR)
		b.WriteByte(' ')
		b.WriteString(r.RelayHost)
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
