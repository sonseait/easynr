.DEFAULT_GOAL := help
.PHONY: help build run test test-race test-e2e test-docker test-ui vet fmt fmt-check verify clean
help: ## List commands
	@awk 'BEGIN {FS = ":.*## "} /^[a-zA-Z_-]+:.*## / {printf "  %-16s %s\n", $$1, $$2}' $(MAKEFILE_LIST)
build: ## Build standalone binary
	CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o bin/easynr ./cmd/easynr
run: build ## Run locally with ./data on port 4873 by default
	./bin/easynr
test: ## Unit and integration tests
	go test ./... -count=1
test-race: ## Tests with race detector
	go test -race ./... -count=1
test-e2e: ## Test publish/install/ci with real npm CLI
	EASYNR_E2E=1 go test ./tests/e2e -v -count=1
test-docker: ## Smoke-test a prebuilt image (see docs/build.md)
	EASYNR_DOCKER_E2E=1 go test ./tests/e2e -run TestDockerLifecycle -v -count=1
test-ui: ## Browser tests (npm ci first)
	npm run test:ui
vet: ## Static analysis
	go vet ./...
fmt: ## Format Go source
	gofmt -w cmd internal tests
fmt-check: ## Check Go formatting
	@test -z "$$(gofmt -l cmd internal tests)" || { gofmt -l cmd internal tests; exit 1; }
verify: fmt-check vet test-race build ## Formatting, analysis, tests and build
	go mod verify
clean: ## Remove default binary, preserve registry data
	rm -f bin/easynr
