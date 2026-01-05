# Troubleshooting

## Quick Fixes

| Issue | Solution |
|-------|----------|
| Command not found | Add `~/.local/bin` to PATH |
| Port in use | `lsof -i :8317` then kill or change port |
| No models | Run `llm-mux login antigravity` |
| 503 error | Check auth: `ls ~/.config/llm-mux/auth/` |
| 429 rate limit | Wait or add more accounts |

---

## Installation

**Command not found**
```bash
echo 'export PATH="$HOME/.local/bin:$PATH"' >> ~/.zshrc && source ~/.zshrc
```

**Permission denied**
```bash
curl -fsSL .../install.sh | bash -s -- --dir ~/.local/bin
```

---

## Startup

**Port already in use**
```bash
lsof -i :8317           # Find process
kill $(lsof -t -i :8317)  # Kill it
# Or change port in config.yaml
```

**Config not found**
```bash
llm-mux init
```

---

## Authentication

**No models available**
```bash
llm-mux login antigravity  # or: login claude, login copilot
curl http://localhost:8317/v1/models
```

**OAuth fails**
```bash
llm-mux login claude --no-browser  # Copy URL manually
```

**Token expired**
```bash
llm-mux login antigravity  # Re-authenticate
```

---

## API Errors

| Code | Cause | Fix |
|------|-------|-----|
| 503 | No providers | Login to a provider |
| 429 | Rate limited | Wait or add accounts |
| 404 | Model not found | Check `curl localhost:8317/v1/models` |

**Enable quota switching:**
```yaml
quota-exceeded:
  switch-project: true
```

---

## Service Issues

**macOS - restart service**
```bash
launchctl unload ~/Library/LaunchAgents/com.llm-mux.plist
launchctl load ~/Library/LaunchAgents/com.llm-mux.plist
```

**Linux - enable on boot**
```bash
loginctl enable-linger $(whoami)
systemctl --user enable llm-mux
```

**Windows - check task**
```powershell
Get-ScheduledTaskInfo -TaskName "llm-mux Background Service"
Start-ScheduledTask -TaskName "llm-mux Background Service"
```

---

## Debug

```yaml
debug: true
logging-to-file: true
```

**View logs:**
```bash
tail -50 ~/.local/var/log/llm-mux.log  # macOS
journalctl --user -u llm-mux -n 50     # Linux
```

---

## Performance

**Streaming with Large Context Windows**

llm-mux is optimized for streaming responses with large context windows:
- Buffer pools pre-allocate 4KB for event serialization (reduced reallocations)
- Scanner uses 256KB buffer for efficient streaming (handles large chunks)
- Memory management prevents pool bloat from oversized responses

These optimizations provide:
- Reduced memory allocations during streaming
- Lower GC pressure on long-running instances
- Better throughput for large responses

No configuration needed - optimizations are automatic.

---

## Reset

```bash
mv ~/.config/llm-mux ~/.config/llm-mux.bak
llm-mux init
llm-mux login antigravity
```

---

## Help

- [GitHub Issues](https://github.com/nghyane/llm-mux/issues)
