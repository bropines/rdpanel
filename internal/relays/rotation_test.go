package relays

import (
	"fmt"
	"strings"
	"testing"
)

func rel(id int64, host string, w int, h Health) Relay {
	return Relay{ID: id, Name: host, Kind: KindStatic, PublicHost: host, Weight: w, InRotation: true,
		AdminState: StateEnabled, Health: h}
}

func TestComputeRotationWeights(t *testing.T) {
	rot := ComputeRotation([]Relay{
		rel(1, "a.example.com", 3, HealthActive),
		rel(2, "b.example.com", 1, HealthDegraded),
		rel(3, "c.example.com", 2, HealthOffline), // offline: out
		rel(4, "d.example.com", 0, HealthActive),  // weight 0: out
		rel(5, "a.example.com", 2, HealthActive),  // duplicate host: first wins
		{ID: 6, PublicHost: "e.example.com", Weight: 1, InRotation: false, AdminState: StateEnabled, Health: HealthActive},
		{ID: 7, PublicHost: "f.example.com", Weight: 1, InRotation: true, AdminState: StateDraining, Health: HealthActive},
		{ID: 8, PublicHost: "g.example.com", Weight: 1, InRotation: true, AdminState: StateEnabled, Health: HealthPending},
	})
	if got := strings.Join(rot.Servers, ","); got != "a.example.com,a.example.com,b.example.com,a.example.com" {
		t.Fatalf("servers %s", got)
	}
	if len(rot.Members) != 2 || rot.Fallback || rot.Shrunk || rot.Error != "" {
		t.Fatalf("rotation %+v", rot)
	}
}

func TestComputeRotationInterleaves(t *testing.T) {
	rot := ComputeRotation([]Relay{rel(1, "a", 2, HealthActive), rel(2, "b", 2, HealthActive), rel(3, "c", 1, HealthActive)})
	if got := strings.Join(rot.Servers, ","); got != "a,b,c,a,b" {
		t.Fatalf("servers %s", got)
	}
}

func TestComputeRotationFallback(t *testing.T) {
	local := Relay{ID: 1, Name: "local", Kind: KindLocal, PublicHost: "rd.example.com", Weight: 1, InRotation: true,
		AdminState: StateEnabled, Health: HealthOffline}
	rot := ComputeRotation([]Relay{local, rel(2, "b", 1, HealthOffline)})
	if !rot.Fallback || len(rot.Servers) != 1 || rot.Servers[0] != "rd.example.com" {
		t.Fatalf("fallback %+v", rot)
	}
	// Nothing at all: nil (hbbs keeps its -r list).
	if rot := ComputeRotation([]Relay{rel(2, "b", 1, HealthOffline)}); rot.Servers != nil {
		t.Fatalf("want nil, got %v", rot.Servers)
	}
	local.AdminState = StateDisabled
	if rot := ComputeRotation([]Relay{local}); rot.Servers != nil {
		t.Fatalf("disabled local must not be a fallback: %v", rot.Servers)
	}
}

func TestComputeRotationShrinks(t *testing.T) {
	var rs []Relay
	// 20 hosts of ~40 bytes with weight 5 = ~4 KB; must shrink below 986 bytes.
	for i := range 20 {
		w := 5
		if i%2 == 1 {
			w = 1
		}
		rs = append(rs, rel(int64(i+1), fmt.Sprintf("relay-%02d.very-long-domain-name.example.com", i), w, HealthActive))
	}
	rot := ComputeRotation(rs)
	n := len(strings.Join(rot.Servers, ","))
	if n > MaxRotationBytes || !rot.Shrunk || rot.Error != "" {
		t.Fatalf("size %d shrunk %v err %q", n, rot.Shrunk, rot.Error)
	}
	for _, m := range rot.Members {
		if m.Weight < 1 {
			t.Fatalf("member dropped to weight 0: %+v", m)
		}
	}
	// The heavier relays keep more entries than the light ones.
	if rot.Members[0].Weight <= rot.Members[1].Weight && rot.Members[0].Weight != 1 {
		t.Fatalf("weights %+v", rot.Members[:2])
	}
}

func TestComputeRotationTruncates(t *testing.T) {
	var rs []Relay
	for i := range 40 {
		rs = append(rs, rel(int64(i+1), fmt.Sprintf("relay-%02d.extremely-long-subdomain.example.com", i), 1, HealthActive))
	}
	rot := ComputeRotation(rs)
	if rot.Error == "" || len(strings.Join(rot.Servers, ",")) > MaxRotationBytes || len(rot.Members) >= 40 {
		t.Fatalf("truncation: %d members, err %q", len(rot.Members), rot.Error)
	}
}

func TestNextHealth(t *testing.T) {
	cases := []struct {
		name string
		in   healthInput
		want Health
	}{
		{"static first ok", healthInput{Kind: KindStatic, Prev: HealthPending, Probeable: true, Probed: true, ProbeOK: true}, HealthActive},
		{"static one fail", healthInput{Kind: KindStatic, Prev: HealthActive, Probeable: true, Probed: true, Fails: 1}, HealthDegraded},
		{"static three fails", healthInput{Kind: KindStatic, Prev: HealthDegraded, Probeable: true, Probed: true, Fails: 3}, HealthOffline},
		{"static pending fail", healthInput{Kind: KindStatic, Prev: HealthPending, Probeable: true, Probed: true, Fails: 1}, HealthPending},
		{"offline stays until ok", healthInput{Kind: KindStatic, Prev: HealthOffline, Probeable: true, Probed: true, Fails: 1}, HealthOffline},
		{"not probed keeps", healthInput{Kind: KindStatic, Prev: HealthActive, Probeable: true}, HealthActive},
		{"agent unapproved", healthInput{Kind: KindAgent, Prev: HealthActive, Probeable: true, Probed: true, ProbeOK: true}, HealthPending},
		{"agent ok", healthInput{Kind: KindAgent, Approved: true, Connected: true, Probeable: true, Probed: true, ProbeOK: true}, HealthActive},
		{"agent ws down", healthInput{Kind: KindAgent, Approved: true, Probeable: true, Probed: true, ProbeOK: true}, HealthDegraded},
		{"agent lag", healthInput{Kind: KindAgent, Approved: true, Connected: true, ConfigLag: true, Probeable: true, Probed: true, ProbeOK: true}, HealthDegraded},
		{"agent offline", healthInput{Kind: KindAgent, Approved: true, Connected: true, Prev: HealthActive, Probeable: true, Probed: true, Fails: 3}, HealthOffline},
		{"local loopback up", healthInput{Kind: KindLocal, LocalUp: true}, HealthActive},
		{"local loopback down", healthInput{Kind: KindLocal}, HealthOffline},
		{"local probe fails, hbbr up", healthInput{Kind: KindLocal, Prev: HealthActive, LocalUp: true, Probeable: true, Probed: true, Fails: 5}, HealthDegraded},
		{"local probe fails, hbbr down", healthInput{Kind: KindLocal, Prev: HealthActive, Probeable: true, Probed: true, Fails: 3}, HealthOffline},
	}
	for _, c := range cases {
		if got := nextHealth(c.in); got != c.want {
			t.Errorf("%s: got %s want %s", c.name, got, c.want)
		}
	}
}

func TestApplyAutoWeights(t *testing.T) {
	relays := []Relay{
		rel(1, "low-load.example.com", 1, HealthActive),
		rel(2, "high-load.example.com", 1, HealthActive),
		rel(3, "mid-load.example.com", 1, HealthActive),
		rel(4, "offline.example.com", 1, HealthOffline),
	}
	sessions := map[int64]int{
		1: 0,
		2: 100,
		3: 50,
		4: 0,
	}

	res := ApplyAutoWeights(relays, sessions)
	// Least loaded (id 1) should get max weight 5
	if res[0].Weight != 5 {
		t.Fatalf("expected low-load weight 5, got %d", res[0].Weight)
	}
	// Highest loaded (id 2) should get min weight 1
	if res[1].Weight != 1 {
		t.Fatalf("expected high-load weight 1, got %d", res[1].Weight)
	}
	// Mid-loaded (id 3) should get intermediate weight 3
	if res[2].Weight != 3 {
		t.Fatalf("expected mid-load weight 3, got %d", res[2].Weight)
	}
	// Offline relay remains unchanged
	if res[3].Weight != 1 {
		t.Fatalf("expected offline weight 1, got %d", res[3].Weight)
	}

	// When all have equal load, original weights are kept
	equalSessions := map[int64]int{1: 10, 2: 10, 3: 10}
	equalRes := ApplyAutoWeights(relays, equalSessions)
	if equalRes[0].Weight != 1 || equalRes[1].Weight != 1 || equalRes[2].Weight != 1 {
		t.Fatalf("expected original weights preserved when load equal, got %v", equalRes)
	}
}
