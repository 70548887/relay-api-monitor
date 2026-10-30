import type * as Domain from './generated/domain';

export type Protocol = 'chat'|'responses'|'anthropic';
export type Platform = Domain.Platform;
export type Channel = Omit<Domain.Channel,'protocol'> & {protocol:Protocol};
export type IntelligenceSchedule = Domain.IntelligenceSchedule;
export type Model = Omit<Domain.Model,'protocol'> & {protocol:Protocol|''};
export type Usage = Domain.Usage;
export type ProbeResult = Domain.ProbeResult;
export type PromptTemplate = Domain.PromptTemplate;
export type Job = Domain.Job & {requested_model?:string;channel_name?:string;context_policy?:string;visibility_reasons?:string[]};
export type Settings = Domain.Settings;
export type Schedule = Domain.Schedule;
export type IntelligencePlanStatus=Domain.IntelligencePlanStatus;
export type IntelligenceGroupStatus=Domain.IntelligenceGroupStatus;
export type IntelligenceStatusResponse=Domain.IntelligenceStatusResponse;

/** Public/admin lists omit source and raw payloads; detail endpoints add them. */
export interface Artifact extends Omit<Domain.Artifact,'code'|'raw_response'> {
 code?:string;
 raw_response?:string;
 preview_document?:string;
 platform_name?:string;
 channel_name?:string;
 display_name?:string;
 last_attempt?:Job;
}
export interface ModelView extends Model {
 platform_id:string;
 platform_name:string;
 channel_name:string;
 status:'unknown'|'ok'|'slow'|'failed'|'disabled';
 stale:boolean;
 running:boolean;
 last_probe:ProbeResult|null;
 success_rate:number|null;
 samples:number;
 timeline:{at:string;status:string;count:number}[];
}
export interface MonitorData {
 platforms:Platform[];
 channels:Pick<Channel,'id'|'platform_id'|'name'|'sort'>[];
 models:ModelView[];
 summary:{platforms:number;models:number;normal:number;slow:number;failed:number;stale:number};
 updated_at:string;
}
export interface Session {user:{id:string;username:string};csrf_token:string}
export interface Overview {worker:Pick<Domain.WorkerHealth,'online'|'heartbeat_at'>;counts:{queued:number;running:number;failed:number};recent_jobs:Job[]}
export interface TestDone {context_policy?:string;received_bytes?:number;text_bytes?:number;state?:string;first_text_ms:number|null;duration_ms:number;usage:Usage;finish_reason:string;returned_model:string;http_status:number;requested_model?:string;reasoning_effort?:string;thinking_mode?:string;thinking_budget?:number}

/** Public intelligence responses contain no credentials or unrestricted snapshots. */
export interface GroupVerification {status:string;group?:string;checked_at?:string|null;message?:string;provider?:string;scope?:'token_binding'}
export interface PublicAttempt {
 id:string;state:string;model_id?:string;template_id?:string;created_at?:string;started_at?:string|null;finished_at?:string|null;
 error_code?:string;error_message?:string;cancel_requested?:boolean;progress_chars?:number;progress_at?:string|null;returned_model?:string;
 snapshot?:string|Record<string,unknown>;requested_model?:string;protocol?:string;reasoning_effort?:string;thinking_mode?:string;thinking_budget?:number;
 artifact_id?:string;usage?:Usage;first_text_ms?:number|null;duration_ms?:number;finish_reason?:string;
 context_policy?:string;
}
export interface IntelligenceGroup {
 id:string;channel_id:string;channel_name:string;platform_id:string;platform_name:string;model_id:string;requested_model:string;
 models:{id:string;upstream_id:string;display_name:string;protocol:string}[];group_verification:GroupVerification;
 reasoning_effort:string;thinking_mode:string;thinking_budget:number;interval_seconds:number;next_run_at:string|null;automatic_enabled:boolean;
 schedule_state?:string;schedule_message?:string;maintenance?:boolean;context_policy?:string;
 latest_attempt:PublicAttempt|null;history:Artifact[];
}
export interface IntelligenceData {groups:IntelligenceGroup[];history_scope:'group'|'model';updated_at:string;worker_online:boolean}

export type UpdateState = 'idle'|'checking'|'available'|'downloading'|'ready'|'draining'|'stopping'|'backing_up'|'starting'|'validating'|'succeeded'|'failed'|'rolled_back'|'pending_restart'|string;
export interface UpdateCurrent {version:string;commit?:string;build_time?:string;os?:string;arch?:string}
export interface UpdateAsset {name:string;os?:string;arch?:string;size?:number;sha256?:string;download_url?:string;compatible?:boolean;verified?:boolean;local_path?:string}
export interface UpdateRelease {tag_name:string;name?:string;published_at?:string;body?:string;url?:string;assets?:UpdateAsset[]}
export interface UpdateInfo {current_version:string;latest_version:string;commit?:string;release_url?:string;published_at?:string;asset_name?:string;asset_size?:number;sha256?:string;compatible:boolean;available:boolean;notes?:string}
export interface UpdateOperation {id?:string;kind?:string;state:UpdateState;version?:string;progress?:number;error_code?:string;message?:string;started_at?:string;updated_at?:string;finished_at?:string;bytes_downloaded?:number;total_bytes?:number;bytes_per_second?:number;eta_seconds?:number}
export interface UpdateMaintenance {active:boolean;draining_requests?:number;running_jobs?:number;worker_online?:boolean}
export interface UpdateSnapshot {enabled:boolean;install_enabled?:boolean;install_mode?:'binary'|'container';repository:string;current:UpdateCurrent;latest?:UpdateInfo|null;release?:UpdateRelease|null;state?:UpdateState;operation?:UpdateOperation|null;history?:UpdateHistory[];checked_at?:string|null;launcher_managed?:boolean;launcher_protocol?:number;auto_restart?:boolean;maintenance?:UpdateMaintenance}
export interface UpdateHistory {id:string;kind?:string;state:UpdateState;version?:string;message?:string;created_at?:string;finished_at?:string}
