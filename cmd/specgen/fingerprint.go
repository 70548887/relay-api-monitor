package main

import "relaylab/internal/fingerprint"

func addFingerprintContract(schemas, paths object, add func(string, string, string, string, string, bool, bool)) {
	props := func(name string) object { return schemas[name].(object)["properties"].(object) }
	enum := func(schema, field string, values ...string) { props(schema)[field].(object)["enum"] = values }
	enum("FingerprintBank", "source", "online", "local")
	enum("FingerprintAnalysis", "verdict", "match", "family", "mismatch", "unlisted", "undetermined")
	enum("FingerprintAnalysis", "level", "full", "reference", "test")
	enum("FingerprintAnalysis", "reason", "", "no_valid_answers", "not_model_like")
	enum("FingerprintSummary", "verdict", "match", "family", "mismatch", "unlisted", "undetermined")
	count := props("FingerprintAnswerInput")["expected_count"].(object)
	count["minimum"], count["maximum"] = fingerprint.MinExpectedCount, fingerprint.MaxExpectedCount
	answers := props("FingerprintAnalyzeRequest")["answers"].(object)
	answers["minItems"], answers["maxItems"] = 1, fingerprint.MaxChallenges
	enum("Model", "fingerprint_mode", "auto", "manual", "unlisted")
	props("Model")["fingerprint_mode"].(object)["description"] = "指纹匹对的期望模型：auto 按上游模型 ID 自动匹配（省略时同此），manual 使用 fingerprint_model，unlisted 表示参考库未收录"
	questions := props("Model")["fingerprint_questions"].(object)
	questions["minimum"], questions["maximum"], questions["description"] = 1, fingerprint.MaxChallenges, "每轮检测的题数，省略时为 3"
	enum("FingerprintModelSettings", "mode", "auto", "manual", "unlisted")
	props("FingerprintModelSettings")["mode"].(object)["description"] = "期望模型：auto 按上游模型 ID 自动匹配（省略时同此），manual 使用 model，unlisted 表示参考库未收录"
	props("FingerprintModelSettings")["model"].(object)["description"] = "manual 时必填，须为参考库中的模型 ID；其他方式下不保存"
	asked := props("FingerprintModelSettings")["questions"].(object)
	asked["minimum"], asked["maximum"], asked["description"] = 1, fingerprint.MaxChallenges, "每轮检测的题数：3 题最准确，2 题可参考，1 题仅作测试；省略时为 3"
	schemas["FingerprintModelSettingsWrite"] = writeSchema(schemas["FingerprintModelSettings"].(object), []string{})
	schemas["FingerprintCheckRequest"].(object)["required"] = []string{"connection_id", "connection_revision", "model"}
	props("FingerprintCheckRequest")["model"].(object)["description"] = "要检测的模型，即发给连接的模型 ID"
	enum("FingerprintCheckRequest", "mode", "auto", "manual", "unlisted")
	props("FingerprintCheckRequest")["mode"].(object)["description"] = "期望模型：auto 按 model 自动匹配（省略时同此），manual 使用 expected_model，unlisted 表示参考库未收录"
	props("FingerprintCheckRequest")["expected_model"].(object)["description"] = "manual 时必填，须为参考库中的模型 ID；其他方式下忽略"
	checked := props("FingerprintCheckRequest")["questions"].(object)
	checked["minimum"], checked["maximum"], checked["description"] = 1, fingerprint.MaxChallenges, "本轮的题数：3 题最准确，2 题可参考，1 题仅作测试；省略时为 3"
	props("FingerprintCheck")["errors"].(object)["description"] = "请求失败的题，按题号排列；message 是连接给出的原因，不含密钥。全部成功时为空数组"
	props("FingerprintCheck")["question_ms"].(object)["description"] = "每道题的请求用时（毫秒），顺序同题号"
	props("FingerprintCheck")["returned_model"].(object)["description"] = "连接在回答里报告的模型名，取第一道报告了模型名的题；都没有报告时为空"
	props("FingerprintCheck")["usage"].(object)["description"] = "本轮各题用量之和；连接没有报告的项为 null"

	add("/public/fingerprint", "get", "指纹匹对：参考库状态与可选的期望模型", "", "FingerprintOverview", false, false)
	add("/public/fingerprint/challenges", "get", "指纹匹对：取一轮题目，每次随机生成", "", "FingerprintChallengeSet", false, false)
	paths["/public/fingerprint/challenges"].(object)["get"].(object)["parameters"] = []any{object{"name": "count", "in": "query", "description": "题数：3 题最准确，2 题可参考，1 题仅作测试", "schema": object{"type": "integer", "minimum": 1, "maximum": fingerprint.MaxChallenges, "default": fingerprint.MaxChallenges}}}
	add("/public/fingerprint/analyze", "post", "指纹匹对：为粘贴的 1 到 3 个回答评分，不保存；按 IP 限频", "FingerprintAnalyzeRequest", "FingerprintAnalysis", false, false)
	responses := paths["/public/fingerprint/analyze"].(object)["post"].(object)["responses"].(object)
	responses["429"] = object{"description": "请求过于频繁，Retry-After 给出重试秒数", "content": object{"application/json": object{"schema": ref("Error")}}}
	responses["503"] = object{"description": "参考库暂不可用", "content": object{"application/json": object{"schema": ref("Error")}}}
	add("/public/fingerprint/results", "get", "指纹匹对：每个公开模型的结论，取最近 3 轮中多数轮次的结论；尚未检测的模型 summary 为 null", "", "FingerprintPublicResults", false, false)
	add("/public/fingerprint/models/{id}/results", "get", "指纹匹对：一个公开模型保存的检测轮次，新的在前；失败的请求只给出代码，不给出上游的报错原文", "", "FingerprintResult", false, true)

	unavailable := object{"description": "参考库暂不可用", "content": object{"application/json": object{"schema": ref("Error")}}}
	add("/test/fingerprint", "post", "指纹匹对：会员用自己保存的连接检测一个模型，一轮 1 到 3 题同时发出，按收到的回答评分；结果只返回这一次，不保存也不公开", "FingerprintCheckRequest", "FingerprintCheck", true, false)
	check := paths["/test/fingerprint"].(object)["post"].(object)
	check["security"] = []any{object{"memberCookieAuth": []string{}}}
	answered := check["responses"].(object)
	answered["200"].(object)["description"] = "本轮结果。某一题的请求失败不算接口失败：errors 列出失败的题，analysis 按收到的回答给出"
	answered["403"] = object{"description": "来源或 CSRF 验证失败，或连接已停用"}
	answered["404"] = object{"description": "连接不存在", "content": object{"application/json": object{"schema": ref("Error")}}}
	answered["409"] = object{"description": "连接在选定后改动过，connection_revision 已过期"}
	answered["429"] = object{"description": "请求过于频繁，或本小时的检测次数已用完（校验未通过而被拒绝的请求不计次；开始检测后即使连接没有答上来也计次）；Retry-After 给出重试秒数", "content": object{"application/json": object{"schema": ref("Error")}}}
	answered["503"] = unavailable
	delete(answered, "502")
	add("/admin/fingerprint", "get", "指纹匹对：参考库状态、参考模型，以及每个未归档模型的检测设置、最近一轮结果、访客看到的结论与最近一次检测任务", "", "AdminFingerprintOverview", true, false)
	add("/admin/fingerprint/models/{id}", "put", "指纹匹对：保存一个模型的期望模型、每轮题数与定时开关，不改动模型的其他配置", "FingerprintModelSettingsWrite", "FingerprintModelSettings", true, false)
	paths["/admin/fingerprint/models/{id}"].(object)["put"].(object)["responses"].(object)["503"] = unavailable
	add("/admin/fingerprint/models/{id}/results", "get", "指纹匹对：一个模型保存的检测轮次，新的在前，每个模型最多保留 30 轮", "", "FingerprintResult", true, true)
	add("/admin/fingerprint/bank/refresh", "post", "指纹匹对：重新下载在线参考库；失败时原有参考库不变，设置了本地参考库目录时不可用，已有更新在进行时返回 409", "", "FingerprintBankRefresh", true, false)
	paths["/admin/fingerprint/bank/refresh"].(object)["post"].(object)["responses"].(object)["503"] = unavailable
}
