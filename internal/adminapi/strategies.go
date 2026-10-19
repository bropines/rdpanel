package adminapi

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/bropines/rdpanel/internal/httpx"
	"github.com/bropines/rdpanel/internal/store"
)

type clientStrategyDTO struct {
	ID            int64             `json:"id"`
	Name          string            `json:"name"`
	Note          string            `json:"note"`
	ConfigOptions map[string]string `json:"config_options"`
	IsDefault     bool              `json:"is_default"`
	DeviceCount   int               `json:"device_count"`
	CreatedAt     time.Time         `json:"created_at"`
	UpdatedAt     time.Time         `json:"updated_at"`
}

func newClientStrategyDTO(s *store.ClientStrategy) clientStrategyDTO {
	opts := s.ConfigOptions
	if opts == nil {
		opts = make(map[string]string)
	}
	return clientStrategyDTO{
		ID:            s.ID,
		Name:          s.Name,
		Note:          s.Note,
		ConfigOptions: opts,
		IsDefault:     s.IsDefault,
		DeviceCount:   s.DeviceCount,
		CreatedAt:     s.CreatedAt,
		UpdatedAt:     s.UpdatedAt,
	}
}

type clientStrategyInput struct {
	Name          *string            `json:"name"`
	Note          *string            `json:"note"`
	ConfigOptions *map[string]string `json:"config_options"`
	IsDefault     *bool              `json:"is_default"`
}

func (in *clientStrategyInput) validate(create bool) map[string]string {
	f := map[string]string{}
	if in.Name != nil {
		*in.Name = strings.TrimSpace(*in.Name)
		if *in.Name == "" || runes(*in.Name) > 64 {
			f["name"] = "must be 1-64 characters"
		}
	} else if create {
		f["name"] = "required"
	}
	if in.Note != nil && runes(*in.Note) > 4096 {
		f["note"] = "must be at most 4096 characters"
	}
	if create && in.ConfigOptions == nil {
		f["config_options"] = "required"
	}
	return f
}

func (a *resources) handleListStrategies(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	strats, err := a.d.Store.ClientStrategies.List(ctx)
	if err != nil {
		a.fail500(w, r, "list strategies", err)
		return
	}
	out := make([]clientStrategyDTO, 0, len(strats))
	for _, s := range strats {
		out = append(out, newClientStrategyDTO(s))
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"items": out})
}

func (a *resources) handleCreateStrategy(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var in clientStrategyInput
	if !decodeJSON(w, r, &in) {
		return
	}
	if f := in.validate(true); len(f) > 0 {
		validationFailed(w, "invalid strategy", f)
		return
	}
	s := &store.ClientStrategy{
		Name:          *in.Name,
		ConfigOptions: *in.ConfigOptions,
	}
	if in.Note != nil {
		s.Note = *in.Note
	}
	if in.IsDefault != nil {
		s.IsDefault = *in.IsDefault
	}
	if err := a.d.Store.ClientStrategies.Create(ctx, s); err != nil {
		if errors.Is(err, store.ErrConflict) {
			conflict(w, "conflict", "a strategy with this name already exists")
			return
		}
		a.writeStoreErr(w, r, "create strategy", err)
		return
	}
	row, err := a.d.Store.ClientStrategies.Get(ctx, s.ID)
	if err != nil {
		a.fail500(w, r, "create strategy", err)
		return
	}
	a.audit(ctx, r, "strategy.create", "strategy", idStr(s.ID), map[string]any{"name": s.Name})
	httpx.WriteJSON(w, http.StatusCreated, newClientStrategyDTO(row))
}

func (a *resources) handleGetStrategy(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id, ok := pathID(w, r, "strategy_id")
	if !ok {
		return
	}
	s, err := a.d.Store.ClientStrategies.Get(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		httpx.NotFound(w, r)
		return
	}
	if err != nil {
		a.fail500(w, r, "get strategy", err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, newClientStrategyDTO(s))
}

func (a *resources) handleUpdateStrategy(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id, ok := pathID(w, r, "strategy_id")
	if !ok {
		return
	}
	var in clientStrategyInput
	if !decodeJSON(w, r, &in) {
		return
	}
	if f := in.validate(false); len(f) > 0 {
		validationFailed(w, "invalid strategy", f)
		return
	}

	var s *store.ClientStrategy
	err := a.d.Store.Tx(ctx, func(tx *store.Store) error {
		var err error
		s, err = tx.ClientStrategies.Get(ctx, id)
		if err != nil {
			return err
		}
		if in.Name != nil {
			s.Name = *in.Name
		}
		if in.Note != nil {
			s.Note = *in.Note
		}
		if in.ConfigOptions != nil {
			s.ConfigOptions = *in.ConfigOptions
		}
		if in.IsDefault != nil {
			s.IsDefault = *in.IsDefault
		}
		return tx.ClientStrategies.Update(ctx, s)
	})
	if errors.Is(err, store.ErrNotFound) {
		httpx.NotFound(w, r)
		return
	}
	if errors.Is(err, store.ErrConflict) {
		conflict(w, "conflict", "a strategy with this name already exists")
		return
	}
	if err != nil {
		a.writeStoreErr(w, r, "update strategy", err)
		return
	}

	row, err := a.d.Store.ClientStrategies.Get(ctx, id)
	if err != nil {
		a.fail500(w, r, "update strategy", err)
		return
	}
	a.audit(ctx, r, "strategy.update", "strategy", idStr(id), map[string]any{"name": row.Name})
	httpx.WriteJSON(w, http.StatusOK, newClientStrategyDTO(row))
}

func (a *resources) handleDeleteStrategy(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id, ok := pathID(w, r, "strategy_id")
	if !ok {
		return
	}
	s, err := a.d.Store.ClientStrategies.Get(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		httpx.NotFound(w, r)
		return
	}
	if err != nil {
		a.fail500(w, r, "delete strategy", err)
		return
	}
	if s.IsDefault {
		validationFailed(w, "invalid operation", map[string]string{"strategy": "cannot delete the default strategy"})
		return
	}
	if err := a.d.Store.ClientStrategies.Delete(ctx, id); err != nil {
		a.writeStoreErr(w, r, "delete strategy", err)
		return
	}
	a.audit(ctx, r, "strategy.delete", "strategy", idStr(id), map[string]any{"name": s.Name})
	w.WriteHeader(http.StatusNoContent)
}

func (a *resources) handleSetDefaultStrategy(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id, ok := pathID(w, r, "strategy_id")
	if !ok {
		return
	}
	if err := a.d.Store.ClientStrategies.SetDefault(ctx, id); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			httpx.NotFound(w, r)
			return
		}
		a.writeStoreErr(w, r, "set default strategy", err)
		return
	}
	row, err := a.d.Store.ClientStrategies.Get(ctx, id)
	if err != nil {
		a.fail500(w, r, "set default strategy", err)
		return
	}
	a.audit(ctx, r, "strategy.set_default", "strategy", idStr(id), map[string]any{"name": row.Name})
	httpx.WriteJSON(w, http.StatusOK, newClientStrategyDTO(row))
}
