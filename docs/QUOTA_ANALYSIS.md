# Phân tích cơ chế Quota: antigravity-kit vs llm-mux

## Tổng quan

Tài liệu này phân tích chi tiết cơ chế quản lý quota từ [antigravity-kit](https://github.com/duongductrong/antigravity-kit) và so sánh với triển khai hiện tại trong llm-mux để xác định các điểm thiếu sót và cơ hội cải tiến.

**Ngày phân tích:** 2026-01-04

---

## 1. Cơ chế Quota trong antigravity-kit

### 1.1. Kiến trúc tổng quan

Antigravity-kit triển khai một hệ thống quota monitoring hoàn chỉnh với các tính năng:

```typescript
// Cấu trúc dữ liệu quota chính
interface ModelQuota {
  name: string           // Tên model (gemini-2.5-pro, claude-sonnet-4)
  percentage: number     // Phần trăm quota còn lại (0-100)
  resetTime: string      // Thời gian reset quota (ISO 8601)
}

interface QuotaResult {
  projectId?: string           // Google Cloud Project ID
  subscriptionTier?: string    // Tier đăng ký (free/paid)
  models: ModelQuota[]         // Danh sách quota các model
  isForbidden?: boolean        // Tài khoản bị cấm (403)
}
```

### 1.2. Nguồn dữ liệu quota

**API Endpoints sử dụng:**

1. **`loadCodeAssist`** - Lấy thông tin project và subscription tier
   ```
   POST https://cloudcode-pa.googleapis.com/v1internal:loadCodeAssist
   Authorization: Bearer {access_token}
   Body: { metadata: { ideType: "ANTIGRAVITY" } }
   
   Response:
   {
     "cloudaicompanionProject": "project-id-xxx",
     "currentTier": { "id": "free", "quotaTier": "FREE" },
     "paidTier": { "id": "paid", "quotaTier": "PREMIUM" }
   }
   ```

2. **`fetchAvailableModels`** - Lấy thông tin quota chi tiết từng model
   ```
   POST https://cloudcode-pa.googleapis.com/v1internal:fetchAvailableModels
   Authorization: Bearer {access_token}
   Body: { project: "project-id-xxx" }
   
   Response:
   {
     "models": {
       "claude-sonnet-4": {
         "quotaInfo": {
           "remainingFraction": 0.75,    // 75% quota còn lại
           "resetTime": "2026-01-04T20:00:00Z"
         }
       },
       "gemini-2.5-pro": {
         "quotaInfo": {
           "remainingFraction": 0.23,    // 23% quota còn lại
           "resetTime": "2026-01-04T20:00:00Z"
         }
       }
     }
   }
   ```

### 1.3. Tính năng CLI quota monitoring

**Command:** `agk auth quota`

**Chức năng:**
- Hiển thị realtime quota status cho tất cả models
- Auto-refresh theo interval (mặc định 30s)
- Visual progress bars với color-coding:
  - 🟢 Green: >= 70% quota còn lại
  - 🟡 Yellow: 30-69% quota còn lại
  - 🔴 Red: < 30% quota còn lại
- Countdown timer và manual reload (press 'r')
- Hiển thị subscription tier (Free/Premium)
- Xử lý trường hợp forbidden (403)

**UI Example:**
```
  📊 Quota Status - user@example.com
  💎 Subscription: premium

  ┌────────────────────────────┬──────────────┬───────────────┐
  │ Model                      │ Quota        │ Reset Time    │
  ├────────────────────────────┼──────────────┼───────────────┤
  │ claude-sonnet-4            │ ██████████ 75% │ in 3h 45m    │
  │ gemini-2.5-pro             │ ██░░░░░░░░ 23% │ in 3h 45m    │
  └────────────────────────────┴──────────────┴───────────────┘

  ⟳ Auto-refresh in 28s | Press 'r' to reload | Press 'q' to exit
```

### 1.4. Token storage và security

- **macOS Keychain** integration cho secure token storage
- **Fallback** to file-based storage với `--insecure` flag
- **Refresh token** được lưu an toàn và tự động refresh access token khi cần

---

## 2. Cơ chế Quota trong llm-mux (Hiện tại)

### 2.1. Kiến trúc hiện tại

```go
// internal/provider/quota_group.go
type QuotaGroupResolver func(provider, model string) string

// internal/provider/quota_config.go
type ProviderQuotaConfig struct {
    Provider       string
    WindowDuration time.Duration      // 5h cho antigravity
    QuotaType      QuotaType          // Requests hoặc Tokens
    EstimatedLimit int64              // 1M tokens cho antigravity
    GroupResolver  QuotaGroupResolver // Group models by family
    StaggerBucket  time.Duration      // 30m stagger window
    StickyEnabled  bool               // Sticky session cho providers
}
```

### 2.2. Quota group mechanism

**Triển khai:**
- **Quota grouping**: Models cùng family share quota (tất cả Claude models dùng chung, tất cả Gemini models dùng chung)
- **Blocking index**: O(1) lookup để check model có bị blocked không
- **Auto-recovery**: Tự động unblock khi hết thời gian retry

```go
// Example: Antigravity quota resolver
func AntigravityQuotaGroupResolver(provider, model string) string {
    return extractModelFamily(model)  // "claude-sonnet-4" → "claude"
}
```

### 2.3. Management API

**Endpoints hiện có:**
```
GET  /v0/management/quota-exceeded/switch-project
PUT  /v0/management/quota-exceeded/switch-project
GET  /v0/management/quota-exceeded/switch-preview-model
PUT  /v0/management/quota-exceeded/switch-preview-model
```

**Chức năng:**
- Configuration cho behavior khi quota exceeded
- Không có endpoint để xem quota status thực tế
- Không có realtime monitoring

### 2.4. OAuth và project ID fetching

**Đã có:**
- `fetchAntigravityProjectID()` trong `oauth_api.go` - gọi `loadCodeAssist`
- Lưu `project_id` vào auth metadata khi OAuth
- Access token refresh mechanism

**Chưa có:**
- Không gọi `fetchAvailableModels` để lấy quota info
- Không track quota percentage cho từng model
- Không có quota visualization/monitoring

---

## 3. So sánh chi tiết

| Tính năng | antigravity-kit | llm-mux | Ghi chú |
|-----------|-----------------|---------|---------|
| **Quota Data Source** | ✅ `fetchAvailableModels` API | ❌ Không có | llm-mux chỉ dùng `loadCodeAssist` |
| **Real-time Quota Info** | ✅ Per-model percentage + reset time | ❌ | llm-mux chỉ có estimated limits |
| **CLI Monitoring** | ✅ `agk auth quota` với UI | ❌ | llm-mux không có quota command |
| **Management API** | ❌ | ⚠️ Partial | Chỉ có config endpoints |
| **Quota Grouping** | ✅ By model family | ✅ Có tương đương | llm-mux implementation tốt |
| **Auto-refresh** | ✅ Configurable interval | ❌ | llm-mux không monitor |
| **Subscription Tier** | ✅ Hiển thị Free/Premium | ❌ | llm-mux không track |
| **403 Handling** | ✅ Detect forbidden accounts | ⚠️ Generic error | llm-mux cần cải thiện |
| **Visual Feedback** | ✅ Progress bars, colors | ❌ | llm-mux không có UI |
| **Quota Recovery** | ✅ Automatic với countdown | ✅ Có trong code | Cả hai đều có |

---

## 4. Các điểm thiếu sót trong llm-mux

### 4.1. ❌ CRITICAL: Thiếu quota monitoring API

**Vấn đề:**
- llm-mux **KHÔNG** gọi API `fetchAvailableModels` để lấy quota thực tế
- Chỉ dựa vào `EstimatedLimit` (hard-coded: 1M tokens)
- Không biết quota còn bao nhiêu % cho đến khi gặp 429 error

**Impact:**
- User không thể proactively monitor quota
- Không thể plan usage effectively
- Sudden 429 errors without warning

### 4.2. ❌ HIGH: Thiếu CLI quota command

**Vấn đề:**
- Không có command tương đương `agk auth quota`
- Không có cách nào để user xem quota status

**Đề xuất:**
```bash
llm-mux quota list                    # List all accounts with quota
llm-mux quota show --provider antigravity  # Show specific provider
llm-mux quota monitor --interval 30s  # Monitor với auto-refresh
```

### 4.3. ⚠️ MEDIUM: Thiếu Management API endpoints

**Vấn đề:**
- Không có REST API để query quota
- Web UI không thể hiển thị quota status

**Đề xuất endpoints:**
```
GET  /v0/management/quota/status                      # All providers
GET  /v0/management/quota/status/:provider           # Specific provider
GET  /v0/management/quota/status/:provider/:authId   # Specific auth
POST /v0/management/quota/refresh/:provider/:authId  # Force refresh
```

### 4.4. ⚠️ MEDIUM: Không track subscription tier

**Vấn đề:**
- `loadCodeAssist` response có `currentTier` và `paidTier` nhưng không parse
- Không lưu vào auth metadata
- Không thể distinguish Free vs Premium accounts

**Impact:**
- Không thể áp dụng different quota policies cho Free/Premium
- Không thể show tier info to users

### 4.5. ℹ️ LOW: Thiếu quota visualization

**Vấn đề:**
- Không có visual feedback (progress bars, colors)
- Chỉ có text logs

**Đề xuất:**
- CLI: Color-coded output, progress bars
- API: Include percentage và visualization data

---

## 5. Đề xuất cải tiến

### 5.1. Phase 1: Core Quota Fetching (CRITICAL)

**File changes:**
```
internal/runtime/executor/quota_fetcher.go          [NEW]
internal/provider/quota_manager.go                   [ENHANCE]
internal/provider/types.go                          [ADD TYPES]
```

**Implementations:**

```go
// internal/provider/types.go
type QuotaInfo struct {
    ModelID          string    `json:"model_id"`
    RemainingFraction float64   `json:"remaining_fraction"`  // 0.0 - 1.0
    RemainingPercent int       `json:"remaining_percent"`   // 0 - 100
    ResetTime        time.Time `json:"reset_time"`
    LastUpdated      time.Time `json:"last_updated"`
}

type AuthQuotaStatus struct {
    AuthID           string      `json:"auth_id"`
    Provider         string      `json:"provider"`
    Email            string      `json:"email,omitempty"`
    ProjectID        string      `json:"project_id,omitempty"`
    SubscriptionTier string      `json:"subscription_tier,omitempty"`
    Models           []QuotaInfo `json:"models"`
    IsForbidden      bool        `json:"is_forbidden"`
    LastChecked      time.Time   `json:"last_checked"`
}

// internal/runtime/executor/quota_fetcher.go
func FetchAntigravityQuota(ctx context.Context, accessToken, projectID string) (*provider.AuthQuotaStatus, error) {
    // 1. Call fetchAvailableModels
    // 2. Parse response
    // 3. Return QuotaInfo for each model
}
```

### 5.2. Phase 2: Management API (HIGH)

**File changes:**
```
internal/api/handlers/management/quota_status.go    [NEW]
internal/api/management.go                          [ADD ROUTES]
```

**API Implementation:**

```go
// GET /v0/management/quota/status
func (h *Handler) GetQuotaStatus(c *gin.Context) {
    statuses := []provider.AuthQuotaStatus{}
    
    for _, auth := range h.manager.ListAuths() {
        if auth.Provider != "antigravity" {
            continue
        }
        
        status, err := h.fetchQuotaForAuth(auth)
        if err != nil {
            log.Warnf("Failed to fetch quota for %s: %v", auth.ID, err)
            continue
        }
        
        statuses = append(statuses, *status)
    }
    
    c.JSON(200, gin.H{"quotas": statuses})
}
```

### 5.3. Phase 3: CLI Commands (HIGH)

**File changes:**
```
internal/cli/quota.go                               [NEW]
internal/cli/root.go                                [ADD COMMAND]
```

**CLI Implementation:**

```go
// llm-mux quota list
var quotaListCmd = &cobra.Command{
    Use:   "list",
    Short: "List quota status for all accounts",
    Run: func(cmd *cobra.Command, args []string) {
        // Call management API
        // Display table with quota info
    },
}

// llm-mux quota monitor
var quotaMonitorCmd = &cobra.Command{
    Use:   "monitor",
    Short: "Monitor quota with auto-refresh",
    Run: func(cmd *cobra.Command, args []string) {
        // Similar to antigravity-kit implementation
        // Clear screen, refresh periodically
        // Show progress bars
    },
}
```

### 5.4. Phase 4: Subscription Tier Tracking (MEDIUM)

**Changes:**

```go
// internal/api/handlers/management/oauth_api.go
func fetchAntigravityProjectID(...) (projectID, tier string, err error) {
    // Parse currentTier and paidTier from response
    // Return both projectID and subscriptionTier
}

// Save tier to auth metadata
metadata["subscription_tier"] = tier  // "free" or "premium"
```

### 5.5. Phase 5: Enhanced Quota Manager (MEDIUM)

**Features:**
- Background quota refresh every 5-10 minutes
- Cache quota data để avoid API calls
- Emit events khi quota thấp (< 30%)
- Auto-switch accounts when quota depleted

```go
type QuotaManager struct {
    cache       map[string]*AuthQuotaStatus
    cacheMu     sync.RWMutex
    refreshInterval time.Duration
    lowThreshold    float64  // 0.3 = 30%
    hooks       []QuotaHook
}

type QuotaHook interface {
    OnQuotaLow(auth *Auth, quota *QuotaInfo)
    OnQuotaExhausted(auth *Auth)
    OnQuotaRecovered(auth *Auth)
}
```

---

## 6. Implementation Priority

### Must Have (P0)
1. ✅ Implement `FetchAntigravityQuota()` - Gọi `fetchAvailableModels`
2. ✅ Add quota types (`QuotaInfo`, `AuthQuotaStatus`)
3. ✅ Management API: `GET /v0/management/quota/status`

### Should Have (P1)
4. ✅ CLI: `llm-mux quota list`
5. ✅ CLI: `llm-mux quota monitor`
6. ✅ Track subscription tier in auth metadata
7. ✅ Parse tier from `loadCodeAssist` response

### Nice to Have (P2)
8. ⭕ Background quota refresher
9. ⭕ Quota event hooks
10. ⭕ Auto-switch on quota exceeded
11. ⭕ Visual progress bars in CLI

---

## 7. Migration Path

### Step 1: Add types (No breaking changes)
```bash
git checkout -b feature/quota-types
# Add types to internal/provider/types.go
# Run tests
git commit -m "Add quota info types"
```

### Step 2: Implement fetcher
```bash
git checkout -b feature/quota-fetcher
# Add internal/runtime/executor/quota_fetcher.go
# Add tests
git commit -m "Implement antigravity quota fetcher"
```

### Step 3: Management API
```bash
git checkout -b feature/quota-api
# Add internal/api/handlers/management/quota_status.go
# Update routes
# Test with curl
git commit -m "Add quota status API endpoints"
```

### Step 4: CLI commands
```bash
git checkout -b feature/quota-cli
# Add internal/cli/quota.go
# Test commands
git commit -m "Add quota CLI commands"
```

---

## 8. Testing Strategy

### Unit Tests
```go
// internal/runtime/executor/quota_fetcher_test.go
func TestFetchAntigravityQuota(t *testing.T) {
    // Mock HTTP responses
    // Test parsing
    // Test error handling
}
```

### Integration Tests
```bash
# Start server
llm-mux serve

# Test API
curl http://localhost:8317/v0/management/quota/status

# Test CLI
llm-mux quota list
llm-mux quota monitor --interval 5s
```

### Manual Testing
1. Add antigravity account with OAuth
2. Check quota appears in API response
3. Verify CLI displays correct information
4. Test auto-refresh functionality
5. Verify subscription tier is tracked

---

## 9. Security Considerations

### Access Token Security
- ✅ Access tokens chỉ dùng in-memory
- ✅ Refresh tokens encrypted at rest
- ⚠️ Quota API calls expose access token to Google - OK vì chính thức

### API Rate Limiting
- `fetchAvailableModels` có rate limit từ Google
- **Recommendation**: Cache quota data, refresh max 1 time/minute
- Background refresh: 5-10 minutes interval

### Authorization
- Management API endpoints cần authentication
- CLI commands cần access to auth files
- Quota data không sensitive nhưng cần protect

---

## 10. Kết luận

### Summary
llm-mux có **nền tảng quota management tốt** (grouping, recovery, blocking) nhưng **thiếu real-time quota monitoring** - tính năng quan trọng nhất của antigravity-kit.

### Key Gaps
1. ❌ **Không gọi `fetchAvailableModels` API** → Không biết quota thực tế
2. ❌ **Không có quota monitoring UI/CLI** → User blind về quota status
3. ⚠️ **Không track subscription tier** → Không distinguish Free/Premium
4. ⚠️ **Không có Management API cho quota** → Không thể integrate vào tools

### Recommended Action
**Implement Phase 1-3 (P0 + P1)** để đạt feature parity với antigravity-kit:
- Core quota fetching (2-3 days)
- Management API (1-2 days)
- CLI commands (2-3 days)

**Total effort: ~1-2 weeks** for complete implementation.

### Benefits
- ✅ Proactive quota monitoring thay vì reactive error handling
- ✅ Better user experience với visual feedback
- ✅ API integration cho external tools
- ✅ Feature parity với antigravity-kit
- ✅ Foundation cho advanced features (auto-switch, hooks)

---

**Document Version:** 1.0  
**Last Updated:** 2026-01-04  
**Author:** Sisyphus (AI Analysis)  
**Status:** ✅ Complete Analysis
