.PHONY: build desktop-build desktop-run install safe-install check-forward-only check-version-tag check-install-path clean test test-makefile test-e2e-container check-up-to-date

BINARY := gt
BINARY_DESKTOP := gt-desktop
BUILD_DIR := .
INSTALL_DIR := $(HOME)/.local/bin
BUILD_TAGS := gms_pure_go
# Native tests run from inside live Gas Town sessions, but unit tests must not
# inherit that session's Dolt endpoint, workspace identity, or non-interactive
# shell startup hook. CI starts clean; make test must provide the same contract.
TEST_ENV := env \
	-u BD_DB \
	-u BEADS_DB \
	-u BEADS_DIR \
	-u BEADS_DOLT_AUTO_START \
	-u BEADS_DOLT_DATABASE \
	-u BEADS_DOLT_DATA_DIR \
	-u BEADS_DOLT_HOST \
	-u BEADS_DOLT_PORT \
	-u BEADS_DOLT_SERVER_DATABASE \
	-u BEADS_DOLT_SERVER_HOST \
	-u BEADS_DOLT_SERVER_MODE \
	-u BEADS_DOLT_SERVER_PORT \
	-u BEADS_DOLT_SERVER_SOCKET \
	-u BEADS_DOLT_SHARED_SERVER \
	-u BEADS_SHARED_SERVER_DIR \
	-u GT_DOLT_DATA \
	-u GT_DOLT_EXTERNAL \
	-u GT_DOLT_HOST \
	-u GT_DOLT_IGNORE_CONFIG \
	-u GT_DOLT_PORT \
	-u GT_TOWN_ROOT \
	BASH_ENV=
E2E_IMAGE ?= gastown-test
E2E_BUILD_FLAGS ?=
E2E_RUN_FLAGS ?= --rm
E2E_BUILD_RETRIES ?= 1
E2E_RUN_RETRIES ?= 1

# Get version info for ldflags
VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
COMMIT := $(shell git rev-parse --short HEAD 2>/dev/null || echo "unknown")
BUILD_TIME := $(shell date -u +"%Y-%m-%dT%H:%M:%SZ")

LDFLAGS := -s -w \
           -X github.com/steveyegge/gastown/internal/cmd.Version=$(VERSION) \
           -X github.com/steveyegge/gastown/internal/cmd.Commit=$(COMMIT) \
           -X github.com/steveyegge/gastown/internal/cmd.BuildTime=$(BUILD_TIME) \
           -X github.com/steveyegge/gastown/internal/cmd.BuiltProperly=1

build:
	go build -tags "$(BUILD_TAGS)" -ldflags "$(LDFLAGS)" -o $(BUILD_DIR)/$(BINARY)-proxy-server ./cmd/gt-proxy-server
	go build -tags "$(BUILD_TAGS)" -ldflags "$(LDFLAGS)" -o $(BUILD_DIR)/$(BINARY)-proxy-client ./cmd/gt-proxy-client
	go build -tags "$(BUILD_TAGS)" -ldflags "$(LDFLAGS)" -o $(BUILD_DIR)/$(BINARY) ./cmd/gt
	./scripts/verify-cgo.sh "$(BUILD_DIR)/$(BINARY)"

desktop-build:
	go build -tags "$(BUILD_TAGS)" -ldflags "$(LDFLAGS)" -o $(BUILD_DIR)/$(BINARY_DESKTOP) ./cmd/gt-desktop
	./scripts/verify-cgo.sh "$(BUILD_DIR)/$(BINARY_DESKTOP)"

desktop-run:
	go run -tags "$(BUILD_TAGS)" ./cmd/gt-desktop

check-up-to-date:
ifndef SKIP_UPDATE_CHECK
	@# Skip check on detached HEAD (tag checkouts, CI builds)
	@if ! git symbolic-ref HEAD >/dev/null 2>&1; then exit 0; fi
	@# Use the current branch's tracking ref (works for main, carry/operational, etc.)
	@UPSTREAM=$$(git rev-parse --abbrev-ref --symbolic-full-name @{u} 2>/dev/null); \
	if [ -z "$$UPSTREAM" ]; then \
		echo "Warning: no upstream tracking branch set, skipping update check"; \
		exit 0; \
	fi; \
	REMOTE_NAME=$$(echo "$$UPSTREAM" | cut -d/ -f1); \
	REMOTE_BRANCH=$$(echo "$$UPSTREAM" | cut -d/ -f2-); \
	git fetch "$$REMOTE_NAME" "$$REMOTE_BRANCH" --quiet 2>/dev/null || true; \
	LOCAL=$$(git rev-parse HEAD 2>/dev/null); \
	REMOTE=$$(git rev-parse "$$UPSTREAM" 2>/dev/null); \
	if [ -n "$$REMOTE" ] && [ "$$LOCAL" != "$$REMOTE" ]; then \
		echo "ERROR: Local branch is not up to date with $$UPSTREAM"; \
		echo "  Local:  $$(git rev-parse --short HEAD)"; \
		echo "  Remote: $$(git rev-parse --short $$UPSTREAM)"; \
		echo "Run 'git pull' first, or use SKIP_UPDATE_CHECK=1 to override"; \
		exit 1; \
	fi
endif

# check-forward-only: Ensure HEAD is a descendant of the currently installed binary's commit.
# Prevents rebuilding to an older or diverged commit, which caused a crash loop where
# the replaced binary broke session startup hooks → witness respawned → loop every 1-2 min.
check-forward-only:
ifndef SKIP_FORWARD_CHECK
	@BINARY_COMMIT=$$($(INSTALL_DIR)/$(BINARY) version --verbose 2>/dev/null | grep -o '@[a-f0-9]*' | head -1 | tr -d '@'); \
	if [ -n "$$BINARY_COMMIT" ] && [ "$$BINARY_COMMIT" != "unknown" ]; then \
		HEAD_COMMIT=$$(git rev-parse HEAD 2>/dev/null); \
		if [ "$$BINARY_COMMIT" = "$$HEAD_COMMIT" ] || [ "$$(git rev-parse --short HEAD)" = "$$BINARY_COMMIT" ]; then \
			echo "Binary is already at HEAD, nothing to do"; \
			exit 1; \
		fi; \
		if ! git merge-base --is-ancestor "$$BINARY_COMMIT" HEAD 2>/dev/null; then \
			echo "ERROR: HEAD ($$(git rev-parse --short HEAD)) is NOT a descendant of installed binary ($$BINARY_COMMIT)"; \
			echo "This would be a DOWNGRADE. Refusing to rebuild."; \
			echo "Use SKIP_FORWARD_CHECK=1 to override (dangerous)."; \
			exit 1; \
		fi; \
		echo "Forward-only check passed: $$BINARY_COMMIT → $$(git rev-parse --short HEAD)"; \
	else \
		echo "Warning: cannot determine installed binary commit, skipping forward check"; \
	fi
endif

check-install-path:
	@resolved=$$(command -v $(BINARY) 2>/dev/null || true); \
	if [ "$$resolved" != "$(INSTALL_DIR)/$(BINARY)" ]; then \
		echo "Warning: $(BINARY) resolves to $${resolved:-nothing in PATH}, not $(INSTALL_DIR)/$(BINARY)"; \
		echo "  Add this before other PATH entries in your shell profile:"; \
		echo '  export PATH="$(INSTALL_DIR):$$PATH"'; \
	fi

install: check-up-to-date build
	@mkdir -p $(INSTALL_DIR)
	@rm -f $(INSTALL_DIR)/$(BINARY)
	@cp $(BUILD_DIR)/$(BINARY) $(INSTALL_DIR)/$(BINARY)
	@# Nuke any stale go-install binaries that shadow the canonical location
	@for bad in $(HOME)/go/bin/$(BINARY) $(HOME)/bin/$(BINARY); do \
		if [ -f "$$bad" ]; then \
			echo "Removing stale $$bad (use make install, not go install)"; \
			rm -f "$$bad"; \
		fi; \
	done
	@echo "Installed $(BINARY) to $(INSTALL_DIR)/$(BINARY)"
	@$(MAKE) --no-print-directory check-install-path
	@# Restart daemon so it picks up the new binary.
	@# A stale daemon is a recurring source of bugs (wrong session prefixes, etc.)
	@if $(INSTALL_DIR)/$(BINARY) daemon status >/dev/null 2>&1; then \
		echo "Restarting daemon to pick up new binary..."; \
		$(INSTALL_DIR)/$(BINARY) daemon stop >/dev/null 2>&1 || true; \
		sleep 1; \
		$(INSTALL_DIR)/$(BINARY) daemon start >/dev/null 2>&1 && \
			echo "Daemon restarted." || \
			echo "Warning: daemon restart failed (start manually with: gt daemon start)"; \
	fi
	@# Sync plugins from build repo to town runtime directories.
	@# Prevents drift when plugin fixes merge but runtime dirs are stale.
	@$(INSTALL_DIR)/$(BINARY) plugin sync --source $(CURDIR)/plugins 2>/dev/null && \
		echo "Plugins synced." || true

# safe-install: Replace binary WITHOUT restarting daemon or killing sessions.
# Use this for automated rebuilds (e.g., rebuild-gt plugin). Sessions pick up
# the new binary on their next natural cycle/handoff.
safe-install: check-up-to-date check-forward-only build
	@mkdir -p $(INSTALL_DIR)
	@# Atomic-ish replace: copy to temp then move (move is atomic on same filesystem)
	@cp $(BUILD_DIR)/$(BINARY) $(INSTALL_DIR)/$(BINARY).new
	@mv $(INSTALL_DIR)/$(BINARY).new $(INSTALL_DIR)/$(BINARY)
	@# Nuke any stale go-install binaries that shadow the canonical location
	@for bad in $(HOME)/go/bin/$(BINARY) $(HOME)/bin/$(BINARY); do \
		if [ -f "$$bad" ]; then \
			echo "Removing stale $$bad (use make install, not go install)"; \
			rm -f "$$bad"; \
		fi; \
	done
	@echo "Installed $(BINARY) to $(INSTALL_DIR)/$(BINARY) (daemon NOT restarted)"
	@$(MAKE) --no-print-directory check-install-path
	@echo "Sessions will pick up new binary on next cycle."

# check-version-tag: Verify that if HEAD is tagged vX.Y.Z, the Version constant
# in internal/cmd/version.go equals X.Y.Z. No-op when HEAD is untagged, so it is
# safe to run on every build but only fails release tag checkouts.
# Prevents recurrence of gh#3459 (v0.13.0 shipped reporting 0.12.1).
check-version-tag:
	@TAG=$$(git describe --tags --exact-match HEAD 2>/dev/null || true); \
	if [ -z "$$TAG" ]; then \
		echo "check-version-tag: HEAD is not a release tag, skipping"; \
		exit 0; \
	fi; \
	case "$$TAG" in \
		v[0-9]*) TAG_VERSION=$${TAG#v} ;; \
		*) echo "check-version-tag: tag '$$TAG' is not a vX.Y.Z release tag, skipping"; exit 0 ;; \
	esac; \
	CODE_VERSION=$$(grep -E '^[[:space:]]*Version[[:space:]]*=[[:space:]]*"' internal/cmd/version.go | head -1 | sed 's/.*"\([^"]*\)".*/\1/'); \
	if [ -z "$$CODE_VERSION" ]; then \
		echo "ERROR: could not parse Version from internal/cmd/version.go"; \
		exit 1; \
	fi; \
	if [ "$$TAG_VERSION" != "$$CODE_VERSION" ]; then \
		echo "ERROR: version mismatch between git tag and Version constant"; \
		echo "  git tag at HEAD:          $$TAG (expects Version=$$TAG_VERSION)"; \
		echo "  internal/cmd/version.go:  Version=$$CODE_VERSION"; \
		echo ""; \
		echo "Run scripts/bump-version.sh before tagging, or re-tag HEAD correctly."; \
		echo "See gh#3459 for background."; \
		exit 1; \
	fi; \
	echo "check-version-tag: OK (tag $$TAG matches Version=$$CODE_VERSION)"

clean:
	rm -f $(BUILD_DIR)/$(BINARY)

test: test-makefile
	$(TEST_ENV) go test -tags "$(BUILD_TAGS)" ./...

test-makefile:
	$(TEST_ENV) bash scripts/check-install-path_test.sh
	$(TEST_ENV) bash -n scripts/verify-cgo.sh
	$(TEST_ENV) bash -n plugins/stuck-agent-dog/run.sh
	$(TEST_ENV) bash -n plugins/stuck-agent-dog/run_test.sh
	$(TEST_ENV) bash plugins/stuck-agent-dog/run_test.sh

# Run e2e tests in isolated container (the only supported way to run them)
test-e2e-container:
ifeq ($(OS),Windows_NT)
	@powershell -NoProfile -Command "$$max=$(E2E_BUILD_RETRIES); for($$i=1; $$i -le $$max; $$i++){ docker build $(E2E_BUILD_FLAGS) -f Dockerfile.e2e -t $(E2E_IMAGE) .; if($$LASTEXITCODE -eq 0){ break }; if($$i -eq $$max){ exit 1 }; Write-Host ('docker build failed (attempt ' + $$i + '), retrying...'); Start-Sleep -Seconds 2 }"
	@powershell -NoProfile -Command "$$max=$(E2E_RUN_RETRIES); for($$i=1; $$i -le $$max; $$i++){ docker run $(E2E_RUN_FLAGS) $(E2E_IMAGE); if($$LASTEXITCODE -eq 0){ break }; if($$i -eq $$max){ exit 1 }; Write-Host ('docker run failed (attempt ' + $$i + '), retrying...'); Start-Sleep -Seconds 2 }"
else
	@attempt=1; \
	while [ $$attempt -le $(E2E_BUILD_RETRIES) ]; do \
		docker build $(E2E_BUILD_FLAGS) -f Dockerfile.e2e -t $(E2E_IMAGE) . && break; \
		if [ $$attempt -eq $(E2E_BUILD_RETRIES) ]; then exit 1; fi; \
		echo "docker build failed (attempt $$attempt), retrying..."; \
		attempt=$$((attempt+1)); \
		sleep 2; \
	done
	@attempt=1; \
	while [ $$attempt -le $(E2E_RUN_RETRIES) ]; do \
		docker run $(E2E_RUN_FLAGS) $(E2E_IMAGE) && break; \
		if [ $$attempt -eq $(E2E_RUN_RETRIES) ]; then exit 1; fi; \
		echo "docker run failed (attempt $$attempt), retrying..."; \
		attempt=$$((attempt+1)); \
		sleep 2; \
	done
endif
