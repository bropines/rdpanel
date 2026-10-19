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

	isDef := 0
	if s.IsDefault {
		isDef = 1
		// Unset other defaults
		if _, err := r.q.ExecContext(ctx, `UPDATE client_strategies SET is_default = 0`); err != nil {
			return mapErr(err)
		}
	}

	return mapErr(r.q.QueryRowContext(ctx, `INSERT INTO client_strategies (name, note, config_options, is_default, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?) RETURNING id`,
		s.Name, s.Note, string(optsJSON), isDef, toMs(s.CreatedAt), toMs(s.UpdatedAt)).Scan(&s.ID))
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

	if s.IsDefault {
		// Clear other defaults
		if _, err := r.q.ExecContext(ctx, `UPDATE client_strategies SET is_default = 0 WHERE id <> ?`, s.ID); err != nil {
			return mapErr(err)
		}
	}

	isDef := 0
	if s.IsDefault {
		isDef = 1
	}

	return expectOne(r.q.ExecContext(ctx, `UPDATE client_strategies SET name = ?, note = ?, config_options = ?, is_default = ?, updated_at = ? WHERE id = ?`,
		s.Name, s.Note, string(optsJSON), isDef, toMs(s.UpdatedAt), s.ID))
}

// SetDefault marks strategy id as the default, unsetting all others.
func (r *ClientStrategies) SetDefault(ctx context.Context, id int64) error {
	now := toMs(r.now())
	if _, err := r.q.ExecContext(ctx, `UPDATE client_strategies SET is_default = 0 WHERE id <> ?`, id); err != nil {
		return mapErr(err)
	}
	return expectOne(r.q.ExecContext(ctx, `UPDATE client_strategies SET is_default = 1, updated_at = ? WHERE id = ?`, now, id))
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

// ResolveForDevice resolves the effective MDM strategy for a device:
// 1. Device's own strategy_id (if set)
// 2. Device group's strategy_id (if device belongs to a group with strategy_id set)
// 3. System default strategy (is_default = 1)
func (r *ClientStrategies) ResolveForDevice(ctx context.Context, d *Device) (*ClientStrategy, error) {
	if d == nil {
		return nil, nil
	}

	if d.StrategyID != nil && *d.StrategyID > 0 {
		strat, err := r.Get(ctx, *d.StrategyID)
		if err == nil {
			return strat, nil
		}
		if !errors.Is(err, ErrNotFound) {
			return nil, err
		}
	}

	if d != nil && d.DeviceGroupID != nil && *d.DeviceGroupID > 0 {
		var groupStratID sql.NullInt64
		err := r.q.QueryRowContext(ctx, `SELECT strategy_id FROM device_groups WHERE id = ?`, *d.DeviceGroupID).Scan(&groupStratID)
		if err == nil && groupStratID.Valid && groupStratID.Int64 > 0 {
			strat, err := r.Get(ctx, groupStratID.Int64)
			if err == nil {
				return strat, nil
			}
			if !errors.Is(err, ErrNotFound) {
				return nil, err
			}
		}
	}

	strat, err := r.GetDefault(ctx)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return strat, nil
}
