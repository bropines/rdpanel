package agent

import (
	"bufio"
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/bropines/rdpanel/internal/relays/agentproto"
)

// Files in the agent data directory.
const (
	KeyFile    = "agent.key"
	StateFile  = "agent-state.json"
	HealthFile = "agent-health.json"
)

// State is persisted across restarts: the agent keeps enforcing its last
// config while the panel is unreachable.
type State struct {
	PanelURL  string             `json:"panel_url"`
	AgentID   int64              `json:"agent_id"`
	RelayName string             `json:"relay_name,omitempty"`
	Config    *agentproto.Config `json:"config,omitempty"`
}

// LoadState reads dir/agent-state.json (zero State when missing).
func LoadState(dir string) (State, error) {
	var st State
	raw, err := os.ReadFile(filepath.Join(dir, StateFile))
	if errors.Is(err, os.ErrNotExist) {
		return st, nil
	}
	if err != nil {
		return st, err
	}
	if err := json.Unmarshal(raw, &st); err != nil {
		return st, fmt.Errorf("agent: %s: %w", StateFile, err)
	}
	return st, nil
}

// SaveState writes dir/agent-state.json atomically (0600).
func SaveState(dir string, st State) error {
	raw, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	return writeAtomic(filepath.Join(dir, StateFile), raw, 0o600)
}

func writeAtomic(path string, data []byte, mode os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// LoadOrCreateKey returns the agent's ed25519 identity from dir/agent.key
// (base64 of the 32-byte seed, 0600), generating it on first start.
func LoadOrCreateKey(dir string) (ed25519.PrivateKey, bool, error) {
	path := filepath.Join(dir, KeyFile)
	raw, err := os.ReadFile(path)
	if err == nil {
		seed, derr := base64.StdEncoding.DecodeString(strings.TrimSpace(string(raw)))
		if derr != nil || len(seed) != ed25519.SeedSize {
			return nil, false, fmt.Errorf("agent: %s is not a base64 ed25519 seed", path)
		}
		return ed25519.NewKeyFromSeed(seed), false, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, false, err
	}
	seed := make([]byte, ed25519.SeedSize)
	if _, err := rand.Read(seed); err != nil {
		return nil, false, err
	}
	if err := writeAtomic(path, []byte(base64.StdEncoding.EncodeToString(seed)+"\n"), 0o600); err != nil {
		return nil, false, err
	}
	return ed25519.NewKeyFromSeed(seed), true, nil
}

// Health is written to dir/agent-health.json for `rdpanel agent health`.
type Health struct {
	Time      time.Time `json:"time"`
	Connected bool      `json:"connected"`
	// Run is the desired hbbr state; HBBRUp the observed one.
	Run    bool `json:"run"`
	HBBRUp bool `json:"hbbr_up"`
}

// CheckHealth reads the health file: it must be fresh, and hbbr must be up
// whenever it should run.
func CheckHealth(dir string, now time.Time, maxAge time.Duration) error {
	raw, err := os.ReadFile(filepath.Join(dir, HealthFile))
	if err != nil {
		return fmt.Errorf("no health file (is the agent running?): %w", err)
	}
	var h Health
	if err := json.Unmarshal(raw, &h); err != nil {
		return err
	}
	if age := now.Sub(h.Time); age > maxAge {
		return fmt.Errorf("health file is %s old", age.Round(time.Second))
	}
	if h.Run && !h.HBBRUp {
		return errors.New("hbbr is not running")
	}
	return nil
}

// ---- host sampling (Linux /proc; zero values elsewhere) ----

type hostSampler struct {
	proc    string
	lastAt  time.Time
	lastRx  uint64
	lastTx  uint64
	hasLast bool
}

func (h *hostSampler) sample(now time.Time) agentproto.HostStatus {
	var out agentproto.HostStatus
	if raw, err := os.ReadFile(filepath.Join(h.proc, "loadavg")); err == nil {
		if f := strings.Fields(string(raw)); len(f) > 0 {
			out.Load1, _ = strconv.ParseFloat(f[0], 64)
		}
	}
	if raw, err := os.ReadFile(filepath.Join(h.proc, "meminfo")); err == nil {
		var total, avail float64
		sc := bufio.NewScanner(bytes.NewReader(raw))
		for sc.Scan() {
			f := strings.Fields(sc.Text())
			if len(f) < 2 {
				continue
			}
			v, _ := strconv.ParseFloat(f[1], 64)
			switch f[0] {
			case "MemTotal:":
				total = v
			case "MemAvailable:":
				avail = v
			}
		}
		if total > 0 {
			out.MemUsedPct = (total - avail) / total * 100
		}
	}
	if rx, tx, ok := readNetDev(filepath.Join(h.proc, "net", "dev")); ok {
		if h.hasLast && now.After(h.lastAt) && rx >= h.lastRx && tx >= h.lastTx {
			secs := now.Sub(h.lastAt).Seconds()
			out.RxBps = float64(rx-h.lastRx) * 8 / secs
			out.TxBps = float64(tx-h.lastTx) * 8 / secs
		}
		h.lastAt, h.lastRx, h.lastTx, h.hasLast = now, rx, tx, true
	}
	return out
}

// readNetDev sums the byte counters of every interface except lo.
func readNetDev(path string) (rx, tx uint64, ok bool) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return 0, 0, false
	}
	sc := bufio.NewScanner(bytes.NewReader(raw))
	for sc.Scan() {
		name, rest, found := strings.Cut(sc.Text(), ":")
		if !found {
			continue
		}
		if strings.TrimSpace(name) == "lo" {
			continue
		}
		f := strings.Fields(rest)
		if len(f) < 9 {
			continue
		}
		r, err1 := strconv.ParseUint(f[0], 10, 64)
		t, err2 := strconv.ParseUint(f[8], 10, 64)
		if err1 != nil || err2 != nil {
			continue
		}
		rx, tx, ok = rx+r, tx+t, true
	}
	return rx, tx, ok
}
