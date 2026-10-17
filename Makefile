.PHONY: test integration vet build

test:
	go test -count=1 ./...

integration:
	@test -n "$$TEST_DATABASE_URL" || (echo 'TEST_DATABASE_URL is required'; exit 1)
	@test -n "$$TEST_REDIS_URL" || (echo 'TEST_REDIS_URL is required'; exit 1)
	go test -race -count=1 -timeout=60s ./...
	go vet ./...

vet:
	go vet ./...

build:
	go build -o bin/server ./cmd/server
	go build -o bin/cli ./cmd/cli

SWAG := $(shell go env GOPATH)/bin/swag
FRAMEWORK_DIR := $(shell go list -m -f '{{.Dir}}' github.com/FlyTOmeLight/server-go)
SWAG_SCAN_DIRS := internal/app/agent,internal/app/session,internal/app/run,internal/app/evaluation,internal/app/platform,sdk/go/agenticv1,$(FRAMEWORK_DIR)/errors,$(shell go env GOROOT)/src/encoding/json

.PHONY: doc install-doc
install-doc:
	go install github.com/swaggo/swag/cmd/swag@v1.8.4

doc:
	$(SWAG) init -d $(SWAG_SCAN_DIRS) -g doc.go --instanceName=Agentic --parseInternal -o doc/openapi
	python3 scripts/normalize-openapi.py
