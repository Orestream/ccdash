# ccdash developer Makefile. Run `make help` for the list.
.DEFAULT_GOAL := help
.PHONY: help setup dev-backend dev-frontend build build-frontend \
        test test-backend test-frontend lint lint-backend lint-frontend \
        fmt tidy clean

BACKEND  := backend
FRONTEND := frontend

help: ## Show this help
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) \
		| sort | awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-16s\033[0m %s\n", $$1, $$2}'

setup: ## Install all dependencies
	cd $(BACKEND) && go mod download
	cd $(FRONTEND) && npm install

dev-backend: ## Run the backend API server (:10001)
	cd $(BACKEND) && go run ./cmd/ccdash

dev-frontend: ## Run the Vite dev server (:10000)
	cd $(FRONTEND) && npm run dev

build: build-frontend ## Build backend binary and frontend bundle
	cd $(BACKEND) && go build -o ccdash ./cmd/ccdash

build-frontend: ## Build the production frontend bundle
	cd $(FRONTEND) && npm run build

test: test-backend test-frontend ## Run all tests

test-backend: ## Run Go tests
	cd $(BACKEND) && go test ./...

test-frontend: ## Run frontend tests
	cd $(FRONTEND) && npm test

lint: lint-backend lint-frontend ## Run all linters

lint-backend: ## Run go vet + golangci-lint
	cd $(BACKEND) && go vet ./... && golangci-lint run ./...

lint-frontend: ## Run ESLint
	cd $(FRONTEND) && npm run lint

fmt: ## Format Go code
	cd $(BACKEND) && gofmt -w ./cmd ./internal

tidy: ## Tidy Go modules
	cd $(BACKEND) && go mod tidy

clean: ## Remove build artifacts
	rm -f $(BACKEND)/ccdash $(BACKEND)/*.db
	rm -rf $(FRONTEND)/dist
