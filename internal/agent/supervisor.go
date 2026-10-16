package agent

import (
	"bufio"
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/bropines/rdpanel/internal/relays/agentproto"
)

// Process is a running hbbr.
type Process interface {
	PID() int
	// Wait blocks until the process exits.
	Wait() error
	// Stop asks the process to exit (SIGTERM) and kills it after grace.
	Stop(grace time.Duration)
}

// Runner starts hbbr processes (injectable for tests).
type Runner interface {
	Start(ctx context.Context, args []string) (Process, error)
}

// ExecRunner runs a real hbbr binary in Dir (where it reads
// blacklist.txt/blocklist.txt). Output lines are logged with an "hbbr" source.
type ExecRunner struct {
	Bin string
	Dir string
	Log *slog.Logger
}

type execProc struct {
	cmd  *exec.Cmd
	done chan struct{}
	err  error
}

func (p *execProc) PID() int    { return p.cmd.Process.Pid }
func (p *execProc) Wait() error { <-p.done; return p.err }
func (p *execProc) Stop(grace time.Duration) {
	_ = p.cmd.Process.Signal(syscall.SIGTERM)
	select {
	case <-p.done:
	case <-time.After(grace):
		_ = p.cmd.Process.Kill()
		<-p.done
	}
}

// childEnv is the agent's environment without variables that change hbbr's
// behaviour behind the agent's back (PORT makes hbbr listen on PORT+1; KEY
// would replace -k) or that carry agent secrets.
func childEnv() []string {
	var out []string
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		switch {
		case k == "PORT", k == "KEY", k == "KEY_PRIV", k == "KEY_PUB", strings.HasPrefix(k, "RDPANEL_AGENT_"):
			continue
		}
		out = append(out, kv)
	}
	return out
}

// Start implements Runner.
func (r ExecRunner) Start(_ context.Context, args []string) (Process, error) {
	cmd := exec.Command(r.Bin, args...)
	cmd.Dir = r.Dir
	cmd.Env = childEnv()
	pr, pw := io.Pipe()
	cmd.Stdout, cmd.Stderr = pw, pw
	if err := cmd.Start(); err != nil {
		pw.Close()
		return nil, err
	}
	p := &execProc{cmd: cmd, done: make(chan struct{})}
	log := r.Log
	go func() {
		sc := bufio.NewScanner(pr)
		sc.Buffer(make([]byte, 0, 4096), 64<<10)
		for sc.Scan() {
			if log != nil {
				log.Info(sc.Text(), slog.String("source", "hbbr"))
			}
		}
		_, _ = io.Copy(io.Discard, pr)
	}()
	go func() {
		p.err = cmd.Wait()
		pw.Close()
		close(p.done)
	}()
	return p, nil
}

// spec is the desired hbbr invocation.
type spec struct {
	key  string
	port int
}

func (s spec) args() []string {
	return []string{"-p", strconv.Itoa(s.port), "-k", s.key}
}

// Supervisor runs hbbr as a child process and restarts it with backoff. It is
// deliberately small and independent of the WebSocket session, so a session
// problem never touches running relays (design §5.1, option 1).
type Supervisor struct {
	runner Runner
	log    *slog.Logger
	port   int

	mu      sync.Mutex
	want    *spec
	cur     *spec
	proc    Process
	status  agentproto.HBBRStatus
	changed chan struct{}
	restart chan struct{}
	started chan struct{}

	// MinBackoff/MaxBackoff bound restart delays; StableAfter resets them.
	MinBackoff, MaxBackoff, StableAfter time.Duration
}

// NewSupervisor builds a supervisor for hbbr on port.
func NewSupervisor(r Runner, port int, log *slog.Logger) *Supervisor {
	return &Supervisor{runner: r, log: log, port: port, changed: make(chan struct{}, 1),
		restart: make(chan struct{}, 1), started: make(chan struct{}, 1),
		MinBackoff: time.Second, MaxBackoff: 30 * time.Second, StableAfter: time.Minute}
}

// ErrNoKey refuses to start hbbr without an explicit hbbs public key: `-k _`
// or no key would make hbbr generate a foreign key pair on a fresh host.
var ErrNoKey = errors.New("agent: no hbbs public key yet; hbbr is not started")

// Set changes the desired state: run hbbr with key, or stop it.
func (s *Supervisor) Set(key string, run bool) {
	s.mu.Lock()
	var w *spec
	if run && key != "" {
		w = &spec{key: key, port: s.port}
	}
	s.want = w
	s.mu.Unlock()
	notify(s.changed)
}

// Restart restarts a running hbbr (the "restart-hbbr" command).
func (s *Supervisor) Restart() { notify(s.restart) }

// Started fires after every hbbr start (the agent then re-applies its config).
func (s *Supervisor) Started() <-chan struct{} { return s.started }

// Status returns the hbbr status.
func (s *Supervisor) Status() agentproto.HBBRStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.status
}

func notify(ch chan struct{}) {
	select {
	case ch <- struct{}{}:
	default:
	}
}

// Run supervises until ctx ends; hbbr is stopped on return.
func (s *Supervisor) Run(ctx context.Context) {
	backoff := s.MinBackoff
	for {
		s.mu.Lock()
		want := s.want
		s.mu.Unlock()
		if want == nil {
			s.setStatus(func(st *agentproto.HBBRStatus) { st.Up, st.PID, st.KeyFP = false, 0, "" })
			select {
			case <-ctx.Done():
				return
			case <-s.changed:
				continue
			}
		}
		proc, err := s.runner.Start(ctx, want.args())
		if err != nil {
			s.log.Error("agent: start hbbr", slog.Any("err", err))
			s.setStatus(func(st *agentproto.HBBRStatus) { st.Up, st.LastError = false, err.Error() })
			if !s.sleep(ctx, &backoff) {
				return
			}
			continue
		}
		startedAt := time.Now()
		s.mu.Lock()
		s.proc, s.cur = proc, want
		s.status.Up, s.status.PID, s.status.Since = true, proc.PID(), startedAt.UTC()
		s.status.KeyFP, s.status.LastError = agentproto.KeyFingerprint(want.key), ""
		s.mu.Unlock()
		s.log.Info("agent: hbbr started", slog.Int("pid", proc.PID()), slog.Int("port", want.port),
			slog.String("key_fp", agentproto.KeyFingerprint(want.key)))
		notify(s.started)

		exited := make(chan error, 1)
		go func() { exited <- proc.Wait() }()
		if !s.watch(ctx, proc, want, exited, startedAt, &backoff) {
			return
		}
	}
}

// watch waits on a running hbbr until it has to be restarted (returns true)
// or ctx ends (stops it, returns false).
func (s *Supervisor) watch(ctx context.Context, proc Process, want *spec, exited chan error,
	startedAt time.Time, backoff *time.Duration) bool {
	stop := func() {
		proc.Stop(5 * time.Second)
		<-exited
	}
	for {
		select {
		case <-ctx.Done():
			stop()
			s.setStatus(func(st *agentproto.HBBRStatus) { st.Up, st.PID = false, 0 })
			return false
		case <-s.restart:
			s.log.Info("agent: restarting hbbr on request")
			stop()
			s.countRestart("restart requested")
			return true
		case <-s.changed:
			s.mu.Lock()
			same := s.want != nil && *s.want == *want
			s.mu.Unlock()
			if same {
				continue
			}
			s.log.Info("agent: hbbr configuration changed, restarting")
			stop()
			s.countRestart("configuration changed")
			return true
		case err := <-exited:
			msg := "exited"
			if err != nil {
				msg = err.Error()
			}
			s.log.Warn("agent: hbbr exited", slog.String("status", msg))
			s.countRestart("hbbr " + msg)
			if time.Since(startedAt) >= s.StableAfter {
				*backoff = s.MinBackoff
			}
			return s.sleep(ctx, backoff)
		}
	}
}

func (s *Supervisor) countRestart(reason string) {
	s.setStatus(func(st *agentproto.HBBRStatus) {
		st.Up, st.PID = false, 0
		st.Restarts++
		st.LastError = reason
	})
}

func (s *Supervisor) setStatus(fn func(*agentproto.HBBRStatus)) {
	s.mu.Lock()
	fn(&s.status)
	s.mu.Unlock()
}

func (s *Supervisor) sleep(ctx context.Context, backoff *time.Duration) bool {
	d := *backoff
	*backoff = min(*backoff*2, s.MaxBackoff)
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	case <-s.changed:
		notify(s.changed) // re-read the desired state right away
		return true
	}
}
