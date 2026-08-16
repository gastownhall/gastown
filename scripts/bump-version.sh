#!/bin/bash
set -e

# =============================================================================
# VERSION BUMP SCRIPT FOR GAS TOWN
# =============================================================================
#
# This script handles version bumping for Gas Town releases.
# It updates version numbers across all components.
#
# QUICK START:
#   ./scripts/bump-version.sh X.Y.Z --commit --tag --push --install
#
# WHAT IT UPDATES:
#   - internal/cmd/version.go   - CLI version constant
#   - npm-package/package.json  - npm package version
#   - flake.nix                 - Nix flake package version and vendorHash (if nix available)
#   - CHANGELOG.md              - Creates release entry from [Unreleased]
#
# =============================================================================

# Source common functions
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/lib/common.sh"

# Usage message
usage() {
    echo "Usage: $0 <version> [--commit] [--tag] [--push] [--install]"
    echo ""
    echo "Bump version across all Gas Town components."
    echo ""
    echo "Arguments:"
    echo "  <version>        Semantic version (e.g., 0.2.0, 1.0.0)"
    echo "  --commit         Automatically create a git commit"
    echo "  --tag            Create annotated git tag (requires --commit)"
    echo "  --push           Push commit and tag to origin (requires --tag)"
    echo "  --install        Rebuild and install gt binary to GOPATH/bin"
    echo ""
    echo "Note: flake.nix version and vendorHash are updated automatically if nix is available."
    echo ""
    echo "Examples:"
    echo "  $0 0.2.0                        # Update versions and show diff"
    echo "  $0 0.2.0 --install              # Update versions and rebuild/install gt"
    echo "  $0 0.2.0 --commit               # Update versions and commit"
    echo "  $0 0.2.0 --commit --tag         # Update, commit, and tag"
    echo "  $0 0.2.0 --commit --tag --push  # Full release preparation"
    echo ""
    echo "Recommended release command:"
    echo "  $0 X.Y.Z --commit --tag --push --install"
    exit 1
}

# Validate semantic versioning
validate_version() {
    local version=$1
    if ! [[ $version =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
        echo -e "${RED}Error: Invalid version format '$version'${NC}"
        echo "Expected semantic version format: MAJOR.MINOR.PATCH (e.g., 0.2.0)"
        exit 1
    fi
}

# Get current version from version.go
get_current_version() {
    grep 'Version = ' internal/cmd/version.go | sed 's/.*"\(.*\)".*/\1/'
}

# Update CHANGELOG.md: move [Unreleased] to [version]
update_changelog() {
    local version=$1
    local date=$(date +%Y-%m-%d)

    if [ ! -f "CHANGELOG.md" ]; then
        echo -e "${YELLOW}Warning: CHANGELOG.md not found, skipping${NC}"
        return
    fi

    # Check if there's an [Unreleased] section
    if ! grep -q "## \[Unreleased\]" CHANGELOG.md; then
        echo -e "${YELLOW}Warning: No [Unreleased] section in CHANGELOG.md${NC}"
        echo -e "${YELLOW}You may need to manually update CHANGELOG.md${NC}"
        return
    fi

    sed_i "s/## \[Unreleased\]/## [Unreleased]\n\n## [$version] - $date/" CHANGELOG.md
}

# Main script
main() {
    if [ $# -lt 1 ]; then
        usage
    fi

    NEW_VERSION=$1
    AUTO_COMMIT=false
    AUTO_TAG=false
    AUTO_PUSH=false
    AUTO_INSTALL=false

    # Parse flags
    shift
    while [ $# -gt 0 ]; do
        case "$1" in
            --commit)
                AUTO_COMMIT=true
                ;;
            --tag)
                AUTO_TAG=true
                ;;
            --push)
                AUTO_PUSH=true
                ;;
            --install)
                AUTO_INSTALL=true
                ;;
            *)
                echo -e "${RED}Error: Unknown option '$1'${NC}"
                usage
                ;;
        esac
        shift
    done

    # Validate flag dependencies
    if [ "$AUTO_TAG" = true ] && [ "$AUTO_COMMIT" = false ]; then
        echo -e "${RED}Error: --tag requires --commit${NC}"
        exit 1
    fi
    if [ "$AUTO_PUSH" = true ] && [ "$AUTO_TAG" = false ]; then
        echo -e "${RED}Error: --push requires --tag${NC}"
        exit 1
    fi

    # Validate version format
    validate_version "$NEW_VERSION"

    # Check if we're in the repo root
    if [ ! -f "internal/cmd/version.go" ]; then
        echo -e "${RED}Error: Must run from repository root${NC}"
        exit 1
    fi

    # Get current version
    CURRENT_VERSION=$(get_current_version)

    echo -e "${YELLOW}Bumping version: $CURRENT_VERSION → $NEW_VERSION${NC}"
    echo ""

    # Check for uncommitted changes
    if ! git diff-index --quiet HEAD --; then
        echo -e "${YELLOW}Warning: You have uncommitted changes${NC}"
        if [ "$AUTO_COMMIT" = true ]; then
            echo -e "${RED}Error: Cannot auto-commit with existing uncommitted changes${NC}"
            exit 1
        fi
        read -p "Continue anyway? (y/N) " -n 1 -r
        echo
        if [[ ! $REPLY =~ ^[Yy]$ ]]; then
            exit 1
        fi
    fi

    echo "Updating version files..."

    # 1. Update internal/cmd/version.go
    echo "  • internal/cmd/version.go"
    update_file "internal/cmd/version.go" \
        "Version = \"$CURRENT_VERSION\"" \
        "Version = \"$NEW_VERSION\""

    # 2. Update npm-package/package.json
    echo "  • npm-package/package.json"
    update_file "npm-package/package.json" \
        "\"version\": \"$CURRENT_VERSION\"" \
        "\"version\": \"$NEW_VERSION\""

    # 3. Update flake.nix (if nix is available)
    if command -v nix &> /dev/null; then
        echo "  • flake.nix (version)"
        update_file "flake.nix" \
            "version = \"$CURRENT_VERSION\"" \
            "version = \"$NEW_VERSION\""

        echo "  • flake.nix (vendorHash)"
        if ! "$SCRIPT_DIR/update-nix-flake.sh"; then
            echo -e "${RED}Error: Failed to update vendorHash${NC}"
            exit 1
        fi
    else
        echo -e "  ${YELLOW}• flake.nix (skipped - nix not in PATH)${NC}"
    fi

    # 4. Update CHANGELOG.md
    echo "  • CHANGELOG.md"
    update_changelog "$NEW_VERSION"

    echo ""
    echo -e "${GREEN}✓ Version updated to $NEW_VERSION${NC}"
    echo ""

    # Show diff
    echo "Changed files:"
    git diff --stat
    echo ""

    # Verify versions match
    echo "Verifying version consistency..."
    VERSION_GO=$(grep 'Version = ' internal/cmd/version.go | sed 's/.*"\(.*\)".*/\1/')
    VERSION_NPM=$(grep '"version"' npm-package/package.json | head -1 | sed 's/.*"\([0-9.]*\)".*/\1/')

    if command -v nix &> /dev/null; then
        VERSION_NIX=$(grep 'version = ' flake.nix | head -1 | sed 's/.*"\(.*\)".*/\1/')
        if [ "$VERSION_GO" = "$NEW_VERSION" ] && [ "$VERSION_NPM" = "$NEW_VERSION" ] && [ "$VERSION_NIX" = "$NEW_VERSION" ]; then
            echo -e "${GREEN}✓ All versions match: $NEW_VERSION${NC}"
        else
            echo -e "${RED}✗ Version mismatch detected!${NC}"
            echo "  version.go: $VERSION_GO"
            echo "  package.json: $VERSION_NPM"
            echo "  flake.nix: $VERSION_NIX"
            exit 1
        fi
    else
        if [ "$VERSION_GO" = "$NEW_VERSION" ] && [ "$VERSION_NPM" = "$NEW_VERSION" ]; then
            echo -e "${GREEN}✓ All versions match: $NEW_VERSION${NC}"
        else
            echo -e "${RED}✗ Version mismatch detected!${NC}"
            echo "  version.go: $VERSION_GO"
            echo "  package.json: $VERSION_NPM"
            exit 1
        fi
    fi

    echo ""

    # Auto-install if requested
    if [ "$AUTO_INSTALL" = true ]; then
        echo "Rebuilding and installing gt..."

        # Use make install which sets version ldflags
        # and handles codesigning on macOS
        if ! make install; then
            echo -e "${RED}✗ make install failed${NC}"
            exit 1
        fi

        echo -e "${GREEN}✓ gt installed${NC}"

        # Verify - check both GOPATH/bin and ~/.local/bin (Makefile default)
        INSTALL_DIR="${HOME}/.local/bin"
        if [ -x "$INSTALL_DIR/gt" ]; then
            INSTALLED_VERSION=$("$INSTALL_DIR/gt" version 2>&1 | grep -oE '[0-9]+\.[0-9]+\.[0-9]+' | head -1)
        else
            INSTALLED_VERSION=$(gt version 2>&1 | grep -oE '[0-9]+\.[0-9]+\.[0-9]+' | head -1)
        fi

        if [ "$INSTALLED_VERSION" = "$NEW_VERSION" ]; then
            echo -e "${GREEN}✓ Verified: gt version $INSTALLED_VERSION${NC}"
        else
            echo -e "${YELLOW}⚠ gt reports $INSTALLED_VERSION (expected $NEW_VERSION)${NC}"
        fi
        echo ""
    fi

    # Auto-commit if requested
    if [ "$AUTO_COMMIT" = true ]; then
        echo "Creating git commit..."

        git add internal/cmd/version.go \
                npm-package/package.json

        if command -v nix &> /dev/null && [ -f "flake.nix" ]; then
            git add flake.nix
        fi

        if [ -f "CHANGELOG.md" ]; then
            git add CHANGELOG.md
        fi

        local FLAKE_LINE=""
        if command -v nix &> /dev/null; then
            FLAKE_LINE=$'\n'"- flake.nix: $CURRENT_VERSION → $NEW_VERSION"
        fi

        git commit -m "chore: Bump version to $NEW_VERSION

Updated all component versions:
- gt CLI: $CURRENT_VERSION → $NEW_VERSION
- npm package: $CURRENT_VERSION → $NEW_VERSION${FLAKE_LINE}

Generated by scripts/bump-version.sh"

        echo -e "${GREEN}✓ Commit created${NC}"
        echo ""

        # Auto-tag if requested
        if [ "$AUTO_TAG" = true ]; then
            echo "Creating git tag v$NEW_VERSION..."
            git tag -a "v$NEW_VERSION" -m "Release v$NEW_VERSION"
            echo -e "${GREEN}✓ Tag created${NC}"
            echo ""
        fi

        # Auto-push if requested
        if [ "$AUTO_PUSH" = true ]; then
            echo "Pushing to origin..."
            git push origin main
            git push origin "v$NEW_VERSION"
            echo -e "${GREEN}✓ Pushed to origin${NC}"
            echo ""
            echo -e "${GREEN}Release v$NEW_VERSION initiated!${NC}"
            echo "GitHub Actions will build artifacts in ~5-10 minutes."
            echo "Monitor: https://github.com/jonbaldie/gastown/actions"
        elif [ "$AUTO_TAG" = true ]; then
            echo "Next steps:"
            echo "  git push origin main"
            echo "  git push origin v$NEW_VERSION"
        else
            echo "Next steps:"
            echo "  git push origin main"
            echo "  git tag -a v$NEW_VERSION -m 'Release v$NEW_VERSION'"
            echo "  git push origin v$NEW_VERSION"
        fi
    else
        echo "Review the changes above."
        echo ""
        echo "To commit and release:"
        echo "  $0 $NEW_VERSION --commit --tag --push --install"
    fi
}

main "$@"
