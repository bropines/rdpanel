package app

import (
	"bufio"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/bropines/rustdesk-panel/internal/events"
)

func TestHBBSWiring(t *testing.T) {
	a := newTestApp(t, nil)
	if a.Events == nil || a.HBBS.Presence == nil || a.HBBS.Control == nil || a.HBBS.Reconciler == nil {
		t.Fatalf("hbbs integration not wired: %+v", a.HBBS)
	}
	off := newTestApp(t, map[string]string{"RDPANEL_HBBS_ENABLED": "false"})
	if off.HBBS.Presence != nil || off.HBBS.Reconciler != nil || off.Events == nil {
		t.Fatal("disabled integration must not create workers")
	}
	// Non-loopback rendezvous address: presence only, no admin channel.
	remote := newTestApp(t, map[string]string{"RDPANEL_HBBS_ID_SERVER": "10.0.0.5:21116"})
	if remote.HBBS.Presence == nil || remote.HBBS.Control != nil || remote.HBBS.Reconciler != nil {
		t.Fatalf("non-loopback wiring %+v", remote.HBBS)
	}
}

func TestEventsEndpoint(t *testing.T) {
	a := newTestApp(t, map[string]string{"RDPANEL_HBBS_ENABLED": "false"})
	// Default: no authenticator -> deny.
	rec := do(t, a.Handler(), http.MethodGet, "/api/v1/events", "127.0.0.1:1", nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("default deny: %d", rec.Code)
	}

	a.EventsAuth = events.AuthenticatorFunc(func(r *http.Request) (events.Principal, error) {
		if r.Header.Get("X-Test") == "ok" {
			return events.Principal{UserID: 1, Role: events.RoleAdmin}, nil
		}
		return events.Principal{}, events.ErrUnauthenticated
	})
	srv := httptest.NewServer(a.Handler())
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/api/v1/events", nil)
	req.Header.Set("X-Test", "ok")
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status %d", resp.StatusCode)
	}
	sc := bufio.NewScanner(resp.Body)
	sc.Scan() // retry:
	for a.Events.Subscribers() == 0 {
		time.Sleep(5 * time.Millisecond)
	}
	a.Events.Publish(events.PresenceChanged, events.Presence{ID: "123456789", Online: true, Source: "hbbs"}, events.Admins)
	var got []string
	for sc.Scan() {
		if l := sc.Text(); l != "" {
			got = append(got, l)
		}
		if len(got) == 3 {
			break
		}
	}
	if len(got) != 3 || got[1] != "event: presence.changed" || !strings.Contains(got[2], `"id":"123456789"`) {
		t.Fatalf("stream %q", got)
	}
}
