package relays

import (
	"fmt"
	"strings"
)

// MaxRotationBytes bounds the comma-joined relay list: hbbs reads one admin
// command with a single 1024-byte read() and the control client caps lines at
// 1000 bytes, including the "relay-servers " prefix.
const MaxRotationBytes = 1000 - len("relay-servers ")

// Member is one relay in the computed rotation.
type Member struct {
	ID     int64  `json:"id"`
	Name   string `json:"name"`
	Host   string `json:"host"`
	Weight int    `json:"weight"` // effective (possibly shrunk) weight
}

// Rotation is the computed hbbs relay list.
type Rotation struct {
	// Servers is what the reconciler sends as `rs` (nil: leave hbbs' list alone).
	Servers []string `json:"servers"`
	Members []Member `json:"members"`
	// Fallback is set when no relay is healthy and the local relay is used
	// anyway (hbbs must never get an empty list).
	Fallback bool `json:"fallback"`
	// Shrunk is set when weights were scaled down to fit MaxRotationBytes.
	Shrunk bool   `json:"shrunk"`
	Error  string `json:"error,omitempty"`
}

// Eligible reports whether r belongs in hbbs' relay list: admin-enabled, in
// rotation, weight > 0 and healthy (active, or degraded: the probe passes but
// the agent connection or config lags, clients are fine).
func Eligible(r Relay) bool {
	return r.InRotation && r.AdminState == StateEnabled && r.Weight > 0 &&
		(r.Health == HealthActive || r.Health == HealthDegraded)
}

// ComputeRotation builds the weighted relay list from relays (in id order).
// Duplicates are weights: hbbs round-robins over the list and keeps repeated
// entries. Entries are interleaved (smooth weighted round-robin) so that
// consecutive sessions spread over relays. Weights shrink proportionally when
// the list does not fit into MaxRotationBytes.
func ComputeRotation(relays []Relay) Rotation {
	var members []Member
	seen := map[string]bool{}
	for _, r := range relays {
		if !Eligible(r) || seen[r.PublicHost] {
			continue
		}
		seen[r.PublicHost] = true
		members = append(members, Member{ID: r.ID, Name: r.Name, Host: r.PublicHost, Weight: r.Weight})
	}
	var rot Rotation
	if len(members) == 0 {
		for _, r := range relays {
			if r.Kind == KindLocal && r.AdminState != StateDisabled {
				rot.Fallback = true
				rot.Members = []Member{{ID: r.ID, Name: r.Name, Host: r.PublicHost, Weight: 1}}
				rot.Servers = []string{r.PublicHost}
				return rot
			}
		}
		return rot
	}
	maxW := 0
	for _, m := range members {
		maxW = max(maxW, m.Weight)
	}
	orig := make([]int, len(members))
	for i, m := range members {
		orig[i] = m.Weight
	}
	for scale := maxW; scale >= 1; scale-- {
		for i := range members {
			// ceil(w*scale/maxW) keeps every member at >= 1.
			members[i].Weight = (orig[i]*scale + maxW - 1) / maxW
		}
		list := interleave(members)
		if size(list) <= MaxRotationBytes {
			rot.Servers, rot.Members, rot.Shrunk = list, members, scale < maxW
			return rot
		}
	}
	// Even weight 1 does not fit: keep the prefix that does.
	var kept []Member
	n := 0
	for _, m := range members {
		m.Weight = 1
		add := len(m.Host)
		if len(kept) > 0 {
			add++
		}
		if n+add > MaxRotationBytes {
			break
		}
		n += add
		kept = append(kept, m)
	}
	rot.Servers, rot.Members, rot.Shrunk = interleave(kept), kept, true
	rot.Error = fmt.Sprintf("%d of %d relays do not fit into hbbs' 1 KiB admin command and were left out",
		len(members)-len(kept), len(members))
	return rot
}

func size(list []string) int {
	if len(list) == 0 {
		return 0
	}
	return len(strings.Join(list, ","))
}

// interleave expands weights with smooth weighted round-robin (nginx): for
// weights a=3, b=1 it yields a,a,b,a.
func interleave(ms []Member) []string {
	total := 0
	for _, m := range ms {
		total += m.Weight
	}
	cur := make([]int, len(ms))
	out := make([]string, 0, total)
	for range total {
		best := -1
		for i, m := range ms {
			cur[i] += m.Weight
			if best < 0 || cur[i] > cur[best] {
				best = i
			}
		}
		cur[best] -= total
		out = append(out, ms[best].Host)
	}
	return out
}
