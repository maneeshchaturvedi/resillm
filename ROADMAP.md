# resillm Roadmap

## Phase 1: Testing & Stability (Priority: High)

### 1.1 Unit Tests

**Goal:** Achieve >80% code coverage on core components

**Files to create:**
- `internal/resilience/circuitbreaker_test.go`
- `internal/resilience/retry_test.go`
- `internal/router/router_test.go`
- `internal/config/config_test.go`
- `internal/providers/openai_test.go`
- `internal/providers/anthropic_test.go`

**Test scenarios:**

#### Circuit Breaker Tests
- [ ] Starts in closed state
- [ ] Opens after N consecutive failures
- [ ] Rejects requests when open
- [ ] Transitions to half-open after timeout
- [ ] Closes after N successes in half-open
- [ ] Reopens on failure in half-open
- [ ] Thread safety under concurrent access

#### Retry Logic Tests
- [ ] No retry on success
- [ ] Retries on retryable errors (429, 5xx)
- [ ] No retry on non-retryable errors (400, 401, 404)
- [ ] Respects max attempts
- [ ] Exponential backoff timing
- [ ] Jitter is applied
- [ ] Context cancellation stops retries

#### Router Tests
- [ ] Routes to primary provider
- [ ] Falls back on primary failure
- [ ] Skips providers with open circuits
- [ ] Returns error when all providers fail
- [ ] Tracks retry count correctly
- [ ] Records metrics on success/failure

#### Config Tests
- [ ] Parses valid YAML
- [ ] Expands environment variables
- [ ] Applies defaults
- [ ] Validates required fields
- [ ] Rejects invalid configs

#### Provider Translation Tests (Anthropic)
- [ ] Converts OpenAI messages to Anthropic format
- [ ] Extracts system message correctly
- [ ] Handles multimodal content
- [ ] Converts tools/functions
- [ ] Normalizes response to OpenAI format
- [ ] Maps finish reasons correctly

**Estimated effort:** 2-3 days

---

### 1.2 Integration Tests

**Goal:** End-to-end testing with mock providers

**Files to create:**
- `internal/server/server_test.go`
- `test/integration/integration_test.go`
- `test/mocks/provider_mock.go`

**Test scenarios:**

#### HTTP API Tests
- [ ] POST /v1/chat/completions returns valid response
- [ ] Streaming responses work correctly
- [ ] Invalid requests return proper errors
- [ ] Health endpoint returns status
- [ ] Metrics endpoint returns Prometheus format

#### Fallback Integration Tests
- [ ] Primary success - no fallback
- [ ] Primary fails - fallback succeeds
- [ ] All providers fail - returns error
- [ ] Circuit breaker triggers fallback

**Mock server approach:**
```go
// test/mocks/provider_mock.go
type MockProvider struct {
    responses  []MockResponse
    callCount  int
    shouldFail bool
    latency    time.Duration
}
```

**Estimated effort:** 2 days

---

## Phase 2: Budget & Cost Control (Priority: Medium)

### 2.1 Budget Tracking Implementation

**Goal:** Track and enforce spending limits

**Files to create/modify:**
- `internal/budget/tracker.go`
- `internal/budget/store.go`
- `internal/server/handlers.go` (add budget check)

**Features:**

#### Cost Tracking
- [ ] Track cost per request (already calculated per provider)
- [ ] Aggregate by hour/day windows
- [ ] Store in memory with periodic snapshots
- [ ] Expose via `/v1/budget` endpoint

#### Budget Enforcement
- [ ] Check budget before each request
- [ ] Reject requests when budget exceeded (configurable)
- [ ] Allow with warning option
- [ ] Reset hourly/daily counters automatically

#### Alerting (Future)
- [ ] Webhook on threshold reached
- [ ] Slack integration
- [ ] Email alerts

**Data structures:**
```go
type BudgetTracker struct {
    mu           sync.RWMutex
    hourlySpend  float64
    dailySpend   float64
    hourStart    time.Time
    dayStart     time.Time
    config       config.BudgetConfig
}

type BudgetStatus struct {
    CurrentHour  SpendWindow `json:"current_hour"`
    CurrentDay   SpendWindow `json:"current_day"`
    RequestsLeft int         `json:"requests_left_estimate"`
}

type SpendWindow struct {
    Spent     float64   `json:"spent"`
    Limit     float64   `json:"limit"`
    Remaining float64   `json:"remaining"`
    ResetsAt  time.Time `json:"resets_at"`
}
```

**Estimated effort:** 1-2 days

---

## Phase 3: Configuration & Operations (Priority: Medium)

### 3.1 Config Hot-Reload

**Goal:** Reload configuration without restart

**Files to modify:**
- `internal/config/watcher.go` (new)
- `internal/server/server.go`
- `internal/router/router.go`

**Approaches:**

#### Option A: File watcher (Recommended)
- Watch config file for changes using fsnotify
- Validate new config before applying
- Atomically swap config
- Log changes

#### Option B: Admin API
- POST `/admin/reload` triggers reload
- Already stubbed in handlers.go

**Implementation:**
```go
type ConfigWatcher struct {
    path     string
    onChange func(*Config) error
    watcher  *fsnotify.Watcher
}

func (w *ConfigWatcher) Start(ctx context.Context) error {
    // Watch for file changes
    // Debounce rapid changes
    // Validate before applying
    // Call onChange callback
}
```

**What can be reloaded:**
- [x] Model routing (fallback chains)
- [x] Resilience settings (retry, circuit breaker)
- [x] Budget limits
- [x] Logging level
- [ ] Provider credentials (requires reconnection)
- [ ] Server ports (requires restart)

**Estimated effort:** 1 day

---

### 3.2 AWS Bedrock Provider

**Goal:** Support AWS Bedrock as a provider

**Files to create:**
- `internal/providers/bedrock.go`

**Bedrock specifics:**
- Uses AWS SDK (IAM auth, not API key)
- Different request/response format
- Model IDs like `anthropic.claude-3-sonnet-20240229-v1:0`
- Supports Claude, Llama, Titan models

**Request format:**
```go
type BedrockRequest struct {
    ModelId string `json:"-"` // In URL
    Body    struct {
        AnthropicVersion string    `json:"anthropic_version"`
        MaxTokens        int       `json:"max_tokens"`
        System           string    `json:"system,omitempty"`
        Messages         []Message `json:"messages"`
    }
}
```

**Config:**
```yaml
providers:
  bedrock:
    region: us-east-1
    # Uses AWS credentials from environment/IAM
```

**Estimated effort:** 1-2 days

---

## Phase 4: Observability (Priority: Low)

### 4.1 OpenTelemetry Tracing

**Goal:** Distributed tracing support

**Files to create/modify:**
- `internal/telemetry/tracing.go`
- `internal/server/middleware.go`

**Features:**
- [ ] Trace each request end-to-end
- [ ] Span per provider attempt
- [ ] Include retry/fallback info
- [ ] Export to Jaeger/Zipkin/OTLP

**Implementation:**
```go
// Add to each request
span := tracer.Start(ctx, "chat_completion")
defer span.End()

span.SetAttributes(
    attribute.String("model", req.Model),
    attribute.String("provider", provider.Name()),
    attribute.Int("retries", retries),
)
```

**Estimated effort:** 1 day

---

### 4.2 Request/Response Logging

**Goal:** Structured logging for debugging

**Features:**
- [ ] Log request metadata (model, tokens)
- [ ] Optional full request/response logging
- [ ] PII redaction options
- [ ] Log sampling for high volume

**Config:**
```yaml
logging:
  log_requests: true
  log_responses: false
  sample_rate: 0.1  # Log 10% of requests
  redact_pii: true
```

**Estimated effort:** 0.5 days

---

## Phase 5: Advanced Features (Priority: Low)

### 5.1 Admin UI

**Goal:** Simple web UI for monitoring

**Files to create:**
- `internal/server/admin.go`
- `web/` (embedded static files)

**Features:**
- [ ] Provider status dashboard
- [ ] Real-time metrics charts
- [ ] Circuit breaker states
- [ ] Budget usage
- [ ] Recent requests log
- [ ] Config viewer (no secrets)

**Approach:**
- Embed static files in binary
- Use htmx for interactivity
- No build step required

**Estimated effort:** 2-3 days

---

### 5.2 Request Caching

**Goal:** Cache identical requests to reduce costs

**Files to create:**
- `internal/cache/cache.go`

**Features:**
- [ ] Hash request (model + messages + params)
- [ ] TTL-based expiration
- [ ] Memory or Redis backend
- [ ] Cache hit/miss metrics

**Config:**
```yaml
cache:
  enabled: true
  backend: memory  # or redis
  ttl: 1h
  max_size: 1000  # entries
```

**Estimated effort:** 1-2 days

---

### 5.3 Load Balancing Strategies

**Goal:** Distribute load across multiple keys/endpoints

**Current:** Simple fallback chain
**Future:**
- [ ] Round-robin across endpoints
- [ ] Weighted distribution
- [ ] Least-latency routing
- [ ] Cost-optimized routing

**Config:**
```yaml
models:
  gpt-4o:
    load_balance:
      strategy: least_latency  # round_robin, weighted, cost
      endpoints:
        - provider: openai
          api_key: ${OPENAI_KEY_1}
          weight: 2
        - provider: openai
          api_key: ${OPENAI_KEY_2}
          weight: 1
```

**Estimated effort:** 1-2 days

---

## Implementation Order

| Phase | Feature | Priority | Effort | Dependencies |
|-------|---------|----------|--------|--------------|
| 1.1 | Unit Tests | High | 2-3 days | None |
| 1.2 | Integration Tests | High | 2 days | 1.1 |
| 2.1 | Budget Tracking | Medium | 1-2 days | None |
| 3.1 | Config Hot-Reload | Medium | 1 day | None |
| 3.2 | Bedrock Provider | Medium | 1-2 days | None |
| 4.1 | OpenTelemetry | Low | 1 day | None |
| 4.2 | Request Logging | Low | 0.5 days | None |
| 5.1 | Admin UI | Low | 2-3 days | 4.x |
| 5.2 | Request Caching | Low | 1-2 days | None |
| 5.3 | Load Balancing | Low | 1-2 days | None |

**Total estimated effort:** ~2-3 weeks for all features

---

## Recommended Approach

### Week 1: Stability
1. Unit tests for core components
2. Integration tests with mocks
3. Fix any bugs found

### Week 2: Operations
1. Budget tracking
2. Config hot-reload
3. Bedrock provider

### Week 3: Polish
1. OpenTelemetry (if needed)
2. Enhanced logging
3. Documentation

### Future
- Admin UI
- Caching
- Advanced load balancing
