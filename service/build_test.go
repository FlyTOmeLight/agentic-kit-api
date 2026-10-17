package service

import (
	"context"
	"testing"

	"github.com/FlyTOmeLight/server-go/conf"
)

func TestReloadPreservesPublicPaths(t *testing.T) {
	settings, err := conf.FromSettings(map[string]any{"AppName": "agentic", "AppCode": 100, "RunMode": "release"})
	if err != nil {
		t.Fatal(err)
	}
	manager, err := conf.NewManagerFromConfig(settings)
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close(context.Background())
	manager.Subscribe(prepareHTTPConfig)
	next := manager.Snapshot()
	next.HTTPServer.APIBasePath = "/prefix"
	if err := manager.Update(next); err == nil {
		t.Fatal("route prefix change accepted")
	}
	if manager.Snapshot().HTTPServer.APIBasePath != "" {
		t.Fatal("failed update changed public paths")
	}
}
