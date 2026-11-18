package service

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/FlyTOmeLight/server-go/conf"
	"github.com/FlyTOmeLight/server-go/httpintegration"
	"github.com/FlyTOmeLight/server-go/swagger"
	"github.com/gin-gonic/gin"

	"github.com/FlyTOmeLight/agentic-kit-api/internal/config"
	"github.com/FlyTOmeLight/agentic-kit-api/internal/sandboxcenter"
	"github.com/FlyTOmeLight/agentic-kit-api/pkg/durable"
	"github.com/FlyTOmeLight/agentic-kit-api/pkg/execution"
)

type openAPIParameter struct {
	Name     string `json:"name"`
	In       string `json:"in"`
	Required bool   `json:"required"`
}

type openAPIOperation struct {
	Parameters []openAPIParameter         `json:"parameters"`
	Responses  map[string]json.RawMessage `json:"responses"`
}

type openAPISchema struct {
	Type       string                   `json:"type"`
	Required   []string                 `json:"required"`
	Properties map[string]openAPISchema `json:"properties"`
}

type openAPISpec struct {
	Paths       map[string]map[string]openAPIOperation `json:"paths"`
	Definitions map[string]openAPISchema               `json:"definitions"`
}

func TestOpenAPIMatchesRoutes(t *testing.T) {
	raw, err := os.ReadFile("../doc/openapi/Agentic_swagger.json")
	if err != nil {
		t.Fatal(err)
	}
	var spec openAPISpec
	if err := json.Unmarshal(raw, &spec); err != nil {
		t.Fatal(err)
	}
	router := gin.New()
	// Route enumeration needs no live cluster; the provider only validates its
	// configuration at construction.
	provider, err := sandboxcenter.NewProvider(sandboxcenter.K8sConfig{
		Context: "contract-test", Namespace: "sandbox-center", GuardImage: "unused",
	})
	if err != nil {
		t.Fatal(err)
	}
	artifactServer, _ := newFakeArtifact(t)
	newRoutes(
		&durable.Store{},
		nil,
		newArtifactClient(t, artifactServer),
		nil,
		sandboxcenter.NewStore(nil),
		provider,
		config.CallbackConfig{Enabled: true},
		Authentication{
			User:        []gin.HandlerFunc{func(c *gin.Context) { c.Next() }},
			Service:     []gin.HandlerFunc{func(c *gin.Context) { c.Next() }},
			Propagation: []gin.HandlerFunc{func(c *gin.Context) { c.Next() }},
			Callback:    []gin.HandlerFunc{func(c *gin.Context) { c.Next() }},
		},
		execution.PricingCatalog{},
	).register(router.Group(""))
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
	for _, route := range []struct {
		method string
		path   string
	}{
		{method: "post", path: "/v1/memories"},
		{method: "put", path: "/v1/memories/{id}"},
		{method: "post", path: "/v1/memories/{id}/retire"},
	} {
		if !hasRequiredHeaderParameter(spec.Paths[route.path][route.method].Parameters, "Idempotency-Key") {
			t.Errorf("missing required Idempotency-Key header: %s %s", route.method, route.path)
		}
	}
	retire := spec.Paths["/v1/memories/{id}/retire"]["post"]
	if _, ok := retire.Responses["503"]; !ok {
		t.Errorf("missing 503 response: post /v1/memories/{id}/retire")
	}
	if got := spec.Definitions["agenticv1.UpdateMemoryRequest"].Properties["restore"].Type; got != "boolean" {
		t.Errorf("UpdateMemoryRequest.restore type = %q, want boolean", got)
	}
	for _, tc := range []struct {
		definition string
		required   []string
	}{
		{definition: "agenticv1.CreateMemoryRequest", required: []string{"document"}},
		{
			definition: "agenticv1.UpdateMemoryRequest",
			required:   []string{"document", "expectedRevision"},
		},
		{definition: "agenticv1.RetireMemoryRequest", required: []string{"expectedRevision"}},
	} {
		schema := spec.Definitions[tc.definition]
		if _, ok := schema.Properties["idempotencyKey"]; ok {
			t.Errorf("%s must not accept idempotencyKey in JSON body", tc.definition)
		}
		for _, field := range tc.required {
			if !slices.Contains(schema.Required, field) {
				t.Errorf("%s must require JSON field %q", tc.definition, field)
			}
		}
	}
}

func hasRequiredHeaderParameter(parameters []openAPIParameter, name string) bool {
	for _, parameter := range parameters {
		if parameter.In == "header" && parameter.Name == name {
			return parameter.Required
		}
	}
	return false
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
