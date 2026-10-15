// Command wsclient reproduces a RustDesk client in WebSocket/TCP rendezvous
// mode against a real hbbs and prints its online timeline, to check hbbs'
// TCP_KEEPALIVE option (bropines/rustdesk-server >= 393f872).
//
// It connects to hbbs' WebSocket port, registers with a RegisterPk (fresh
// id, uuid and 32-byte pk) and afterwards ONLY echoes the empty frames the
// server sends, like the client does. As the client, it reconnects and
// re-registers when nothing was received for -client-timeout (keep_alive*1.5
// = 90 s). Meanwhile it polls the id's online state with OnlineRequest on the
// rendezvous TCP port, the way the panel does.
//
//	go run ./scripts/wsclient -ws 127.0.0.1:31118 -hbbs 127.0.0.1:31116 -duration 150s
//
// With -toggle-at/-admin it flips tcp-keepalive through the admin channel
// mid-run (runtime toggle check).
//
// Demo/test tool only: the protobuf encoding is hand-written for the few
// fields used, and the WebSocket client is the minimum RFC 6455 subset hbbs
// needs (no extensions, no fragmentation of sent frames).
package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/bropines/rustdesk-panel/internal/hbbs/control"
	"github.com/bropines/rustdesk-panel/internal/hbbs/presence"
	"github.com/bropines/rustdesk-panel/internal/hbbs/wire"
)

func main() {
	wsAddr := flag.String("ws", "127.0.0.1:21118", "hbbs WebSocket address (rendezvous port + 2)")
	hbbsAddr := flag.String("hbbs", "127.0.0.1:21116", "hbbs rendezvous TCP address (OnlineRequest)")
	adminAddr := flag.String("admin", "", "hbbs admin address (rendezvous port - 1), needed for -toggle-at")
	dur := flag.Duration("duration", 150*time.Second, "how long to run")
	poll := flag.Duration("poll", time.Second, "online poll interval")
	clientTimeout := flag.Duration("client-timeout", 90*time.Second, "reconnect after this long without any frame from hbbs")
	toggleAt := flag.Duration("toggle-at", 0, "set tcp-keepalive to -toggle-to after this long (0 = never)")
	toggleTo := flag.Bool("toggle-to", true, "value for -toggle-at")
	id := flag.String("id", "", "RustDesk id to register (default: random)")
	flag.Parse()

	if *id == "" {
		var b [4]byte
		_, _ = rand.Read(b[:])
		*id = fmt.Sprintf("wsit%08x", binary.BigEndian.Uint32(b[:]))
	}
	uuid, pk := make([]byte, 16), make([]byte, 32)
	_, _ = rand.Read(uuid)
	_, _ = rand.Read(pk)

	start := time.Now()
	logf := func(format string, a ...any) {
		fmt.Printf("[%6.1fs] %s\n", time.Since(start).Seconds(), fmt.Sprintf(format, a...))
	}
	ctx, cancel := context.WithTimeout(context.Background(), *dur)
	defer cancel()

	st := &stats{}
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		runClient(ctx, *wsAddr, *id, uuid, pk, *clientTimeout, logf, st)
	}()

	if *toggleAt > 0 {
		if *adminAddr == "" {
			log.Fatal("-toggle-at needs -admin")
		}
		ctl, err := control.New(control.Config{HBBSAddr: *adminAddr, HBBRAddr: "127.0.0.1:1"})
		if err != nil {
			log.Fatal(err)
		}
		if v, err := ctl.TCPKeepalive(ctx); err == nil {
			logf("admin: tcp-keepalive is %v", v)
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case <-ctx.Done():
				return
			case <-time.After(*toggleAt):
			}
			if err := ctl.SetTCPKeepalive(ctx, *toggleTo); err != nil {
				logf("admin: set tcp-keepalive: %v", err)
				return
			}
			v, err := ctl.TCPKeepalive(ctx)
			logf("admin: tcp-keepalive set to %v (read-back %v %v)", *toggleTo, v, err)
		}()
	}

	p, err := presence.New(presence.Config{Addr: *hbbsAddr, Devices: noDevices{}})
	if err != nil {
		log.Fatal(err)
	}
	var (
		last     *bool
		onlineN  int
		samples  int
		segStart = time.Now()
	)
	t := time.NewTicker(*poll)
	defer t.Stop()
	for ctx.Err() == nil {
		qctx, qcancel := context.WithTimeout(ctx, 3*time.Second)
		got, _, err := p.Query(qctx, []string{*id})
		qcancel()
		if err != nil {
			if ctx.Err() == nil {
				logf("online query: %v", err)
			}
		} else {
			on := got[0]
			samples++
			if on {
				onlineN++
			}
			if last == nil || *last != on {
				if last != nil {
					logf("ONLINE=%v (previous state lasted %.1fs)", on, time.Since(segStart).Seconds())
				} else {
					logf("ONLINE=%v", on)
				}
				segStart = time.Now()
				last = &on
			}
		}
		select {
		case <-ctx.Done():
		case <-t.C:
		}
	}
	wg.Wait()
	st.mu.Lock()
	defer st.mu.Unlock()
	fmt.Printf("SUMMARY id=%s duration=%s online=%d/%d samples (%.0f%%) registrations=%d keepalives=%d",
		*id, dur.String(), onlineN, samples, 100*float64(onlineN)/float64(max(samples, 1)), st.registrations, len(st.keepalives))
	if n := len(st.keepalives); n > 1 {
		var gaps []string
		for i := 1; i < n; i++ {
			gaps = append(gaps, fmt.Sprintf("%.1f", st.keepalives[i].Sub(st.keepalives[i-1]).Seconds()))
		}
		fmt.Printf(" keepalive_gaps_s=[%s]", strings.Join(gaps, " "))
	}
	fmt.Println()
}

type noDevices struct{}

func (noDevices) ListRustdeskIDs(context.Context) ([]string, error) { return nil, nil }

type stats struct {
	mu            sync.Mutex
	registrations int
	keepalives    []time.Time
}

// runClient keeps one registered WebSocket connection, reconnecting like the
// RustDesk client: after clientTimeout without any received frame (the
// client does not react to the server closing the socket any faster than
// that in this model; see the closed-by-server log line for when it did).
func runClient(ctx context.Context, addr, id string, uuid, pk []byte, clientTimeout time.Duration,
	logf func(string, ...any), st *stats) {
	for ctx.Err() == nil {
		regAt := time.Now()
		err := session(ctx, addr, id, uuid, pk, clientTimeout, logf, st)
		if ctx.Err() != nil {
			return
		}
		logf("client: session ended: %v", err)
		// Reconnect when the client's own timer fires: clientTimeout after the
		// last frame (here: after registration or the last keepalive).
		wait := clientTimeout - time.Since(regAt)
		st.mu.Lock()
		if n := len(st.keepalives); n > 0 && st.keepalives[n-1].After(regAt) {
			wait = clientTimeout - time.Since(st.keepalives[n-1])
		}
		st.mu.Unlock()
		if wait > 0 {
			select {
			case <-ctx.Done():
				return
			case <-time.After(wait):
			}
		}
	}
}

func session(ctx context.Context, addr, id string, uuid, pk []byte, clientTimeout time.Duration,
	logf func(string, ...any), st *stats) error {
	ws, err := dialWS(ctx, addr)
	if err != nil {
		return err
	}
	defer ws.Close()
	go func() { <-ctx.Done(); ws.Close() }()

	if err := ws.writeBinary(encodeRegisterPk(id, uuid, pk)); err != nil {
		return err
	}
	logf("client: RegisterPk sent (id %s)", id)
	for {
		_ = ws.conn.SetReadDeadline(time.Now().Add(clientTimeout))
		msg, err := ws.readMessage()
		if err != nil {
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				return fmt.Errorf("no frame for %s (client timeout)", clientTimeout)
			}
			if errors.Is(err, io.EOF) || errors.Is(err, errClosed) {
				logf("client: closed by server")
				return err
			}
			return err
		}
		if len(msg) == 0 {
			st.mu.Lock()
			st.keepalives = append(st.keepalives, time.Now())
			st.mu.Unlock()
			logf("client: empty frame received, echoing")
			if err := ws.writeBinary(nil); err != nil {
				return err
			}
			continue
		}
		env, err := wire.DecodeEnvelope(msg)
		if err != nil {
			logf("client: undecodable message %x", msg)
			continue
		}
		if env.Field == 16 { // register_pk_response
			st.mu.Lock()
			st.registrations++
			st.mu.Unlock()
			logf("client: RegisterPkResponse %x (empty = OK)", env.Body)
			continue
		}
		logf("client: message field %d ignored", env.Field)
	}
}

// encodeRegisterPk returns RendezvousMessage{register_pk(15){id=1, uuid=2, pk=3}}.
func encodeRegisterPk(id string, uuid, pk []byte) []byte {
	field := func(dst []byte, num int, v []byte) []byte {
		dst = binary.AppendUvarint(dst, uint64(num)<<3|2)
		dst = binary.AppendUvarint(dst, uint64(len(v)))
		return append(dst, v...)
	}
	inner := field(nil, 1, []byte(id))
	inner = field(inner, 2, uuid)
	inner = field(inner, 3, pk)
	return field(nil, 15, inner)
}

// ---- minimal RFC 6455 client ----

var errClosed = errors.New("websocket closed")

type wsConn struct {
	conn net.Conn
	br   *bufio.Reader
	wmu  sync.Mutex
}

func dialWS(ctx context.Context, addr string) (*wsConn, error) {
	var d net.Dialer
	c, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, err
	}
	key := make([]byte, 16)
	_, _ = rand.Read(key)
	k := base64.StdEncoding.EncodeToString(key)
	req := "GET / HTTP/1.1\r\nHost: " + addr + "\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n" +
		"Sec-WebSocket-Key: " + k + "\r\nSec-WebSocket-Version: 13\r\n\r\n"
	_ = c.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := io.WriteString(c, req); err != nil {
		c.Close()
		return nil, err
	}
	br := bufio.NewReader(c)
	resp, err := http.ReadResponse(br, nil)
	if err != nil {
		c.Close()
		return nil, err
	}
	resp.Body.Close()
	h := sha1.Sum([]byte(k + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"))
	if resp.StatusCode != http.StatusSwitchingProtocols || resp.Header.Get("Sec-WebSocket-Accept") != base64.StdEncoding.EncodeToString(h[:]) {
		c.Close()
		return nil, fmt.Errorf("websocket handshake: %s", resp.Status)
	}
	_ = c.SetDeadline(time.Time{})
	return &wsConn{conn: c, br: br}, nil
}

func (w *wsConn) Close() error { return w.conn.Close() }

func (w *wsConn) writeFrame(op byte, payload []byte) error {
	w.wmu.Lock()
	defer w.wmu.Unlock()
	hdr := []byte{0x80 | op}
	switch n := len(payload); {
	case n < 126:
		hdr = append(hdr, 0x80|byte(n))
	case n <= 0xFFFF:
		hdr = append(hdr, 0x80|126)
		hdr = binary.BigEndian.AppendUint16(hdr, uint16(n))
	default:
		hdr = append(hdr, 0x80|127)
		hdr = binary.BigEndian.AppendUint64(hdr, uint64(n))
	}
	var mask [4]byte
	_, _ = rand.Read(mask[:])
	hdr = append(hdr, mask[:]...)
	body := make([]byte, len(payload))
	for i, b := range payload {
		body[i] = b ^ mask[i%4]
	}
	_ = w.conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	_, err := w.conn.Write(append(hdr, body...))
	return err
}

func (w *wsConn) writeBinary(p []byte) error { return w.writeFrame(0x2, p) }

// readMessage returns the next binary/text message, answering pings and
// reassembling fragments.
func (w *wsConn) readMessage() ([]byte, error) {
	var msg []byte
	for {
		var h [2]byte
		if _, err := io.ReadFull(w.br, h[:]); err != nil {
			return nil, err
		}
		fin, op := h[0]&0x80 != 0, h[0]&0x0F
		n := uint64(h[1] & 0x7F)
		switch n {
		case 126:
			var b [2]byte
			if _, err := io.ReadFull(w.br, b[:]); err != nil {
				return nil, err
			}
			n = uint64(binary.BigEndian.Uint16(b[:]))
		case 127:
			var b [8]byte
			if _, err := io.ReadFull(w.br, b[:]); err != nil {
				return nil, err
			}
			n = binary.BigEndian.Uint64(b[:])
		}
		if n > 1<<20 {
			return nil, fmt.Errorf("websocket frame too large: %d", n)
		}
		var mask []byte
		if h[1]&0x80 != 0 {
			mask = make([]byte, 4)
			if _, err := io.ReadFull(w.br, mask); err != nil {
				return nil, err
			}
		}
		p := make([]byte, n)
		if _, err := io.ReadFull(w.br, p); err != nil {
			return nil, err
		}
		for i := range p {
			if mask != nil {
				p[i] ^= mask[i%4]
			}
		}
		switch op {
		case 0x8: // close
			_ = w.writeFrame(0x8, p)
			return nil, errClosed
		case 0x9: // ping
			if err := w.writeFrame(0xA, p); err != nil {
				return nil, err
			}
			continue
		case 0xA: // pong
			continue
		}
		msg = append(msg, p...)
		if fin {
			if msg == nil {
				msg = []byte{}
			}
			return msg, nil
		}
	}
}

func init() {
	log.SetFlags(0)
	log.SetOutput(os.Stderr)
}
