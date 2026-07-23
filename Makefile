BINARY := quickspin
PKG := ./cmd/quickspin
BIN_DIR := bin

.PHONY: all build run test fmt vet tidy clean

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

clean: ## Remove build artifacts
	rm -rf $(BIN_DIR)
