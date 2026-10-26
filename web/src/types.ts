import type * as Domain from './generated/domain';

export type Protocol = 'chat'|'responses'|'anthropic';
export type Platform = Domain.Platform;
export type Channel = Omit<Domain.Channel,'protocol'> & {protocol:Protocol};
export type Model = Omit<Domain.Model,'protocol'> & {protocol:Protocol|''};
export type Usage = Domain.Usage;
export type ProbeResult = Domain.ProbeResult;
export type PromptTemplate = Domain.PromptTemplate;
export type Job = Domain.Job;
export type Settings = Domain.Settings;
export type Schedule = Domain.Schedule;

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
export interface TestDone {first_text_ms:number|null;duration_ms:number;usage:Usage;finish_reason:string;returned_model:string;http_status:number;requested_model?:string;reasoning_effort?:string;thinking_mode?:string;thinking_budget?:number}

/** Public intelligence responses contain no credentials or unrestricted snapshots. */
export interface GroupVerification {status:string;group?:string;checked_at?:string|null;message?:string;provider?:string;scope?:'token_binding'}
export interface PublicAttempt {
 id:string;state:string;model_id?:string;template_id?:string;created_at?:string;started_at?:string|null;finished_at?:string|null;
 error_code?:string;error_message?:string;cancel_requested?:boolean;progress_chars?:number;progress_at?:string|null;returned_model?:string;
 snapshot?:string|Record<string,unknown>;requested_model?:string;protocol?:string;reasoning_effort?:string;thinking_mode?:string;thinking_budget?:number;
 artifact_id?:string;usage?:Usage;first_text_ms?:number|null;duration_ms?:number;finish_reason?:string;
}
export interface IntelligenceGroup {
 id:string;channel_id:string;channel_name:string;platform_id:string;platform_name:string;model_id:string;requested_model:string;
 models:{id:string;upstream_id:string;display_name:string;protocol:string}[];group_verification:GroupVerification;
 reasoning_effort:string;thinking_mode:string;thinking_budget:number;interval_seconds:number;next_run_at:string|null;automatic_enabled:boolean;
 latest_attempt:PublicAttempt|null;history:Artifact[];
}
export interface IntelligenceData {groups:IntelligenceGroup[];history_scope:'group'|'model';updated_at:string;worker_online:boolean}

export type UpdateState = 'idle'|'checking'|'available'|'downloading'|'ready'|'draining'|'stopping'|'backing_up'|'starting'|'validating'|'succeeded'|'failed'|'rolled_back'|'pending_restart'|string;
export interface UpdateCurrent {version:string;commit?:string;build_time?:string;os?:string;arch?:string}
export interface UpdateAsset {name:string;os?:string;arch?:string;size?:number;sha256?:string;download_url?:string;compatible?:boolean;verified?:boolean;local_path?:string}
export interface UpdateRelease {tag_name:string;name?:string;published_at?:string;body?:string;url?:string;assets?:UpdateAsset[]}
export interface UpdateInfo {current_version:string;latest_version:string;release_url?:string;published_at?:string;asset_name?:string;asset_size?:number;sha256?:string;compatible:boolean;available:boolean;notes?:string}
export interface UpdateOperation {id?:string;state:UpdateState;version?:string;progress?:number;error_code?:string;message?:string;started_at?:string;finished_at?:string}
export interface UpdateMaintenance {active:boolean;draining_requests?:number;running_jobs?:number;worker_online?:boolean}
export interface UpdateSnapshot {enabled:boolean;repository:string|{owner:string;repo:string};current:UpdateCurrent;latest?:UpdateInfo|null;release?:UpdateRelease|null;state?:UpdateState;operation?:UpdateOperation|null;staged_path?:string;history?:UpdateHistory[];checked_at?:string|null}
export interface UpdateHistory {id:string;kind?:string;state:UpdateState;version?:string;message?:string;created_at?:string;finished_at?:string}
