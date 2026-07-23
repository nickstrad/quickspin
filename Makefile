BINARY := quickspin
PKG := ./cmd/quickspin
BIN_DIR := bin

.PHONY: all build run test fmt vet tidy docs docs-build docs-install clean

all: build

build: ## Build the binary into bin/
	go build -o $(BIN_DIR)/$(BINARY) $(PKG)

run: ## Run the app (pass args with ARGS="...")
	go run $(PKG) $(ARGS)

test: ## Run all tests
	go test ./...

fmt: ## Format all Go code
	go fmt ./...

vet: ## Report suspicious constructs
	go vet ./...

tidy: ## Sync go.mod/go.sum
	go mod tidy

NPM_DOCS := npm --prefix docs
DOCS_DEPS := docs/node_modules/.package-lock.json

$(DOCS_DEPS): docs/package-lock.json
	$(NPM_DOCS) ci

docs-install: ## Install the documentation reader dependencies from the lockfile
	$(NPM_DOCS) ci

docs: $(DOCS_DEPS) ## Start the local MDX documentation reader
	$(NPM_DOCS) run dev

docs-build: $(DOCS_DEPS) ## Type-check and build the documentation reader
	$(NPM_DOCS) run build

clean: ## Remove build artifacts
	rm -rf $(BIN_DIR)
