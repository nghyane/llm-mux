# llm-mux

**AI Gateway** — Turns subscription LLMs (Claude Pro, Copilot, Gemini) into standard APIs.

## Providers

`gemini` | `vertex` | `gemini-cli` | `aistudio` | `antigravity` | `claude` | `codex` | `qwen` | `iflow` | `cline` | `kiro` | `github-copilot`

## Structure

```
llm-mux/
├── cmd/server/          # Entry point (main.go)
├── internal/
│   ├── api/             # HTTP server, routes, handlers
│   │   └── handlers/format/  # API format handlers (OpenAI, Claude, etc.)
│   ├── auth/            # Provider-specific OAuth/token logic
│   │   └── login/       # OAuth authenticators (Claude, Gemini, etc.)
│   ├── cmd/             # CLI commands (*_login.go, run.go)
│   ├── config/          # YAML config parsing, defaults
│   ├── provider/        # Core: Auth, Manager, Request, Response
│   ├── runtime/executor/# Provider execution (see AGENTS.md)
│   ├── service/         # Service orchestration: Builder, Service
│   ├── translator/      # IR translation layer (see AGENTS.md)
│   └── watcher/         # File watchers, hot reload
├── pkg/llmmux/          # Minimal public API for embedding
└── docs/                # User documentation
```

## Where to Look

| Task | Location | Notes |
|------|----------|-------|
| Add new provider | `internal/auth/{provider}/`, `internal/runtime/executor/{provider}_executor.go`, `internal/translator/from_ir/` | Follow existing patterns |
| Add API format | `internal/translator/to_ir/`, `internal/api/handlers/format/` | Parse to IR, add handler |
| Modify streaming | `internal/runtime/executor/stream_*.go` | StreamTranslator, ChunkBufferStrategy |
| Change config | `internal/config/config.go` | Add field, update NewDefaultConfig() |
| Add CLI command | `internal/cmd/` | Follow *_login.go pattern |
| Embed as library | `pkg/llmmux/` | Minimal public API |

## Architecture

**Double-V Translation Model:**
```
Input Format ──► IR (UnifiedChatRequest) ──► Provider Format
                        ▲
                        │
Provider Response ◄── IR (UnifiedEvent) ◄── Output Format
```

- **IR Layer**: `internal/translator/ir/` — canonical request/response types
- **to_ir/**: Parse input formats (OpenAI, Claude, Gemini, Ollama) → IR
- **from_ir/**: Convert IR → provider-specific payloads
- **Executors**: `internal/runtime/executor/*_executor.go` — HTTP clients per provider

## Code Standards

### Go Conventions
- `New` prefix ONLY for constructors returning custom types (not interfaces)
- Unexported helpers: `lowercase`
- Exported APIs: `Uppercase` with doc comments
- Group related constants in structs (not bare `const` blocks)

### Performance
- Pool expensive objects (`sync.Pool` for readers, buffers, builders)
- Tune HTTP transport for high concurrency
- Return pooled objects in `Close()` methods

### Organization
```
config/constants → single source of truth
helpers/factories → reusable functions  
types/interfaces → separate file
```

## Anti-Patterns (Forbidden)

| Pattern | Why | Alternative |
|---------|-----|-------------|
| `New*` returning interface | Violates constructor convention | Return concrete type |
| Ungrouped global constants | Hard to discover/maintain | Group in struct |
| Missing doc on exported API | Breaks godoc | Add `// FuncName ...` |
| Legacy format branching | Increases complexity | Use IR translator |

## Defaults

| Setting | Value |
|---------|-------|
| Port | `8317` |
| Auth dir | `$XDG_CONFIG_HOME/llm-mux/auth` |
| Disable auth | `true` (local-first) |
| Request retry | `3` |
| Max retry interval | `30s` |
| Canonical translator | `true` |

## Commands

```bash
make build    # Build binary
make test     # Run tests
make clean    # Remove artifacts
make release  # Show release options
```

## Release

```bash
./scripts/release.sh status          # Show version
./scripts/release.sh release v2.0.17 # Full release
./scripts/release.sh dev             # Docker dev release
```

## Testing

When testing API changes, load **build-deploy** skill first to rebuild and run the server.
Test with skill: **llm-mux-test**

## Refactoring Workflow

1. **Plan** — Load `sequential-thinking` skill, break into phases
2. **Create Todos** — Track all tasks with TodoWrite
3. **Phase 1** — Create new unified components (single agent)
4. **Phase 2** — Migrate callsites (parallel sub-agents by group)
5. **Phase 3** — Remove legacy code (single agent)
6. **Phase 4** — Build + test verification

### Sub-agent Strategy
- Group independent files for parallel execution
- Each sub-agent: read → edit → verify build
- Report back: files changed, build status

## CI/CD

| Workflow | Trigger | Action |
|----------|---------|--------|
| `docker-image.yml` | `workflow_dispatch` | Build + push to DockerHub |
| `release.yaml` | `workflow_dispatch` | GoReleaser → GitHub Releases |
| `pr-path-guard.yml` | PRs | **Block** changes to `internal/translator/**` |

## Notes

- **PR Guard**: Changes to `internal/translator/` require maintainer approval
- **XDG Compliance**: All user data under `~/.config/llm-mux/`
