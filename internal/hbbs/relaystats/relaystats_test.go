package relaystats

import (
	"context"
	"errors"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/bropines/rustdesk-panel/internal/hbbs/control"
)

type fakeSource struct {
	rows  []control.RelayUsage
	err   error
	calls int
	actor string
}

func (f *fakeSource) Usage(ctx context.Context) ([]control.RelayUsage, error) {
	f.calls++
	f.actor = control.ActorFrom(ctx).Name
	return f.rows, f.err
}

func TestPollOnce(t *testing.T) {
	src := &fakeSource{rows: []control.RelayUsage{
		{Addr: "203.0.113.1:5000", TotalMB: 2, CurrentKbps: 1500},
		{Addr: "203.0.113.2:5001", TotalMB: 0.5, CurrentKbps: 500},
	}}
	reachable := true
	reg := prometheus.NewRegistry()
	p, err := New(Config{Source: src, Registerer: reg, Reachable: func() bool { return reachable }})
	if err != nil {
		t.Fatal(err)
	}
	p.PollOnce(context.Background())
	if got := testutil.ToFloat64(p.mSessions); got != 2 {
		t.Fatalf("sessions = %v", got)
	}
	if got := testutil.ToFloat64(p.mBandwidth); got != 2_000_000 {
		t.Fatalf("bandwidth = %v", got)
	}
	if got := testutil.ToFloat64(p.mBytes); got != 2.5*1024*1024 {
		t.Fatalf("bytes = %v", got)
	}
	if testutil.ToFloat64(p.mUp) != 1 || src.actor != Actor {
		t.Fatalf("up=%v actor=%q", testutil.ToFloat64(p.mUp), src.actor)
	}

	src.err = errors.New("boom")
	p.PollOnce(context.Background())
	if testutil.ToFloat64(p.mUp) != 0 || testutil.ToFloat64(p.mSessions) != 0 || testutil.ToFloat64(p.mErrors) != 1 {
		t.Fatal("error poll must zero the gauges and count the error")
	}

	reachable = false
	calls := src.calls
	p.PollOnce(context.Background())
	if src.calls != calls {
		t.Fatal("poll must be skipped while hbbr is unreachable")
	}
	if n, err := testutil.GatherAndCount(reg); err != nil || n != 5 {
		t.Fatalf("gathered %d series, err %v", n, err)
	}
}
