.PHONY: help
help: ## Prints help for targets
	@cat $(MAKEFILE_LIST) | \
		grep -E '^[a-zA-Z0-9_-]+:.*?## .*$$' | \
		awk 'BEGIN {FS = ":.*?## "}; {printf "\033[36m%-30s\033[0m %s\n", $$1, $$2}'

.PHONY: build
build: ## Build binary in working directory
	go build -o ./bin/nzb-proxy ./cmd/nzb-proxy

.PHONY: test
test: ## Run race tests and vet, and write coverage.out
	go test -race -coverprofile=coverage.out ./...
	go vet ./...

.PHONY: e2e
e2e: ## Test real Prowlarr and Radarr with the proxy in kind
	go test -tags=e2e -count=1 -timeout=18m -v ./tests/e2e
