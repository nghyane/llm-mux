# Service Management

The installer sets up llm-mux as a background service.

## macOS (launchd)

**Location:** `~/Library/LaunchAgents/com.llm-mux.plist`

```bash
launchctl start com.llm-mux     # Start
launchctl stop com.llm-mux      # Stop
launchctl list | grep llm-mux   # Status
tail -f ~/.local/var/log/llm-mux.log  # Logs

# Enable/disable auto-start
launchctl load ~/Library/LaunchAgents/com.llm-mux.plist
launchctl unload ~/Library/LaunchAgents/com.llm-mux.plist
```

---

## Linux (systemd)

**Location:** `~/.config/systemd/user/llm-mux.service`

```bash
systemctl --user start llm-mux    # Start
systemctl --user stop llm-mux     # Stop
systemctl --user status llm-mux   # Status
journalctl --user -u llm-mux -f   # Logs

# Enable/disable auto-start
systemctl --user enable llm-mux
systemctl --user disable llm-mux

# Run without login (important!)
loginctl enable-linger $(whoami)
```

---

## Windows (Scheduled Task)

**Task:** `llm-mux Background Service`

```powershell
Start-ScheduledTask -TaskName "llm-mux Background Service"
Stop-ScheduledTask -TaskName "llm-mux Background Service"
Get-ScheduledTask -TaskName "llm-mux Background Service" | Select-Object State

# Enable/disable
Enable-ScheduledTask -TaskName "llm-mux Background Service"
Disable-ScheduledTask -TaskName "llm-mux Background Service"

# Remove
Unregister-ScheduledTask -TaskName "llm-mux Background Service" -Confirm:$false
```

---

## Manual Setup

If installed with `--no-service`:

### macOS

```bash
mkdir -p ~/Library/LaunchAgents ~/.local/var/log
cat > ~/Library/LaunchAgents/com.llm-mux.plist << 'EOF'
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key><string>com.llm-mux</string>
    <key>ProgramArguments</key><array><string>/usr/local/bin/llm-mux</string></array>
    <key>RunAtLoad</key><true/>
    <key>KeepAlive</key><dict><key>SuccessfulExit</key><false/></dict>
    <key>StandardOutPath</key><string>~/.local/var/log/llm-mux.log</string>
    <key>StandardErrorPath</key><string>~/.local/var/log/llm-mux.log</string>
</dict>
</plist>
EOF
launchctl load ~/Library/LaunchAgents/com.llm-mux.plist
```

### Linux

```bash
mkdir -p ~/.config/systemd/user
cat > ~/.config/systemd/user/llm-mux.service << 'EOF'
[Unit]
Description=llm-mux
After=network-online.target

[Service]
ExecStart=%h/.local/bin/llm-mux
Restart=on-failure

[Install]
WantedBy=default.target
EOF
systemctl --user daemon-reload && systemctl --user enable --now llm-mux
```

### Windows

```powershell
$Action = New-ScheduledTaskAction -Execute "$env:LOCALAPPDATA\Programs\llm-mux\llm-mux.exe"
$Trigger = New-ScheduledTaskTrigger -AtLogon
Register-ScheduledTask -TaskName "llm-mux Background Service" -Action $Action -Trigger $Trigger
Start-ScheduledTask -TaskName "llm-mux Background Service"
```

---

## Run Manually

```bash
llm-mux                    # Foreground
nohup llm-mux > /tmp/llm-mux.log 2>&1 &  # Background
```

See [Troubleshooting](troubleshooting.md#service-issues) for service issues.
