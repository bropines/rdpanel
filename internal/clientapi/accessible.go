package clientapi

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/bropines/rustdesk-panel/internal/store"
)

// Accessibility policy for the client's "Accessible devices" tab:
//   - admins see every user, device and device group;
//   - members of a "shared" user group see the group's users and the devices
//     those users own;
//   - everyone else sees themselves and their own devices.
type accessScope struct {
	all     bool
	groupID *int64 // shared user group
	userID  int64
}

func (h *handler) scopeFor(ctx context.Context, u *store.User) (accessScope, error) {
	if u.IsAdmin() {
		return accessScope{all: true}, nil
	}
	if u.UserGroupID != nil {
		g, err := h.st.UserGroups.Get(ctx, *u.UserGroupID)
		if err != nil {
			return accessScope{}, err
		}
		if g.Visibility == store.VisibilityShared {
			return accessScope{groupID: &g.ID}, nil
		}
	}
	return accessScope{userID: u.ID}, nil
}

func (s accessScope) devices() store.DeviceScope {
	switch {
	case s.all:
		return store.DeviceScope{}
	case s.groupID != nil:
		return store.DeviceScope{OwnerGroupID: s.groupID}
	default:
		id := s.userID
		return store.DeviceScope{OwnerUserID: &id}
	}
}

func (h *handler) listFail(ctx context.Context, w http.ResponseWriter, err error) {
	h.log.ErrorContext(ctx, "accessible list", slog.Any("err", err))
	internalError(w)
}

// accessibleUser is the subset of UserPayload the client reads in lists
// (name, display_name); other people's emails and notes are not exposed.
type accessibleUser struct {
	Name        string `json:"name"`
	DisplayName string `json:"display_name"`
	Avatar      string `json:"avatar"`
	Status      int    `json:"status"`
}

// users implements GET /api/users?current&pageSize&accessible&status=1.
func (h *handler) users(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	me := sessionFrom(ctx).user
	scope, err := h.scopeFor(ctx, me)
	if err != nil {
		h.listFail(ctx, w, err)
		return
	}
	pg := parsePage(r)
	var status store.UserStatus
	switch r.URL.Query().Get("status") {
	case "1":
		status = store.UserActive
	case "0":
		status = store.UserDisabled
	}
	var (
		users []*store.User
		total int
	)
	if scope.all || scope.groupID != nil {
		users, total, err = h.st.Users.List(ctx, store.ListUsersParams{Status: status, UserGroupID: scope.groupID,
			Limit: pg.limit, Offset: pg.offset})
		if err != nil {
			h.listFail(ctx, w, err)
			return
		}
	} else if status == "" || me.Status == status {
		total = 1
		if pg.offset == 0 {
			users = []*store.User{me}
		}
	}
	out := make([]accessibleUser, 0, len(users))
	for _, u := range users {
		out = append(out, accessibleUser{Name: u.Username, DisplayName: u.DisplayName, Avatar: u.AvatarURL,
			Status: userStatus(u)})
	}
	writeJSON(w, http.StatusOK, newList(total, out))
}

// peerPayload is PeerPayload (hbbs.dart). info.os is stored exactly as
// sysinfo sent it; the client derives the platform icon from it.
type peerPayload struct {
	ID              string   `json:"id"`
	Info            peerInfo `json:"info"`
	Status          int      `json:"status"`
	User            string   `json:"user"`
	UserName        string   `json:"user_name"`
	DeviceGroupName string   `json:"device_group_name"`
	Note            string   `json:"note"`
}

type peerInfo struct {
	Username   string `json:"username"`
	OS         string `json:"os"`
	DeviceName string `json:"device_name"`
}

// peers implements GET /api/peers?current&pageSize&accessible&status=1.
func (h *handler) peers(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	scope, err := h.scopeFor(ctx, sessionFrom(ctx).user)
	if err != nil {
		h.listFail(ctx, w, err)
		return
	}
	pg := parsePage(r)
	devs, total, err := h.st.Devices.List(ctx, store.ListDevicesParams{Scope: scope.devices(), Limit: pg.limit,
		Offset: pg.offset})
	if err != nil {
		h.listFail(ctx, w, err)
		return
	}
	out := make([]peerPayload, 0, len(devs))
	for _, d := range devs {
		out = append(out, peerPayload{
			ID:     d.RustdeskID,
			Info:   peerInfo{Username: d.OSUsername, OS: d.OS, DeviceName: d.Hostname},
			Status: 1, User: d.OwnerUsername, UserName: d.OwnerUsername, DeviceGroupName: d.DeviceGroupName,
			Note: d.Note,
		})
	}
	writeJSON(w, http.StatusOK, newList(total, out))
}

type deviceGroupPayload struct {
	Name string `json:"name"`
}

// deviceGroups implements GET /api/device-group/accessible?current&pageSize:
// every group for admins, otherwise the groups of accessible devices.
func (h *handler) deviceGroups(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	scope, err := h.scopeFor(ctx, sessionFrom(ctx).user)
	if err != nil {
		h.listFail(ctx, w, err)
		return
	}
	pg := parsePage(r)
	groups, total, err := h.st.DeviceGroups.List(ctx, scope.devices(), pg.limit, pg.offset)
	if err != nil {
		h.listFail(ctx, w, err)
		return
	}
	out := make([]deviceGroupPayload, 0, len(groups))
	for _, g := range groups {
		out = append(out, deviceGroupPayload{Name: g.Name})
	}
	writeJSON(w, http.StatusOK, newList(total, out))
}
