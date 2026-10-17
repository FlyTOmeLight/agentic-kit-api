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

	"github.com/FlyTOmeLight/agentic-kit-api/command/evaluation"
	"github.com/FlyTOmeLight/agentic-kit-api/command/migration"
)

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
	command.Append(&migration.CommandBuilder{}, &evaluation.CommandBuilder{})
	if err := command.ExecuteContext(ctx); err != nil {
		fmt.Fprintln(os.Stderr, err)
		if errors.Is(err, evaluation.ErrFailed) {
			os.Exit(2)
		}
		os.Exit(1)
	}
}
