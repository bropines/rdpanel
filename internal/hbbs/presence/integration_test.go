package presence

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"net"
	"os"
	"testing"
	"time"

	"github.com/bropines/rustdesk-panel/internal/hbbs/wire"
)

// TestIntegrationRealHBBS runs against a real hbbs when RDPANEL_IT_HBBS is set
// to its rendezvous TCP address (e.g. 127.0.0.1:21116):
//
//  1. a fresh id is reported offline;
//  2. after registering it with a RegisterPk over TCP (as a client does), it is
//     reported online by the batched OnlineRequest (bit order checked by
//     placing it in the middle of a chunk).
func TestIntegrationRealHBBS(t *testing.T) {
	addr := os.Getenv("RDPANEL_IT_HBBS")
	if addr == "" {
		t.Skip("RDPANEL_IT_HBBS not set")
	}
	p, err := New(Config{Addr: addr, Devices: &staticLister{}})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	var rnd [4]byte
	_, _ = rand.Read(rnd[:])
	id := fmt.Sprintf("it%08x", binary.BigEndian.Uint32(rnd[:]))
	ids := []string{"nonexistent01", "nonexistent02", "nonexistent03", id, "nonexistent04", "nonexistent05", "nonexistent06", "nonexistent07", "nonexistent08"}

	got, rtt, err := p.Query(ctx, ids)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	t.Logf("rtt %v, before registration: %v", rtt, got)
	for i, on := range got {
		if on {
			t.Fatalf("id %d online before registration", i)
		}
	}

	conn := registerPeer(t, addr, id)
	defer conn.Close()

	deadline := time.Now().Add(5 * time.Second)
	for {
		got, _, err = p.Query(ctx, ids)
		if err != nil {
			t.Fatal(err)
		}
		if got[3] {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("registered id never reported online: %v", got)
		}
		time.Sleep(200 * time.Millisecond)
	}
	for i, on := range got {
		if on != (i == 3) {
			t.Fatalf("bit %d = %v (bitmap order)", i, on)
		}
	}
	// Empty probe.
	if _, _, err := p.Query(ctx, nil); err != nil {
		t.Fatalf("empty probe: %v", err)
	}
}

// registerPeer sends RendezvousMessage{register_pk(15){id=1, uuid=2, pk=3}}
// over TCP and waits for the RegisterPkResponse (field 16). Test-only encoding.
func registerPeer(t *testing.T, addr, id string) net.Conn {
	t.Helper()
	conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	pk := make([]byte, 32)
	uuid := make([]byte, 16)
	_, _ = rand.Read(pk)
	_, _ = rand.Read(uuid)
	field := func(dst []byte, num int, v []byte) []byte {
		dst = binary.AppendUvarint(dst, uint64(num)<<3|2)
		dst = binary.AppendUvarint(dst, uint64(len(v)))
		return append(dst, v...)
	}
	inner := field(nil, 1, []byte(id))
	inner = field(inner, 2, uuid)
	inner = field(inner, 3, pk)
	frame, _ := wire.AppendFrame(nil, field(nil, 15, inner))
	if _, err := conn.Write(frame); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 4; i++ {
		msg, err := wire.ReadFrame(conn, 1<<16)
		if err != nil {
			t.Fatalf("register: %v", err)
		}
		env, err := wire.DecodeEnvelope(msg)
		if err != nil {
			t.Fatal(err)
		}
		if env.Field == 16 {
			t.Logf("RegisterPkResponse body %x (empty = OK)", env.Body)
			_ = conn.SetDeadline(time.Time{})
			return conn
		}
	}
	t.Fatal("no RegisterPkResponse")
	return nil
}
