package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/FlyTOmeLight/server-go/app"
	"github.com/FlyTOmeLight/server-go/httpintegration"

	"github.com/FlyTOmeLight/agentic-kit-api/service"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	path := flag.String("config", os.Getenv("CONFIG_FILE"), "TOML configuration file")
	flag.Parse()
	return app.Run(ctx, app.Options{ConfigPath: *path, Setup: httpintegration.Install}, service.Build)
}
