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
