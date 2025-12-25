---
name: build-deploy
description: Build llm-mux binary and restart the local launchd service
---

## Overview

This skill provides instructions for building and deploying llm-mux locally on macOS.

## Paths

| Item | Path |
|------|------|
| Binary | `/Users/nghiahoang/Dev/CLIProxyAPI-Extended/llm-mux` |
| Global symlink | `/usr/local/bin/llm-mux` |
| LaunchAgent | `~/Library/LaunchAgents/com.nghiahoang.llm-mux.plist` |
| Config | `~/.config/llm-mux/config.yaml` |
| Auth files | `~/.config/llm-mux/auth/` |

## Build Commands

```bash
# Build binary
go build -o llm-mux ./cmd/server

# Restart launchd service
launchctl stop com.nghiahoang.llm-mux && launchctl start com.nghiahoang.llm-mux
```

## Full Deploy Workflow

1. **Build the binary**:
   ```bash
   go build -o llm-mux ./cmd/server
   ```

2. **Restart the service**:
   ```bash
   launchctl stop com.nghiahoang.llm-mux && launchctl start com.nghiahoang.llm-mux
   ```

3. **Verify the service is running**:
   ```bash
   launchctl list | grep llm-mux
   ```

4. **Check logs** (if needed):
   ```bash
   tail -f ~/Library/Logs/llm-mux.log
   ```

## One-liner

```bash
go build -o llm-mux ./cmd/server && launchctl stop com.nghiahoang.llm-mux && launchctl start com.nghiahoang.llm-mux
```

## When to use

- After making code changes that need to be tested locally
- When updating the running llm-mux service
- After pulling new changes from git
