package service

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/FlyTOmeLight/server-go/conf"
	"github.com/FlyTOmeLight/server-go/httpintegration"
	"github.com/FlyTOmeLight/server-go/swagger"
	"github.com/gin-gonic/gin"

	"github.com/FlyTOmeLight/agentic-kit-api/internal/app/platform"
)

func TestOpenAPIMatchesRoutes(t *testing.T) {
	raw, err := os.ReadFile("../doc/openapi/Agentic_swagger.json")
	if err != nil {
		t.Fatal(err)
	}
	var spec struct {
		Paths map[string]map[string]struct {
			Parameters []struct {
				Name     string `json:"name"`
				In       string `json:"in"`
				Required bool   `json:"required"`
			} `json:"parameters"`
		} `json:"paths"`
	}
	if err := json.Unmarshal(raw, &spec); err != nil {
		t.Fatal(err)
	}
	router := gin.New()
	newRoutes(nil, nil, &platform.Config{}, nil).register(router.Group(""))
	colon := regexp.MustCompile(`:([A-Za-z0-9_]+)`)
	documented := map[string]bool{}
	for _, route := range router.Routes() {
		path := colon.ReplaceAllString(route.Path, "{$1}")
		key := route.Method + " " + path
		operation, ok := spec.Paths[path][strings.ToLower(route.Method)]
		if !ok {
			t.Errorf("undocumented route: %s", key)
			continue
		}
		documented[key] = true
		for _, parameter := range operation.Parameters {
			if parameter.In == "path" &&
				(!strings.Contains(path, "{"+parameter.Name+"}") || !parameter.Required) {
				t.Errorf("invalid path parameter %s for %s", parameter.Name, key)
			}
		}
	}
	for path, operations := range spec.Paths {
		for method := range operations {
			if !documented[strings.ToUpper(method)+" "+path] {
				t.Errorf("documented route is not registered: %s %s", method, path)
			}
		}
	}
}

func TestSwaggerServesGeneratedContract(t *testing.T) {
	settings, err := conf.FromSettings(
		map[string]any{
			"AppName":    "agentic",
			"AppCode":    100,
			"RunMode":    "release",
			"HTTPServer": map[string]any{"SwaggerEnable": true},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	engine := gin.New()
	swagger.DocPrefix = "Agentic"
	if err := httpintegration.Install(engine, settings); err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	engine.ServeHTTP(response, httptest.NewRequest("GET", "/swagger/doc.json", nil))
	if response.Code != 200 {
		t.Fatalf("Swagger status: %d: %s", response.Code, response.Body.String())
	}
	var spec map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &spec); err != nil {
		t.Fatal(err)
	}
	if spec["paths"] == nil {
		t.Fatal("generated routes missing from served contract")
	}
}
