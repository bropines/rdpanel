package app

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"strings"

	"github.com/bropines/rdpanel/internal/buildinfo"
	"github.com/bropines/rdpanel/internal/config"
)

// Stdio bundles the process streams so the CLI is testable.
type Stdio struct {
	In  io.Reader
	Out io.Writer
	Err io.Writer
}

const usage = `rdpanel — RustDesk API server and admin panel

Usage:
  rdpanel serve                                   run the HTTP server
  rdpanel user create [--admin] [--password-stdin] [--email E] [--display-name N] <username>
  rdpanel user reset-password [--password-stdin] <username>
  rdpanel migrate-legacy --from <rustdeskapi.db> [--dry-run] [--report <file>] [--force]
                         [--allow-version N] [--legacy-tz Z]
                                                  import a lejianwen/rustdesk-api v2.7 database
  rdpanel config print                            show effective config (secrets redacted)
  rdpanel version                                 show build information

Passwords are read from the terminal (with confirmation) or, with
--password-stdin or when stdin is not a terminal, from the first line of stdin.
They are never logged.

Configuration: RDPANEL_* environment variables and the optional YAML file
named by RDPANEL_CONFIG. See README.md.
`

// Main runs the command line and returns the process exit code
// (0 success, 1 failure, 2 usage error). args excludes the program name.
func Main(ctx context.Context, args []string, env config.LookupFunc, stdio Stdio) int {
	err := run(ctx, args, env, stdio)
	switch {
	case err == nil:
		return 0
	case errors.Is(err, flag.ErrHelp):
		fmt.Fprint(stdio.Out, usage)
		return 0
	case errors.Is(err, errUsage):
		fmt.Fprintf(stdio.Err, "rdpanel: %v\n\n%s", err, usage)
		return 2
	default:
		fmt.Fprintf(stdio.Err, "rdpanel: %v\n", err)
		return 1
	}
}

func run(ctx context.Context, args []string, env config.LookupFunc, stdio Stdio) error {
	if len(args) == 0 {
		return fmt.Errorf("%w: missing command", errUsage)
	}
	switch args[0] {
	case "-h", "--help", "help":
		return flag.ErrHelp
	case "version", "--version":
		fmt.Fprintln(stdio.Out, buildinfo.Get().String())
		return nil
	case "serve":
		if len(args) > 1 {
			return fmt.Errorf("%w: serve takes no arguments", errUsage)
		}
		return cmdServe(ctx, env, stdio)
	case "migrate-legacy":
		return cmdMigrateLegacy(ctx, args[1:], env, stdio)
	case "config":
		if len(args) != 2 || args[1] != "print" {
			return fmt.Errorf("%w: expected `config print`", errUsage)
		}
		return cmdConfigPrint(env, stdio)
	case "user":
		if len(args) < 2 {
			return fmt.Errorf("%w: expected `user create` or `user reset-password`", errUsage)
		}
		switch args[1] {
		case "create":
			return cmdUserCreate(ctx, args[2:], env, stdio)
		case "reset-password":
			return cmdUserResetPassword(ctx, args[2:], env, stdio)
		default:
			return fmt.Errorf("%w: unknown user command %q", errUsage, args[1])
		}
	default:
		return fmt.Errorf("%w: unknown command %q", errUsage, args[0])
	}
}

func cmdServe(ctx context.Context, env config.LookupFunc, stdio Stdio) error {
	cfg, err := config.Load(env)
	if err != nil {
		return err
	}
	log := NewLogger(cfg.Log, stdio.Err)
	info := buildinfo.Get()
	log.Info("starting rdpanel", slog.String("version", info.Version), slog.String("commit", info.Commit))
	a, err := New(ctx, cfg, log)
	if err != nil {
		return err
	}
	defer a.Close()
	return a.Serve(ctx)
}

func cmdConfigPrint(env config.LookupFunc, stdio Stdio) error {
	cfg, err := config.Load(env)
	if err != nil {
		return err
	}
	out, err := cfg.Redacted()
	if err != nil {
		return err
	}
	_, err = stdio.Out.Write(out)
	return err
}

// parseInterspersed parses flags that may appear before or after positional
// arguments (e.g. `user create alice --admin`).
func parseInterspersed(fs *flag.FlagSet, args []string) ([]string, error) {
	var positional []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		rest := fs.Args()
		if len(rest) == 0 {
			return positional, nil
		}
		if rest[0] == "--" {
			return append(positional, rest[1:]...), nil
		}
		positional = append(positional, rest[0])
		args = rest[1:]
	}
}

func newFlagSet(name string, stdio Stdio) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(stdio.Err)
	return fs
}

func singleUsername(pos []string) (string, error) {
	if len(pos) != 1 || strings.TrimSpace(pos[0]) == "" {
		return "", fmt.Errorf("%w: expected exactly one <username>", errUsage)
	}
	return strings.TrimSpace(pos[0]), nil
}

func usageErr(err error) error {
	if errors.Is(err, flag.ErrHelp) {
		return err
	}
	return fmt.Errorf("%w: %v", errUsage, err)
}
