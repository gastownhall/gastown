.PHONY: build desktop-build desktop-run install safe-install check-forward-only check-version-tag check-release-integrated check-install-path clean test test-makefile test-e2e-container check-up-to-date temp

temp:
	@./scripts/audit-temp-workspaces.sh

BINARY := gt
BINARY_DESKTOP := gt-desktop
BUILD_DIR := .
INSTALL_DIR := $(HOME)/.local/bin
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

# ICU4C detection for macOS (required by go-icu-regex transitive dependency).
# Homebrew installs icu4c as a keg-only package, so headers/libs aren't on the
# default search path. Auto-detect the prefix and export CGo flags.
ifeq ($(shell uname),Darwin)
  ICU_PREFIX := $(shell brew --prefix icu4c 2>/dev/null)
  ifneq ($(ICU_PREFIX),)
    export CGO_CPPFLAGS += -I$(ICU_PREFIX)/include
    export CGO_LDFLAGS  += -L$(ICU_PREFIX)/lib
  endif
endif

build:
	go build -ldflags "$(LDFLAGS)" -o $(BUILD_DIR)/$(BINARY)-proxy-server ./cmd/gt-proxy-server
	go build -ldflags "$(LDFLAGS)" -o $(BUILD_DIR)/$(BINARY)-proxy-client ./cmd/gt-proxy-client
	go build -ldflags "$(LDFLAGS)" -o $(BUILD_DIR)/$(BINARY) ./cmd/gt

desktop-build:
	go build -ldflags "$(LDFLAGS)" -o $(BUILD_DIR)/$(BINARY_DESKTOP) ./cmd/gt-desktop

desktop-run:
	go run ./cmd/gt-desktop

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
	if ! git fetch "$$REMOTE_NAME" "$$REMOTE_BRANCH" --quiet; then \
		echo "ERROR: Could not refresh tracking branch $$UPSTREAM"; \
		exit 1; \
	fi; \
	LOCAL=$$(git rev-parse HEAD 2>/dev/null); \
	REMOTE=$$(git rev-parse "$$UPSTREAM" 2>/dev/null); \
	if [ -n "$$REMOTE" ] && ! git merge-base --is-ancestor "$$REMOTE" "$$LOCAL"; then \
		echo "ERROR: Local branch is behind or diverged from $$UPSTREAM"; \
		echo "  Local:  $$(git rev-parse --short HEAD)"; \
		echo "  Remote: $$(git rev-parse --short $$UPSTREAM)"; \
		echo "Update the branch or merge the integration line before installing"; \
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
	mise_resolved=$$(command -v mise >/dev/null 2>&1 && mise which $(BINARY) 2>/dev/null || true); \
	if [ "$$resolved" != "$(INSTALL_DIR)/$(BINARY)" ] && [ -z "$$mise_resolved" -o "$$resolved" != "$$mise_resolved" ]; then \
		echo "Warning: $(BINARY) resolves to $${resolved:-nothing in PATH}, not $(INSTALL_DIR)/$(BINARY)"; \
		echo "  Select the intended binary with mise, or add this before other PATH entries:"; \
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

# check-release-integrated: Verify HEAD is an ancestor of the fork's main branch
# before it can be tagged. No-op when HEAD is untagged, so it is safe to run on
# every build but only fails release tag checkouts.
#
# RELEASING.md states "Merge the release PR into fork main; never tag an
# unintegrated lane commit." That rule was stated only in prose and was violated
# twice: v1.2.1-dc14 and v1.2.1-dc15 were both tagged off lanes that never
# merged. dc14 carried commit 5e286ace (the gtf-k2k doctor guard), which was
# then reachable from a tag and from no branch, and was absent from every
# release that followed. Published tags are immutable, so the only defence is
# refusing to create a bad one.
#
# REMOTE_MAIN can be overridden when the fork is not on the 'origin' remote.
REMOTE_MAIN ?= origin/main
check-release-integrated:
	@TAG=$$(git describe --tags --exact-match HEAD 2>/dev/null || true); \
	if [ -z "$$TAG" ]; then \
		echo "check-release-integrated: HEAD is not a release tag, skipping"; \
		exit 0; \
	fi; \
	case "$$TAG" in \
		v[0-9]*) ;; \
		*) echo "check-release-integrated: tag '$$TAG' is not a vX.Y.Z release tag, skipping"; exit 0 ;; \
	esac; \
	if ! git rev-parse --verify --quiet "$(REMOTE_MAIN)" >/dev/null; then \
		echo "ERROR: cannot resolve $(REMOTE_MAIN); fetch it first or set REMOTE_MAIN=<remote>/<branch>"; \
		exit 1; \
	fi; \
	if ! git merge-base --is-ancestor HEAD "$(REMOTE_MAIN)"; then \
		echo "ERROR: HEAD is not integrated into $(REMOTE_MAIN); refusing to tag"; \
		echo "  tag being created:  $$TAG"; \
		echo "  HEAD:               $$(git rev-parse --short HEAD)"; \
		echo "  $(REMOTE_MAIN):     $$(git rev-parse --short $(REMOTE_MAIN))"; \
		echo "  commits on HEAD not in $(REMOTE_MAIN): $$(git rev-list --count $(REMOTE_MAIN)..HEAD)"; \
		echo ""; \
		echo "Tagging here would publish work that no branch contains, and tags are immutable."; \
		echo "Merge the release PR into fork main first, then tag the merge commit."; \
		echo "See RELEASING.md and bead gtf-8wo (dc14/dc15 were tagged this way)."; \
		exit 1; \
	fi; \
	echo "check-release-integrated: OK ($$TAG is an ancestor of $(REMOTE_MAIN))"

clean:
	rm -f $(BUILD_DIR)/$(BINARY)

test: test-makefile
	./scripts/go-test.sh ./...

test-makefile:
	bash scripts/check-install-path_test.sh
	bash scripts/check-up-to-date_test.sh
	bash plugins/rebuild-gt/run_test.sh
	bash -n plugins/stuck-agent-dog/run.sh
	bash -n plugins/stuck-agent-dog/run_test.sh
	bash plugins/stuck-agent-dog/run_test.sh
	bash -n plugins/dolt-backup/run.sh
	bash -n plugins/dolt-backup/run_test.sh
	bash plugins/dolt-backup/run_test.sh

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
