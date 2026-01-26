# Contributing to resillm

Thank you for your interest in contributing to resillm! We're building the foundation for LLM reliability engineering, and we'd love your help.

## Ways to Contribute

### Code Contributions

**Good First Issues:**
- Add AWS Bedrock provider
- Add Google Vertex AI provider
- Implement request caching (identical prompts)
- Add OpenTelemetry tracing
- Build admin web UI

**Advanced Contributions:**
- Load balancing strategies (round-robin, least-latency)
- Per-request budget limits
- Webhook alerts
- Chaos injection features (Phase 2)

### Non-Code Contributions
- Documentation improvements
- Bug reports and feature requests
- Testing and feedback
- Blog posts and tutorials
- Spreading the word

## Development Setup

### Prerequisites
- Go 1.21 or later
- Docker (optional, for containerized testing)
- Access to at least one LLM provider API (OpenAI, Anthropic, etc.)

### Getting Started

```bash
# Clone the repository
git clone https://github.com/resillm/resillm.git
cd resillm

# Install dependencies
go mod download

# Run tests
go test ./...

# Build
go build -o resillm ./cmd/resillm

# Run locally
./resillm --config config.yaml
```

### Project Structure

```
resillm/
├── cmd/resillm/        # Main entry point
├── internal/
│   ├── config/         # Configuration loading & hot-reload
│   ├── server/         # HTTP server & handlers
│   ├── router/         # Request routing & fallback logic
│   ├── providers/      # LLM provider implementations
│   ├── resilience/     # Circuit breaker, retry logic
│   ├── budget/         # Cost tracking & enforcement
│   ├── metrics/        # Prometheus metrics
│   └── types/          # Shared types
└── test/               # Integration tests
```

## Coding Guidelines

### Style
- Follow standard Go conventions (`gofmt`, `golint`)
- Use meaningful variable and function names
- Keep functions focused and small
- Add comments for non-obvious logic

### Testing
- Write tests for all new functionality
- Aim for 80%+ code coverage
- Include both unit tests and integration tests
- Use table-driven tests where appropriate

```go
func TestFeature(t *testing.T) {
    tests := []struct {
        name     string
        input    string
        expected string
    }{
        {"basic case", "input", "expected"},
        {"edge case", "edge", "result"},
    }

    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            result := Feature(tt.input)
            if result != tt.expected {
                t.Errorf("got %v, want %v", result, tt.expected)
            }
        })
    }
}
```

### Commits
- Write clear, descriptive commit messages
- Use present tense ("Add feature" not "Added feature")
- Reference issues when applicable (`Fixes #123`)
- Keep commits focused on a single change

### Pull Requests
- Create a branch from `main` for your work
- Keep PRs focused and reasonably sized
- Update documentation if needed
- Ensure all tests pass
- Request review from maintainers

## Adding a New Provider

One of the most valuable contributions is adding support for new LLM providers. Here's how:

### 1. Create the Provider File

```go
// internal/providers/newprovider.go
package providers

type NewProvider struct {
    apiKey  string
    baseURL string
    client  *http.Client
}

func NewNewProvider(cfg config.ProviderConfig) (*NewProvider, error) {
    // Initialize the provider
}

func (p *NewProvider) Name() string {
    return "newprovider"
}

func (p *NewProvider) ExecuteChat(ctx context.Context, req *types.ChatCompletionRequest, model string) (*types.ChatCompletionResponse, error) {
    // Convert request to provider format
    // Make API call
    // Convert response back to OpenAI format
}

func (p *NewProvider) ExecuteChatStream(ctx context.Context, req *types.ChatCompletionRequest, model string) (<-chan types.StreamChunk, error) {
    // Implement streaming
}

func (p *NewProvider) CalculateCost(model string, usage types.Usage) float64 {
    // Calculate cost based on token usage
}
```

### 2. Register the Provider

Add to `internal/providers/registry.go`:

```go
case "newprovider":
    provider, err = NewNewProvider(cfg)
```

### 3. Add Configuration

Update `internal/config/config.go` if needed for provider-specific settings.

### 4. Write Tests

Create `internal/providers/newprovider_test.go` with comprehensive tests.

### 5. Update Documentation

Add the provider to README.md and any relevant documentation.

## Reporting Issues

### Bug Reports

Include:
- resillm version
- Go version
- Operating system
- Configuration (redact API keys!)
- Steps to reproduce
- Expected vs actual behavior
- Relevant logs

### Feature Requests

Include:
- Clear description of the feature
- Use case / motivation
- Proposed implementation (optional)
- Willingness to contribute

## Community

- Be respectful and inclusive
- Help others learn and grow
- Give constructive feedback
- Celebrate contributions of all sizes

## License

By contributing, you agree that your contributions will be licensed under the MIT License.

---

## Areas We're Especially Interested In

### Phase 1: Core Resilience (Current)
- Bug fixes and improvements
- Additional providers
- Performance optimizations
- Documentation

### Phase 2: Chaos Injection (Coming Soon)
- Latency injection middleware
- Error injection framework
- Response corruption simulation
- Rate limit simulation

### Phase 3: Chaos Automation (Future)
- Chaos experiment definitions
- Scheduled chaos testing
- CI/CD integration
- Chaos dashboards

### Phase 4: AI-Specific Chaos (Vision)
- Prompt attack testing
- Hallucination detection
- Guardrail validation
- Model degradation simulation

If you're interested in working on any of these areas, please reach out! We'd love to collaborate.

---

Questions? Open an issue or reach out to the maintainers.

**Thank you for helping make LLM applications more reliable!**
