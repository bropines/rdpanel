package app

import (
	"context"
	"fmt"
	"os"

	"github.com/bropines/rustdesk-panel/internal/config"
	"github.com/bropines/rustdesk-panel/internal/crypto"
	"github.com/bropines/rustdesk-panel/internal/legacy"
)

// cmdMigrateLegacy implements `rdpanel migrate-legacy`.
func cmdMigrateLegacy(ctx context.Context, args []string, env config.LookupFunc, stdio Stdio) error {
	fs := newFlagSet("migrate-legacy", stdio)
	from := fs.String("from", "", "legacy rustdesk-api database (rustdeskapi.db), opened read-only")
	dryRun := fs.Bool("dry-run", false, "import inside a transaction and roll it back")
	reportPath := fs.String("report", "", "write the JSON report to this file")
	force := fs.Bool("force", false, "import even if the target database already has users or a completed import")
	allowVersion := fs.Int("allow-version", -1, "accept this legacy schema version instead of 265")
	legacyTZ := fs.String("legacy-tz", "UTC", "time zone for legacy timestamps without a UTC offset (IANA name or +hh:mm)")
	pos, err := parseInterspersed(fs, args)
	if err != nil {
		return usageErr(err)
	}
	if len(pos) != 0 {
		return fmt.Errorf("%w: migrate-legacy takes no positional arguments", errUsage)
	}
	if *from == "" {
		return fmt.Errorf("%w: --from is required", errUsage)
	}
	loc, err := legacy.ParseLocation(*legacyTZ)
	if err != nil {
		return fmt.Errorf("%w: --legacy-tz: %v", errUsage, err)
	}

	cfg, err := config.Load(env)
	if err != nil {
		return err
	}
	log := NewLogger(cfg.Log, stdio.Err)
	d, err := openDB(ctx, cfg, log)
	if err != nil {
		return err
	}
	defer d.Close()
	// Same key as `serve`: imported secrets must be readable by the server.
	key, _, err := crypto.LoadAEADKey(cfg.SecretKey.Reveal(), cfg.SecretKeyFile)
	if err != nil {
		return err
	}
	aead, err := crypto.NewAEAD(key)
	if err != nil {
		return err
	}

	opts := legacy.Options{SourcePath: *from, DryRun: *dryRun, Force: *force, LegacyLocation: loc, AEAD: aead, Log: log}
	if *allowVersion >= 0 {
		v := *allowVersion
		opts.AllowVersion = &v
	}
	rep, runErr := legacy.Run(ctx, d, opts)
	if rep != nil {
		fmt.Fprint(stdio.Out, rep.Summary())
		if *reportPath != "" {
			if err := os.WriteFile(*reportPath, rep.JSON(), 0o600); err != nil {
				return fmt.Errorf("write report: %w", err)
			}
			fmt.Fprintf(stdio.Out, "report written to %s\n", *reportPath)
		}
	}
	if runErr != nil {
		return runErr
	}
	if *dryRun {
		fmt.Fprintln(stdio.Out, "dry run: nothing was written to the target database")
	}
	return nil
}
