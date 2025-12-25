---
name: build-deploy
description: Build llm-mux binary and run locally for development/debugging
---

## Overview

Build and run llm-mux locally for development. For production, use `install.sh` or Docker.

## Paths

| Item | Path |
|------|------|
| Binary | `/Users/nghiahoang/Dev/CLIProxyAPI-Extended/llm-mux` |
| Config | `~/.config/llm-mux/config.yaml` |
| Auth files | `~/.config/llm-mux/auth/` |

## Build & Run

```bash
pkill -f llm-mux; go build -o llm-mux ./cmd/server && ./llm-mux
```

Press `Ctrl+C` to stop.

## Enable Debug Logging

Set in `~/.config/llm-mux/config.yaml`:
```yaml
debug: true
```

## When to use

- Developing new features
- Troubleshooting issues
- Checking logs in real-time
