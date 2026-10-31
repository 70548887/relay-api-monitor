package main

func addBackupContract(schemas, paths object, add func(string, string, string, string, string, bool, bool)) {
	str := func() object { return object{"type": "string"} }
	secret := func() object { return object{"type": "string", "writeOnly": true} }
	obj := func(props object, required ...string) object {
		return object{"type": "object", "properties": props, "required": required}
	}
	schemas["BackupStorageInput"] = obj(object{"endpoint": str(), "region": str(), "bucket": str(), "prefix": str(), "force_path_style": object{"type": "boolean"}, "access_key_id": secret(), "secret_access_key": secret()}, "endpoint", "bucket")
	policyProps := object{}
	for key, value := range schemas["BackupSchedule"].(object)["properties"].(object) {
		switch key {
		case "recipient", "recipient_id", "recipient_verified", "next_run_at", "reconciliation_required":
			continue
		}
		policyProps[key] = value
	}
	schemas["BackupScheduleWrite"] = obj(policyProps, "enabled", "cron", "timezone", "mode", "retain_days", "retain_count")
	schemas["BackupSnapshot"] = obj(object{"schedule": ref("BackupSchedule"), "storage": ref("BackupStorage"), "active": ref("BackupRun"), "last_success": ref("BackupRun"), "records": object{"type": "array", "items": ref("BackupRun")}, "next_runs": object{"type": "array", "items": object{"type": "string", "format": "date-time"}}, "backup_dir": str(), "native_restore_supported": object{"type": "boolean"}, "deployment_kind": object{"type": "string", "enum": []string{"launcher", "docker", "unmanaged"}}}, "schedule", "records", "next_runs", "native_restore_supported", "deployment_kind")
	schemas["BackupRequestID"] = obj(object{"request_id": object{"type": "string", "minLength": 8, "maxLength": 100}}, "request_id")
	schemas["BackupReauth"] = obj(object{"password": secret()}, "password")
	schemas["BackupRecipient"] = obj(object{"recipient": str()}, "recipient")
	schemas["BackupIdentity"] = obj(object{"identity": secret()}, "identity")
	schemas["BackupGeneratedKey"] = obj(object{"identity": object{"type": "string", "description": "仅本次返回的恢复私钥，服务端不持久保存"}, "recipient": str()}, "identity", "recipient")
	schemas["BackupProtection"] = obj(object{"protected": object{"type": "boolean"}}, "protected")
	schemas["BackupAcknowledgeMissing"] = obj(object{"confirm": object{"type": "string", "const": "确认副本遗失"}}, "confirm")
	schemas["BackupPreflightInput"] = obj(object{"record_id": str(), "identity": secret()}, "record_id")
	schemas["BackupPreflightResult"] = obj(object{"id": str(), "backup_id": str(), "expires_at": object{"type": "string", "format": "date-time"}, "manifest": object{"type": "object"}, "native_restore_supported": object{"type": "boolean"}, "deployment_kind": str(), "docker_instructions": object{"type": "array", "items": str()}}, "id", "expires_at", "manifest")
	schemas["BackupRestoreApply"] = obj(object{"preflight_id": str()}, "preflight_id")
	schemas["BackupRestoreAccepted"] = obj(object{"operation_id": str(), "status_token": object{"type": "string", "description": "一小时内仅查询本操作；不要写入URL或持久存储"}}, "operation_id", "status_token")
	schemas["BackupRestoreOperation"] = obj(object{"id": str(), "operation_id": str(), "state": str(), "message": str(), "error_code": str(), "started_at": object{"type": "string", "format": "date-time"}, "updated_at": object{"type": "string", "format": "date-time"}}, "operation_id", "state", "message")
	schemas["BackupSnapshot"].(object)["properties"].(object)["restore_operation"] = ref("BackupRestoreOperation")
	schemas["BackupPreview"] = obj(object{"next_runs": object{"type": "array", "items": object{"type": "string", "format": "date-time"}}}, "next_runs")
	schemas["BackupStorageHealth"] = obj(object{"checked_at": object{"type": "string", "format": "date-time"}, "status": object{"type": "string", "enum": []string{"untested", "ok", "failed"}}, "message": str(), "operation": str()}, "status", "message")
	healthProps := schemas["BackupStorageHealth"].(object)["properties"].(object)
	for _, name := range []string{"stage", "error_code", "cleanup_error_code", "cleanup_message"} {
		healthProps[name] = str()
	}
	healthProps["duration_ms"] = object{"type": "integer", "format": "int64", "minimum": 0}
	healthProps["http_status"] = object{"type": "integer", "description": "上游HTTP状态，不是本地登录状态"}
	testProps := schemas["BackupStorageTestResult"].(object)["properties"].(object)
	testProps["status"] = object{"type": "string", "enum": []string{"ok", "failed"}}
	testProps["request_id"] = object{"type": "string", "description": "服务端生成的诊断编号，可关联运行日志和审计，不含凭据"}
	testProps["test_scope"] = object{"type": "string", "enum": []string{"saved", "draft"}, "description": "服务端精确比较配置和凭据确定的测试归属；不以浏览器表单推测"}
	testProps["storage_id"] = object{"type": "string", "description": "仅saved时提供，绑定测试开始时实际使用的存储修订"}
	testProps["storage_revision"] = object{"type": "integer", "minimum": 1, "description": "仅saved时提供；旧修订结果不代表当前配置已验证"}
	schemas["BackupStorageTestResponse"] = obj(object{"ok": object{"type": "boolean"}, "result": ref("BackupStorageTestResult")}, "ok", "result")
	schemas["BackupStorageTestError"] = obj(object{"error": obj(object{"code": str(), "message": str()}, "code", "message"), "request_id": str(), "result": ref("BackupStorageTestResult")}, "error", "request_id", "result")
	schemas["BackupStorage"].(object)["properties"].(object)["health"] = ref("BackupStorageHealth")
	schemas["BackupRetentionItem"] = obj(object{"id": str(), "file_name": str(), "size_bytes": object{"type": "integer", "format": "int64"}, "local_state": str(), "remote_state": str(), "started_at": object{"type": "string", "format": "date-time"}}, "id", "file_name", "size_bytes", "local_state", "remote_state", "started_at")
	schemas["BackupRetentionPreview"] = obj(object{"count": object{"type": "integer"}, "local_copies": object{"type": "integer"}, "remote_copies": object{"type": "integer"}, "total_bytes": object{"type": "integer", "format": "int64"}, "items": object{"type": "array", "items": ref("BackupRetentionItem")}}, "count", "local_copies", "remote_copies", "total_bytes", "items")
	schemas["BackupCloudCandidate"] = obj(object{"id": object{"type": "string", "description": "服务端签发的不透明候选标识，不是对象键"}, "file_name": str(), "size_bytes": object{"type": "integer", "format": "int64"}, "last_modified": object{"type": "string", "format": "date-time"}, "imported": object{"type": "boolean"}, "manifest_available": object{"type": "boolean"}, "status": object{"type": "string", "enum": []string{"pending", "imported"}}, "message": str()}, "id", "file_name", "size_bytes", "imported", "status")
	schemas["BackupCloudScanInput"] = obj(object{"continuation_token": secret()})
	schemas["BackupCloudScanResult"] = obj(object{"items": object{"type": "array", "items": ref("BackupCloudCandidate")}, "continuation_token": str(), "truncated": object{"type": "boolean"}}, "items", "truncated")
	schemas["BackupCloudImportInput"] = obj(object{"candidate_id": str(), "identity": secret()}, "candidate_id", "identity")
	schemas["BackupSnapshot"].(object)["properties"].(object)["reconciliation_warnings"] = object{"type": "array", "items": str()}
	for _, r := range []struct {
		path, method, summary, request, response string
		array                                    bool
	}{
		{"/status", "get", "读取备份运行状态与部署恢复能力", "", "BackupSnapshot", false},
		{"/storage", "get", "读取脱敏存储配置", "", "BackupStorage", false},
		{"/storage", "put", "幂等保存存储配置；相同配置及凭据保留修订和验证结果，实际变更才创建新修订", "BackupStorageInput", "BackupStorage", false},
		{"/storage/test", "post", "执行存储小对象写读校验与删除，返回阶段、耗时及脱敏诊断", "BackupStorageInput", "BackupStorageTestResponse", false},
		{"/schedule", "get", "读取定时备份和保留策略", "", "BackupSchedule", false},
		{"/schedule", "put", "保存备份策略，保存不立即执行", "BackupScheduleWrite", "BackupSchedule", false},
		{"/schedule/preview", "post", "预览未来三次执行时间", "BackupScheduleWrite", "BackupPreview", false},
		{"/schedule/retention-preview", "post", "保存前预览保留策略将清理的已登记副本", "BackupScheduleWrite", "BackupRetentionPreview", false},
		{"/records", "get", "分页读取备份记录", "", "BackupRun", true},
		{"/records", "post", "幂等创建异步备份", "BackupRequestID", "BackupRun", false},
		{"/records/{id}", "get", "读取单个备份状态", "", "BackupRun", false},
		{"/records/{id}/download", "get", "管理员再次验证后下载加密备份", "", "", false},
		{"/records/{id}/retry-upload", "post", "只重新上传原加密包", "", "BackupRun", false},
		{"/records/{id}/protection", "patch", "设置备份保护，解除需再次验证", "BackupProtection", "", false},
		{"/records/{id}", "delete", "管理员再次验证后删除未保护备份", "", "", false},
		{"/records/{id}/acknowledge-missing", "post", "明确确认遗失的本地副本，仅解除核对阻塞，不删除远端对象", "BackupAcknowledgeMissing", "", false},
		{"/reauth", "post", "再次验证管理员密码，当前会话五分钟有效", "BackupReauth", "", false},
		{"/keys/generate", "post", "一次性生成恢复密钥，不持久保存私钥", "", "BackupGeneratedKey", false},
		{"/keys/recipient", "put", "设置恢复公钥", "BackupRecipient", "", false},
		{"/keys/verify", "post", "临时提供私钥验证恢复公钥", "BackupIdentity", "", false},
		{"/restore/preflight", "post", "校验记录或流式导入备份，仅操作暂存数据库", "BackupPreflightInput", "BackupPreflightResult", false},
		{"/restore/apply", "post", "异步提交原生launcher数据恢复", "BackupRestoreApply", "BackupRestoreAccepted", false},
		{"/reconcile", "post", "恢复后核对备份索引，管理员再次验证", "", "", false},
		{"/cloud/scan", "post", "分页扫描当前存储前缀中的备份候选，不返回对象键或凭据", "BackupCloudScanInput", "BackupCloudScanResult", false},
		{"/cloud/import", "post", "管理员再次验证后下载并完整校验候选，仅导入受保护记录，不替换当前业务数据", "BackupCloudImportInput", "BackupRun", false},
		{"/operations/{id}", "get", "按管理员会话或单操作只读凭证查询恢复状态", "", "BackupRestoreOperation", false},
	} {
		add("/admin/backups"+r.path, r.method, r.summary, r.request, r.response, true, r.array)
	}
	base := "/admin/backups"
	connectionResponses := paths[base+"/storage/test"].(object)["post"].(object)["responses"].(object)
	for _, status := range []string{"400", "408", "502", "504"} {
		connectionResponses[status] = object{"description": "连接测试失败，保留主错误及独立清理结果；上游401/403不撤销管理员登录", "content": object{"application/json": object{"schema": ref("BackupStorageTestError")}}}
	}
	connectionResponses["400"].(object)["content"].(object)["application/json"].(object)["schema"] = object{"oneOf": []any{ref("BackupStorageTestError"), ref("Error")}}
	for _, v := range []struct{ path, method string }{{"/records", "post"}, {"/records/{id}/retry-upload", "post"}, {"/restore/apply", "post"}} {
		responses := paths[base+v.path].(object)[v.method].(object)["responses"].(object)
		responses["202"] = responses["200"]
		delete(responses, "200")
	}
	list := paths[base+"/records"].(object)["get"].(object)
	list["parameters"] = []any{object{"name": "limit", "in": "query", "schema": object{"type": "integer", "minimum": 1, "maximum": 1000, "default": 20}}, object{"name": "offset", "in": "query", "schema": object{"type": "integer", "minimum": 0, "default": 0}}}
	for _, p := range []object{
		{"name": "state", "in": "query", "schema": str(), "description": "精确筛选执行状态"},
		{"name": "trigger", "in": "query", "schema": object{"type": "string", "enum": []string{"manual", "scheduled", "imported"}}},
		{"name": "protection", "in": "query", "schema": object{"type": "string", "enum": []string{"ordinary", "monthly", "protected"}}},
		{"name": "from", "in": "query", "schema": object{"type": "string", "format": "date-time"}, "description": "UTC/RFC3339 开始时间，包含边界"},
		{"name": "to", "in": "query", "schema": object{"type": "string", "format": "date-time"}, "description": "UTC/RFC3339 结束时间，包含边界"},
	} {
		list["parameters"] = append(list["parameters"].([]any), p)
	}
	download := paths[base+"/records/{id}/download"].(object)["get"].(object)
	download["responses"].(object)["200"] = object{"description": "受鉴权的加密备份附件；不返回S3签名URL", "content": object{"application/octet-stream": object{"schema": object{"type": "string", "format": "binary"}}}}
	preflight := paths[base+"/restore/preflight"].(object)["post"].(object)
	preflight["requestBody"].(object)["content"].(object)["multipart/form-data"] = object{"schema": obj(object{"file": object{"type": "string", "format": "binary", "description": "最大3GiB，不能与record_id同时提供"}, "identity": secret(), "record_id": str()})}
	preflight["responses"].(object)["413"] = object{"description": "备份导入超过专用3GiB限制"}
	op := paths[base+"/operations/{id}"].(object)["get"].(object)
	op["security"] = []any{object{"cookieAuth": []string{}}, object{"restoreStatusToken": []string{}}}
	params := op["parameters"].([]any)
	op["parameters"] = append(params, object{"name": "X-Restore-Status-Token", "in": "header", "schema": str(), "description": "可替代登录，仅查询该操作，不得用于写操作"})
}
