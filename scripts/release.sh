#!/bin/bash
set -euo pipefail

# =============================================================================
# llm-mux Release Script
# Builds binaries (goreleaser) and Docker images locally
# =============================================================================

DOCKERHUB_REPO="nghyane/llm-mux"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_DIR="$(dirname "$SCRIPT_DIR")"
YES_FLAG=false

# Colors
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

log_info() { echo -e "${BLUE}[INFO]${NC} $1"; }
log_success() { echo -e "${GREEN}[SUCCESS]${NC} $1"; }
log_warn() { echo -e "${YELLOW}[WARN]${NC} $1"; }
log_error() { echo -e "${RED}[ERROR]${NC} $1"; }

# =============================================================================
# Pre-flight checks
# =============================================================================
preflight_check() {
    log_info "Running pre-flight checks..."
    
    local missing=()
    
    command -v go >/dev/null 2>&1 || missing+=("go")
    command -v goreleaser >/dev/null 2>&1 || missing+=("goreleaser")
    command -v docker >/dev/null 2>&1 || missing+=("docker")
    command -v gh >/dev/null 2>&1 || missing+=("gh")
    
    if [ ${#missing[@]} -ne 0 ]; then
        log_error "Missing required tools: ${missing[*]}"
        echo "Install with:"
        echo "  brew install go goreleaser docker gh"
        exit 1
    fi
    
    # Check gh auth
    if ! gh auth status >/dev/null 2>&1; then
        log_error "GitHub CLI not authenticated. Run: gh auth login"
        exit 1
    fi
    
    # Check docker
    if ! docker info >/dev/null 2>&1; then
        log_error "Docker is not running"
        exit 1
    fi
    
    log_success "All checks passed"
}

# =============================================================================
# Get version info
# =============================================================================
get_version_info() {
    cd "$PROJECT_DIR"
    
    COMMIT=$(git rev-parse --short HEAD)
    BUILD_DATE=$(date -u +%Y-%m-%dT%H:%M:%SZ)
    LATEST_TAG=$(git tag --sort=-v:refname | head -1)
    
    # Check if HEAD is exactly on a tag
    EXACT_TAG=$(git describe --tags --exact-match 2>/dev/null || echo "")
    
    if [ -n "$EXACT_TAG" ]; then
        # On a tag - use clean version
        VERSION="$EXACT_TAG"
    else
        # Not on tag - use snapshot version matching goreleaser format
        if [ -n "$LATEST_TAG" ]; then
            # Extract version numbers and increment patch
            BASE_VERSION="${LATEST_TAG#v}"
            MAJOR=$(echo "$BASE_VERSION" | cut -d. -f1)
            MINOR=$(echo "$BASE_VERSION" | cut -d. -f2)
            PATCH=$(echo "$BASE_VERSION" | cut -d. -f3)
            NEXT_PATCH=$((PATCH + 1))
            VERSION="${MAJOR}.${MINOR}.${NEXT_PATCH}-next"
        else
            VERSION="dev-${COMMIT}"
        fi
    fi
    
    export VERSION COMMIT BUILD_DATE LATEST_TAG
}

# =============================================================================
# Show current status
# =============================================================================
show_status() {
    get_version_info
    
    echo ""
    echo "========================================"
    echo "  llm-mux Release Script"
    echo "========================================"
    echo ""
    echo "  Latest tag:    ${LATEST_TAG:-none}"
    echo "  Current:       $VERSION"
    echo "  Commit:        $COMMIT"
    echo "  Build date:    $BUILD_DATE"
    echo ""
}

confirm() {
    local prompt="$1"
    if [ "$YES_FLAG" = "true" ]; then
        log_info "$prompt [auto-confirmed with --yes]"
        return 0
    fi
    read -p "$prompt [y/N] " -n 1 -r
    echo
    [[ $REPLY =~ ^[Yy]$ ]]
}

# =============================================================================
# Create new tag
# =============================================================================
create_tag() {
    local new_version="$1"
    
    if [[ ! "$new_version" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
        log_error "Invalid version format. Use: vX.Y.Z (e.g., v2.0.17)"
        exit 1
    fi
    
    if git rev-parse "$new_version" >/dev/null 2>&1; then
        log_error "Tag $new_version already exists"
        exit 1
    fi
    
    log_info "Creating tag $new_version..."
    git tag "$new_version"
    
    if confirm "Push tag to origin?"; then
        git push origin "$new_version"
        log_success "Tag $new_version pushed"
    else
        log_warn "Tag created locally but not pushed"
    fi
}

# =============================================================================
# Build with GoReleaser
# =============================================================================
build_binaries() {
    local mode="${1:-snapshot}"
    local skip_homebrew="${2:-false}"
    
    cd "$PROJECT_DIR"
    get_version_info
    
    export GITHUB_TOKEN=$(gh auth token)
    export HOMEBREW_TAP_TOKEN="${HOMEBREW_TAP_TOKEN:-$GITHUB_TOKEN}"
    
    log_info "Building binaries with goreleaser ($mode mode)..."
    
    if [ "$mode" = "release" ]; then
        if [ "$skip_homebrew" = "true" ]; then
            log_info "Skipping Homebrew cask publish"
            goreleaser release --clean --skip=homebrew_casks
        else
            goreleaser release --clean
        fi
    else
        goreleaser release --snapshot --clean
    fi
    
    log_success "Binaries built in dist/"
    ls -la dist/*.tar.gz dist/*.zip 2>/dev/null || true
}

# =============================================================================
# Build Docker images
# =============================================================================
build_docker() {
    local push="${1:-false}"
    local tag_latest="${2:-true}"
    
    cd "$PROJECT_DIR"
    get_version_info
    
    log_info "Building Docker images..."
    
    docker buildx create --name llm-mux-builder --use 2>/dev/null || docker buildx use llm-mux-builder 2>/dev/null || true
    
    local push_flag=""
    if [ "$push" = "true" ]; then
        push_flag="--push"
        log_info "Will push to $DOCKERHUB_REPO"
    else
        push_flag="--load"
        log_warn "Building for local only (single platform)"
    fi
    
    local tags="-t $DOCKERHUB_REPO:$VERSION"
    if [ "$tag_latest" = "true" ] && [ "$push" = "true" ]; then
        tags="$tags -t $DOCKERHUB_REPO:latest"
    fi
    
    if [ "$push" = "true" ]; then
        docker buildx build \
            --platform linux/amd64,linux/arm64 \
            --build-arg VERSION="$VERSION" \
            --build-arg COMMIT="$COMMIT" \
            --build-arg BUILD_DATE="$BUILD_DATE" \
            $tags \
            $push_flag \
            .
    else
        docker build \
            --build-arg VERSION="$VERSION" \
            --build-arg COMMIT="$COMMIT" \
            --build-arg BUILD_DATE="$BUILD_DATE" \
            -t "$DOCKERHUB_REPO:$VERSION" \
            .
    fi
    
    log_success "Docker build complete"
    if [ "$push" = "true" ]; then
        if [ "$tag_latest" = "true" ]; then
            log_success "Pushed: $DOCKERHUB_REPO:latest, $DOCKERHUB_REPO:$VERSION"
        else
            log_success "Pushed: $DOCKERHUB_REPO:$VERSION"
        fi
        
        log_info "Verifying pushed image..."
        if docker run --rm "$DOCKERHUB_REPO:$VERSION" ./llm-mux --version 2>/dev/null | head -1; then
            log_success "Image verification passed"
        else
            log_warn "Could not verify image (may need manual check)"
        fi
    fi
}

# =============================================================================
# Full release
# =============================================================================
full_release() {
    local version="$1"
    
    show_status
    
    log_info "Starting full release for $version"
    echo ""
    
    if ! confirm "This will create tag, build binaries (+ Homebrew), and push Docker. Continue?"; then
        log_warn "Aborted"
        exit 0
    fi
    
    create_tag "$version"
    build_binaries "release" "false"
    build_docker "true" "true"
    
    echo ""
    log_success "========================================"
    log_success "Release $version complete!"
    log_success "========================================"
    echo ""
    echo "  GitHub Release: https://github.com/nghyane/llm-mux/releases/tag/$version"
    echo "  Docker Hub:     https://hub.docker.com/r/$DOCKERHUB_REPO/tags"
    echo ""
}

# Dev release (Docker only, no Homebrew, no latest tag)
# =============================================================================
dev_release() {
    show_status
    
    log_info "Starting dev release (Docker only, no Homebrew)"
    echo ""
    
    if ! confirm "This will push Docker image with dev version (no latest tag). Continue?"; then
        log_warn "Aborted"
        exit 0
    fi
    
    build_docker "true" "false"
    
    echo ""
    log_success "Dev release complete!"
    echo "  Docker Hub: https://hub.docker.com/r/$DOCKERHUB_REPO/tags"
    echo ""
}

# =============================================================================
# Usage
# =============================================================================
usage() {
    echo "Usage: $0 <command> [options]"
    echo ""
    echo "Commands:"
    echo "  status              Show current version info"
    echo "  tag <version>       Create and push a new tag (e.g., v2.0.17)"
    echo "  snapshot            Build binaries locally (no publish)"
    echo "  binaries            Build and publish binaries + Homebrew to GitHub"
    echo "  binaries-only       Build and publish binaries (skip Homebrew)"
    echo "  docker              Build Docker image locally"
    echo "  docker-push         Build and push Docker image (with :latest)"
    echo "  docker-dev          Build and push Docker dev image (no :latest)"
    echo "  release <version>   Full release: tag + binaries + Homebrew + docker"
    echo "  dev                 Dev release: Docker only (no Homebrew, no :latest)"
    echo ""
    echo "Options:"
    echo "  --yes, -y           Skip confirmation prompts"
    echo ""
    echo "Examples:"
    echo "  $0 status"
    echo "  $0 snapshot"
    echo "  $0 release v2.0.17"
    echo "  $0 release v2.0.17 --yes"
    echo "  $0 dev"
    echo ""
}

# =============================================================================
# Main
# =============================================================================
main() {
    cd "$PROJECT_DIR"
    
    local cmd=""
    local version_arg=""
    
    while [[ $# -gt 0 ]]; do
        case "$1" in
            --yes|-y)
                YES_FLAG=true
                shift
                ;;
            -*)
                log_error "Unknown option: $1"
                usage
                exit 1
                ;;
            *)
                if [ -z "$cmd" ]; then
                    cmd="$1"
                elif [ -z "$version_arg" ]; then
                    version_arg="$1"
                fi
                shift
                ;;
        esac
    done
    
    case "$cmd" in
        status)
            show_status
            ;;
        tag)
            preflight_check
            create_tag "$version_arg"
            ;;
        snapshot)
            preflight_check
            show_status
            build_binaries "snapshot"
            ;;
        binaries)
            preflight_check
            show_status
            build_binaries "release" "false"
            ;;
        binaries-only)
            preflight_check
            show_status
            build_binaries "release" "true"
            ;;
        docker)
            preflight_check
            show_status
            build_docker "false"
            ;;
        docker-push)
            preflight_check
            show_status
            build_docker "true" "true"
            ;;
        docker-dev)
            preflight_check
            show_status
            build_docker "true" "false"
            ;;
        release)
            if [ -z "$version_arg" ]; then
                log_error "Version required. Example: $0 release v2.0.17"
                exit 1
            fi
            preflight_check
            full_release "$version_arg"
            ;;
        dev)
            preflight_check
            dev_release
            ;;
        help|--help|-h)
            usage
            ;;
        "")
            usage
            exit 1
            ;;
        *)
            log_error "Unknown command: $cmd"
            usage
            exit 1
            ;;
    esac
}

main "$@"
