# upstream public API

`New() *Client` creates the production client. It resolves and validates public IPs before every request, pins dialing to a validated IP, refuses redirects and protects `api.zxcbug.com` and its subdomains. `NewTestClient(doer HTTPDoer) *Client` is the explicit test-only injectable constructor.

```go
type Config struct {
    BaseURL, APIKey, Protocol, DiscoveryProtocol string
    ModelsPath, GeneratePath, AnthropicVersion, ChatTokenField string
}
type Request struct {
    Config Config
    Model, Prompt string
    MaxTokens int
    Timeout time.Duration
}
type Event struct {
    Type, Text string // meta, text, usage
    Usage *domain.Usage
    HTTPStatus int
    ReturnedModel string
}
type Result struct {
    Text, ReturnedModel, Protocol, FinishReason string
    HTTPStatus int
    FirstTextMS *int64
    DurationMS int64
    Usage domain.Usage
    Complete bool
}
type Error struct { Code, Message string; HTTPStatus int }
type HTTPDoer interface { Do(*http.Request) (*http.Response, error) }
func (c *Client) ListModels(context.Context, Config) ([]string, error)
func (c *Client) Generate(context.Context, Request, func(Event) error) (Result, error)
```

Protocols are `chat`, `responses`, `anthropic`; discovery is `openai` (default) or `anthropic`. Generate always requests streaming but accepts JSON fallback. Callbacks are synchronous and errors cancel processing. `meta` fires as soon as HTTP response headers arrive, including non-2xx. Missing usage remains nil. Completion requires explicit protocol terminal success, a nonempty visible text body and no length truncation. Errors return the partial `Result` with measured timing and available usage.

Authentication material echoed by an upstream is redacted before text callbacks and `Result` delivery, including keys split across text events. Returned model/finish metadata is also redacted. A model listing that includes the configured key as a model ID fails with `credential_exposure`. Generation POSTs deliberately have no `GetBody` replay function or idempotency header, so the transport cannot replay a partially written billable generation.

`artifact.Extract(text, kind string) (code, actualKind string, err error)` supports `html` and `svg`. `artifact.Preview(code, kind string) string` returns sandbox srcdoc content with an initial restrictive CSP; callers must set iframe `sandbox="allow-scripts"` without `allow-same-origin`.
