# ============================================================================
# THIS IS FOR LOCAL DEVELOPMENT ONLY
# ============================================================================
# Requires .env file - podman-compose will automatically load it

COMPOSE_FILE=compose.dev.yaml
DB_CONTAINER=app_db
APP_CONTAINER=app_api

# --- Release artefact metadata --------------------------------------------
#
# Production releases are cut by pushing a `v*` tag — .github/workflows/release.yml
# runs goreleaser end-to-end: cross-compiles the api + migrator binaries,
# generates SPDX-JSON SBOMs, computes SHA-256 checksums, and publishes a
# GitHub Release with SLSA Level 3 build provenance. Release notes are
# extracted from CHANGELOG.md via scripts/extract-changelog.sh; the workflow
# FAILS if a `## [X.Y.Z]` section is missing or empty.
#
# `make prod-build` reproduces the snapshot bundle locally (without publishing
# or signing); useful for smoke-testing the build before tagging.
# `make goreleaser-check` runs the full pipeline the CI workflow performs,
# so you can validate everything before pushing a release tag.
#
# Version metadata (Version, GitCommit, BuildDate) is injected into
# internal/version at build time by GoReleaser's ldflags — see .goreleaser.yaml.
# VERSION here is only used for the snapshot label printed by `make prod-build`;
# the real version on a tagged release is the git tag.
VERSION ?= 0.1.0
# --------------------------------------------------------------------------

.PHONY: setup pre-commit-install \
        build run logs destroy clean \
        db-wait db-migrate db-reset db-status \
        prod-build goreleaser-check changelog-check \
        ci verify \
        test-unit test-pretty test-integration test-scripts test \
        lint fmt vet \
        pre-commit-run vulncheck semgrep socket deadcode \
        help

# ============================================================================
# First-time setup
# ============================================================================

# Full first-time setup: copies .env, installs pre-commit hooks, builds containers
setup:
	@echo "Setting up development environment..."
	@if [ ! -f .env ]; then \
		cp .env.example .env; \
		echo "Created .env from .env.example — fill in your values. Then run make setup again. Also, make sure that the repo is a git repo. If not setup will fail."; \
		exit 1; \
	fi
	@$(MAKE) pre-commit-install
	@$(MAKE) build
	@echo ""
	@echo "Setup complete. Run 'make help' to see available commands."

# Install and warm up pre-commit hooks
pre-commit-install:
	@echo "Installing pre-commit hooks..."
	@pre-commit install
	@echo "Pre-warming hook cache (downloads happen once per machine)..."
	@pre-commit install --install-hooks
	@echo "Pre-commit hooks installed."

# ============================================================================
# Development
# ============================================================================

# Build containers and run migrations (first time or after container changes)
build:
	@echo "Building development containers..."
	@podman-compose -f $(COMPOSE_FILE) build
	@echo "Starting containers..."
	@podman-compose -f $(COMPOSE_FILE) up -d
	@$(MAKE) db-wait
	@$(MAKE) db-migrate
	@echo "Restarting app to pick up migrated database..."
	@podman-compose -f $(COMPOSE_FILE) restart app
	@echo "Build complete. Application running at http://localhost:8080"
	@echo "Use 'make logs' to view application logs."

# Start containers and run migrations
run:
	@echo "Starting development environment..."
	@podman-compose -f $(COMPOSE_FILE) up -d
	@$(MAKE) db-wait
	@$(MAKE) db-migrate
	@echo "Restarting app to pick up migrated database..."
	@podman-compose -f $(COMPOSE_FILE) restart app
	@echo "Development environment ready at http://localhost:8080"

# View application logs
logs:
	@echo "Viewing application logs (Ctrl+C to exit)..."
	@podman logs -f $(APP_CONTAINER)

# Destroy all containers, volumes, and images
destroy:
	@echo "Stopping and removing containers, volumes, and images..."
	@podman-compose -f $(COMPOSE_FILE) down -v --rmi all
	@echo "Pruning dangling images..."
	@podman image prune -f
	@echo "Cleanup complete."

# Delete all temp, build, test, and release artifacts. `dist/` is the
# GoReleaser snapshot/release output — also covered by .gitignore.
#
# Does NOT touch .env: it holds the developer's local secrets/config (created by
# `make setup`), is not a build artifact, and deleting it on `make clean` would
# silently destroy their working configuration.
clean:
	@echo "Cleaning temp, build, test, and release artifacts..."
	@rm -rf _tmp_/ tmp/ bin/ _BUILD_/ _test_results_/ dist/
	@rm -rf .golangci-lint-cache/
	@rm -f *.out *.coverprofile *.test
	@echo "Clean complete."

# ============================================================================
# Database
# ============================================================================

# Wait for database to be ready
db-wait:
	@echo "Waiting for database to be ready..."
	@for i in 1 2 3 4 5 6 7 8 9 10; do \
		podman-compose -f $(COMPOSE_FILE) exec -T db pg_isready -U "$${DB_USER:-postgres}" > /dev/null 2>&1 && break || \
		(echo "Waiting for database... ($$i/10)" && sleep 2); \
	done
	@echo "Database is ready."

# Run pending migrations using the embedded migrator binary.
# Runs `go run ./cmd/migrate up` INSIDE the app container so it inherits
# the compose-provided DB_* env vars without leaking them to the host.
db-migrate:
	@echo "Running database migrations..."
	@podman-compose -f $(COMPOSE_FILE) exec -T app go run ./cmd/migrate up
	@echo "Migrations complete."

# Roll back every applied migration.
# DEV-ONLY: this target passes the destructive gates inline because the
# only database it can reach is the local compose dev database. NEVER mirror
# this pattern in a production wrapper — operators must satisfy the gates
# themselves there so the action is logged and intentional.
db-reset:
	@echo "Resetting dev database (destructive — dev only)..."
	@podman-compose -f $(COMPOSE_FILE) exec -T -e MIGRATOR_ALLOW_DESTRUCTIVE=true app \
		go run ./cmd/migrate --confirm-destructive reset
	@echo "Database reset complete."

# Check migration status
db-status:
	@echo "Checking migration status..."
	@podman-compose -f $(COMPOSE_FILE) exec -T app go run ./cmd/migrate status

# ============================================================================
# Release
# ============================================================================

# Build a local snapshot of the production release using GoReleaser.
# Produces cross-compiled binaries under dist/ — Linux, macOS, and Windows
# for amd64 and arm64 (windows/arm64 skipped). Skips the SBOM step (so syft
# isn't required) and never publishes.
#
# A real tagged release is cut by pushing a tag — the GitHub Actions workflow
# at .github/workflows/release.yml runs `goreleaser release` end-to-end,
# including SBOM generation and SLSA Level 3 provenance. Release notes are
# extracted from CHANGELOG.md by scripts/extract-changelog.sh; a missing or
# empty `## [X.Y.Z]` section aborts the release BEFORE goreleaser runs.
#
# Requires goreleaser installed locally: https://goreleaser.com/install/
#
# Runtime reminders for whoever runs the published binaries:
#   - Run the migrator (go-api-migrator) against the prod DB BEFORE deploying
#     a new API binary (the API checks `requiredTables` at startup and refuses
#     to start if the schema is missing).
#   - Pass DB_* env vars via --env-file or orchestrator secrets.
#   - Restrict METRICS_PORT (default 9090) at the infrastructure layer — it
#     has no authentication.
prod-build:
	@command -v goreleaser >/dev/null 2>&1 || { \
		echo "goreleaser not found. Install: https://goreleaser.com/install/"; \
		exit 1; \
	}
	@echo "Building snapshot release..."
	@goreleaser release --snapshot --clean --skip=sbom
	@echo ""
	@echo "Snapshot artefacts written to dist/. (SBOM generation skipped — install syft to include it.)"
	@echo "To cut a real release: update CHANGELOG.md, then git tag vX.Y.Z && git push --tags"

# Validate the release pipeline end-to-end against a local snapshot. Mirrors
# the steps in .github/workflows/release.yml (config check, extract-changelog
# tests, full snapshot build, SLSA subjects extraction via the workflow's jq
# filter, version-banner smoke test on native-arch binaries). Catches issues
# that `goreleaser check` alone misses — for example, a malformed jq filter
# that only manifests when applied to a real dist/artifacts.json, or an
# extract-changelog.sh regression.
#
# Run this before pushing a release tag.
goreleaser-check:
	@command -v goreleaser >/dev/null 2>&1 || { \
		echo "goreleaser not found. Install: https://goreleaser.com/install/"; \
		exit 1; \
	}
	@command -v jq >/dev/null 2>&1 || { \
		echo "jq not found. Install jq for the subjects-extraction step."; \
		exit 1; \
	}
	@echo "==> Validating goreleaser config..."
	@goreleaser check
	@echo ""
	@echo "==> Running extract-changelog tests..."
	@$(MAKE) --no-print-directory test-scripts
	@echo ""
	@echo "==> Building full snapshot release..."
	@if command -v syft >/dev/null 2>&1; then \
		goreleaser release --snapshot --clean; \
	else \
		echo "(syft not installed — skipping SBOM step)"; \
		goreleaser release --snapshot --clean --skip=sbom; \
	fi
	@echo ""
	@echo "==> Verifying SLSA subject extraction matches the workflow filter..."
	@HASHES=$$(jq -r '.[] | select(.type=="Binary" and .extra.Checksum != null) | "\(.extra.Checksum | sub("^sha256:"; ""))  \(.name)"' dist/artifacts.json); \
	COUNT=$$(printf '%s\n' "$$HASHES" | grep -c .); \
	if [ "$$COUNT" -eq 0 ]; then \
		echo "FAIL: jq filter produced no Binary subjects — check .github/workflows/release.yml"; \
		exit 1; \
	fi; \
	echo "OK: $$COUNT subjects extracted:"; \
	printf '%s\n' "$$HASHES" | sed 's/^/    /'
	@echo ""
	@echo "==> Smoke-testing native-arch binaries..."
	@HOST_OS=$$(uname -s | tr '[:upper:]' '[:lower:]'); \
	HOST_ARCH=$$(uname -m | sed 's/x86_64/amd64/;s/aarch64/arm64/'); \
	API_DIR=$$(find dist -maxdepth 1 -type d -name "api_$${HOST_OS}_$${HOST_ARCH}*" | head -1); \
	MIG_DIR=$$(find dist -maxdepth 1 -type d -name "migrator_$${HOST_OS}_$${HOST_ARCH}*" | head -1); \
	if [ -z "$$API_DIR" ] || [ -z "$$MIG_DIR" ]; then \
		echo "WARN: no native-arch build found for $$HOST_OS/$$HOST_ARCH — skipping binary smoke test"; \
	else \
		"$$API_DIR/go-api" --version >/dev/null && echo "OK: go-api --version"; \
		"$$MIG_DIR/go-api-migrator" --version >/dev/null && echo "OK: go-api-migrator --version"; \
	fi
	@echo ""
	@echo "All goreleaser checks passed. Safe to push a release tag."

# Verify CHANGELOG.md has a non-empty section for a given version BEFORE
# tagging. Usage: make changelog-check VERSION=1.2.3 (no leading v).
#
# The Makefile sets a snapshot default of VERSION=0.1.0 for prod-build's
# label, so we can't just check whether VERSION is empty. Instead we
# require the operator to pass VERSION explicitly — via the command line
# or the environment — and reject the in-Makefile default.
changelog-check:
	@case "$(origin VERSION)" in \
		"command line"|"environment"|"environment override"|"override") ;; \
		*) echo "usage: make changelog-check VERSION=x.y.z" >&2; exit 2 ;; \
	esac
	@./scripts/extract-changelog.sh "$(VERSION)"

# ============================================================================
# Testing
# ============================================================================
#
# Taxonomy: each command string lives in exactly ONE target. The umbrellas
# (ci, verify) COMPOSE the granular targets via $(MAKE) — they never repeat a
# command. Granular targets below NEED NO PODMAN except test-integration.

# Race-enabled unit tests (pgxmock — no real database). The canonical unit
# suite. NO PODMAN.
test-unit:
	@echo "==> go test -race ./..." && go test -race ./...

# Presentation wrapper over the unit suite: the SAME -race run as test-unit,
# rendered as a formatted table by tests/test_runner.go (which also passes
# -race, so pretty and plain test exactly the same thing). NO PODMAN.
test-pretty:
	@go run tests/test_runner.go

# Integration tests against a real Postgres. PODMAN REQUIRED — start the dev
# database first (e.g. `make run`).
#
# -p 1 forces sequential package execution. Several packages share the same
# database — the migrator integration tests, for instance, drop/re-apply the
# schema mid-test — and running packages in parallel would race them against
# packages that expect the schema to exist (e.g. userlog's CRUD tests).
test-integration:
	@echo "Running integration tests (requires running database)..."
	@# Pipe -json through the gate so an all-SKIPPED run (no DB/OPA stack env) fails
	@# LOUDLY instead of exiting 0 and letting `verify` claim success having run
	@# zero assertions. pipefail (bash) makes a build error / test failure in the
	@# left side fail the pipeline too. The gate is the single verdict on stdout.
	@bash -c 'set -o pipefail; go test -p 1 -tags integration -json ./... | go run tests/integration_gate.go'

# The repo's shell-script Go tests (the extract-changelog fixture) — the exact
# subset the goreleaser-config CI job runs. NO PODMAN.
test-scripts:
	@go test ./scripts/...

# DEPRECATED alias for test-unit, kept so existing muscle memory and external
# references do not break silently. Prefer `make test-unit`.
test:
	@echo "note: 'make test' is a deprecated alias — use 'make test-unit'."
	@$(MAKE) --no-print-directory test-unit

# Umbrella 1 — ci (NO PODMAN). The canonical local gate agents run after every
# change: build, then vet + lint + race unit tests. Composes the granular
# targets via $(MAKE), so no command string is duplicated. Mirrors CI minus the
# real-Postgres integration tests (see verify / test-integration).
ci:
	@echo "==> go build ./..." && go build ./...
	@$(MAKE) --no-print-directory vet lint test-unit
	@echo ""
	@echo "✓ ci passed (build, vet, lint, unit tests)."

# Umbrella 2 — verify (PODMAN REQUIRED). The full pre-commit gate: ci plus the
# real-Postgres integration tests. Run `make run` first (or point at a reachable
# dev database). Composes ci + test-integration — no command string duplicated.
#
# The checkmark below is HONEST: test-integration pipes through tests/
# integration_gate.go, which fails the target when zero integration tests ran
# (all t.Skip()ped because the DB/OPA stack env is absent). So this line prints
# only after integration tests actually executed — `make verify` can no longer
# claim success having verified nothing.
verify:
	@$(MAKE) --no-print-directory ci test-integration
	@echo ""
	@echo "✓ verify passed (ci + integration tests actually ran)."

# ============================================================================
# Code quality
# ============================================================================

# Run golangci-lint (must report 0 issues before commit).
lint:
	@echo "==> golangci-lint run" && golangci-lint run

# Run go vet.
vet:
	@echo "==> go vet ./..." && go vet ./...

# Format all Go files in place.
fmt:
	@gofmt -l -w .

# Run all pre-commit hooks manually against all files.
pre-commit-run:
	@pre-commit run --all-files

# Run govulncheck against all packages.
vulncheck:
	@govulncheck -show verbose ./...

# Run semgrep with pinned rulesets, not --config=auto: `auto` fetches whatever
# the registry serves at scan time, so a passing run can start failing on an
# upstream rule change. The two r/ rules (SHA-pinned actions, no curl|sh) are
# not in p/github-actions and are pinned explicitly. Keep in sync with the
# semgrep hook args in .pre-commit-config.yaml.
semgrep:
	@semgrep --config=p/golang --config=p/github-actions \
		--config=r/yaml.github-actions.security.github-actions-mutable-action-tag \
		--config=r/yaml.github-actions.security.gha-curl-pipe-shell \
		--error --skip-unknown-extensions .

# Run Socket.dev supply chain scan.
# Requires: npm install -g socket && socket login
# Scans all dependencies for known malicious packages and supply chain risks.
socket:
	@socket scan create .

# Whole-program dead-code analysis: reachability from the binaries' mains via
# golang.org/x/tools/cmd/deadcode. Finds EXPORTED, whole-program-unreachable
# code that the unexported-only linters (unused/unparam) cannot. An OCCASIONAL
# check, not per-change. The expected remaining hits are deliberate template
# surface (internal/httpclient, shared.WithUserID) — each carries a
# "Template surface" doc marker so it is not re-flagged as dead. NO PODMAN.
# Version is pinned (never @latest in a committed command).
deadcode:
	@go run golang.org/x/tools/cmd/deadcode@v0.46.0 ./...

# ============================================================================
# Help
# ============================================================================

help:
	@echo ""
	@echo "Development"
	@echo "-----------"
	@echo "  setup            First-time setup: copies .env, installs hooks, builds containers"
	@echo "  build            Build dev containers and run migrations"
	@echo "  run              Start containers and run migrations"
	@echo "  logs             View application logs"
	@echo "  destroy          Destroy all containers, volumes, and images"
	@echo "  clean            Delete all temp, build, test, and release artifacts"
	@echo ""
	@echo "Database"
	@echo "--------"
	@echo "  db-migrate       Run pending migrations (via embedded migrator)"
	@echo "  db-reset         Roll back all migrations (dev-only, destructive gates inline)"
	@echo "  db-status        Check migration status"
	@echo "  db-wait          Wait for database to be ready"
	@echo ""
	@echo "Release"
	@echo "-------"
	@echo "  prod-build       Snapshot the production release locally (cross-compiled binaries in dist/)"
	@echo "  goreleaser-check Validate the release pipeline end-to-end (run before pushing a tag)"
	@echo "  changelog-check  Confirm CHANGELOG has a non-empty section (VERSION=x.y.z)"
	@echo ""
	@echo "Testing"
	@echo "-------"
	@echo "  ci               Umbrella gate (no podman): build + vet + lint + race unit tests"
	@echo "  verify           Full pre-commit gate (podman): ci + integration tests"
	@echo "  test-unit        Race-enabled unit tests (pgxmock, no real DB)"
	@echo "  test-pretty      Unit suite with formatted table output (also -race)"
	@echo "  test-integration Integration tests against real Postgres (requires running DB)"
	@echo "  test-scripts     Shell-script Go tests (extract-changelog fixture)"
	@echo "  test             Deprecated alias for test-unit"
	@echo ""
	@echo "Code quality"
	@echo "------------"
	@echo "  lint             Run golangci-lint"
	@echo "  vet              Run go vet"
	@echo "  fmt              Format all Go files"
	@echo "  pre-commit-run   Run all pre-commit hooks against all files"
	@echo "  vulncheck        Run govulncheck against all packages"
	@echo "  semgrep          Run semgrep security scan"
	@echo "  socket           Run Socket.dev supply chain scan"
	@echo "  deadcode         Whole-program dead-code analysis (occasional check)"
	@echo ""
	@echo "Typical workflow"
	@echo "----------------"
	@echo "  First time:  make setup"
	@echo "  Daily:       make run -> make logs"
	@echo "  Verify:      make ci   (no podman)  |  make verify  (with podman)"
	@echo "  Fresh start: make destroy -> make build"
	@echo "  Release:     make goreleaser-check -> git tag vX.Y.Z -> git push --tags"
	@echo "  Tidy up:     make clean"
	@echo ""
