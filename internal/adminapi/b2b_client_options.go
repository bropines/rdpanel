package adminapi

import (
	"net/http"
	"strings"
	"time"

	"github.com/bropines/rdpanel/internal/clientopts"
	"github.com/bropines/rdpanel/internal/httpx"
)

// Client options pushed to every RustDesk client in heartbeat responses
// (strategy config_options): the WebRTC ICE servers and congestion control.
// See docs/webrtc.md.

// clientOptionsDTO is components.schemas.ClientOptions.
type clientOptionsDTO struct {
	ICEServers              string  `json:"ice_servers"`
	WebRTCCongestionControl *bool   `json:"webrtc_congestion_control"`
	Pushed                  bool    `json:"pushed"`
	ModifiedAt              *string `json:"modified_at"`
}

// clientOptionsInput is components.schemas.ClientOptionsInput.
type clientOptionsInput struct {
	ICEServers              *string `json:"ice_servers"`
	WebRTCCongestionControl *bool   `json:"webrtc_congestion_control"`
}

func newClientOptionsDTO(o *clientopts.Options) clientOptionsDTO {
	if o == nil {
		return clientOptionsDTO{}
	}
	at := time.UnixMilli(o.ModifiedAt).UTC().Format(time.RFC3339)
	return clientOptionsDTO{ICEServers: strings.ReplaceAll(o.ICEServers, ",", "\n"),
		WebRTCCongestionControl: o.WebRTCCongestion, Pushed: true, ModifiedAt: &at}
}

func (c *b2bCore) handleGetClientOptions(w http.ResponseWriter, r *http.Request) {
	o, err := c.d.ClientOptions.Get(r.Context())
	if err != nil {
		c.fail(w, r, "client options", err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, newClientOptionsDTO(o))
}

func (c *b2bCore) handlePutClientOptions(w http.ResponseWriter, r *http.Request) {
	var in clientOptionsInput
	if !decodeBody(w, r, &in, 64<<10) {
		return
	}
	ice := ""
	if in.ICEServers != nil {
		ice = *in.ICEServers
	}
	if _, err := clientopts.NormalizeICEServers(ice); err != nil {
		validationFailed(w, "invalid client options", map[string]string{"ice_servers": err.Error()})
		return
	}
	ctx := r.Context()
	u := CurrentUser(ctx)
	o, err := c.d.ClientOptions.Set(ctx, ice, in.WebRTCCongestionControl, &u.ID)
	if err != nil {
		c.fail(w, r, "client options", err)
		return
	}
	// TURN URLs carry credentials: the audit records only how many servers.
	n := 0
	if o.ICEServers != "" {
		n = strings.Count(o.ICEServers, ",") + 1
	}
	recordAudit(ctx, c.d, r, "server.client_options.update", "client_options", "",
		map[string]any{"ice_servers": n, "webrtc_congestion_control": o.WebRTCCongestion})
	httpx.WriteJSON(w, http.StatusOK, newClientOptionsDTO(o))
}

type clientOptionsProbeInput struct {
	ICEServers string `json:"ice_servers"`
}

type iceServerProbeDTO struct {
	Server    string   `json:"server"`
	Scheme    string   `json:"scheme"`
	Reachable bool     `json:"reachable"`
	RTTMs     *float64 `json:"rtt_ms,omitempty"`
	Error     *string  `json:"error,omitempty"`
}

type iceServerProbeListDTO struct {
	Items []iceServerProbeDTO `json:"items"`
}

func (c *b2bCore) handlePostClientOptionsProbe(w http.ResponseWriter, r *http.Request) {
	var in clientOptionsProbeInput
	if !decodeBody(w, r, &in, 64<<10) {
		return
	}
	ctx := r.Context()
	allowPrivate := false
	if c.d.Relays != nil {
		if opts, err := c.d.Relays.GetOptions(ctx); err == nil {
			allowPrivate = opts.AllowPrivateHosts
		}
	}
	results := clientopts.ProbeICEServers(ctx, in.ICEServers, allowPrivate)
	items := make([]iceServerProbeDTO, 0, len(results))
	for _, res := range results {
		dto := iceServerProbeDTO{
			Server:    res.Server,
			Scheme:    res.Scheme,
			Reachable: res.Reachable,
		}
		if res.Reachable {
			rtt := res.RTTMs
			dto.RTTMs = &rtt
		}
		if res.Error != "" {
			errStr := res.Error
			dto.Error = &errStr
		}
		items = append(items, dto)
	}
	httpx.WriteJSON(w, http.StatusOK, iceServerProbeListDTO{Items: items})
}
