# Service Management

The installer sets up llm-mux as a background service that starts automatically on login.

## macOS (launchd)

### Service Location

```
~/Library/LaunchAgents/com.llm-mux.plist
```

### Commands

```bash
# Start service
launchctl start com.llm-mux

# Stop service
launchctl stop com.llm-mux

# Restart service
launchctl stop com.llm-mux && launchctl start com.llm-mux

# Check status
launchctl list | grep llm-mux

# View logs
tail -f ~/.local/var/log/llm-mux.log

# Disable auto-start
launchctl unload ~/Library/LaunchAgents/com.llm-mux.plist

# Enable auto-start
launchctl load ~/Library/LaunchAgents/com.llm-mux.plist
```

### Manual Service Setup

If you installed with `--no-service`, create the service manually:

```bash
mkdir -p ~/Library/LaunchAgents ~/.local/var/log

cat > ~/Library/LaunchAgents/com.llm-mux.plist << 'EOF'
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>com.llm-mux</string>
    <key>ProgramArguments</key>
    <array>
        <string>/usr/local/bin/llm-mux</string>
    </array>
    <key>RunAtLoad</key>
    <true/>
    <key>KeepAlive</key>
    <dict>
        <key>SuccessfulExit</key>
        <false/>
    </dict>
    <key>StandardOutPath</key>
    <string>~/.local/var/log/llm-mux.log</string>
    <key>StandardErrorPath</key>
    <string>~/.local/var/log/llm-mux.log</string>
</dict>
</plist>
EOF

launchctl load ~/Library/LaunchAgents/com.llm-mux.plist
```

---

## Linux (systemd)

### Service Location

```
~/.config/systemd/user/llm-mux.service
```

### Commands

```bash
# Start service
systemctl --user start llm-mux

# Stop service
systemctl --user stop llm-mux

# Restart service
systemctl --user restart llm-mux

# Check status
systemctl --user status llm-mux

# View logs
journalctl --user -u llm-mux -f

# View recent logs
journalctl --user -u llm-mux --since "1 hour ago"

# Disable auto-start
systemctl --user disable llm-mux

# Enable auto-start
systemctl --user enable llm-mux
```

### Enable Lingering

To run the service without being logged in:

```bash
loginctl enable-linger $(whoami)
```

### Manual Service Setup

If you installed with `--no-service`:

```bash
mkdir -p ~/.config/systemd/user

cat > ~/.config/systemd/user/llm-mux.service << 'EOF'
[Unit]
Description=llm-mux - Multi-provider LLM gateway
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=%h/.local/bin/llm-mux
Restart=on-failure
RestartSec=5

[Install]
WantedBy=default.target
EOF

systemctl --user daemon-reload
systemctl --user enable llm-mux
systemctl --user start llm-mux
```

---

## Windows (Scheduled Task)

### Task Name

```
llm-mux Background Service
```

### Commands (PowerShell)

```powershell
# Start service
Start-ScheduledTask -TaskName "llm-mux Background Service"

# Stop service
Stop-ScheduledTask -TaskName "llm-mux Background Service"

# Check status
Get-ScheduledTask -TaskName "llm-mux Background Service" | Select-Object State

# Get detailed info
Get-ScheduledTaskInfo -TaskName "llm-mux Background Service"

# Disable auto-start
Disable-ScheduledTask -TaskName "llm-mux Background Service"

# Enable auto-start
Enable-ScheduledTask -TaskName "llm-mux Background Service"

# Remove task
Unregister-ScheduledTask -TaskName "llm-mux Background Service" -Confirm:$false
```

### Manual Task Setup

If you installed with `-NoService`:

```powershell
$Action = New-ScheduledTaskAction -Execute "$env:LOCALAPPDATA\Programs\llm-mux\llm-mux.exe"
$Trigger = New-ScheduledTaskTrigger -AtLogon
$Settings = New-ScheduledTaskSettingsSet -AllowStartIfOnBatteries -DontStopIfGoingOnBatteries -RestartCount 3 -RestartInterval (New-TimeSpan -Minutes 1)

Register-ScheduledTask -TaskName "llm-mux Background Service" -Action $Action -Trigger $Trigger -Settings $Settings
Start-ScheduledTask -TaskName "llm-mux Background Service"
```

---

## Running Manually

Instead of using a service, you can run llm-mux manually:

```bash
# Foreground (see logs directly)
llm-mux

# Background
nohup llm-mux > ~/.local/var/log/llm-mux.log 2>&1 &

# With screen/tmux
screen -S llm-mux llm-mux
# Detach: Ctrl+A, D
# Reattach: screen -r llm-mux
```

---

## Troubleshooting Services

### Service won't start

1. Check if port is in use:
   ```bash
   lsof -i :8317  # macOS/Linux
   netstat -an | findstr 8317  # Windows
   ```

2. Check logs for errors:
   ```bash
   # macOS
   tail -50 ~/.local/var/log/llm-mux.log
   
   # Linux
   journalctl --user -u llm-mux -n 50
   ```

3. Verify binary exists:
   ```bash
   which llm-mux
   llm-mux --version
   ```

### Service starts but API not responding

1. Check if process is running:
   ```bash
   pgrep -l llm-mux
   ```

2. Test the API:
   ```bash
   curl -v http://localhost:8317/v1/models
   ```

3. Check if authentication is complete:
   ```bash
   ls ~/.config/llm-mux/auth/
   ```
