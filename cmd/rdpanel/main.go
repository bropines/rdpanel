// Command rdpanel is the RustDesk API server and admin panel.
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/bropines/rustdesk-panel/internal/app"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := app.Main(ctx, os.Args[1:], os.LookupEnv, app.Stdio{In: os.Stdin, Out: os.Stdout, Err: os.Stderr})
	stop()
	os.Exit(code)
}
