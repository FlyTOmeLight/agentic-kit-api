package main

import (
	"context"
	"flag"
	"log"
	"os"
	"time"

	"github.com/FlyTOmeLight/agentic-kit-api/internal/config"
	"github.com/FlyTOmeLight/agentic-kit-api/pkg/durable"
)

func main() {
	path := flag.String("config", os.Getenv("CONFIG_FILE"), "TOML configuration file")
	flag.Parse()
	cfg, err := config.Load(*path)
	if err != nil {
		log.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	s, err := durable.Open(ctx, cfg.Database.URL)
	if err != nil {
		log.Fatal(err)
	}
	defer s.Close()
	if err = s.Migrate(ctx); err != nil {
		log.Fatal(err)
	}
}
