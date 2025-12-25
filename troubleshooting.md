# Troubleshooting

Common issues and solutions for llm-mux.

## Installation Issues

### Command not found after install

**Cause:** Install directory not in PATH.

**Solution:**
```bash
# Check where it was installed
which llm-mux || ls ~/.local/bin/llm-mux

# Add to PATH (bash/zsh)
echo 'export PATH="$HOME/.local/bin:$PATH"' >> ~/.zshrc
source ~/.zshrc

# Or for fish
fish_add_path ~/.local/bin
```

### Permission denied during install

**Cause:** Trying to install to `/usr/local/bin` without sudo.

**Solution:**
```bash
# Install to user directory instead
curl -fsSL .../install.sh | bash -s -- --dir ~/.local/bin
```

---

## Startup Issues

### Port already in use (EADDRINUSE)

**Cause:** Another process is using port 8317.

**Solution:**
```bash
# Find the process
lsof -i :8317  # macOS/Linux
netstat -ano | findstr 8317  # Windows

# Kill it
kill $(lsof -t -i :8317)

# Or change port in config.yaml
port: 9000
```

### Config file not found

**Cause:** Configuration not initialized.

**Solution:**
```bash
llm-mux --init
```

### Service won't start

**Cause:** Various.

**Solution:**
```bash
# Check logs
tail -50 ~/.local/var/log/llm-mux.log  # macOS
journalctl --user -u llm-mux -n 50     # Linux

# Try running manually to see errors
llm-mux
```

---

## Authentication Issues

### No models available

**Cause:** No providers authenticated.

**Solution:**
```bash
# Login to at least one provider
llm-mux --antigravity-login  # Gemini
llm-mux --claude-login       # Claude
llm-mux --copilot-login      # GitHub Copilot

# Verify models
curl http://localhost:8317/v1/models
```

### OAuth login fails

**Cause:** Browser didn't open or authorization failed.

**Solution:**
```bash
# Try with manual browser
llm-mux --claude-login --no-browser
# Copy the URL and open manually
```

### Token expired

**Cause:** OAuth token expired and refresh failed.

**Solution:**
```bash
# Re-authenticate
llm-mux --antigravity-login

# Check token files
ls -la ~/.config/llm-mux/auth/
```

### "Unauthorized" errors from provider

**Cause:** Subscription expired or quota exceeded.

**Solution:**
1. Check your subscription is active
2. Wait for quota reset
3. Add another account for load balancing

---

## API Issues

### 503 Service Unavailable

**Cause:** No providers available for the requested model.

**Solution:**
```bash
# Check available models
curl http://localhost:8317/v1/models

# Verify authentication
ls ~/.config/llm-mux/auth/
```

### 429 Rate Limited

**Cause:** Provider quota exceeded.

**Solution:**
1. Wait for quota reset
2. Add multiple accounts for load balancing
3. Enable quota switching in config:
   ```yaml
   quota-exceeded:
     switch-project: true
   ```

### Model not found

**Cause:** Model name incorrect or not available.

**Solution:**
```bash
# List available models
curl http://localhost:8317/v1/models | jq '.data[].id'

# Use exact model name from list
```

### Streaming not working

**Cause:** Client doesn't support SSE.

**Solution:**
```bash
# Test with curl
curl http://localhost:8317/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{"model": "gemini-2.5-pro", "messages": [{"role": "user", "content": "Hi"}], "stream": true}'
```

---

## Service Issues

### macOS: Service stops after sleep

**Cause:** launchd configuration issue.

**Solution:**
```bash
# Reload service
launchctl unload ~/Library/LaunchAgents/com.llm-mux.plist
launchctl load ~/Library/LaunchAgents/com.llm-mux.plist
```

### Linux: Service not running after reboot

**Cause:** Lingering not enabled.

**Solution:**
```bash
# Enable lingering
loginctl enable-linger $(whoami)

# Re-enable service
systemctl --user enable llm-mux
```

### Windows: Task not starting

**Cause:** Task scheduler issue.

**Solution:**
```powershell
# Check task status
Get-ScheduledTaskInfo -TaskName "llm-mux Background Service"

# Manually start
Start-ScheduledTask -TaskName "llm-mux Background Service"
```

---

## Network Issues

### Connection refused

**Cause:** Server not running or wrong port.

**Solution:**
```bash
# Check if server is running
pgrep llm-mux

# Check which port
grep "port:" ~/.config/llm-mux/config.yaml

# Start server
llm-mux
```

### Timeout errors

**Cause:** Network issues or slow provider response.

**Solution:**
1. Check internet connection
2. Try a different provider
3. Check if proxy is configured correctly:
   ```yaml
   proxy-url: ""  # Clear if not needed
   ```

### SSL/TLS errors

**Cause:** Certificate issues with proxy.

**Solution:**
```bash
# Check proxy configuration
grep proxy-url ~/.config/llm-mux/config.yaml

# Try without proxy
# Remove or comment out proxy-url in config
```

---

## Debug Mode

Enable verbose logging:

```yaml
# In config.yaml
debug: true
logging-to-file: true
```

Or via management API:

```bash
curl -X PUT \
  -H "X-Management-Key: your-key" \
  -H "Content-Type: application/json" \
  -d '{"enabled": true}' \
  http://localhost:8317/v0/management/debug
```

---

## Reset Everything

If all else fails, start fresh:

```bash
# Stop service
launchctl stop com.llm-mux 2>/dev/null  # macOS
systemctl --user stop llm-mux 2>/dev/null  # Linux

# Backup and remove config
mv ~/.config/llm-mux ~/.config/llm-mux.bak

# Re-initialize
llm-mux --init

# Re-authenticate
llm-mux --antigravity-login
```

---

## Get Help

- [GitHub Issues](https://github.com/nghyane/llm-mux/issues)
- Check logs: `~/.local/var/log/llm-mux.log` or `journalctl --user -u llm-mux`
