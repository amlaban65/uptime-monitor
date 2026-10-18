BIN := bin/uptime-monitor
TEST_DATABASE_URL ?= postgres://postgres:postgres@127.0.0.1:5432/postgres?sslmode=disable

.PHONY: build test test-integration lint fmt run compose-up compose-down tf-check

build:
	CGO_ENABLED=0 go build -trimpath -o $(BIN) ./cmd/uptime-monitor

# Unit tests only: integration tests skip when TEST_DATABASE_URL is unset.
test:
	go test -race ./...

test-integration:
	TEST_DATABASE_URL='$(TEST_DATABASE_URL)' go test -race -count=1 ./...

lint:
	go vet ./...
	golangci-lint run ./...

fmt:
	gofmt -w .

run: build
	./$(BIN)

compose-up:
	docker compose up --build -d

compose-down:
	docker compose down -v

tf-check:
	cd deploy/terraform && terraform fmt -check -recursive && terraform init -backend=false && terraform validate
