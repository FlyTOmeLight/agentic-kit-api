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
