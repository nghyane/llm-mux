# @sdk/access 开发指引

`github.com/router-for-me/CLIProxyAPI/v6/sdk/access` 包负责代理的入站访问认证。它提供一个轻量的管理器，用于按顺序链接多种凭证校验实现，让服务器在 CLI 运行时内外都能复用相同的访问控制逻辑。

## 引用方式

```go
import (
    sdkaccess "github.com/router-for-me/CLIProxyAPI/v6/sdk/access"
    "github.com/router-for-me/CLIProxyAPI/v6/internal/config"
)
```

通过 `go get github.com/router-for-me/CLIProxyAPI/v6/sdk/access` 添加依赖。

## 管理器生命周期

```go
manager := sdkaccess.NewManager()
providers, err := sdkaccess.BuildProviders(cfg)
if err != nil {
    return err
}
manager.SetProviders(providers)
```

- `NewManager` 创建空管理器。
- `SetProviders` 替换提供者切片并做防御性拷贝。
- `Providers` 返回适合并发读取的快照。
- `BuildProviders` 将 `config.Config` 中的访问配置转换成可运行的提供者。当配置没有显式声明但包含顶层 `api-keys` 时，会自动挂载内建的 `config-api-key` 提供者。

## 认证请求

```go
result, err := manager.Authenticate(ctx, req)
switch {
case err == nil:
    // Authentication succeeded; result carries provider and principal.
case errors.Is(err, sdkaccess.ErrNoCredentials):
    // No recognizable credentials were supplied.
case errors.Is(err, sdkaccess.ErrInvalidCredential):
    // Credentials were present but rejected.
default:
    // Provider surfaced a transport-level failure.
}
```

`Manager.Authenticate` 按配置顺序遍历提供者。遇到成功立即返回，`ErrNotHandled` 会继续尝试下一个；若发现 `ErrNoCredentials` 或 `ErrInvalidCredential`，会在遍历结束后汇总给调用方。

若管理器本身为 `nil` 或尚未注册提供者，调用会返回 `nil, nil`，让调用方无需针对错误做额外分支即可关闭访问控制。

`Result` 提供认证提供者标识、解析出的主体以及可选元数据（例如凭证来源）。

## 配置结构

在 `config.yaml` 的 `auth.providers` 下定义访问提供者：

```yaml
auth:
  providers:
    - name: inline-api
      type: config-api-key
      api-keys:
        - sk-test-123
        - sk-prod-456
```

条目映射到 `config.AccessProvider`：`name` 指定实例名，`type` 选择注册的工厂，`sdk` 可引用第三方模块，`api-keys` 提供内联凭证，`config` 用于传递特定选项。

### 引入外部 SDK 提供者

若要消费其它 Go 模块输出的访问提供者，可在配置里填写 `sdk` 字段并在代码中引入该包，利用其 `init` 注册过程：

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

通过空白标识符导入即可确保 `init` 调用，先于 `BuildProviders` 完成 `sdkaccess.RegisterProvider`。

## 内建提供者

当前 SDK 默认内置：

- `config-api-key`：校验配置中的 API Key。它从 `Authorization: Bearer`、`X-Goog-Api-Key`、`X-Api-Key` 以及查询参数 `?key=` 提取凭证，不匹配时抛出 `ErrInvalidCredential`。

导入第三方包即可通过 `sdkaccess.RegisterProvider` 注册更多类型。

### 元数据与审计

`Result.Metadata` 用于携带提供者特定的上下文信息。内建的 `config-api-key` 会记录凭证来源（`authorization`、`x-goog-api-key`、`x-api-key` 或 `query-key`）。自定义提供者同样可以填充该 Map，以便丰富日志与审计场景。

## 编写自定义提供者

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

自定义提供者需要实现 `Identifier()` 与 `Authenticate()`。在 `init` 中调用 `RegisterProvider` 暴露给配置层，工厂函数既能读取当前条目，也能访问完整根配置。

## 错误语义

- `ErrNoCredentials`：任何提供者都未识别到凭证。
- `ErrInvalidCredential`：至少一个提供者处理了凭证但判定无效。
- `ErrNotHandled`：告诉管理器跳到下一个提供者，不影响最终错误统计。

自定义错误（例如网络异常）会马上冒泡返回。

## 与 cliproxy 集成

使用 `sdk/cliproxy` 构建服务时会自动接入 `@sdk/access`。如果需要扩展内置行为，可传入自定义管理器：

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

服务会复用该管理器处理每一个入站请求，实现与 CLI 二进制一致的访问控制体验。

### 动态热更新提供者

当配置发生变化时，可以重新构建提供者并替换当前列表：

```go
providers, err := sdkaccess.BuildProviders(newCfg)
if err != nil {
    log.Errorf("reload auth providers failed: %v", err)
    return
}
accessManager.SetProviders(providers)
```

这一流程与 `cliproxy.Service.refreshAccessProviders` 和 `api.Server.applyAccessConfig` 保持一致，避免为更新访问策略而重启进程。
