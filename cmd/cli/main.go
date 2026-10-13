package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/FlyTOmeLight/server-go/cli"
	"github.com/spf13/cobra"
)

var errEvaluationFailed = errors.New("evaluation thresholds were not met")

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	command := cli.NewCommandLine(
		&cobra.Command{
			Use:           "agenticctl",
			Short:         "Agentic Kit administration",
			SilenceUsage:  true,
			SilenceErrors: true,
		},
	)
	command.Root.AddCommand(newMigrationCommand(), newEvaluationCommand())
	if err := command.Root.ExecuteContext(ctx); err != nil {
		fmt.Fprintln(os.Stderr, err)
		if errors.Is(err, errEvaluationFailed) {
			os.Exit(2)
		}
		os.Exit(1)
	}
}
