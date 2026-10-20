package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/bropines/rdpanel/internal/db"
)

// ClientStrategy is an MDM configuration policy applied to RustDesk clients.
type ClientStrategy struct {
	ID            int64             `json:"id"`
	Name          string            `json:"name"`
	Note          string            `json:"note"`
	ConfigOptions map[string]string `json:"config_options"`
	IsDefault     bool              `json:"is_default"`
	DeviceCount   int               `json:"device_count"`
	CreatedAt     time.Time         `json:"created_at"`
	UpdatedAt     time.Time         `json:"updated_at"`
}

// ClientStrategies is the repository for client MDM strategies.
type ClientStrategies struct {
	q   db.Querier
	d   *db.DB
	now func() time.Time
}

func scanStrategy(row interface{ Scan(...any) error }) (*ClientStrategy, error) {
	var (
		s                    ClientStrategy
		optsRaw              string
		isDef                int
		createdAt, updatedAt int64
		count                sql.NullInt64
	)
	err := row.Scan(&s.ID, &s.Name, &s.Note, &optsRaw, &isDef, &createdAt, &updatedAt, &count)
	if err != nil {
		return nil, mapErr(err)
	}
	s.IsDefault = isDef == 1
	s.CreatedAt, s.UpdatedAt = fromMs(createdAt), fromMs(updatedAt)
	if count.Valid {
		s.DeviceCount = int(count.Int64)
	}
	if optsRaw != "" {
		if err := json.Unmarshal([]byte(optsRaw), &s.ConfigOptions); err != nil {
			s.ConfigOptions = make(map[string]string)
		}
	} else {
		s.ConfigOptions = make(map[string]string)
	}
	return &s, nil
}

const strategyColumnsWithCount = `s.id, s.name, s.note, s.config_options, s.is_default, s.created_at, s.updated_at,
	(SELECT count(*) FROM devices d
	 LEFT JOIN device_groups g ON g.id = d.device_group_id
	 WHERE d.strategy_id = s.id
	    OR (d.strategy_id IS NULL AND g.strategy_id = s.id)
	    OR (d.strategy_id IS NULL AND (g.strategy_id IS NULL OR d.device_group_id IS NULL) AND s.is_default = 1)
	) AS device_count`

// strategyColumnsLean is strategyColumnsWithCount without the device count
// (a scan of every device): the heartbeat path resolves strategies with it.
const strategyColumnsLean = `s.id, s.name, s.note, s.config_options, s.is_default, s.created_at, s.updated_at,
	CAST(NULL AS BIGINT) AS device_count`

// Create inserts s. If s.IsDefault is true, all other strategies are marked non-default.
func (r *ClientStrategies) Create(ctx context.Context, s *ClientStrategy) error {
	s.Name = strings.TrimSpace(s.Name)
	if s.Name == "" || len(s.Name) > 64 {
		return fmt.Errorf("%w: strategy name must be 1-64 characters", ErrInvalid)
	}
	if s.ConfigOptions == nil {
		s.ConfigOptions = make(map[string]string)
	}
	optsJSON, err := json.Marshal(s.ConfigOptions)
	if err != nil {
		return fmt.Errorf("%w: invalid config_options: %v", ErrInvalid, err)
	}

	now := r.now()
	if s.CreatedAt.IsZero() {
		s.CreatedAt = now
	}
	s.UpdatedAt = now

	// One transaction, insert first: a failed insert (e.g. a duplicate name)
	// leaves the current default in place.
	return atomic(ctx, r.d, r.q, func(q db.Querier) error {
		if err := q.QueryRowContext(ctx, `INSERT INTO client_strategies (name, note, config_options, is_default, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?) RETURNING id`,
			s.Name, s.Note, string(optsJSON), boolInt(s.IsDefault), toMs(s.CreatedAt), toMs(s.UpdatedAt)).Scan(&s.ID); err != nil {
			return mapErr(err)
		}
		if s.IsDefault {
			return clearOtherDefaults(ctx, q, "client_strategies", s.ID)
		}
		return nil
	})
}

// clearOtherDefaults unsets is_default on every row of table but id.
func clearOtherDefaults(ctx context.Context, q db.Querier, table string, id int64) error {
	_, err := q.ExecContext(ctx, `UPDATE `+table+` SET is_default = 0 WHERE is_default = 1 AND id <> ?`, id)
	return mapErr(err)
}

// Get returns the strategy with the given ID.
func (r *ClientStrategies) Get(ctx context.Context, id int64) (*ClientStrategy, error) {
	return scanStrategy(r.q.QueryRowContext(ctx, `SELECT `+strategyColumnsWithCount+` FROM client_strategies s WHERE s.id = ?`, id))
}

// GetByName returns the strategy with the given name (case-insensitive).
func (r *ClientStrategies) GetByName(ctx context.Context, name string) (*ClientStrategy, error) {
	return scanStrategy(r.q.QueryRowContext(ctx, `SELECT `+strategyColumnsWithCount+` FROM client_strategies s WHERE s.name = ? COLLATE NOCASE`, strings.TrimSpace(name)))
}

// GetDefault returns the default strategy (is_default = 1), or ErrNotFound.
func (r *ClientStrategies) GetDefault(ctx context.Context) (*ClientStrategy, error) {
	return scanStrategy(r.q.QueryRowContext(ctx, `SELECT `+strategyColumnsWithCount+` FROM client_strategies s WHERE s.is_default = 1 LIMIT 1`))
}

// List returns all strategies ordered by is_default DESC, name COLLATE NOCASE.
func (r *ClientStrategies) List(ctx context.Context) ([]*ClientStrategy, error) {
	rows, err := r.q.QueryContext(ctx, `SELECT `+strategyColumnsWithCount+` FROM client_strategies s ORDER BY s.is_default DESC, s.name COLLATE NOCASE`)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	var out []*ClientStrategy
	for rows.Next() {
		s, err := scanStrategy(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, mapErr(rows.Err())
}

// Update updates name, note, config_options, is_default for s.
func (r *ClientStrategies) Update(ctx context.Context, s *ClientStrategy) error {
	s.Name = strings.TrimSpace(s.Name)
	if s.Name == "" || len(s.Name) > 64 {
		return fmt.Errorf("%w: strategy name must be 1-64 characters", ErrInvalid)
	}
	if s.ConfigOptions == nil {
		s.ConfigOptions = make(map[string]string)
	}
	optsJSON, err := json.Marshal(s.ConfigOptions)
	if err != nil {
		return fmt.Errorf("%w: invalid config_options: %v", ErrInvalid, err)
	}

	s.UpdatedAt = r.now()

	// One transaction, the row first: updating a missing id (or a duplicate
	// name) leaves the current default in place.
	return atomic(ctx, r.d, r.q, func(q db.Querier) error {
		if err := expectOne(q.ExecContext(ctx, `UPDATE client_strategies SET name = ?, note = ?, config_options = ?, is_default = ?, updated_at = ? WHERE id = ?`,
			s.Name, s.Note, string(optsJSON), boolInt(s.IsDefault), toMs(s.UpdatedAt), s.ID)); err != nil {
			return err
		}
		if s.IsDefault {
			return clearOtherDefaults(ctx, q, "client_strategies", s.ID)
		}
		return nil
	})
}

// SetDefault marks strategy id as the default, unsetting all others. An
// unknown id is ErrNotFound and changes nothing.
func (r *ClientStrategies) SetDefault(ctx context.Context, id int64) error {
	now := toMs(r.now())
	return atomic(ctx, r.d, r.q, func(q db.Querier) error {
		if err := expectOne(q.ExecContext(ctx, `UPDATE client_strategies SET is_default = 1, updated_at = ? WHERE id = ?`, now, id)); err != nil {
			return err
		}
		return clearOtherDefaults(ctx, q, "client_strategies", id)
	})
}

// Delete removes strategy id. It returns ErrInvalid if the strategy is the default.
func (r *ClientStrategies) Delete(ctx context.Context, id int64) error {
	s, err := r.Get(ctx, id)
	if err != nil {
		return err
	}
	if s.IsDefault {
		return fmt.Errorf("%w: cannot delete the default strategy", ErrInvalid)
	}
	return expectOne(r.q.ExecContext(ctx, `DELETE FROM client_strategies WHERE id = ?`, id))
}

// ResolveForDevice resolves the effective MDM strategy for a device, or nil:
// 1. Device's own strategy_id (if set)
// 2. Device group's strategy_id (if device belongs to a group with strategy_id set)
// 3. System default strategy (is_default = 1), when there is one
//
// It runs on every heartbeat, so it reads no device counts.
func (r *ClientStrategies) ResolveForDevice(ctx context.Context, d *Device) (*ClientStrategy, error) {
	if d == nil {
		return nil, nil
	}
	// one returns the strategy the query selects, or nil.
	one := func(query string, args ...any) (*ClientStrategy, error) {
		s, err := scanStrategy(r.q.QueryRowContext(ctx, `SELECT `+strategyColumnsLean+query, args...))
		if errors.Is(err, ErrNotFound) {
			return nil, nil
		}
		return s, err
	}
	if d.StrategyID != nil && *d.StrategyID > 0 {
		if s, err := one(` FROM client_strategies s WHERE s.id = ?`, *d.StrategyID); s != nil || err != nil {
			return s, err
		}
	}
	if d.DeviceGroupID != nil && *d.DeviceGroupID > 0 {
		if s, err := one(` FROM device_groups g JOIN client_strategies s ON s.id = g.strategy_id WHERE g.id = ?`,
			*d.DeviceGroupID); s != nil || err != nil {
			return s, err
		}
	}
	return one(` FROM client_strategies s WHERE s.is_default = 1 ORDER BY s.id LIMIT 1`)
}

// ManagedKeys returns every option name that some strategy sets: on a device
// under a strategy the panel owns all of them.
func (r *ClientStrategies) ManagedKeys(ctx context.Context) (map[string]struct{}, error) {
	rows, err := r.q.QueryContext(ctx, `SELECT config_options FROM client_strategies`)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	keys := map[string]struct{}{}
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return nil, mapErr(err)
		}
		var opts map[string]string
		if json.Unmarshal([]byte(raw), &opts) != nil {
			continue // unreadable rows are pushed as {} by scanStrategy too
		}
		for k := range opts {
			keys[k] = struct{}{}
		}
	}
	return keys, mapErr(rows.Err())
}
