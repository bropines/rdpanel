package federation

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/bropines/rdpanel/internal/relays"
)

// PublicRelayDTO is what a panel announces to peers.
type PublicRelayDTO struct {
	Name       string `json:"name"`
	PublicHost string `json:"public_host"`
	Region     string `json:"region,omitempty"`
	Health     string `json:"health"`
}

// HealthDTO is the response to /api/v1/federation/health.
type HealthDTO struct {
	Status        string `json:"status"`
	Version       string `json:"version"`
	ActiveRelays  int    `json:"active_relays"`
	TimestampNano int64  `json:"timestamp_nano"`
}

// RelaySource provides active local relays to announce.
type RelaySource interface {
	List(ctx context.Context) ([]relays.View, relays.Rotation, error)
}

// Config configures the federation service.
type Config struct {
	Store        *Store
	Relays       RelaySource
	Version      string
	SyncInterval time.Duration
	Log          *slog.Logger
	HTTPClient   *http.Client
}

// Service manages federation peering, relay synchronization and public federation endpoints.
type Service struct {
	st         *Store
	relays     RelaySource
	version    string
	interval   time.Duration
	log        *slog.Logger
	client     *http.Client
	peerTokens map[int64]string
	mu         sync.RWMutex
}

// NewService creates a new federation service.
func NewService(cfg Config) *Service {
	client := cfg.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	interval := cfg.SyncInterval
	if interval <= 0 {
		interval = 60 * time.Second
	}
	log := cfg.Log
	if log == nil {
		log = slog.Default()
	}
	return &Service{
		st:         cfg.Store,
		relays:     cfg.Relays,
		version:    cfg.Version,
		interval:   interval,
		log:        log,
		client:     client,
		peerTokens: make(map[int64]string),
	}
}

// RegisterPeerToken caches the plaintext token in-memory for outgoing sync requests.
func (s *Service) RegisterPeerToken(peerID int64, token string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.peerTokens[peerID] = token
}

// Store returns the underlying store.
func (s *Service) Store() *Store { return s.st }

// PublicHealth returns local health for peering verification.
func (s *Service) PublicHealth(ctx context.Context) HealthDTO {
	activeCount := 0
	if s.relays != nil {
		views, _, err := s.relays.List(ctx)
		if err == nil {
			for _, v := range views {
				if v.Health == relays.HealthActive {
					activeCount++
				}
			}
		}
	}
	return HealthDTO{
		Status:        "ok",
		Version:       s.version,
		ActiveRelays:  activeCount,
		TimestampNano: time.Now().UTC().UnixNano(),
	}
}

// PublicRelays returns local active relays for peering peers.
func (s *Service) PublicRelays(ctx context.Context) ([]PublicRelayDTO, error) {
	if s.relays == nil {
		return nil, nil
	}
	views, _, err := s.relays.List(ctx)
	if err != nil {
		return nil, err
	}
	var res []PublicRelayDTO
	for _, v := range views {
		if v.Health == relays.HealthActive && v.InRotation && v.AdminState == relays.StateEnabled {
			res = append(res, PublicRelayDTO{
				Name:       v.Name,
				PublicHost: v.PublicHost,
				Region:     v.Region,
				Health:     string(v.Health),
			})
		}
	}
	return res, nil
}

// SyncPeer synchronizes announced relays from a single peer.
func (s *Service) SyncPeer(ctx context.Context, id int64) error {
	p, err := s.st.Get(ctx, id)
	if err != nil {
		return err
	}

	s.mu.RLock()
	token := s.peerTokens[id]
	s.mu.RUnlock()

	// 1. Ping remote peer health
	healthURL := fmt.Sprintf("%s/api/v1/federation/health", p.APIURL)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, healthURL, nil)
	if err != nil {
		s.recordFailure(ctx, id, err)
		return err
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := s.client.Do(req)
	if err != nil {
		s.recordFailure(ctx, id, err)
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		err := fmt.Errorf("peer health returned HTTP %d", resp.StatusCode)
		s.recordFailure(ctx, id, err)
		return err
	}

	// 2. Fetch announced relays if sync_relays enabled
	if p.SyncRelays {
		relaysURL := fmt.Sprintf("%s/api/v1/federation/relays", p.APIURL)
		rreq, err := http.NewRequestWithContext(ctx, http.MethodGet, relaysURL, nil)
		if err != nil {
			s.recordFailure(ctx, id, err)
			return err
		}
		if token != "" {
			rreq.Header.Set("Authorization", "Bearer "+token)
		}
		rresp, err := s.client.Do(rreq)
		if err != nil {
			s.recordFailure(ctx, id, err)
			return err
		}
		defer rresp.Body.Close()

		if rresp.StatusCode == http.StatusOK {
			bodyBytes, _ := io.ReadAll(io.LimitReader(rresp.Body, 512<<10))
			var remoteRelays []PublicRelayDTO
			if err := json.Unmarshal(bodyBytes, &remoteRelays); err == nil {
				federated := make([]FederatedRelay, 0, len(remoteRelays))
				for _, r := range remoteRelays {
					if r.PublicHost != "" {
						federated = append(federated, FederatedRelay{
							PeerID:     id,
							RemoteName: r.Name,
							PublicHost: r.PublicHost,
							Region:     r.Region,
							Health:     r.Health,
						})
					}
				}
				_ = s.st.ReplaceFederatedRelays(ctx, id, federated)
			}
		}
	}

	_ = s.st.UpdateStatus(ctx, id, StatusActive, nil)
	return nil
}

func (s *Service) recordFailure(ctx context.Context, id int64, err error) {
	msg := err.Error()
	_ = s.st.UpdateStatus(ctx, id, StatusUnreachable, &msg)
}

// SyncAll passes through all peers and attempts sync.
func (s *Service) SyncAll(ctx context.Context) {
	peers, err := s.st.List(ctx)
	if err != nil {
		return
	}
	for _, p := range peers {
		if err := s.SyncPeer(ctx, p.ID); err != nil && ctx.Err() == nil {
			s.log.Debug("federation: sync failed", slog.String("peer", p.Name), slog.Any("err", err))
		}
	}
}

// Start begins the background sync loop.
func (s *Service) Start(ctx context.Context) {
	go func() {
		// Initial sync after 5s
		select {
		case <-ctx.Done():
			return
		case <-time.After(5 * time.Second):
			s.SyncAll(ctx)
		}

		ticker := time.NewTicker(s.interval)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				s.SyncAll(ctx)
			}
		}
	}()
}
