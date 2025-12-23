#!/bin/bash
# ============================================================
# llm-mux installer
# https://github.com/nghyane/llm-mux
#
# Usage:
#   curl -fsSL https://raw.githubusercontent.com/nghyane/llm-mux/main/install.sh | bash
#
# Options:
#   --no-service    Skip service installation (binary only)
#   --version VER   Install specific version (default: latest)
#   --dir DIR       Custom install directory
#   --no-verify     Skip checksum verification
#   --help          Show help
# ============================================================

set -euo pipefail

# --- Configuration -------------------------------------------

REPO="nghyane/llm-mux"
BINARY_NAME="llm-mux"
SERVICE_NAME="com.llm-mux"

VERSION=""
INSTALL_DIR=""
SKIP_SERVICE=false
SKIP_VERIFY=false
FORCE_INSTALL=false

# Paths detected from binary (set by init_config)
DETECTED_CONFIG_PATH=""
DETECTED_CRED_PATH=""

# --- Utilities -----------------------------------------------

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m'

log()   { echo -e "${GREEN}==>${NC} $*"; }
info()  { echo -e "${BLUE}   $*${NC}"; }
warn()  { echo -e "${YELLOW}warning:${NC} $*" >&2; }
error() { echo -e "${RED}error:${NC} $*" >&2; exit 1; }

command_exists() { command -v "$1" &>/dev/null; }

# Check if running as root (not recommended)
check_root() {
    if [[ "${EUID:-$(id -u)}" -eq 0 ]]; then
        warn "Running as root is not recommended. Consider running as a regular user."
    fi
}

# --- Platform Detection --------------------------------------

OS=""
ARCH=""

detect_platform() {
    local uname_s uname_m
    uname_s=$(uname -s)
    uname_m=$(uname -m)

    case "$uname_s" in
        Darwin*)                    OS="darwin" ;;
        Linux*)                     OS="linux" ;;
        FreeBSD*)                   OS="freebsd" ;;
        MINGW*|MSYS*|CYGWIN*)       OS="windows" ;;
        *)                          error "Unsupported OS: $uname_s. Supported: macOS, Linux, FreeBSD, Windows (via WSL/Git Bash)" ;;
    esac

    case "$uname_m" in
        x86_64|amd64)               ARCH="amd64" ;;
        arm64|aarch64)              ARCH="arm64" ;;
        armv7l|armv6l)              ARCH="arm" ;;
        i386|i686)                  ARCH="386" ;;
        *)                          error "Unsupported architecture: $uname_m. Supported: x86_64, arm64, armv7l, i386" ;;
    esac
}

detect_install_dir() {
    [[ -n "$INSTALL_DIR" ]] && return

    # Priority order: /usr/local/bin > ~/.local/bin > ~/bin
    local dirs=("/usr/local/bin" "$HOME/.local/bin" "$HOME/bin")

    for dir in "${dirs[@]}"; do
        if [[ -d "$dir" && -w "$dir" ]]; then
            INSTALL_DIR="$dir"
            return
        fi
    done

    # Try to create ~/.local/bin
    if mkdir -p "$HOME/.local/bin" 2>/dev/null; then
        INSTALL_DIR="$HOME/.local/bin"
        return
    fi

    error "No writable install directory found. Use --dir to specify a custom directory."
}

# --- Network Utilities ---------------------------------------

fetch() {
    local url="$1" output="$2" timeout="${3:-30}"

    if command_exists curl; then
        curl -fsSL --connect-timeout "$timeout" --max-time 120 "$url" -o "$output"
    elif command_exists wget; then
        wget -q --timeout="$timeout" "$url" -O "$output"
    else
        error "Neither curl nor wget found. Please install one of them."
    fi
}

fetch_text() {
    local url="$1" timeout="${2:-30}"

    if command_exists curl; then
        curl -fsSL --connect-timeout "$timeout" --max-time 60 "$url"
    elif command_exists wget; then
        wget -q --timeout="$timeout" -O- "$url"
    else
        error "Neither curl nor wget found. Please install one of them."
    fi
}

get_latest_version() {
    [[ -n "$VERSION" ]] && return

    log "Checking latest version..."

    local response
    response=$(fetch_text "https://api.github.com/repos/${REPO}/releases/latest" 15) || {
        error "Failed to fetch latest release info. Check your internet connection or specify --version."
    }

    # Try jq first, fallback to grep/sed
    if command_exists jq; then
        VERSION=$(echo "$response" | jq -r '.tag_name // empty')
    else
        VERSION=$(echo "$response" | grep -m1 '"tag_name"' | sed -E 's/.*"tag_name"[[:space:]]*:[[:space:]]*"([^"]+)".*/\1/')
    fi

    if [[ -z "$VERSION" ]]; then
        error "Failed to parse latest version from GitHub API response."
    fi

    info "Latest version: $VERSION"
}

# --- Checksum Verification -----------------------------------

verify_checksum() {
    local file="$1" checksums_file="$2"
    local filename expected actual

    if [[ "$SKIP_VERIFY" == "true" ]]; then
        warn "Checksum verification skipped (--no-verify)"
        return 0
    fi

    if [[ ! -f "$checksums_file" ]]; then
        warn "Checksums file not available. Skipping verification."
        return 0
    fi

    filename=$(basename "$file")
    expected=$(grep -E "^[a-fA-F0-9]+[[:space:]]+(\*)?${filename}$" "$checksums_file" 2>/dev/null | awk '{print $1}' | head -1)

    if [[ -z "$expected" ]]; then
        warn "No checksum found for $filename in checksums file."
        return 0
    fi

    # Determine which sha256 tool to use
    if command_exists sha256sum; then
        actual=$(sha256sum "$file" | awk '{print $1}')
    elif command_exists shasum; then
        actual=$(shasum -a 256 "$file" | awk '{print $1}')
    elif command_exists openssl; then
        actual=$(openssl dgst -sha256 "$file" | awk '{print $NF}')
    else
        warn "No SHA256 tool found (sha256sum, shasum, or openssl). Skipping verification."
        return 0
    fi

    if [[ "${expected,,}" != "${actual,,}" ]]; then
        error "Checksum verification FAILED for $filename!
    Expected: $expected
    Actual:   $actual
    The downloaded file may be corrupted or tampered with."
    fi

    info "Checksum verified"
}

# --- Download & Install --------------------------------------

install_binary() {
    local tmp_dir version_num ext archive_name download_url checksums_url binary_path
    local old_dir

    old_dir=$(pwd)
    tmp_dir=$(mktemp -d)
    trap "cd '$old_dir'; rm -rf '$tmp_dir'" EXIT

    version_num="${VERSION#v}"
    ext="tar.gz"
    [[ "$OS" == "windows" ]] && ext="zip"

    archive_name="llm-mux_${version_num}_${OS}_${ARCH}.${ext}"
    download_url="https://github.com/${REPO}/releases/download/${VERSION}/${archive_name}"
    checksums_url="https://github.com/${REPO}/releases/download/${VERSION}/checksums.txt"

    log "Downloading ${BINARY_NAME} ${VERSION} for ${OS}/${ARCH}..."
    info "URL: $download_url"

    fetch "$download_url" "$tmp_dir/$archive_name" || {
        error "Failed to download $archive_name. The version or platform may not be available."
    }

    # Download checksums (optional, don't fail if not available)
    log "Downloading checksums..."
    fetch "$checksums_url" "$tmp_dir/checksums.txt" 2>/dev/null || {
        warn "Checksums file not available for this release."
        touch "$tmp_dir/checksums.txt"
    }

    verify_checksum "$tmp_dir/$archive_name" "$tmp_dir/checksums.txt"

    log "Extracting archive..."
    cd "$tmp_dir"

    if [[ "$ext" == "zip" ]]; then
        if command_exists unzip; then
            unzip -q "$archive_name"
        else
            error "unzip command not found. Please install unzip."
        fi
    else
        tar -xzf "$archive_name"
    fi

    # Find the binary (handle nested directories)
    binary_path=$(find . \( -name "$BINARY_NAME" -o -name "${BINARY_NAME}.exe" \) -type f 2>/dev/null | head -1)

    if [[ -z "$binary_path" ]]; then
        error "Binary '$BINARY_NAME' not found in the archive. Archive contents:"
        ls -la "$tmp_dir"
    fi

    log "Installing to ${INSTALL_DIR}..."

    # Create install directory if it doesn't exist
    if [[ ! -d "$INSTALL_DIR" ]]; then
        if ! mkdir -p "$INSTALL_DIR" 2>/dev/null; then
            sudo mkdir -p "$INSTALL_DIR" || error "Failed to create install directory: $INSTALL_DIR"
        fi
    fi

    # Backup existing binary if present
    local target_binary="$INSTALL_DIR/$BINARY_NAME"
    [[ "$OS" == "windows" ]] && target_binary="$INSTALL_DIR/${BINARY_NAME}.exe"

    if [[ -f "$target_binary" ]]; then
        info "Backing up existing binary..."
        mv "$target_binary" "${target_binary}.bak" 2>/dev/null || true
    fi

    # Install the binary
    if [[ -w "$INSTALL_DIR" ]]; then
        cp "$binary_path" "$INSTALL_DIR/"
        chmod +x "$target_binary"
    else
        sudo cp "$binary_path" "$INSTALL_DIR/"
        sudo chmod +x "$target_binary"
    fi

    cd "$old_dir"
    info "Binary installed: $target_binary"
}

# --- Config --------------------------------------------------

init_config() {
    log "Initializing config and credentials..."

    # --init handles both config creation and management key generation
    # It outputs the actual paths used (platform-specific)
    # This avoids hardcoding paths that differ between Windows/Linux/macOS
    local init_output
    if init_output=$("$INSTALL_DIR/$BINARY_NAME" --init 2>&1); then
        # Display key info parsed from binary output
        local mgmt_key

        # Extract management key (format: "Management key: xxx" or "  xxx")
        if echo "$init_output" | grep -q "Management key:"; then
            mgmt_key=$(echo "$init_output" | grep "Management key:" | sed 's/.*Management key:[[:space:]]*//')
        elif echo "$init_output" | grep -q "Generated management key:\|Regenerated management key:"; then
            mgmt_key=$(echo "$init_output" | grep -A1 "management key:" | tail -1 | sed 's/^[[:space:]]*//')
        fi

        # Extract paths from binary output (format: "Created: /path" or "Location: /path")
        if echo "$init_output" | grep -q "^Created:"; then
            DETECTED_CONFIG_PATH=$(echo "$init_output" | grep "^Created:" | sed 's/Created:[[:space:]]*//')
            info "Config created: $DETECTED_CONFIG_PATH"
        fi

        DETECTED_CRED_PATH=$(echo "$init_output" | grep "^Location:" | tail -1 | sed 's/Location:[[:space:]]*//')

        # Derive config path from credentials path if not explicitly created
        if [[ -z "$DETECTED_CONFIG_PATH" && -n "$DETECTED_CRED_PATH" ]]; then
            DETECTED_CONFIG_PATH="$(dirname "$DETECTED_CRED_PATH")/config.yaml"
        fi

        [[ -n "$mgmt_key" ]] && info "Management key: $mgmt_key"
        [[ -n "$DETECTED_CRED_PATH" ]] && info "Credentials: $DETECTED_CRED_PATH"
    else
        warn "Failed to initialize. Run '$BINARY_NAME --init' manually later."
    fi
}

# --- Service: macOS (launchd) --------------------------------

service_macos_plist() {
    local log_dir="$HOME/.local/var/log"
    cat <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>${SERVICE_NAME}</string>
    <key>ProgramArguments</key>
    <array>
        <string>${INSTALL_DIR}/${BINARY_NAME}</string>
    </array>
    <key>RunAtLoad</key>
    <true/>
    <key>KeepAlive</key>
    <dict>
        <key>SuccessfulExit</key>
        <false/>
    </dict>
    <key>ThrottleInterval</key>
    <integer>5</integer>
    <key>StandardOutPath</key>
    <string>${log_dir}/llm-mux.log</string>
    <key>StandardErrorPath</key>
    <string>${log_dir}/llm-mux.log</string>
    <key>EnvironmentVariables</key>
    <dict>
        <key>PATH</key>
        <string>/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin</string>
        <key>HOME</key>
        <string>${HOME}</string>
    </dict>
    <key>WorkingDirectory</key>
    <string>${HOME}</string>
</dict>
</plist>
EOF
}

service_macos_install() {
    local plist_dir="$HOME/Library/LaunchAgents"
    local plist_path="$plist_dir/${SERVICE_NAME}.plist"
    local log_dir="$HOME/.local/var/log"

    log "Setting up launchd service..."

    # Create directories
    mkdir -p "$plist_dir" "$log_dir"

    # Stop existing service if running
    if launchctl list 2>/dev/null | grep -q "$SERVICE_NAME"; then
        info "Stopping existing service..."
        launchctl bootout "gui/$(id -u)/$SERVICE_NAME" 2>/dev/null || \
        launchctl unload "$plist_path" 2>/dev/null || true
        sleep 1
    fi

    # Write plist file
    service_macos_plist > "$plist_path"

    # Load service using modern API if available, fallback to legacy
    if launchctl bootstrap "gui/$(id -u)" "$plist_path" 2>/dev/null; then
        info "Service loaded (bootstrap)"
    elif launchctl load "$plist_path" 2>/dev/null; then
        info "Service loaded (legacy)"
    else
        warn "Failed to load service. You may need to load it manually:"
        echo "    launchctl load $plist_path"
    fi

    info "Service plist: $plist_path"
    info "Log file: $log_dir/llm-mux.log"
}

service_macos_status() {
    if launchctl list 2>/dev/null | grep -q "$SERVICE_NAME"; then
        echo "running"
    else
        echo "stopped"
    fi
}

# --- Service: Linux (systemd) --------------------------------

service_linux_unit() {
    cat <<EOF
[Unit]
Description=llm-mux - Multi-provider LLM gateway
Documentation=https://github.com/${REPO}
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=${INSTALL_DIR}/${BINARY_NAME}
WorkingDirectory=${HOME}
Restart=on-failure
RestartSec=5
StartLimitBurst=3
StartLimitIntervalSec=60
Environment=HOME=${HOME}
Environment=PATH=/usr/local/bin:/usr/bin:/bin

[Install]
WantedBy=default.target
EOF
}

service_linux_install() {
    local service_dir="$HOME/.config/systemd/user"
    local service_path="$service_dir/llm-mux.service"

    log "Setting up systemd user service..."

    # Check if systemd is available
    if ! command_exists systemctl; then
        warn "systemd not available. Skipping service setup."
        warn "You can run llm-mux manually: $INSTALL_DIR/$BINARY_NAME"
        return
    fi

    # Check if user session is available
    if ! systemctl --user status >/dev/null 2>&1; then
        warn "systemd user session not available. This may happen in containers or SSH sessions."
        warn "You can run llm-mux manually or set up the service later."
        return
    fi

    mkdir -p "$service_dir"
    service_linux_unit > "$service_path"

    # Reload and enable
    systemctl --user daemon-reload

    if systemctl --user enable llm-mux 2>/dev/null; then
        info "Service enabled"
    else
        warn "Failed to enable service"
    fi

    if systemctl --user start llm-mux 2>/dev/null; then
        info "Service started"
    else
        warn "Failed to start service. Check: systemctl --user status llm-mux"
    fi

    info "Service unit: $service_path"

    # Enable lingering so service runs without login
    if command_exists loginctl; then
        if loginctl enable-linger "$(whoami)" 2>/dev/null; then
            info "Lingering enabled (service will run without login)"
        fi
    fi
}

service_linux_status() {
    if systemctl --user is-active llm-mux >/dev/null 2>&1; then
        echo "running"
    else
        echo "stopped"
    fi
}

# --- Service: Generic (init.d / rc.local fallback) -----------

service_generic_install() {
    warn "No supported service manager found (launchd/systemd)."
    warn "You can run llm-mux manually: $INSTALL_DIR/$BINARY_NAME"
    warn "Or add it to your shell profile (~/.bashrc or ~/.zshrc):"
    echo ""
    echo "    # Start llm-mux in background"
    echo "    pgrep -x llm-mux >/dev/null || nohup $INSTALL_DIR/$BINARY_NAME &>/dev/null &"
    echo ""
}

# --- Service: Router -----------------------------------------

setup_service() {
    case "$OS" in
        darwin)
            service_macos_install
            ;;
        linux)
            service_linux_install
            ;;
        freebsd|*)
            service_generic_install
            ;;
    esac
}

# --- Output --------------------------------------------------

print_success() {
    local status="(not installed)"

    if [[ "$SKIP_SERVICE" != "true" ]]; then
        case "$OS" in
            darwin)
                [[ $(service_macos_status) == "running" ]] && status="running" || status="stopped"
                ;;
            linux)
                [[ $(service_linux_status) == "running" ]] && status="running" || status="stopped"
                ;;
        esac
    fi

    # Use detected path or fallback to XDG-compliant path
    local config_dir="${XDG_CONFIG_HOME:-$HOME/.config}/llm-mux"
    local config_display="${DETECTED_CONFIG_PATH:-$config_dir/config.yaml}"

    echo ""
    echo -e "${GREEN}======================================================${NC}"
    echo -e "${GREEN} llm-mux ${VERSION} installed successfully!${NC}"
    echo -e "${GREEN}======================================================${NC}"
    echo ""
    echo "  Binary:  $INSTALL_DIR/$BINARY_NAME"
    echo "  Config:  $config_display"
    [[ "$SKIP_SERVICE" != "true" ]] && echo "  Service: $status"
    echo ""
    echo "Next steps:"
    echo ""
    echo "  1. Login to a provider:"
    echo "     $BINARY_NAME --login              # Gemini"
    echo "     $BINARY_NAME --claude-login       # Claude"
    echo "     $BINARY_NAME --copilot-login      # GitHub Copilot"
    echo "     $BINARY_NAME --codex-login        # OpenAI Codex"
    echo ""

    if [[ "$SKIP_SERVICE" == "true" ]]; then
        echo "  2. Start the server:"
        echo "     $BINARY_NAME"
    else
        echo "  2. Service commands:"
        case "$OS" in
            darwin)
                echo "     launchctl stop $SERVICE_NAME    # Stop"
                echo "     launchctl start $SERVICE_NAME   # Start"
                echo "     tail -f ~/.local/var/log/llm-mux.log  # View logs"
                ;;
            linux)
                echo "     systemctl --user stop llm-mux   # Stop"
                echo "     systemctl --user start llm-mux  # Start"
                echo "     journalctl --user -u llm-mux -f # View logs"
                ;;
        esac
    fi

    echo ""
    echo "  3. Test the API:"
    echo "     curl http://localhost:8317/v1/models"
    echo ""

    # PATH warning
    if [[ ":$PATH:" != *":$INSTALL_DIR:"* ]]; then
        echo -e "${YELLOW}Note:${NC} $INSTALL_DIR is not in your PATH."
        echo ""

        local shell_name shell_rc
        shell_name=$(basename "${SHELL:-/bin/bash}")
        case "$shell_name" in
            zsh)  shell_rc="~/.zshrc" ;;
            fish) shell_rc="~/.config/fish/config.fish" ;;
            *)    shell_rc="~/.bashrc" ;;
        esac

        echo "  Add to PATH by running:"
        if [[ "$shell_name" == "fish" ]]; then
            echo "     fish_add_path $INSTALL_DIR"
        else
            echo "     echo 'export PATH=\"$INSTALL_DIR:\$PATH\"' >> $shell_rc"
            echo "     source $shell_rc"
        fi
        echo ""
    fi
}

# --- Help ----------------------------------------------------

usage() {
    cat <<EOF
llm-mux installer

Downloads and installs llm-mux, a multi-provider LLM gateway.

Usage:
    curl -fsSL https://raw.githubusercontent.com/nghyane/llm-mux/main/install.sh | bash
    curl -fsSL ... | bash -s -- [OPTIONS]

Options:
    --no-service        Skip service setup (install binary only)
    --version VERSION   Install specific version (default: latest)
    --dir DIRECTORY     Install to custom directory
    --no-verify         Skip checksum verification
    --force             Force reinstall even if same version exists
    -h, --help          Show this help

Examples:
    # Default install (binary + service)
    curl -fsSL .../install.sh | bash

    # Binary only, no service
    curl -fsSL .../install.sh | bash -s -- --no-service

    # Specific version
    curl -fsSL .../install.sh | bash -s -- --version v1.0.0

    # Custom install directory
    curl -fsSL .../install.sh | bash -s -- --dir ~/bin

Supported platforms:
    - macOS (Intel & Apple Silicon)
    - Linux (x86_64, arm64, armv7)
    - Windows (via WSL or Git Bash)
    - FreeBSD

Requirements:
    - curl or wget
    - tar (for Linux/macOS) or unzip (for Windows)
EOF
    exit 0
}

# --- Main ----------------------------------------------------

parse_args() {
    while [[ $# -gt 0 ]]; do
        case "$1" in
            --no-service)
                SKIP_SERVICE=true
                shift
                ;;
            --no-verify)
                SKIP_VERIFY=true
                shift
                ;;
            --force)
                FORCE_INSTALL=true
                shift
                ;;
            --version)
                if [[ -z "${2:-}" ]]; then
                    error "--version requires a version argument (e.g., --version v1.0.0)"
                fi
                VERSION="$2"
                shift 2
                ;;
            --dir)
                if [[ -z "${2:-}" ]]; then
                    error "--dir requires a directory argument (e.g., --dir ~/bin)"
                fi
                INSTALL_DIR="$2"
                shift 2
                ;;
            -h|--help)
                usage
                ;;
            -*)
                error "Unknown option: $1. Use --help for usage information."
                ;;
            *)
                error "Unexpected argument: $1. Use --help for usage information."
                ;;
        esac
    done
}

check_existing_install() {
    local existing_binary="$INSTALL_DIR/$BINARY_NAME"

    if [[ -x "$existing_binary" && "$FORCE_INSTALL" != "true" ]]; then
        local existing_version
        existing_version=$("$existing_binary" --version 2>/dev/null | head -1 || echo "unknown")

        if [[ "$existing_version" == *"$VERSION"* ]]; then
            info "llm-mux $VERSION is already installed at $existing_binary"
            info "Use --force to reinstall"
            return 1
        fi
    fi
    return 0
}

main() {
    parse_args "$@"

    echo ""
    log "llm-mux installer"
    echo ""

    check_root
    detect_platform
    info "Platform: $OS/$ARCH"

    detect_install_dir
    info "Install directory: $INSTALL_DIR"

    get_latest_version

    if ! check_existing_install; then
        echo ""
        exit 0
    fi

    install_binary
    init_config

    if [[ "$SKIP_SERVICE" != "true" ]]; then
        setup_service
    fi

    print_success
}

main "$@"
