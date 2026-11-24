package service

import (
	"context"
	"testing"

	"github.com/FlyTOmeLight/server-go/conf"
	"github.com/gin-gonic/gin"
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

func TestSetupHTTPValidation(t *testing.T) {
	for _, tc := range []struct {
		name    string
		cfg     *conf.Config
		wantErr bool
	}{
		{
			name: "release default auth",
			cfg:  &conf.Config{RunMode: conf.RunModeRelease},
		},
		{
			name: "test default fake auth",
			cfg: &conf.Config{
				RunMode:    conf.RunModeTest,
				HTTPServer: conf.HTTPServerConfig{AuthType: conf.AuthTypeFake},
			},
		},
		{
			name: "release fake auth rejected",
			cfg: &conf.Config{
				RunMode:    conf.RunModeRelease,
				HTTPServer: conf.HTTPServerConfig{AuthType: conf.AuthTypeFake},
			},
			wantErr: true,
		},
		{
			name: "unsupported auth center rejected",
			cfg: &conf.Config{
				RunMode:    conf.RunModeRelease,
				HTTPServer: conf.HTTPServerConfig{AuthType: conf.AuthTypeAuthCenter},
			},
			wantErr: true,
		},
		{
			name: "authenticated static asset rejected",
			cfg: &conf.Config{
				RunMode: conf.RunModeRelease,
				HTTPServer: conf.HTTPServerConfig{
					Assets: []conf.HTTPServerAssetConfig{
						{URLPath: "/private", FilePath: t.TempDir(), AuthEnable: true},
					},
				},
			},
			wantErr: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := SetupHTTP(gin.New(), tc.cfg)
			if (err != nil) != tc.wantErr {
				t.Fatalf("SetupHTTP() = %v, wantErr=%v", err, tc.wantErr)
			}
		})
	}
}
