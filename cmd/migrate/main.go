package main

import (
	"context"
	"github.com/FlyTOmeLight/agentic-kit-api/pkg/durable"
	"log"
	"os"
	"time"
)

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	s, err := durable.Open(ctx, os.Getenv("DATABASE_URL"))
	if err != nil {
		log.Fatal(err)
	}
	defer s.Close()
	if err = s.Migrate(ctx); err != nil {
		log.Fatal(err)
	}
}
