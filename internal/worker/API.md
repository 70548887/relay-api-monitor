# Worker API

```go
func New(st *store.Store, vault *security.Vault, client *upstream.Client) *Worker
func (w *Worker) Run(ctx context.Context) error
func (w *Worker) WorkOne(ctx context.Context, kind string) (bool, error)
```

`Run` acquires the database worker lease and errors if another worker owns it. It ticks scheduling each second, refreshes worker/task leases every ten seconds, and checks cancellation and worker ownership every second. Lease takeover cancels active HTTP requests and fences old results. Settings are read dynamically, with independent probe/artifact admission budgets and a shared channel budget. Shutdown cancels children, waits for them, then releases the worker lease. `WorkOne` claims and executes one job synchronously for deterministic integration testing; it applies the same job validation, snapshot, request and completion path, but does not acquire the process-wide worker lease or run scheduling/discovery.

Discovery success and failure are saved only if the channel configuration revision still matches the request. Editing a channel invalidates its previous discovery retry backoff; discovery endpoint/key changes also clear the prior success timestamp so the new configuration is checked on the next maintenance pass.

Probe statuses are `ok`, `slow`, and `failed`; canceled/uncertain jobs create no probe result. Job statuses use Store's `succeeded`, `failed`, `canceled`, and `uncertain` contract. Artifacts are committed only after complete stream termination and valid HTML/SVG extraction. Unknown work after a crash remains subject to Store's dispatch recovery and is never resent automatically.

Artifact requests resolve model/channel reasoning settings through `upstream.ResolveReasoning`; probes omit reasoning configuration. Request snapshots and artifacts retain actual request parameters and a verified group only when the verification revision matches the dispatched channel configuration. Stream callbacks count visible Unicode characters, persist progress at most once per second, and completion preserves the latest count. New artifact timeout defaults to 600 seconds; explicitly saved settings and model overrides remain effective.
