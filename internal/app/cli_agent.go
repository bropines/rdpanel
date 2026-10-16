package app

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/bropines/rdpanel/internal/agent"
	"github.com/bropines/rdpanel/internal/buildinfo"
	"github.com/bropines/rdpanel/internal/config"
)

// cmdAgent runs `rdpanel agent [run|health]` (relay host mode, see
// docs/relay-agents.md). Configuration comes from RDPANEL_AGENT_* plus
// RDPANEL_LOG_LEVEL / RDPANEL_LOG_FORMAT.
func cmdAgent(ctx context.Context, args []string, env config.LookupFunc, stdio Stdio) error {
	sub := "run"
	if len(args) > 0 {
		sub = args[0]
	}
	if len(args) > 1 {
		return fmt.Errorf("%w: agent takes at most one argument (run or health)", errUsage)
	}
	cfg, err := agent.FromEnv(env)
	if err != nil {
		return err
	}
	switch sub {
	case "health":
		if err := agent.CheckHealth(cfg.DataDir, time.Now(), time.Minute); err != nil {
			return fmt.Errorf("unhealthy: %w", err)
		}
		fmt.Fprintln(stdio.Out, "healthy")
		return nil
	case "run":
	default:
		return fmt.Errorf("%w: unknown agent command %q", errUsage, sub)
	}
	logCfg := config.Defaults().Log
	if v, ok := env("RDPANEL_LOG_LEVEL"); ok && v != "" {
		logCfg.Level = v
	}
	if v, ok := env("RDPANEL_LOG_FORMAT"); ok && v != "" {
		logCfg.Format = v
	}
	log := NewLogger(logCfg, stdio.Err)
	cfg.Log = log
	info := buildinfo.Get()
	log.Info("starting rdpanel agent", slog.String("version", info.Version), slog.String("commit", info.Commit),
		slog.String("panel", cfg.PanelURL), slog.Int("relay_port", cfg.RelayPort))
	a, err := agent.New(cfg)
	if err != nil {
		return err
	}
	return a.Run(ctx)
}
