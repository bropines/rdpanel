// Command seed-demo fills a running rdpanel with realistic demo data through
// its public APIs: users and groups through the admin API, devices,
// heartbeats, logins, address books and audit records through the RustDesk
// client API (exactly what real clients send).
//
//	RDPANEL_SEED_ADMIN_PASSWORD=... go run ./scripts/seed-demo -url http://127.0.0.1:21114 [-keepalive]
//
// The admin account must exist (rdpanel user create --admin). Re-running is
// safe for users/groups/books (audit rows are added again). With -keepalive it
// only keeps seven of the fake devices sending heartbeats every 10 s until
// interrupted, so they stay "online" in the panel when hbbs is not running.
package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"time"

	"github.com/bropines/rdpanel/internal/hbbs/wire"
)

type device struct {
	ID, Host, OS, CPU, Mem, User, Version string
	Owner                                 string // username that logs in from it ("" = none)
	Group                                 string // device group name
}

var devices = []device{
	{"123456789", "ws-alexei", "Windows 11 Pro 23H2", "Intel Core i7-12700, 12 cores", "32GB", "alexei", "1.4.2", "admin", "Office"},
	{"234567891", "mbp-ivan", "macOS 14.6 Sonoma", "Apple M3 Pro, 12 cores", "36GB", "ivan", "1.4.2", "ivan", "Office"},
	{"345678912", "build-01", "Ubuntu 24.04.1 LTS", "AMD EPYC 7443P, 24 cores", "128GB", "ci", "1.4.1", "", "Servers"},
	{"456789123", "build-02", "Debian GNU/Linux 12 (bookworm)", "AMD EPYC 7443P, 24 cores", "128GB", "ci", "1.4.1", "", "Servers"},
	{"567891234", "reception-pc", "Windows 10 Pro 22H2", "Intel Core i3-10100, 4 cores", "8GB", "reception", "1.3.9", "maria", "Office"},
	{"678912345", "pixel-maria", "Android 14", "Tensor G3", "8GB", "maria", "1.4.0", "maria", ""},
	{"789123456", "nas", "Linux 6.6 (TrueNAS SCALE)", "Intel Xeon E-2236, 6 cores", "64GB", "root", "1.3.9", "", "Servers"},
	{"891234567", "ws-design", "Windows 11 Pro 24H2", "AMD Ryzen 9 7950X, 16 cores", "64GB", "design", "1.4.2", "ivan", "Office"},
	{"912345678", "kiosk-lobby", "Windows 10 IoT Enterprise", "Intel Celeron J4125, 4 cores", "4GB", "kiosk", "1.3.8", "", ""},
	{"102345678", "mbp-old", "macOS 12.7 Monterey", "Intel Core i5-8259U, 4 cores", "16GB", "olga", "1.2.6", "", ""},
}

type users struct {
	Name, Display, Email, Role, Group string
}

var demoUsers = []users{
	{"ivan", "Ivan Sokolov", "ivan@example.com", "user", "Engineering"},
	{"maria", "Maria Kuznetsova", "maria@example.com", "user", "Support"},
	{"olga", "Olga Petrova", "olga@example.com", "user", "Support"},
}

func main() {
	base := flag.String("url", "http://127.0.0.1:21114", "panel base URL")
	admin := flag.String("admin", "admin", "admin username")
	userPw := flag.String("user-password", "demo-password-1", "password of the created demo users")
	keepalive := flag.Bool("keepalive", false, "only send heartbeats every 10 s until interrupted (run after seeding)")
	hbbsAddr := flag.String("hbbs", "", "with -keepalive: also register the devices with this hbbs (rendezvous TCP address) so hbbs presence reports them online")
	flag.Parse()
	adminPw := os.Getenv("RDPANEL_SEED_ADMIN_PASSWORD")
	if adminPw == "" {
		log.Fatal("set RDPANEL_SEED_ADMIN_PASSWORD")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	s := &seeder{base: strings.TrimRight(*base, "/")}
	jar, _ := cookiejar.New(nil)
	s.admin = &http.Client{Jar: jar, Timeout: 20 * time.Second}
	s.hc = &http.Client{Timeout: 20 * time.Second}

	if *keepalive {
		s.hbbs, s.registered = *hbbsAddr, map[string]bool{}
		heartbeatLoop(ctx, s)
		return
	}
	must(s.adminLogin(*admin, adminPw))
	groups := map[string]int64{}
	for _, g := range []string{"Engineering", "Support"} {
		groups[g] = s.ensure("/user-groups", map[string]any{"name": g, "visibility": "shared"}, "name", g)
	}
	dgroups := map[string]int64{}
	for _, g := range []string{"Office", "Servers"} {
		dgroups[g] = s.ensure("/device-groups", map[string]any{"name": g, "note": g + " machines"}, "name", g)
	}
	for _, u := range demoUsers {
		s.ensureUser(u, *userPw, groups[u.Group])
	}

	// Devices register like the client: sysinfo then heartbeat.
	for _, d := range devices {
		code, body := s.client("POST", "/api/sysinfo", "", map[string]any{"id": d.ID, "uuid": uuidOf(d.ID), "cpu": d.CPU,
			"memory": d.Mem, "os": d.OS, "hostname": d.Host, "username": d.User, "version": d.Version})
		if code != 200 {
			log.Fatalf("sysinfo %s: %d %s", d.ID, code, body)
		}
		s.heartbeat(d)
	}
	// Owners sign in from their devices (binds the device to the user).
	tokens := map[string]string{}
	for _, d := range devices {
		if d.Owner == "" || d.Owner == *admin {
			if d.Owner == *admin {
				tokens[d.ID] = s.clientLogin(*admin, adminPw, d)
			}
			continue
		}
		tokens[d.ID] = s.clientLogin(d.Owner, *userPw, d)
	}
	// Device groups and aliases through the admin API.
	for _, d := range devices {
		id := s.deviceID(d.ID)
		patch := map[string]any{"alias": strings.ToUpper(d.Host[:1]) + d.Host[1:]}
		if d.Group != "" {
			patch["device_group_id"] = dgroups[d.Group]
		}
		s.adminDo("PATCH", fmt.Sprintf("/devices/%d", id), patch, nil)
	}

	// Personal address book of ivan, written with the client API.
	if tok := tokens["234567891"]; tok != "" {
		var pb struct{ GUID string }
		code, body := s.client("POST", "/api/ab/personal", tok, nil)
		if code != 200 || json.Unmarshal(body, &pb) != nil {
			log.Fatalf("ab/personal: %d %s", code, body)
		}
		for name, color := range map[string]uint32{"office": 0xFF2196F3, "servers": 0xFFF44336, "home": 0xFF4CAF50} {
			s.client("POST", "/api/ab/tag/add/"+pb.GUID, tok, map[string]any{"name": name, "color": color})
		}
		for _, p := range []map[string]any{
			{"id": "345678912", "alias": "Build 01", "hostname": "build-01", "platform": "Linux", "tags": []string{"servers"}},
			{"id": "456789123", "alias": "Build 02", "hostname": "build-02", "platform": "Linux", "tags": []string{"servers"}},
			{"id": "891234567", "alias": "Design workstation", "hostname": "ws-design", "platform": "Windows", "tags": []string{"office"}},
			{"id": "102345678", "alias": "Old MacBook", "hostname": "mbp-old", "platform": "Mac OS", "tags": []string{"home"}, "note": "spare laptop"},
		} {
			if code, body := s.client("POST", "/api/ab/peer/add/"+pb.GUID, tok, p); code != 200 {
				log.Fatalf("ab add: %d %s", code, body)
			}
		}
	}
	// A shared book owned by the admin, shared with Support.
	bookID := s.ensure("/address-books", map[string]any{"name": "Support desk", "note": "Machines the support team may reach"}, "name", "Support desk")
	s.adminDo("POST", fmt.Sprintf("/address-books/%d/shares", bookID),
		map[string]any{"grantee_type": "group", "grantee_id": groups["Support"], "permission": "read_write"}, nil)
	s.adminDo("POST", fmt.Sprintf("/address-books/%d/entries/from-devices", bookID),
		map[string]any{"device_ids": []int64{s.deviceID("567891234"), s.deviceID("912345678")}, "tags": []string{"frontdesk"}}, nil)

	// Audit records as clients upload them.
	n := 0
	nonce := func() string { n++; return fmt.Sprintf("seed-%d-%d", time.Now().UnixNano(), n) }
	for i, d := range devices[:6] {
		ctrl := devices[(i+1)%len(devices)]
		sid := fmt.Sprintf("%d", 9000000000000000000+uint64(i))
		conn := 10 + i
		s.client("POST", "/api/audit/conn", "", map[string]any{"id": d.ID, "uuid": uuidOf(d.ID), "conn_id": conn, "action": "new",
			"ip": fmt.Sprintf("192.0.2.%d", 10+i), "session_id": 0, "nonce": nonce()})
		s.client("POST", "/api/audit/conn", "", map[string]any{"id": d.ID, "uuid": uuidOf(d.ID), "conn_id": conn,
			"session_id": json.Number(sid), "peer": []string{ctrl.ID, ctrl.Host}, "type": i % 3, "nonce": nonce()})
		if i%2 == 0 {
			s.client("POST", "/api/audit/conn", "", map[string]any{"id": d.ID, "uuid": uuidOf(d.ID), "conn_id": conn, "action": "close", "nonce": nonce()})
		}
		if i%3 == 1 {
			info, _ := json.Marshal(map[string]any{"files": [][]any{{"report.pdf", 482133}, {"notes.txt", 1200}}, "ip": "192.0.2.50", "name": ctrl.Host, "num": 2})
			s.client("POST", "/api/audit/file", "", map[string]any{"id": d.ID, "uuid": uuidOf(d.ID), "peer_id": ctrl.ID, "conn_id": conn,
				"type": i % 2, "path": "C:\\Users\\" + d.User + "\\Documents", "is_file": false, "info": string(info), "nonce": nonce()})
		}
	}
	alarm, _ := json.Marshal(map[string]any{"ip": "203.0.113.77", "id": "999999999", "name": "unknown"})
	s.client("POST", "/api/audit/alarm", "", map[string]any{"id": "912345678", "uuid": uuidOf("912345678"), "typ": 1, "info": string(alarm), "nonce": nonce()})
	// A failed client login for the audit log.
	s.client("POST", "/api/login", "", map[string]any{"username": "ivan", "password": "wrong", "id": "234567891", "uuid": uuidOf("234567891"),
		"deviceInfo": map[string]string{"os": "macOS", "type": "client", "name": "mbp-ivan"}})

	log.Printf("seeded %d devices, %d users", len(devices), len(demoUsers))
}

// heartbeatLoop keeps the first seven devices online (heartbeat fallback,
// plus hbbs registrations with -hbbs).
func heartbeatLoop(ctx context.Context, s *seeder) {
	log.Print("keepalive: sending heartbeats every 10 s (Ctrl-C to stop)")
	for _, d := range devices[:7] {
		s.heartbeat(d)
	}
	t := time.NewTicker(10 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			for _, d := range devices[:7] { // the last three stay offline
				s.heartbeat(d)
			}
		}
	}
}

type seeder struct {
	hbbs       string
	registered map[string]bool
	base       string
	admin      *http.Client
	hc         *http.Client
	csrf       string
}

// registerWithHBBS sends what a client sends to hbbs after start-up, a
// RendezvousMessage{register_pk (15): {id 1, uuid 2, pk 3}} over TCP with the
// rendezvous framing, and waits for the RegisterPkResponse (16). uuid and pk
// are stable per device. Demo/test helper only.
func registerWithHBBS(addr, id string) error {
	conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		return err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
	uuid := sha256.Sum256([]byte("seed-demo-uuid:" + id))
	pk := sha256.Sum256([]byte("seed-demo-pk:" + id))
	field := func(dst []byte, num int, v []byte) []byte {
		dst = binary.AppendUvarint(dst, uint64(num)<<3|2)
		dst = binary.AppendUvarint(dst, uint64(len(v)))
		return append(dst, v...)
	}
	inner := field(nil, 1, []byte(id))
	inner = field(inner, 2, uuid[:16])
	inner = field(inner, 3, pk[:])
	frame, err := wire.AppendFrame(nil, field(nil, 15, inner))
	if err != nil {
		return err
	}
	if _, err := conn.Write(frame); err != nil {
		return err
	}
	for i := 0; i < 4; i++ {
		msg, err := wire.ReadFrame(conn, 1<<16)
		if err != nil {
			return err
		}
		if env, err := wire.DecodeEnvelope(msg); err == nil && env.Field == 16 {
			if len(env.Body) > 0 {
				return fmt.Errorf("RegisterPkResponse %x", env.Body)
			}
			return nil
		}
	}
	return errors.New("no RegisterPkResponse")
}

// pingHBBS sends the periodic UDP RendezvousMessage{register_peer (6): {id 1}}
// that keeps a client "online" in hbbs and waits for register_peer_response (7).
func pingHBBS(addr, id string) error {
	c, err := net.Dial("udp", addr)
	if err != nil {
		return err
	}
	defer c.Close()
	inner := binary.AppendUvarint(nil, 1<<3|2)
	inner = binary.AppendUvarint(inner, uint64(len(id)))
	inner = append(inner, id...)
	msg := binary.AppendUvarint(nil, 6<<3|2)
	msg = binary.AppendUvarint(msg, uint64(len(inner)))
	msg = append(msg, inner...)
	if _, err := c.Write(msg); err != nil {
		return err
	}
	_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 512)
	n, err := c.Read(buf)
	if err != nil {
		return err
	}
	if n == 0 || buf[0] != 7<<3|2 {
		return fmt.Errorf("unexpected answer %x", buf[:n])
	}
	return nil
}

func must(err error) {
	if err != nil {
		log.Fatal(err)
	}
}

// uuidOf derives a stable base64 "uuid" for a device id (clients send base64).
func uuidOf(id string) string {
	h := sha256.Sum256([]byte("seed-demo:" + id))
	return base64.StdEncoding.EncodeToString(h[:16])
}

func (s *seeder) client(method, path, token string, body any) (int, []byte) {
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, s.base+path, rd)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "RustDesk/1.4.2 (seed-demo)")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := s.hc.Do(req)
	if err != nil {
		log.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

func (s *seeder) heartbeat(d device) {
	if s.hbbs != "" {
		if !s.registered[d.ID] {
			// Once: publish the key (later attempts answer TOO_FREQUENT).
			if err := registerWithHBBS(s.hbbs, d.ID); err != nil {
				log.Printf("hbbs register_pk %s: %v", d.ID, err)
			}
			s.registered[d.ID] = true
		}
		if err := pingHBBS(s.hbbs, d.ID); err != nil {
			log.Printf("hbbs register_peer %s: %v", d.ID, err)
		}
	}
	if code, body := s.client("POST", "/api/heartbeat", "", map[string]any{"id": d.ID, "uuid": uuidOf(d.ID), "ver": 1004002, "conns": []int{}}); code != 200 {
		log.Printf("heartbeat %s: %d %s", d.ID, code, body)
	}
}

func (s *seeder) clientLogin(user, pw string, d device) string {
	code, body := s.client("POST", "/api/login", "", map[string]any{"username": user, "password": pw, "id": d.ID, "uuid": uuidOf(d.ID),
		"autoLogin": true, "type": "account", "deviceInfo": map[string]string{"os": d.OS, "type": "client", "name": d.Host}})
	var r struct {
		AccessToken string `json:"access_token"`
		Type        string `json:"type"`
	}
	if code != 200 || json.Unmarshal(body, &r) != nil || r.AccessToken == "" {
		log.Printf("client login %s from %s: %d %s (2FA accounts cannot be seeded)", user, d.ID, code, body)
		return ""
	}
	return r.AccessToken
}

// adminDo calls the admin API; out may be nil. 409 conflicts are returned, not fatal.
func (s *seeder) adminDo(method, path string, body, out any) int {
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, s.base+"/api/v1/admin"+path, rd)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-CSRF-Token", s.csrf)
	resp, err := s.admin.Do(req)
	if err != nil {
		log.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 && resp.StatusCode != http.StatusConflict {
		log.Fatalf("%s %s: %d %s", method, path, resp.StatusCode, b)
	}
	if out != nil && resp.StatusCode < 300 {
		must(json.Unmarshal(b, out))
	}
	return resp.StatusCode
}

func (s *seeder) adminLogin(user, pw string) error {
	var c struct {
		CSRF string `json:"csrf_token"`
	}
	s.adminDo("GET", "/auth/csrf", nil, &c)
	s.csrf = c.CSRF
	var r struct {
		MFARequired bool `json:"mfa_required"`
		Me          *struct {
			CSRF string `json:"csrf_token"`
		} `json:"me"`
	}
	s.adminDo("POST", "/auth/login", map[string]string{"username": user, "password": pw}, &r)
	if r.MFARequired || r.Me == nil {
		return errors.New("the seed admin must not have 2FA enabled")
	}
	s.csrf = r.Me.CSRF
	return nil
}

// ensure creates a resource unless one with field == value exists; returns its id.
func (s *seeder) ensure(path string, body map[string]any, field, value string) int64 {
	var list struct {
		Items []map[string]any `json:"items"`
	}
	s.adminDo("GET", path+"?page_size=500&q="+url.QueryEscape(value), nil, &list)
	for _, it := range list.Items {
		if it[field] == value {
			return int64(it["id"].(float64))
		}
	}
	var created map[string]any
	s.adminDo("POST", path, body, &created)
	return int64(created["id"].(float64))
}

func (s *seeder) ensureUser(u users, pw string, group int64) {
	var list struct {
		Items []map[string]any `json:"items"`
	}
	s.adminDo("GET", "/users?q="+url.QueryEscape(u.Name), nil, &list)
	for _, it := range list.Items {
		if it["username"] == u.Name {
			return
		}
	}
	s.adminDo("POST", "/users", map[string]any{"username": u.Name, "password": pw, "display_name": u.Display,
		"email": u.Email, "role": u.Role, "user_group_id": group}, nil)
}

func (s *seeder) deviceID(rid string) int64 {
	var list struct {
		Items []struct {
			ID         int64  `json:"id"`
			RustdeskID string `json:"rustdesk_id"`
		} `json:"items"`
	}
	s.adminDo("GET", "/devices?q="+rid, nil, &list)
	for _, d := range list.Items {
		if d.RustdeskID == rid {
			return d.ID
		}
	}
	log.Fatalf("device %s not found", rid)
	return 0
}
