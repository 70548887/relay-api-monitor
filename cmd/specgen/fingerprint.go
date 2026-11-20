package main

import "relaylab/internal/fingerprint"

func addFingerprintContract(schemas, paths object, add func(string, string, string, string, string, bool, bool)) {
	props := func(name string) object { return schemas[name].(object)["properties"].(object) }
	enum := func(schema, field string, values ...string) { props(schema)[field].(object)["enum"] = values }
	enum("FingerprintBank", "source", "online", "local")
	enum("FingerprintAnalysis", "verdict", "match", "family", "mismatch", "unlisted", "undetermined")
	enum("FingerprintAnalysis", "level", "full", "reference", "test")
	enum("FingerprintAnalysis", "reason", "", "no_valid_answers", "not_model_like")
	count := props("FingerprintAnswerInput")["expected_count"].(object)
	count["minimum"], count["maximum"] = fingerprint.MinExpectedCount, fingerprint.MaxExpectedCount
	answers := props("FingerprintAnalyzeRequest")["answers"].(object)
	answers["minItems"], answers["maxItems"] = 1, fingerprint.MaxChallenges
	enum("Model", "fingerprint_mode", "auto", "manual", "unlisted")
	props("Model")["fingerprint_mode"].(object)["description"] = "指纹匹对的期望模型：auto 按上游模型 ID 自动匹配（省略时同此），manual 使用 fingerprint_model，unlisted 表示参考库未收录"
	questions := props("Model")["fingerprint_questions"].(object)
	questions["minimum"], questions["maximum"], questions["description"] = 1, fingerprint.MaxChallenges, "每轮检测的题数，省略时为 3"

	add("/public/fingerprint", "get", "指纹匹对：参考库状态与可选的期望模型", "", "FingerprintOverview", false, false)
	add("/public/fingerprint/challenges", "get", "指纹匹对：取一轮题目，每次随机生成", "", "FingerprintChallengeSet", false, false)
	paths["/public/fingerprint/challenges"].(object)["get"].(object)["parameters"] = []any{object{"name": "count", "in": "query", "description": "题数：3 题最准确，2 题可参考，1 题仅作测试", "schema": object{"type": "integer", "minimum": 1, "maximum": fingerprint.MaxChallenges, "default": fingerprint.MaxChallenges}}}
	add("/public/fingerprint/analyze", "post", "指纹匹对：为粘贴的 1 到 3 个回答评分，不保存；按 IP 限频", "FingerprintAnalyzeRequest", "FingerprintAnalysis", false, false)
	responses := paths["/public/fingerprint/analyze"].(object)["post"].(object)["responses"].(object)
	responses["429"] = object{"description": "请求过于频繁，Retry-After 给出重试秒数", "content": object{"application/json": object{"schema": ref("Error")}}}
	responses["503"] = object{"description": "参考库暂不可用", "content": object{"application/json": object{"schema": ref("Error")}}}
}
