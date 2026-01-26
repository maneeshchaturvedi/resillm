# resillm

**A lightweight, language-agnostic LLM resilience proxy that makes your AI applications production-ready.**

Add automatic retries, fallbacks, circuit breakers, budget controls, and observability to any LLM application — without changing a single line of code.

[![Go Version](https://img.shields.io/badge/Go-1.21+-blue.svg)](https://golang.org)
[![License](https://img.shields.io/badge/License-MIT-green.svg)](LICENSE)
[![PRs Welcome](https://img.shields.io/badge/PRs-welcome-brightgreen.svg)](CONTRIBUTING.md)

---

## The Problem

Building production LLM applications is hard. You need to handle:

- **Provider outages** — Your LLM provider goes down, your app goes down
- **Rate limits** — Hit 429 errors during peak traffic
- **Cost explosions** — One bad prompt loop drains your budget
- **Vendor lock-in** — Switching providers means rewriting code
- **Observability gaps** — No idea which models cost what or fail when

Most teams solve this by writing custom retry logic, fallback handlers, and monitoring — scattered across their codebase. Every LLM call becomes wrapped in try-catch blocks and provider-specific code.

## The Solution

**resillm** is a transparent proxy that sits between your application and LLM providers. Your app talks to resillm using the standard OpenAI API format, and resillm handles everything else:

```
┌─────────────┐      ┌──────────┐      ┌─────────────┐
│   Your App  │ ───▶ │ resillm  │ ───▶ │   OpenAI    │
│  (any lang) │      │  proxy   │  │   ├─────────────┤
└─────────────┘      └──────────┘  └─▶ │  Anthropic  │
                          │            ├─────────────┤
                          │            │    Azure    │
                          │            ├─────────────┤
                          └──────────▶ │   Ollama    │
                                       └─────────────┘
```

**Zero code changes required.** Just change your base URL.

---

## Features

### Core Resilience
- **Automatic Retries** — Exponential backoff with jitter for transient failures
- **Provider Fallbacks** — Seamlessly switch to backup providers when primary fails
- **Circuit Breakers** — Prevent cascade failures, fast-fail when providers are unhealthy
- **Request Timeouts** — Configurable connect and request timeouts

### Cost Control
- **Budget Tracking** — Real-time hourly and daily spend monitoring
- **Budget Enforcement** — Reject or warn when limits are exceeded
- **Per-Request Costs** — Track exactly what each request costs
- **Cost Alerts** — Configurable thresholds for budget warnings

### Observability
- **Prometheus Metrics** — Request counts, latencies, token usage, costs, circuit states
- **Structured Logging** — JSON logs with request tracing
- **Response Metadata** — Every response includes provider, latency, cost, retry info
- **Health Endpoints** — Provider status and circuit breaker states

### Operations
- **Hot Reload** — Update configuration without restarts
- **Multi-Provider** — OpenAI, Anthropic, Azure OpenAI, Ollama (Bedrock coming soon)
- **Format Translation** — Automatic request/response format conversion between providers
- **Docker Ready** — Single container deployment with health checks

---

## Quick Start

### 1. Create Configuration

```yaml
# config.yaml
providers:
  openai:
    api_key: ${OPENAI_API_KEY}
  anthropic:
    api_key: ${ANTHROPIC_API_KEY}

models:
  gpt-4o:
    primary:
      provider: openai
      model: gpt-4o
    fallbacks:
      - provider: anthropic
        model: claude-sonnet-4-20250514

resilience:
  retry:
    max_attempts: 3
    initial_backoff: 100ms
  circuit_breaker:
    failure_threshold: 5
    timeout: 30s

budget:
  enabled: true
  max_cost_per_hour: 10.00
  max_cost_per_day: 100.00
  action_on_exceeded: allow_with_warning
```

### 2. Run

```bash
# With Docker
docker run -p 8080:8080 -v $(pwd)/config.yaml:/config.yaml \
  -e OPENAI_API_KEY=$OPENAI_API_KEY \
  -e ANTHROPIC_API_KEY=$ANTHROPIC_API_KEY \
  resillm/resillm

# Or build from source
go build -o resillm ./cmd/resillm
./resillm --config config.yaml
```

### Complete Configuration Example

Here's a production-ready configuration with all available options:

```yaml
# config.yaml - Complete production configuration
server:
  host: "0.0.0.0"
  port: 8080
  metrics_port: 9090
  admin_api_key: ${RESILLM_ADMIN_KEY}  # Required for /admin/* endpoints
  rate_limit:
    enabled: true
    requests_per_second: 10
    burst: 20

providers:
  openai:
    api_key: ${OPENAI_API_KEY}
    base_url: "https://api.openai.com/v1"  # Optional, this is the default
  anthropic:
    api_key: ${ANTHROPIC_API_KEY}
  azure-openai:
    api_key: ${AZURE_OPENAI_API_KEY}
    base_url: "https://your-resource.openai.azure.com"
    api_version: "2024-02-15-preview"
  ollama:
    base_url: "http://localhost:11434"

models:
  gpt-4o:
    primary:
      provider: openai
      model: gpt-4o
    fallbacks:
      - provider: anthropic
        model: claude-sonnet-4-20250514
  claude-sonnet:
    primary:
      provider: anthropic
      model: claude-sonnet-4-20250514
  local:
    primary:
      provider: ollama
      model: llama3.2

resilience:
  retry:
    max_attempts: 3
    initial_backoff: 100ms
    max_backoff: 10s
    backoff_multiplier: 2.0
    retryable_errors: [429, 500, 502, 503, 504]
  circuit_breaker:
    failure_threshold: 5
    success_threshold: 3
    timeout: 30s
    half_open_max_requests: 3
  timeout:
    connect: 5s
    request: 120s

budget:
  enabled: true
  max_cost_per_hour: 10.00
  max_cost_per_day: 100.00
  alert_threshold: 0.8
  action_on_exceeded: reject  # or "allow_with_warning"

metrics:
  enabled: true
  prometheus:
    enabled: true
    path: /metrics

logging:
  level: info      # debug, info, warn, error
  format: json     # json or text
  log_requests: true
  log_responses: false
```

### 3. Point Your App at the Proxy

**Python:**
```python
from openai import OpenAI

client = OpenAI(
    base_url="http://localhost:8080/v1",
    api_key="not-needed"  # Proxy handles authentication
)

response = client.chat.completions.create(
    model="gpt-4o",  # Routes according to your config
    messages=[{"role": "user", "content": "Hello!"}]
)
```

**Node.js:**
```javascript
import OpenAI from 'openai';

const client = new OpenAI({
  baseURL: 'http://localhost:8080/v1',
  apiKey: 'not-needed',
});

const response = await client.chat.completions.create({
  model: 'gpt-4o',
  messages: [{ role: 'user', content: 'Hello!' }],
});
```

**Any Language:** Just set your OpenAI base URL to `http://localhost:8080/v1`

**Python Streaming:**
```python
from openai import OpenAI

client = OpenAI(
    base_url="http://localhost:8080/v1",
    api_key="not-needed"
)

stream = client.chat.completions.create(
    model="gpt-4o",
    messages=[{"role": "user", "content": "Write a haiku about coding"}],
    stream=True
)

for chunk in stream:
    if chunk.choices[0].delta.content:
        print(chunk.choices[0].delta.content, end="", flush=True)
```

**Node.js Streaming:**
```javascript
import OpenAI from 'openai';

const client = new OpenAI({
  baseURL: 'http://localhost:8080/v1',
  apiKey: 'not-needed',
});

const stream = await client.chat.completions.create({
  model: 'gpt-4o',
  messages: [{ role: 'user', content: 'Write a haiku about coding' }],
  stream: true,
});

for await (const chunk of stream) {
  const content = chunk.choices[0]?.delta?.content;
  if (content) process.stdout.write(content);
}
```

---

## Configuration Reference

### Server

```yaml
server:
  host: "0.0.0.0"         # Bind address (default: 0.0.0.0)
  port: 8080              # Main API port (default: 8080)
  metrics_port: 9090      # Prometheus metrics port (default: 9090)
  admin_api_key: ${RESILLM_ADMIN_KEY}  # Required for /admin/* endpoints
  rate_limit:
    enabled: true         # Enable per-IP rate limiting
    requests_per_second: 10  # Sustained request rate
    burst: 20             # Burst allowance above rate
```

### Security

**Admin Authentication:**

The `/admin/reload` endpoint requires authentication when `admin_api_key` is configured:

```bash
# Reload configuration with admin key
curl -X POST http://localhost:8080/admin/reload \
  -H "X-Admin-Key: your-admin-key"
```

**Rate Limiting:**

When enabled, rate limiting applies per client IP using a token bucket algorithm:
- `requests_per_second`: Sustained request rate allowed per IP
- `burst`: Maximum burst of requests above the sustained rate

**Security Headers:**

resillm automatically adds security headers to all responses:
- `X-Content-Type-Options: nosniff`
- `X-Frame-Options: DENY`
- `X-XSS-Protection: 1; mode=block`

### Logging

```yaml
logging:
  level: info          # debug, info, warn, error
  format: json         # json or text
  log_requests: true   # Log incoming requests
  log_responses: false # Log response bodies (verbose)
```

Log output includes structured fields for observability:
- `request_id`: Unique identifier for request tracing
- `provider`: Which provider handled the request
- `model`: Actual model used
- `cost_usd`: Request cost
- `latency_ms`: Total request latency

### Providers

```yaml
providers:
  openai:
    api_key: ${OPENAI_API_KEY}
    base_url: "https://api.openai.com/v1"  # Optional

  anthropic:
    api_key: ${ANTHROPIC_API_KEY}

  azure-openai:
    api_key: ${AZURE_OPENAI_API_KEY}
    base_url: "https://your-resource.openai.azure.com"
    api_version: "2024-02-15-preview"

  ollama:
    base_url: "http://localhost:11434"
```

### Model Routing

```yaml
models:
  # Request "gpt-4o" → try OpenAI, fallback to Anthropic
  gpt-4o:
    primary:
      provider: openai
      model: gpt-4o
    fallbacks:
      - provider: anthropic
        model: claude-sonnet-4-20250514
      - provider: azure-openai
        model: gpt-4o

  # Custom model aliases
  fast:
    primary:
      provider: anthropic
      model: claude-3-5-haiku-20241022

  local:
    primary:
      provider: ollama
      model: llama3.2
```

### Resilience Settings

```yaml
resilience:
  retry:
    max_attempts: 3
    initial_backoff: 100ms
    max_backoff: 10s
    backoff_multiplier: 2.0
    retryable_errors: [429, 500, 502, 503, 504]

  circuit_breaker:
    failure_threshold: 5    # Open after 5 failures
    success_threshold: 3    # Close after 3 successes
    timeout: 30s            # Time before half-open

  timeout:
    connect: 5s
    request: 120s
```

### Budget Controls

```yaml
budget:
  enabled: true
  max_cost_per_hour: 10.00
  max_cost_per_day: 100.00
  alert_threshold: 0.8      # Alert at 80% of limit
  action_on_exceeded: reject  # or "allow_with_warning"
```

---

## API Endpoints

| Endpoint | Method | Description |
|----------|--------|-------------|
| `/v1/chat/completions` | POST | OpenAI-compatible chat API |
| `/v1/chat/completions` | POST (stream) | Streaming chat completions |
| `/health` | GET | Health check |
| `/v1/providers` | GET | Provider status & circuit states |
| `/v1/budget` | GET | Current budget usage |
| `/admin/reload` | POST | Hot-reload configuration |
| `/metrics` | GET | Prometheus metrics (port 9090) |

---

## Response Metadata

Every response includes resillm metadata:

```json
{
  "id": "chatcmpl-abc123",
  "choices": [...],
  "usage": {
    "prompt_tokens": 10,
    "completion_tokens": 20,
    "total_tokens": 30
  },
  "_resillm": {
    "provider": "anthropic",
    "actual_model": "claude-sonnet-4-20250514",
    "requested_model": "gpt-4o",
    "latency_ms": 1234,
    "retries": 1,
    "fallback": true,
    "cost_usd": 0.0045,
    "request_id": "req-abc123"
  }
}
```

---

## Error Handling

### HTTP Status Codes

| Status | Meaning | Action |
|--------|---------|--------|
| 200 | Success | Response includes `_resillm` metadata |
| 400 | Bad Request | Invalid JSON, missing model, validation error |
| 401 | Unauthorized | Missing `X-Admin-Key` for admin endpoints |
| 403 | Forbidden | Invalid `X-Admin-Key` |
| 429 | Too Many Requests | Budget exceeded or rate limited |
| 502 | Bad Gateway | All providers failed (after retries and fallbacks) |
| 503 | Service Unavailable | All circuit breakers open |

### Error Response Format

All errors follow this format:

```json
{
  "error": {
    "type": "budget_exceeded",
    "message": "Hourly budget limit exceeded"
  }
}
```

Common error types:
- `invalid_request` — Malformed request or validation failure
- `budget_exceeded` — Cost limits reached
- `provider_error` — Upstream provider failure
- `unauthorized` — Missing authentication
- `forbidden` — Invalid authentication

### Detecting Fallbacks

Check the `_resillm` metadata in successful responses to detect when a fallback was used:

```python
response = client.chat.completions.create(
    model="gpt-4o",
    messages=[{"role": "user", "content": "Hello"}]
)

# Access raw response for metadata (library-specific)
raw = response.model_extra.get("_resillm", {})
if raw.get("fallback"):
    print(f"Fallback used: {raw['provider']}/{raw['actual_model']}")
if raw.get("retries", 0) > 0:
    print(f"Request was retried {raw['retries']} times")
```

### Budget Warning Headers

When approaching budget limits, responses include a warning header:

```
X-Resillm-Budget-Warning: Approaching hourly budget limit (85% used)
```

---

## Environment Variables

resillm supports `${VAR}` syntax for environment variable expansion in config files.

| Variable | Description | Used In |
|----------|-------------|---------|
| `OPENAI_API_KEY` | OpenAI API key | `providers.openai.api_key` |
| `ANTHROPIC_API_KEY` | Anthropic API key | `providers.anthropic.api_key` |
| `AZURE_OPENAI_API_KEY` | Azure OpenAI API key | `providers.azure-openai.api_key` |
| `RESILLM_ADMIN_KEY` | Admin endpoint authentication | `server.admin_api_key` |

**Important:** If a referenced environment variable is not set, the config will fail to load with an error message indicating which variable is missing. This prevents accidentally running with placeholder values.

```bash
# Set required environment variables
export OPENAI_API_KEY="sk-..."
export ANTHROPIC_API_KEY="sk-ant-..."
export RESILLM_ADMIN_KEY="$(openssl rand -hex 32)"

# Start resillm
./resillm --config config.yaml
```

---

## Metrics

Prometheus metrics available at `:9090/metrics`:

```prometheus
# Request metrics
resillm_requests_total{provider="openai", model="gpt-4o", status="success"}
resillm_request_latency_seconds{provider="openai", model="gpt-4o"}

# Token usage
resillm_tokens_total{provider="openai", model="gpt-4o", type="prompt"}
resillm_tokens_total{provider="openai", model="gpt-4o", type="completion"}

# Cost tracking
resillm_cost_dollars_total{provider="openai", model="gpt-4o"}

# Circuit breaker
resillm_circuit_state{provider="openai"}  # 0=closed, 1=open, 2=half-open
resillm_circuit_failures_total{provider="openai"}

# Fallbacks
resillm_fallbacks_total{from_provider="openai", to_provider="anthropic"}
```

---

## Current Status

### Implemented
- [x] OpenAI-compatible API proxy
- [x] Multi-provider support (OpenAI, Anthropic, Azure, Ollama)
- [x] Request/response format translation
- [x] Automatic retries with exponential backoff
- [x] Provider fallback chains
- [x] Circuit breaker pattern
- [x] Streaming support (SSE)
- [x] Budget tracking with rolling windows
- [x] Budget enforcement (reject/warn)
- [x] Prometheus metrics
- [x] Configuration hot-reload
- [x] Structured JSON logging
- [x] Comprehensive test suite (80%+ coverage)

### Coming Soon
- [ ] AWS Bedrock provider
- [ ] Google Vertex AI provider
- [ ] Request caching (identical prompts)
- [ ] OpenTelemetry tracing
- [ ] Admin web UI
- [ ] Load balancing strategies (round-robin, least-latency)
- [ ] Per-request budget limits
- [ ] Webhook alerts

---

## The Bigger Vision: Chaos Engineering for LLMs

**resillm is the foundation for something bigger.**

Today's LLM applications are fragile. They work in development but fail unpredictably in production. Rate limits hit at the worst times. Models get deprecated. Providers change behavior. Costs spiral out of control.

We're building toward **LLM Chaos Engineering** — a discipline for testing and hardening AI applications:

### What is LLM Chaos?

Just like Netflix's Chaos Monkey tests infrastructure resilience by randomly killing servers, LLM Chaos tests AI application resilience by simulating real-world failures:

- **Latency Injection** — Add 5-30 second delays to expose timeout issues
- **Error Injection** — Return 429/500 errors to test retry logic
- **Response Corruption** — Malformed JSON, truncated responses
- **Cost Spikes** — Simulate expensive model responses
- **Rate Limit Simulation** — Test backoff strategies under pressure
- **Model Degradation** — Return lower-quality responses
- **Prompt Attacks** — Test guardrails with adversarial inputs

### Why This Matters

LLM applications are becoming critical infrastructure. They power customer support, content generation, code assistance, and decision-making. Yet most teams have no idea how their apps behave when:

- Your primary LLM provider has a 2-hour outage
- A provider changes their rate limits
- A prompt causes infinite retry loops
- Costs exceed budget mid-request

**resillm + LLM Chaos = Production-ready AI applications**

### Roadmap to LLM Chaos

1. **Phase 1 (Current)**: Resilience proxy — handle failures gracefully
2. **Phase 2**: Chaos injection — simulate failures on demand
3. **Phase 3**: Chaos automation — continuous resilience testing
4. **Phase 4**: AI-specific chaos — prompt attacks, hallucination testing, guardrail validation

We're building Phase 1 now and looking for contributors to help shape the future of LLM reliability engineering.

---

## Troubleshooting

### Verify Your Setup

Quick health checks to confirm resillm is working:

```bash
# 1. Check resillm is running
curl http://localhost:8080/health

# 2. Check provider status and circuit states
curl http://localhost:8080/v1/providers

# 3. Check budget status
curl http://localhost:8080/v1/budget

# 4. Test a completion (replace with your configured model)
curl http://localhost:8080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{"model": "gpt-4o", "messages": [{"role": "user", "content": "Hi"}]}'
```

### Common Issues

**"Connection refused" when calling resillm**

Ensure resillm is running and listening on the expected port:
```bash
curl http://localhost:8080/health
```
Check that no firewall is blocking the port and the configured `host` allows external connections (use `0.0.0.0` for all interfaces).

**"Unknown model" error**

The requested model isn't configured in your `config.yaml`. Add it to the `models` section:
```yaml
models:
  your-model-name:
    primary:
      provider: openai
      model: gpt-4o
```

**"Circuit open" errors (503)**

A provider is failing repeatedly and the circuit breaker has opened. Check provider status:
```bash
curl http://localhost:8080/v1/providers
```
The circuit will automatically recover after the timeout period (default: 30s). If the issue persists, check your provider API keys and the provider's status page.

**"Budget exceeded" (429)**

Your hourly or daily cost limit has been reached. Check current budget status:
```bash
curl http://localhost:8080/v1/budget
```
Options:
- Wait for the next hour/day window to reset
- Increase limits in config and hot-reload:
  ```bash
  curl -X POST http://localhost:8080/admin/reload -H "X-Admin-Key: your-key"
  ```
- Set `action_on_exceeded: allow_with_warning` to allow requests with a warning header

**"Unexpanded variable" config error**

An environment variable referenced in your config isn't set:
```
provider "openai": API key contains unexpanded variable: ${OPENAI_API_KEY}
```
Set the required environment variable before starting:
```bash
export OPENAI_API_KEY="sk-..."
./resillm --config config.yaml
```

**Rate limited (429) but budget is fine**

If rate limiting is enabled, you may be exceeding the per-IP request rate. Check your `rate_limit` configuration:
```yaml
server:
  rate_limit:
    enabled: true
    requests_per_second: 10  # Increase if needed
    burst: 20
```

---

## Contributing

We welcome contributions! See [CONTRIBUTING.md](CONTRIBUTING.md) for guidelines.

**Good first issues:**
- Add AWS Bedrock provider
- Add Google Vertex AI provider
- Implement request caching
- Add OpenTelemetry tracing
- Build admin web UI

**Looking for contributors interested in:**
- Go backend development
- LLM/AI infrastructure
- Chaos engineering
- DevOps/SRE practices
- Documentation and examples

---

## Building from Source

```bash
# Clone
git clone https://github.com/resillm/resillm.git
cd resillm

# Build
go build -o resillm ./cmd/resillm

# Test
go test ./...

# Run
./resillm --config config.yaml
```

## Docker

```bash
# Build
docker build -t resillm .

# Run
docker run -p 8080:8080 -p 9090:9090 \
  -v $(pwd)/config.yaml:/config.yaml \
  -e OPENAI_API_KEY=$OPENAI_API_KEY \
  resillm
```

---

## License

MIT License — see [LICENSE](LICENSE) for details.

---

## Acknowledgments

### Origin Story

**resillm** was born out of necessity while building [SpecFlow](https://github.com/maneeshchaturvedi/specflow) — an AI-powered specification-driven development platform that enables safe AI-assisted development on legacy codebases.

SpecFlow makes heavy use of LLM APIs for specification generation, drift detection, module synthesis, and context-aware code analysis. During development, we repeatedly hit the same pain points: OpenAI outages would block the entire workflow, rate limits during testing would stall progress, and cost tracking was a constant concern with multiple team members running generations.

We found ourselves writing retry logic, fallback handlers, and budget tracking scattered throughout the codebase. Every LLM call became wrapped in try-catch blocks and provider-specific code. That's when we realized: **this resilience layer should be infrastructure, not application code.**

resillm extracts that resilience layer into a standalone proxy that any LLM application can use — including SpecFlow itself, which now runs all its LLM calls through resillm for automatic retries, provider fallbacks, and cost control.

### Built with

- [zerolog](https://github.com/rs/zerolog) — Structured logging
- [fsnotify](https://github.com/fsnotify/fsnotify) — File watching
- [prometheus/client_golang](https://github.com/prometheus/client_golang) — Metrics

### Inspired by

- [LiteLLM](https://github.com/BerriAI/litellm) — Multi-provider LLM proxy
- [Resilience4j](https://github.com/resilience4j/resilience4j) — Java resilience library
- [Chaos Monkey](https://github.com/Netflix/chaosmonkey) — Netflix's chaos engineering tool
