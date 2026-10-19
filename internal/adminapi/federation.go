package adminapi

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/bropines/rdpanel/internal/federation"
	"github.com/bropines/rdpanel/internal/httpx"
	"github.com/bropines/rdpanel/internal/store"
)

func (c *b2bCore) registerFederation(r chi.Router) {
	r.Group(func(r chi.Router) {
		r.Use(RequireAdmin)
		r.Get("/federation/peers", c.handleListFederationPeers)
		r.Post("/federation/peers", c.handleCreateFederationPeer)
		r.Delete("/federation/peers/{id}", c.handleDeleteFederationPeer)
		r.Post("/federation/peers/{id}/sync", c.handleSyncFederationPeer)
		r.Get("/federation/relays", c.handleListFederatedRelays)
	})
}

func (c *b2bCore) needFederation(w http.ResponseWriter) bool {
	if c.d.Federation == nil {
		httpx.WriteError(w, http.StatusServiceUnavailable, "service_unavailable", "federation is not enabled")
		return false
	}
	return true
}

func (c *b2bCore) handleListFederationPeers(w http.ResponseWriter, r *http.Request) {
	if !c.needFederation(w) {
		return
	}
	peers, err := c.d.Federation.Store().List(r.Context())
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	if peers == nil {
		peers = []federation.Peer{}
	}
	httpx.WriteJSON(w, http.StatusOK, peers)
}

type createFederationPeerInput struct {
	Name       string `json:"name"`
	APIURL     string `json:"api_url"`
	AuthToken  string `json:"auth_token"`
	SyncRelays *bool  `json:"sync_relays"`
}

func (c *b2bCore) handleCreateFederationPeer(w http.ResponseWriter, r *http.Request) {
	if !c.needFederation(w) {
		return
	}
	var in createFederationPeerInput
	if !decodeBody(w, r, &in, 16<<10) {
		return
	}
	syncRelays := true
	if in.SyncRelays != nil {
		syncRelays = *in.SyncRelays
	}

	peer, err := c.d.Federation.Store().Create(r.Context(), federation.CreatePeerInput{
		Name:       in.Name,
		APIURL:     in.APIURL,
		AuthToken:  in.AuthToken,
		SyncRelays: syncRelays,
	})
	if err != nil {
		if errors.Is(err, store.ErrConflict) {
			httpx.WriteError(w, http.StatusConflict, "conflict", "a peer with this name already exists")
			return
		}
		if errors.Is(err, store.ErrInvalid) {
			validationFailed(w, "invalid peer input", map[string]string{"input": err.Error()})
			return
		}
		httpx.WriteError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}

	c.d.Federation.RegisterPeerToken(peer.ID, in.AuthToken)
	// Try immediate sync in background
	go func() {
		_ = c.d.Federation.SyncPeer(r.Context(), peer.ID)
	}()

	recordAudit(r.Context(), c.d, r, "federation.peer.create", "federation_peer", peer.Name,
		map[string]any{"id": peer.ID, "api_url": peer.APIURL, "sync_relays": peer.SyncRelays})
	httpx.WriteJSON(w, http.StatusCreated, peer)
}

func (c *b2bCore) handleDeleteFederationPeer(w http.ResponseWriter, r *http.Request) {
	if !c.needFederation(w) {
		return
	}
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil || id <= 0 {
		httpx.WriteError(w, http.StatusBadRequest, "invalid_parameter", "invalid peer id")
		return
	}

	peer, err := c.d.Federation.Store().Get(r.Context(), id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			httpx.WriteError(w, http.StatusNotFound, "not_found", "peer not found")
			return
		}
		httpx.WriteError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}

	if err := c.d.Federation.Store().Delete(r.Context(), id); err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}

	recordAudit(r.Context(), c.d, r, "federation.peer.delete", "federation_peer", peer.Name,
		map[string]any{"id": peer.ID, "api_url": peer.APIURL})
	w.WriteHeader(http.StatusNoContent)
}

func (c *b2bCore) handleSyncFederationPeer(w http.ResponseWriter, r *http.Request) {
	if !c.needFederation(w) {
		return
	}
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil || id <= 0 {
		httpx.WriteError(w, http.StatusBadRequest, "invalid_parameter", "invalid peer id")
		return
	}

	if err := c.d.Federation.SyncPeer(r.Context(), id); err != nil {
		httpx.WriteError(w, http.StatusBadGateway, "sync_failed", err.Error())
		return
	}

	peer, err := c.d.Federation.Store().Get(r.Context(), id)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusOK, peer)
}

func (c *b2bCore) handleListFederatedRelays(w http.ResponseWriter, r *http.Request) {
	if !c.needFederation(w) {
		return
	}
	relays, err := c.d.Federation.Store().ListFederatedRelays(r.Context())
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	if relays == nil {
		relays = []federation.FederatedRelay{}
	}
	httpx.WriteJSON(w, http.StatusOK, relays)
}
