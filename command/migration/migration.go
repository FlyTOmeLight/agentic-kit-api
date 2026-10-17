package migration

import (
	"context"
	"os"
	"time"

	"github.com/FlyTOmeLight/server-go/db"
	"github.com/spf13/cobra"

	"github.com/FlyTOmeLight/agentic-kit-api/internal/config"
	"github.com/FlyTOmeLight/agentic-kit-api/pkg/durable"
)

type CommandBuilder struct{}

func (*CommandBuilder) NewCommand() *cobra.Command {
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
			database := &db.Module{Config: &cfg.DB}
			if err := database.Initialize(ctx); err != nil {
				return err
			}
			defer database.Close(context.Background())
			store, err := durable.New(database.DB)
			if err != nil {
				return err
			}
			return store.Migrate(ctx)
		},
	}
	command.Flags().StringVar(&path, "config", os.Getenv("CONFIG_FILE"), "TOML configuration file")
	return command
}
