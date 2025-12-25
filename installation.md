# Installation

## Quick Install

### macOS / Linux

```bash
curl -fsSL https://raw.githubusercontent.com/nghyane/llm-mux/main/install.sh | bash
```

### Windows (PowerShell)

```powershell
irm https://raw.githubusercontent.com/nghyane/llm-mux/main/install.ps1 | iex
```

The installer will:
1. Download the latest binary for your platform
2. Verify checksum integrity
3. Install to a directory in your PATH
4. Initialize configuration
5. Set up a background service (optional)

---

## Install Options

### macOS / Linux

```bash
# Binary only, no background service
curl -fsSL .../install.sh | bash -s -- --no-service

# Install specific version
curl -fsSL .../install.sh | bash -s -- --version v1.2.0

# Custom install directory
curl -fsSL .../install.sh | bash -s -- --dir ~/bin

# Skip checksum verification
curl -fsSL .../install.sh | bash -s -- --no-verify

# Force reinstall
curl -fsSL .../install.sh | bash -s -- --force
```

### Windows (PowerShell)

```powershell
# Binary only, no scheduled task
& { $NoService = $true; irm .../install.ps1 | iex }

# Specific version
& { $Version = "v1.2.0"; irm .../install.ps1 | iex }

# Custom directory
& { $InstallPath = "C:\tools\llm-mux"; irm .../install.ps1 | iex }
```

---

## Install Locations

| Platform | Binary | Config | Logs |
|----------|--------|--------|------|
| **macOS** | `/usr/local/bin/llm-mux` | `~/.config/llm-mux/` | `~/.local/var/log/llm-mux.log` |
| **Linux** | `~/.local/bin/llm-mux` | `~/.config/llm-mux/` | `journalctl --user -u llm-mux` |
| **Windows** | `%LOCALAPPDATA%\Programs\llm-mux\` | `%USERPROFILE%\.config\llm-mux\` | Event Viewer |

---

## Self-Update

Update to the latest version:

```bash
llm-mux --update
```

This checks GitHub for the latest release and installs it if newer.

---

## Manual Installation

### From GitHub Releases

1. Download the appropriate archive from [Releases](https://github.com/nghyane/llm-mux/releases)
2. Extract the binary
3. Move to a directory in your PATH
4. Run `llm-mux --init`

### Build from Source

```bash
git clone https://github.com/nghyane/llm-mux.git
cd llm-mux
go build -o llm-mux ./cmd/server/
./llm-mux --init
```

Requires Go 1.21+.

---

## Uninstall

### macOS

```bash
# Stop and remove service
launchctl bootout gui/$(id -u)/com.llm-mux 2>/dev/null
rm ~/Library/LaunchAgents/com.llm-mux.plist

# Remove binary
rm /usr/local/bin/llm-mux

# Remove config and credentials (optional)
rm -rf ~/.config/llm-mux
```

### Linux

```bash
# Stop and remove service
systemctl --user stop llm-mux
systemctl --user disable llm-mux
rm ~/.config/systemd/user/llm-mux.service
systemctl --user daemon-reload

# Remove binary
rm ~/.local/bin/llm-mux

# Remove config and credentials (optional)
rm -rf ~/.config/llm-mux
```

### Windows (PowerShell)

```powershell
# Stop and remove scheduled task
Unregister-ScheduledTask -TaskName "llm-mux Background Service" -Confirm:$false

# Remove binary
Remove-Item -Recurse "$env:LOCALAPPDATA\Programs\llm-mux"

# Remove from PATH (manual step in System Properties)

# Remove config and credentials (optional)
Remove-Item -Recurse "$env:USERPROFILE\.config\llm-mux"
```

---

## Verify Installation

```bash
# Check version
llm-mux --version

# Initialize (if not done by installer)
llm-mux --init

# Start server
llm-mux

# Test API (in another terminal)
curl http://localhost:8317/v1/models
```
