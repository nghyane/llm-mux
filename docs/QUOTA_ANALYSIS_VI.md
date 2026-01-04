# Tóm tắt Phân tích Quota Mechanism

## 🎯 Mục đích
So sánh cơ chế quản lý quota giữa **antigravity-kit** và **llm-mux** để tìm ra các điểm thiếu sót.

---

## 📊 Kết quả Phân tích

### antigravity-kit có gì?

1. **Real-time quota monitoring** ✅
   - Gọi API `fetchAvailableModels` để lấy % quota còn lại cho từng model
   - Biết chính xác: `claude-sonnet-4: 75%`, `gemini-2.5-pro: 23%`
   - Hiển thị thời gian reset quota: "in 3h 45m"

2. **CLI command đẹp** ✅
   ```bash
   agk auth quota
   ```
   - Table với progress bars màu sắc
   - Auto-refresh mỗi 30s
   - Keyboard controls (r=reload, q=quit)

3. **Subscription tier tracking** ✅
   - Biết account là Free hay Premium
   - Hiển thị: `💎 Subscription: premium`

4. **403 Forbidden detection** ✅
   - Phát hiện account bị cấm
   - Hiển thị: `🚫 Status: FORBIDDEN (403)`

### llm-mux có gì?

1. **Quota grouping** ✅
   - Models cùng family share quota (smart!)
   - Khi `claude-sonnet-4` hết quota → block cả family `claude-*`

2. **Auto-recovery** ✅
   - Tự động unblock khi hết retry time
   - O(1) lookup performance

3. **Config management** ✅
   - Settings: `switch-project`, `switch-preview-model`

### llm-mux THIẾU gì?

| Tính năng | antigravity-kit | llm-mux | Impact |
|-----------|-----------------|---------|--------|
| Gọi `fetchAvailableModels` API | ✅ | ❌ | **CRITICAL** |
| Biết % quota còn lại | ✅ | ❌ | **CRITICAL** |
| CLI quota command | ✅ | ❌ | **HIGH** |
| REST API cho quota | ⚠️ | ❌ | **HIGH** |
| Track subscription tier | ✅ | ❌ | **MEDIUM** |
| Visual progress bars | ✅ | ❌ | **LOW** |

---

## ⚠️ Các vấn đề nghiêm trọng

### 1. Không biết quota còn bao nhiêu (CRITICAL)

**Hiện tại:**
```go
// llm-mux chỉ có estimated limit hard-coded
EstimatedLimit: 1_000_000  // Không biết đã dùng bao nhiêu!
```

**User experience:**
- ❌ Không biết còn bao nhiêu quota
- ❌ Bất ngờ gặp 429 error
- ❌ Không thể plan usage

**Antigravity-kit:**
```typescript
// Biết chính xác realtime
{
  "claude-sonnet-4": {
    "percentage": 23,  // Còn 23%!
    "resetTime": "2026-01-04T20:00:00Z"
  }
}
```

### 2. Không có cách xem quota (HIGH)

**Hiện tại:**
```bash
# llm-mux - KHÔNG CÓ COMMAND
$ llm-mux quota list
Error: unknown command "quota"
```

**Antigravity-kit:**
```bash
$ agk auth quota
📊 Quota Status - user@example.com
Model: claude-sonnet-4    ██░░░░░░░░ 23%  Reset: in 3h
Model: gemini-2.5-pro     ████████░░ 87%  Reset: in 3h
```

---

## 💡 Giải pháp

### Phase 1: Implement Quota Fetching (CRITICAL - 2-3 ngày)

**Tạo file mới:**
```
internal/runtime/executor/quota_fetcher.go
```

**Code:**
```go
func FetchAntigravityQuota(ctx context.Context, accessToken, projectID string) (*AuthQuotaStatus, error) {
    // 1. Call API
    url := "https://cloudcode-pa.googleapis.com/v1internal:fetchAvailableModels"
    body := `{"project": "` + projectID + `"}`
    
    // 2. Parse response
    // models["claude-sonnet-4"].quotaInfo.remainingFraction = 0.23
    
    // 3. Return structured data
    return &AuthQuotaStatus{
        Models: []QuotaInfo{
            {
                ModelID: "claude-sonnet-4",
                RemainingPercent: 23,
                ResetTime: time.Parse(...),
            },
        },
    }, nil
}
```

### Phase 2: Add Management API (HIGH - 1-2 ngày)

**Endpoints:**
```
GET /v0/management/quota/status
GET /v0/management/quota/status/antigravity
GET /v0/management/quota/status/antigravity/auth-id-123
```

**Response:**
```json
{
  "quotas": [
    {
      "auth_id": "antigravity-user@example.com.json",
      "provider": "antigravity",
      "email": "user@example.com",
      "subscription_tier": "premium",
      "models": [
        {
          "model_id": "claude-sonnet-4",
          "remaining_percent": 23,
          "reset_time": "2026-01-04T20:00:00Z"
        }
      ]
    }
  ]
}
```

### Phase 3: Add CLI Commands (HIGH - 2-3 ngày)

**Commands:**
```bash
# List quota cho tất cả accounts
llm-mux quota list

# Monitor với auto-refresh
llm-mux quota monitor --interval 30s

# Chi tiết 1 provider
llm-mux quota show --provider antigravity
```

**Output example:**
```
╭─────────────────────────────────────────────────────────────╮
│ Quota Status: user@example.com (Premium)                   │
├─────────────────────────────────────────────────────────────┤
│ Model              │ Quota       │ Reset Time              │
├─────────────────────────────────────────────────────────────┤
│ claude-sonnet-4    │ ██░░░░ 23%  │ in 3h 45m              │
│ gemini-2.5-pro     │ ████░░ 87%  │ in 3h 45m              │
╰─────────────────────────────────────────────────────────────╯
```

### Phase 4: Track Subscription Tier (MEDIUM - 1 ngày)

**Sửa file:**
```go
// internal/api/handlers/management/oauth_api.go
func fetchAntigravityProjectID(...) (projectID, tier string, err error) {
    // Parse response
    tier = response["paidTier"]["id"]  // "premium" or "free"
    
    // Fallback to currentTier
    if tier == "" {
        tier = response["currentTier"]["id"]
    }
    
    return projectID, tier, nil
}

// Lưu vào metadata
metadata["subscription_tier"] = tier
```

### Phase 5: Background Refresh (NICE-TO-HAVE - 2-3 ngày)

**Tính năng:**
- Tự động refresh quota mỗi 5-10 phút
- Cache data để avoid API calls
- Emit events khi quota thấp
- Auto-switch account when quota depleted

---

## 📈 Timeline

```
Week 1:
├─ Day 1-2: Phase 1 (Quota Fetcher)
├─ Day 3-4: Phase 2 (Management API)
└─ Day 5: Testing + Bug fixes

Week 2:
├─ Day 1-2: Phase 3 (CLI Commands)
├─ Day 3: Phase 4 (Subscription Tier)
└─ Day 4-5: Phase 5 (Background Refresh) - Optional
```

**Total: 1-2 tuần** để hoàn thành tất cả

---

## 🎁 Benefits

### Cho Users
✅ **Proactive monitoring** - Biết trước khi hết quota  
✅ **Better planning** - Plan usage dựa trên % còn lại  
✅ **No surprise errors** - Không bất ngờ 429  
✅ **Visual feedback** - Dễ hiểu với progress bars  

### Cho Developers
✅ **REST API** - Integrate vào tools khác  
✅ **Event hooks** - Build advanced features  
✅ **Feature parity** - Ngang level antigravity-kit  

---

## 🚀 Quick Start (Nếu implement)

### Usage sau khi implement:

```bash
# 1. Check quota cho tất cả accounts
llm-mux quota list

# Output:
# Provider     Account               Quota    Status
# antigravity  user1@gmail.com       23%      🔴 LOW
# antigravity  user2@gmail.com       87%      🟢 OK
# gemini       user3@gmail.com       45%      🟡 MEDIUM

# 2. Monitor realtime
llm-mux quota monitor

# 3. API call
curl http://localhost:8317/v0/management/quota/status

# 4. Filter by provider
curl http://localhost:8317/v0/management/quota/status/antigravity
```

---

## 📝 Conclusion

### TL;DR
**llm-mux có nền tảng tốt nhưng thiếu monitoring** - antigravity-kit vượt trội ở real-time visibility.

### Recommendation
👉 **Implement Phase 1-3 (ASAP)** - Các tính năng critical/high priority  
👉 **Phase 4-5 có thể sau** - Nice-to-have features

### ROI
- **Effort:** 1-2 tuần
- **Impact:** 🔥 HUGE - User experience improvement
- **Risk:** ✅ LOW - Không ảnh hưởng code hiện tại

---

**Tài liệu chi tiết:** [QUOTA_ANALYSIS.md](QUOTA_ANALYSIS.md)  
**Ngày:** 2026-01-04  
**Status:** ✅ Analysis Complete
