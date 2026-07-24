BINARY := quickspin
PKG := ./cmd/quickspin
BIN_DIR := bin

# The Lima instance name and the guest architecture are the single source of
# truth for the VM, the Docker context, and hack/validate-01.sh. `?=` lets the
# environment override them, and `export` passes that choice down to the
# recipes' sub-processes.
VM_NAME ?= quickspin
LINUX_ARCH ?= arm64
DOCKER_CONTEXT := lima-$(VM_NAME)
export VM_NAME LINUX_ARCH

.PHONY: all
all: build

.PHONY: build
build: ## Build the binary into bin/
	go build -o $(BIN_DIR)/$(BINARY) $(PKG)

.PHONY: build-linux
build-linux: ## Build the linux binary into bin/
	CGO_ENABLED=0 GOOS=linux GOARCH=$(LINUX_ARCH) go build -o $(BIN_DIR)/linux-$(LINUX_ARCH)/$(BINARY) $(PKG)

.PHONY: run
run: ## Run the app (pass args with ARGS="...")
	go run $(PKG) $(ARGS)

.PHONY: test
test: ## Run all tests
	go test ./...

.PHONY: fmt
fmt: ## Format all Go code
	go fmt ./...

.PHONY: vet
vet: ## Report suspicious constructs
	go vet ./...

.PHONY: tidy
tidy: ## Sync go.mod/go.sum
	go mod tidy

NPM_DOCS := npm --prefix docs
DOCS_DEPS := docs/node_modules/.package-lock.json

$(DOCS_DEPS): docs/package-lock.json
	$(NPM_DOCS) ci

.PHONY: docs-install
docs-install: ## Install the documentation reader dependencies from the lockfile
	$(NPM_DOCS) ci

.PHONY: docs
docs: $(DOCS_DEPS) ## Start the local MDX documentation reader
	$(NPM_DOCS) run dev

.PHONY: docs-build
docs-build: $(DOCS_DEPS) ## Type-check and build the documentation reader
	$(NPM_DOCS) run build

.PHONY: clean
clean: ## Remove build artifacts
	rm -rf $(BIN_DIR)

.PHONY: lima-vm-create
lima-vm-create:
	limactl start lima/quickspin.yaml --name=$(VM_NAME)

.PHONY: lima-vm-delete
lima-vm-delete:
	limactl stop $(VM_NAME)
	limactl delete $(VM_NAME)

.PHONY: lima-vm-shell
lima-vm-shell:
	limactl shell $(VM_NAME)

.PHONY: host-docker-context-create
host-docker-context-create:
	docker context create $(DOCKER_CONTEXT) --docker "host=$$(limactl list $(VM_NAME) --format 'unix://{{.Dir}}/sock/docker.sock')"

.PHONY: host-docker-context-use
host-docker-context-use:
	docker context use $(DOCKER_CONTEXT)

.PHONY: host-docker-context-delete
host-docker-context-delete:
	docker context rm $(DOCKER_CONTEXT)

.PHONY: env-create
env-create: lima-vm-create host-docker-context-create host-docker-context-use

.PHONY: env-cleanup
env-cleanup: lima-vm-delete host-docker-context-delete

.PHONY: env-validate
env-validate: 
	./hack/validate-01.sh