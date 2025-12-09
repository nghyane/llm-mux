# @sdk/access SDK Reference

The `github.com/router-for-me/CLIProxyAPI/v6/sdk/access` package centralizes inbound request authentication for the proxy. It offers a lightweight manager that chains credential providers, so servers can reuse the same access control logic inside or outside the CLI runtime.

## Importing

```go
import (
    sdkaccess "github.com/router-for-me/CLIProxyAPI/v6/sdk/access"
    "github.com/router-for-me/CLIProxyAPI/v6/internal/config"
)
```

Add the module with `go get github.com/router-for-me/CLIProxyAPI/v6/sdk/access`.

## Manager Lifecycle

```go
manager := sdkaccess.NewManager()
providers, err := sdkaccess.BuildProviders(cfg)
if err != nil {
    return err
}
manager.SetProviders(providers)
```

* `NewManager` constructs an empty manager.
* `SetProviders` replaces the provider slice using a defensive copy.
* `Providers` retrieves a snapshot that can be iterated safely from other goroutines.
* `BuildProviders` translates `config.Config` access declarations into runnable providers. When the config omits explicit providers but defines inline API keys, the helper auto-installs the built-in `config-api-key` provider.

## Authenticating Requests

```go
result, err := manager.Authenticate(ctx, req)
switch {
case err == nil:
    // Authentication succeeded; result describes the provider and principal.
case errors.Is(err, sdkaccess.ErrNoCredentials):
    // No recognizable credentials were supplied.
case errors.Is(err, sdkaccess.ErrInvalidCredential):
    // Supplied credentials were present but rejected.
default:
    // Transport-level failure was returned by a provider.
}
```

`Manager.Authenticate` walks the configured providers in order. It returns on the first success, skips providers that surface `ErrNotHandled`, and tracks whether any provider reported `ErrNoCredentials` or `ErrInvalidCredential` for downstream error reporting.

If the manager itself is `nil` or no providers are registered, the call returns `nil, nil`, allowing callers to treat access control as disabled without branching on errors.

Each `Result` includes the provider identifier, the resolved principal, and optional metadata (for example, which header carried the credential).

## Configuration Layout

The manager expects access providers under the `auth.providers` key inside `config.yaml`:

```yaml
auth:
  providers:
    - name: inline-api
      type: config-api-key
      api-keys:
        - sk-test-123
        - sk-prod-456
```

Fields map directly to `config.AccessProvider`: `name` labels the provider, `type` selects the registered factory, `sdk` can name an external module, `api-keys` seeds inline credentials, and `config` passes provider-specific options.

### Loading providers from external SDK modules

To consume a provider shipped in another Go module, point the `sdk` field at the module path and import it for its registration side effect:

```yaml
auth:
  providers:
    - name: partner-auth
      type: partner-token
      sdk: github.com/acme/xplatform/sdk/access/providers/partner
      config:
        region: us-west-2
        audience: cli-proxy
```

```go
import (
    _ "github.com/acme/xplatform/sdk/access/providers/partner" // registers partner-token
    sdkaccess "github.com/router-for-me/CLIProxyAPI/v6/sdk/access"
)
```

The blank identifier import ensures `init` runs so `sdkaccess.RegisterProvider` executes before `BuildProviders` is called.

## Built-in Providers

The SDK ships with one provider out of the box:

- `config-api-key`: Validates API keys declared inline or under top-level `api-keys`. It accepts the key from `Authorization: Bearer`, `X-Goog-Api-Key`, `X-Api-Key`, or the `?key=` query string and reports `ErrInvalidCredential` when no match is found.

Additional providers can be delivered by third-party packages. When a provider package is imported, it registers itself with `sdkaccess.RegisterProvider`.

### Metadata and auditing

`Result.Metadata` carries provider-specific context. The built-in `config-api-key` provider, for example, stores the credential source (`authorization`, `x-goog-api-key`, `x-api-key`, or `query-key`). Populate this map in custom providers to enrich logs and downstream auditing.

## Writing Custom Providers

```go
type customProvider struct{}

func (p *customProvider) Identifier() string { return "my-provider" }

func (p *customProvider) Authenticate(ctx context.Context, r *http.Request) (*sdkaccess.Result, error) {
    token := r.Header.Get("X-Custom")
    if token == "" {
        return nil, sdkaccess.ErrNoCredentials
    }
    if token != "expected" {
        return nil, sdkaccess.ErrInvalidCredential
    }
    return &sdkaccess.Result{
        Provider:  p.Identifier(),
        Principal: "service-user",
        Metadata:  map[string]string{"source": "x-custom"},
    }, nil
}

func init() {
    sdkaccess.RegisterProvider("custom", func(cfg *config.AccessProvider, root *config.Config) (sdkaccess.Provider, error) {
        return &customProvider{}, nil
    })
}
```

A provider must implement `Identifier()` and `Authenticate()`. To expose it to configuration, call `RegisterProvider` inside `init`. Provider factories receive the specific `AccessProvider` block plus the full root configuration for contextual needs.

## Error Semantics

- `ErrNoCredentials`: no credentials were present or recognized by any provider.
- `ErrInvalidCredential`: at least one provider processed the credentials but rejected them.
- `ErrNotHandled`: instructs the manager to fall through to the next provider without affecting aggregate error reporting.

Return custom errors to surface transport failures; they propagate immediately to the caller instead of being masked.

## Integration with cliproxy Service

`sdk/cliproxy` wires `@sdk/access` automatically when you build a CLI service via `cliproxy.NewBuilder`. Supplying a preconfigured manager allows you to extend or override the default providers:

```go
coreCfg, _ := config.LoadConfig("config.yaml")
providers, _ := sdkaccess.BuildProviders(coreCfg)
manager := sdkaccess.NewManager()
manager.SetProviders(providers)

svc, _ := cliproxy.NewBuilder().
  WithConfig(coreCfg).
  WithAccessManager(manager).
  Build()
```

The service reuses the manager for every inbound request, ensuring consistent authentication across embedded deployments and the canonical CLI binary.

### Hot reloading providers

When configuration changes, rebuild providers and swap them into the manager:

```go
providers, err := sdkaccess.BuildProviders(newCfg)
if err != nil {
    log.Errorf("reload auth providers failed: %v", err)
    return
}
accessManager.SetProviders(providers)
```

This mirrors the behaviour in `cliproxy.Service.refreshAccessProviders` and `api.Server.applyAccessConfig`, enabling runtime updates without restarting the process.
