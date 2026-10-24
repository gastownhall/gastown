# Install gtb on a Mac and join a gtb server.
#
#   curl -fsSLO https://raw.githubusercontent.com/baovo15/gtb/feat/gtb-reshape/scripts/install-mac.mk
#   make -f install-mac.mk all CODE=<enrollment-code> WORKSPACE=<folder>
#
# WORKSPACE (default: the current folder) holds the source clone (gtb-test/)
# and the town (gtb-town/). Run every target from that folder, or pass WORKSPACE=.
# Every step is safe to re-run. `all` traces each command and writes a log
# to $(LOG); show it with `make -f install-mac.mk log`.
# Single targets: deps, build, town, join, status, item REPO=<url> (adds the
# rig, enables its gated workflow, creates one new task per run),
# advance ID=<id>, clean-town.
#
# Written for the GNU make 3.81 that macOS ships: no .SHELLFLAGS, so each
# recipe starts with $(SH) for errexit/pipefail (and xtrace under TRACE=1).

WORKSPACE ?= $(CURDIR)
# Absolute, so recipes that `cd` into the town still reach ./gtb-test/gtb.
override WORKSPACE := $(abspath $(WORKSPACE))
SERVER    ?= https://gtb.nhaboc.com
BRANCH    ?= feat/gtb-reshape
REPO_URL  ?= https://github.com/baovo15/gtb.git
LOG       ?= $(WORKSPACE)/gtb-test.log
# DOLT_PORT: set when another Dolt already holds 3307 (e.g. DOLT_PORT=3308).
DOLT_PORT ?=
# RIG: the test rig that item/advance use.
RIG       ?= demo

SRC  := $(WORKSPACE)/gtb-test
GTB  := $(SRC)/gtb
TOWN := $(WORKSPACE)/gtb-town

SHELL := /bin/bash
SH    := set -e -o pipefail;$(if $(TRACE), PS4='+ '; set -x;)
MAKEFILE := $(firstword $(MAKEFILE_LIST))

.PHONY: all run deps build town join status log item advance clean-town check-code

all:
	@$(SH) mkdir -p "$(WORKSPACE)"; \
	{ echo "=== $$(date -u +%Y-%m-%dT%H:%M:%SZ) gtb install"; sw_vers 2>/dev/null || uname -sr; uname -m; \
	  echo "make $(MAKE_VERSION); WORKSPACE=$(WORKSPACE) SERVER=$(SERVER) BRANCH=$(BRANCH) DOLT_PORT=$(DOLT_PORT)"; } > "$(LOG)"; \
	echo "==> log: $(LOG)"; \
	$(MAKE) -f "$(MAKEFILE)" run TRACE=1 CODE="$(CODE)" WORKSPACE="$(WORKSPACE)" SERVER="$(SERVER)" BRANCH="$(BRANCH)" DOLT_PORT="$(DOLT_PORT)" 2>&1 \
	  | sed -E "s/(--code[ =]+[\"']?)[A-Za-z0-9]+/\1<code>/; s/(CODE=[\"']?)[A-Za-z0-9]+/\1<code>/" \
	  | tee -a "$(LOG)"; \
	echo "==> full log: $(LOG)"

run: build town join status

log:
	@cat "$(LOG)"

deps:
	@$(SH) for t in git go tmux dolt; do \
	  command -v $$t >/dev/null && continue; \
	  command -v brew >/dev/null || { echo "$$t is missing and Homebrew is not installed (https://brew.sh)"; exit 1; }; \
	  echo "==> brew install $$t"; brew install $$t; \
	done

build: deps
	@$(SH) if [ ! -d "$(SRC)/.git" ]; then \
	  echo "==> clone $(BRANCH) into $(SRC)"; \
	  git clone -q -b "$(BRANCH)" "$(REPO_URL)" "$(SRC)"; \
	fi; \
	echo "==> update $(SRC)"; \
	cd "$(SRC)"; git fetch -q origin; git checkout -q "$(BRANCH)"; git pull -q --ff-only; \
	echo "==> build gtb (pure Go, no ICU)"; \
	CGO_ENABLED=0 go build -tags gms_pure_go \
	  -ldflags "-X github.com/steveyegge/gastown/internal/cmd.BuiltProperly=1 -X github.com/steveyegge/gastown/internal/cmd.Version=$$(git describe --tags --always) -X github.com/steveyegge/gastown/internal/cmd.Build=$$(git rev-parse --short HEAD) -X github.com/steveyegge/gastown/internal/cmd.Commit=$$(git rev-parse HEAD) -X github.com/steveyegge/gastown/internal/cmd.Branch=$$(git rev-parse --abbrev-ref HEAD)" \
	  -o gtb ./cmd/gtb; \
	"$(GTB)" version

town:
	@$(SH) if [ -f "$(TOWN)/mayor/town.json" ]; then \
	  echo "==> town exists: $(TOWN)"; \
	else \
	  echo "==> gtb install $(TOWN)"; \
	  mkdir -p "$(TOWN)"; \
	  "$(GTB)" install "$(TOWN)" --force$(if $(DOLT_PORT), --dolt-port $(DOLT_PORT)); \
	  [ -f "$(TOWN)/mayor/town.json" ] || { echo "install did not create $(TOWN)/mayor/town.json"; exit 1; }; \
	fi

check-code:
	@[ -n "$(CODE)" ] || { echo "CODE is required: make -f install-mac.mk all CODE=<code from the dashboard's Settings page>"; exit 1; }

join: check-code
	@$(SH) if [ -f "$(TOWN)/.runtime/machine-token" ] || [ -f "$(TOWN)/.runtime/agentlink/machine-token" ]; then \
	  echo "==> already joined; to join again: cd $(TOWN) && $(GTB) join $(SERVER) --code <new code> --force"; \
	else \
	  echo "==> gtb join $(SERVER)"; \
	  cd "$(TOWN)"; "$(GTB)" join "$(SERVER)" --code "$(CODE)"; \
	fi

status:
	@echo "==> machine token (expect -rw-------)"
	@found=; for f in "$(TOWN)/.runtime/machine-token" "$(TOWN)/.runtime/agentlink/machine-token"; do \
	  [ -f "$$f" ] && { ls -l "$$f"; found=1; }; \
	done; [ -n "$$found" ] || echo "no machine token yet"
	@echo "==> done: check the Machines page at $(SERVER)"

item:
	@[ -n "$(REPO)" ] || { echo "REPO is required: make -f install-mac.mk item REPO=<repo url without .tbb/> (creates one new task per run)"; exit 1; }
	@$(SH) cd "$(TOWN)"; \
	[ -d "$(TOWN)/$(RIG)" ] || "$(GTB)" rig add "$(RIG)" "$(REPO)"; \
	cfg="$(TOWN)/$(RIG)/settings/config.json"; \
	if [ ! -f "$$cfg" ]; then \
	  echo "==> enable the gated workflow: $$cfg"; mkdir -p "$$(dirname "$$cfg")"; \
	  printf '%s\n' '{"type":"rig-settings","version":1,"workflow":{"gated":{"enabled":true}}}' > "$$cfg"; \
	elif ! grep -q '"gated"' "$$cfg"; then \
	  echo "$$cfg exists without workflow.gated; add \"workflow\":{\"gated\":{\"enabled\":true}} to it and re-run"; exit 1; \
	fi; \
	"$(GTB)" item new task "demo" --rig "$(RIG)"

advance:
	@[ -n "$(ID)" ] || { echo "ID is required: make -f install-mac.mk advance ID=<item id>"; exit 1; }
	@cd "$(TOWN)"; "$(GTB)" item advance "$(ID)" --rig "$(RIG)"; echo "exit code: $$?"

clean-town:
	@echo "This deletes $(TOWN) (the test town only, not the source clone). Type yes to continue:"; \
	read ans; if [ "$$ans" = yes ]; then rm -rf "$(TOWN)" && echo "removed $(TOWN)"; else echo "kept"; fi
