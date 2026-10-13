package main

import (
	"context"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/FlyTOmeLight/agentic-kit-api/internal/config"
	"github.com/FlyTOmeLight/agentic-kit-api/pkg/durable"
)

func newMigrationCommand() *cobra.Command {
	var path string
	command := &cobra.Command{
		Use:   "migrate",
		Short: "Apply durable database migrations",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load(path)
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
			defer cancel()
			store, err := durable.Open(ctx, cfg.Database.URL)
			if err != nil {
				return err
			}
			defer store.Close()
			return store.Migrate(ctx)
		},
	}
	command.Flags().StringVar(&path, "config", os.Getenv("CONFIG_FILE"), "TOML configuration file")
	return command
}
