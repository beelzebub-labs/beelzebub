DOCKER_COMPOSE := $(shell which docker-compose)

ifeq (${DOCKER_COMPOSE},)
DOCKER_COMPOSE = docker compose
endif

VERSION    := $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
COMMIT     := $(shell git rev-parse --short HEAD 2>/dev/null || echo "unknown")
BUILD_DATE := $(shell date -u +"%Y-%m-%dT%H:%M:%SZ")
LDFLAGS    := -X github.com/beelzebub-labs/beelzebub/v3/cli.Version=$(VERSION) \
              -X github.com/beelzebub-labs/beelzebub/v3/cli.CommitSHA=$(COMMIT) \
              -X github.com/beelzebub-labs/beelzebub/v3/cli.BuildDate=$(BUILD_DATE)

.PHONY: build
build:
	go build -ldflags "$(LDFLAGS)" -o beelzebub .


.PHONY: start
start:
	@command -v go  >/dev/null 2>&1 || { echo "Error: Go is not installed — get it from https://go.dev/dl, then re-run 'make start'."; exit 1; }
	@command -v git >/dev/null 2>&1 || { echo "Error: git is not installed — install git, then re-run 'make start'."; exit 1; }
	@go run . plugin install --no-build
	@$(MAKE) -s build
	@./beelzebub run

.PHONY: docker
docker:
	@command -v docker >/dev/null 2>&1 || { echo "Error: Docker is not installed — get it from https://docs.docker.com/get-docker, then re-run 'make docker'."; exit 1; }
	@${DOCKER_COMPOSE} up -d --build

.PHONY: beelzebub.start
beelzebub.start:
	${DOCKER_COMPOSE} build;
	${DOCKER_COMPOSE} up -d;

.PHONY: beelzebub.stop
beelzebub.stop:
	${DOCKER_COMPOSE} down;

.PHONY: test.unit
test.unit:
	go test ./...

.PHONY: test.unit.verbose
test.unit.verbose:
	go test ./... -v

.PHONY: test.dependencies.start
test.dependencies.start:
	${DOCKER_COMPOSE} -f ./integration_test/docker-compose.yml up -d

.PHONY:	test.dependencies.down
test.dependencies.down:
	${DOCKER_COMPOSE} -f ./integration_test/docker-compose.yml down

# validate-specs validates all honeypot service configurations against the
# per-protocol JSON Schemas in specs/. Exit code 1 on any failure.
.PHONY: validate-specs
validate-specs:
	go run ./cmd/validate-specs

# validate-all runs both schema validation and the full Go validator.
.PHONY: validate-all
validate-all: validate-specs
	go run . validate

.PHONY: test.integration
test.integration:
	INTEGRATION=1 go test ./...

.PHONY: test.integration.verbose
test.integration.verbose:
	INTEGRATION=1 go test ./... -v

.PHONY: docs.install
docs.install:
	cd docs && pnpm install --frozen-lockfile

.PHONY: docs.dev
docs.dev:
	cd docs && pnpm dev

.PHONY: docs.check
docs.check:
	cd docs && ASTRO_TELEMETRY_DISABLED=1 pnpm check && pnpm verify

.PHONY: docs.build
docs.build:
	cd docs && ASTRO_TELEMETRY_DISABLED=1 pnpm test
