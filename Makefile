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
LINUX_BIN := $(BIN_DIR)/linux-$(LINUX_ARCH)/$(BINARY)
# Deferred `=`: the limactl call runs only for the recipes that reference it.
LIMA_DOCKER_HOST = $(shell limactl list $(VM_NAME) --format 'unix://{{.Dir}}/sock/docker.sock')
export VM_NAME LINUX_ARCH

.PHONY: all
all: build

.PHONY: build
build: ## Build the binary into bin/
	go build -o $(BIN_DIR)/$(BINARY) $(PKG)

.PHONY: build-linux
build-linux: ## Build the linux binary into bin/
	CGO_ENABLED=0 GOOS=linux GOARCH=$(LINUX_ARCH) go build -o $(LINUX_BIN) $(PKG)

.PHONY: run
run: ## Run the app (pass args with ARGS="...")
	go run $(PKG) $(ARGS)

# Development servers run verbose: debug is where the per-request access log
# lives, so an unexplained response has a matching line without a restart.
# Override with `make serve LOG_LEVEL=info`.
LOG_LEVEL ?= debug

# The Docker SDK reads DOCKER_HOST and ignores the docker CLI's context, so the
# server needs the socket named even when $(DOCKER_CONTEXT) is already active.
# `?=` yields to an environment that already names a daemon.
.PHONY: serve
serve: export DOCKER_HOST ?= $(LIMA_DOCKER_HOST)
# A darwin binary against the VM's Linux daemon: the GOOS default would pick the
# daemon's runtime, not gVisor.
serve: export QUICKSPIN_DOCKER_RUNTIME ?= runsc
serve: ## Run the control plane on the host against the VM's Docker daemon (flags via ARGS="...")
	go run $(PKG) serve --log-level $(LOG_LEVEL) $(ARGS)

# --host 0.0.0.0 because Lima only forwards guest ports bound to all interfaces,
# and --db under the guest's home because the repo mount is read-only in the guest.
.PHONY: serve-lima
serve-lima: build-linux ## Run the control plane inside the Lima VM, reachable on the host at 127.0.0.1:8080
	limactl shell $(VM_NAME) -- sh -c 'exec "$(CURDIR)/$(LINUX_BIN)" serve --host 0.0.0.0 --log-level $(LOG_LEVEL) --db "$$HOME/quickspin-control-plane.db" $(ARGS)'

# The dedicated live-test VM. It is a different instance from VM_NAME on
# purpose: the live suite sweeps Quickspin containers, and the development VM
# must be structurally out of reach. hack/test-runtime-docker.sh refuses to run
# if these two ever name the same instance.
TEST_VM_NAME ?= quickspin-runtime-test
export TEST_VM_NAME

.PHONY: test
test: ## Run all tests (no Docker needed; the live suite reports itself skipped)
	go test ./...

.PHONY: test-docker
test-docker: ## Run the live Docker suite and CLI smoke against the dedicated test VM
	./hack/test-runtime-docker.sh

.PHONY: test-docker-clean
test-docker-clean: ## Remove managed containers a failed live run left in the test VM
	CLEAN_ONLY=1 ./hack/test-runtime-docker.sh

# Both go through the script rather than calling limactl here. The script owns the
# ownership marker that authorizes its container sweep, and it refuses to act when
# TEST_VM_NAME equals VM_NAME — a recipe that deleted the instance directly would
# be the one path with no such guard.
.PHONY: test-docker-setup
test-docker-setup: ## Create the dedicated test VM without running any test
	SETUP_ONLY=1 ./hack/test-runtime-docker.sh

.PHONY: test-docker-teardown
test-docker-teardown: ## Delete the dedicated test VM
	TEARDOWN_ONLY=1 ./hack/test-runtime-docker.sh

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

# Deletes any existing instance rather than reusing it, so a VM provisioned
# against an older lima/quickspin.yaml cannot survive a rerun.
#
# The VM runs rootful Docker, so DOCKER_HOST below is the daemon's default path
# already; injecting it keeps the guest's value explicit rather than relying on
# the SDK's built-in fallback.
.PHONY: lima-vm-create
lima-vm-create: lima-vm-delete
	limactl start lima/quickspin.yaml --name=$(VM_NAME) --tty=false \
		--set '.env.DOCKER_HOST = "unix:///var/run/docker.sock"'

# --force covers both states a plain delete rejects: an instance that is still
# running, and one that is not there at all.
.PHONY: lima-vm-delete
lima-vm-delete:
	limactl delete --force $(VM_NAME)

.PHONY: lima-vm-shell
lima-vm-shell:
	limactl shell $(VM_NAME)

# `docker context create` refuses an existing name, so the stale one goes first;
# it also has to, since a recreated VM gets a new socket path.
.PHONY: host-docker-context-create
host-docker-context-create: host-docker-context-delete
	docker context create $(DOCKER_CONTEXT) --docker "host=$(LIMA_DOCKER_HOST)"

.PHONY: host-docker-context-use
host-docker-context-use:
	docker context use $(DOCKER_CONTEXT)

.PHONY: host-docker-context-delete
host-docker-context-delete:
	docker context rm -f $(DOCKER_CONTEXT)

.PHONY: env-create
env-create: lima-vm-create host-docker-context-create host-docker-context-use

.PHONY: env-cleanup
env-cleanup: lima-vm-delete host-docker-context-delete

.PHONY: env-validate
env-validate: 
	./hack/validate-01.sh