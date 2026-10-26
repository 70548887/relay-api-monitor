# Store API

Package `relaylab/internal/store`, using `relaylab/internal/domain` types.

All exported methods below take `context.Context` first unless shown otherwise.
Get methods return `(*domain.Type, error)`, with `sql.ErrNoRows` for absent records.
Save methods take values and return error. IDs may be omitted on save and will be generated internally; callers needing the ID should supply `domain.NewID()`.

```go
Open(path string) (*Store, error) // opens and migrates
OpenExisting(path string) (*Store, error) // validates existing schema without migrating
(*Store).Close() error
(*Store).Migrate(ctx context.Context) error
ListPlatforms(ctx) ([]domain.Platform, error)
GetPlatform(ctx, id string) (*domain.Platform, error)
SavePlatform(ctx, value domain.Platform) error
ListChannels(ctx, platformID ...string) ([]domain.Channel, error)
GetChannel(ctx, id string) (*domain.Channel, error)
SaveChannel(ctx, value domain.Channel) error
SaveChannelWithSchedules(ctx, value domain.Channel) error
ListModels(ctx, channelID ...string) ([]domain.Model, error)
GetModel(ctx, id string) (*domain.Model, error)
SaveModel(ctx, value domain.Model) error
SaveModelWithSchedules(ctx, value domain.Model) error
SyncModels(ctx, channelID string, upstreamIDs []string, expectedRevision ...int) error
SetChannelSyncError(ctx, channelID, message string, expectedRevision ...int) error
SetGroupVerification(ctx, channelID string, expectedRevision int, status, group, provider, message string, checkedAt time.Time) error
GetSettings(ctx) (domain.Settings, error)
SaveSettings(ctx, value domain.Settings) error
SaveSettingsAndReconcile(ctx, value domain.Settings) error
ListTemplates(ctx) ([]domain.PromptTemplate, error)
GetTemplate(ctx, id string) (*domain.PromptTemplate, error)
SaveTemplate(ctx, value domain.PromptTemplate) error
EnsureAdmin(ctx, username, passwordHash string) error
HasAdmin(ctx) (bool, error)
GetAdminByUsername(ctx, username string) (*domain.Admin, error)
GetAdminByID(ctx, id string) (*domain.Admin, error)
UpdatePassword(ctx, adminID, passwordHash string) error
SaveSession(ctx, value domain.Session) error
GetSession(ctx, tokenHash string) (*domain.Session, error)
DeleteSession(ctx, tokenHash string) error
ListSchedules(ctx, modelID ...string) ([]domain.Schedule, error)
GetSchedule(ctx, id string) (*domain.Schedule, error)
SaveSchedule(ctx, value domain.Schedule) error
ReconcileSchedules(ctx, model domain.Model) error
TickSchedules(ctx, now time.Time) (int, error)
EnqueueJob(ctx, job domain.Job) (domain.Job, error)
ClaimJob(ctx, kind, owner string, now time.Time) (*domain.Job, error) // nil,nil when none
ClaimJobForChannels(ctx, kind, owner string, now time.Time, channelIDs []string) (*domain.Job, error) // admission filter; nil allows all
MarkDispatching(ctx, id, leaseToken string, deadline time.Time, snapshot string) error
RenewJob(ctx, id, leaseToken string, until time.Time) error
UpdateJobProgress(ctx, id, leaseToken string, chars int64, returnedModel string) error
FinishJob(ctx, job domain.Job, probe *domain.ProbeResult, artifact *domain.Artifact) error
RequestCancel(ctx, id string) error
CancelJobsForChannel(ctx, channelID string) error
CancelJobsForModel(ctx, modelID string) error
JobStateCounts(ctx) (map[string]int, error)
LatestArtifactAttempts(ctx, modelID, templateID string) ([]domain.Job, error)
LatestChannelArtifactAttempts(ctx, channelID, templateID string) ([]domain.Job, error)
LatestPublicChannelArtifactAttempts(ctx, channelID, templateID string) ([]domain.Job, error)
GetJob(ctx, id string) (*domain.Job, error)
ListJobs(ctx, limit ...int) ([]domain.Job, error)
RecoverJobs(ctx, now time.Time) error
AcquireWorkerLease(ctx, owner string, now time.Time) (bool, error)
RenewWorkerLease(ctx, owner string, now time.Time) error
ReleaseWorkerLease(ctx, owner string) error
WorkerHealth(ctx) (domain.WorkerHealth, error)
CheckWorkerLease(ctx, owner string) error
ListProbeResults(ctx, modelID string, since time.Time) ([]domain.ProbeResult, error)
ListProbeResultsLimit(ctx, modelID string, since time.Time, limit int) ([]domain.ProbeResult, error)
ListAllProbeResults(ctx, since time.Time) ([]domain.ProbeResult, error)
ListArtifacts(ctx, modelID string) ([]domain.Artifact, error)
ListArtifactsSummary(ctx, modelID string) ([]domain.Artifact, error)
ListChannelArtifactsSummary(ctx, channelID, templateID string, limit int) ([]domain.Artifact, error)
ListPublicChannelArtifactsSummary(ctx, channelID, templateID string, limit int) ([]domain.Artifact, error)
GetArtifact(ctx, id string) (*domain.Artifact, error)
SetArtifactHidden(ctx, id string, hidden bool) error
DeleteArtifact(ctx, id string) error
Cleanup(ctx, now time.Time) error
AddAudit(ctx, value domain.AuditEvent) error
ListAudit(ctx, limit ...int) ([]domain.AuditEvent, error)
Backup(ctx, targetPath string) error
```

`ErrConflict` means a stale lease/state transition, duplicate scheduled slot, or an already active job of the same model/kind. Job states are `queued`, `claimed`, `dispatching`, `succeeded`, `failed`, `canceled`, and `uncertain`. A claimed job receives a 45-second lease. A dispatching job is fenced by its token and owner. Expired dispatching work becomes `uncertain` and blocks retry until its old deadline plus 30 seconds.

The SQLite handle is available as `Store.DB`. Every write transaction begins immediately, preventing stale read snapshots when the server and worker use independent database handles. Schema version 1 is recorded in `PRAGMA user_version`; future versions are rejected before migration DDL. Compatible version-zero backups can be opened with `OpenExisting` without migration. JSON stores preserve complete domain records; secret channel keys and job lease tokens are separately persisted and restored because their JSON tags omit them.

An empty model ID in `ListProbeResults` or `ListArtifacts` selects every model. Cleanup uses settings retention days (default 30) for probe results, terminal jobs and audit records. Successful artifact retention is the most recent eight per channel/template by default; `ArtifactHistoryScope=model` selects per-model/template retention. Missing scope in existing settings normalizes to `group` while existing timeouts remain unchanged. Retention is applied atomically with completion and during cleanup. Channel summary limits default to eight and cap at 100; empty channel/template filters select all groups/templates.

The `WithSchedules`, `SavePlatform`, and `SaveSettingsAndReconcile` configuration methods atomically commit configuration together with all affected schedules. Disabling a schedule cancels its queued jobs and requests cancellation of claimed or dispatched jobs; re-enabling never revives those old jobs. `SyncModels` also reconciles the channel's schedules within its transaction and updates successful sync health without changing the channel configuration revision. Optional expected revisions reject stale discovery responses and errors in the same write transaction. Editing discovery URL, key or protocol clears old discovery health.

When a worker lease exists, job claim, dispatch, renewal and completion are fenced against that lease's owner and expiry. Dispatch also validates the channel revision and schedule inside its state transition. Direct `WorkOne` tests can execute jobs when no global worker lease exists.

`JobStateCounts` and cancellation methods operate on all matching rows. `LatestArtifactAttempts` returns one latest artifact job per model/template, with optional model/template filters. `ListArtifactsSummary` excludes generated source and raw upstream response while preserving comparison metadata. `ListProbeResultsLimit` returns newest-first results with a default limit of 1000 and maximum of 5000.

`LatestChannelArtifactAttempts` returns the latest artifact attempt per channel/template, including queued, running, failed and canceled jobs without requiring a successful artifact. `UpdateJobProgress` accepts only live dispatch leases and current global worker ownership, never decreases visible character count, and stores no partial text. Completion preserves newer persisted progress. `SetGroupVerification` rejects stale revisions, preserves the encrypted key and configuration revision, and only records a nonempty verified group for `verified` status. Channel configuration saves ignore supplied verification fields and invalidate previous verification when base URL, key or group provider/path/field changes. A representative `IntelligenceModelID` restricts automatic artifact schedules to that model while probe schedules remain independent.

The `PublicChannel` variants apply public/nonarchived model, channel and platform filters before selecting latest attempts or limiting history. Public artifact history also excludes hidden entries. General channel methods retain private and hidden entries for administrative consumers.
