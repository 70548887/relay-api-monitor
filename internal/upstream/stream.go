package upstream

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"time"
	"unicode/utf8"
)

var errStreamDone = errors.New("stream terminal event received")

type callbackError struct{ err error }

func (e *callbackError) Error() string { return e.err.Error() }
func (e *callbackError) Unwrap() error { return e.err }

// readSSE follows the event-stream line algorithm, including CR, CRLF and multiline data.
// The entire decoded response is bounded, so a stream of heartbeats cannot run unbounded.
func readSSE(reader io.Reader, consume func(string, string) error) error {
	r := bufio.NewReaderSize(io.LimitReader(reader, maxStreamBytes+1), 4096)
	var event string
	var lines []string
	total := 0
	eventBytes := 0
	first := true
	skipLF := false
	dispatch := func() error {
		if len(lines) == 0 {
			event = ""
			return nil
		}
		data := strings.Join(lines, "\n")
		lines = nil
		if !utf8.ValidString(data) {
			return &Error{Code: "invalid_response", Message: "Upstream stream contains invalid UTF-8"}
		}
		name := event
		event = ""
		return consume(name, data)
	}
	for {
		var line strings.Builder
		var readErr error
		for {
			b, err := r.ReadByte()
			if err != nil {
				readErr = err
				break
			}
			total++
			if total > maxStreamBytes {
				return &Error{Code: "response_stream_too_large", Message: "上游流式传输总量超过 32 MiB，已停止接收并保留部分正文"}
			}
			eventBytes++
			if eventBytes > maxEventBytes || line.Len() >= maxEventBytes {
				return &Error{Code: "response_event_too_large", Message: "上游单个 SSE 事件或数据行超过 4 MiB，已停止接收"}
			}
			if skipLF {
				skipLF = false
				if b == '\n' {
					continue
				}
			}
			if b == '\n' {
				break
			}
			if b == '\r' {
				// Dispatch CR-terminated events immediately. Looking ahead here
				// would wait for another byte even after a terminal blank line.
				skipLF = true
				break
			}
			line.WriteByte(b)
		}
		value := line.String()
		if first {
			value = strings.TrimPrefix(value, "\ufeff")
			first = false
		}
		if readErr != nil {
			if !errors.Is(readErr, io.EOF) {
				return readErr
			}
			// EOF does not dispatch an unterminated event.
			return nil
		}
		if value == "" {
			eventBytes = 0
			if err := dispatch(); err != nil {
				if errors.Is(err, errStreamDone) {
					return nil
				}
				return err
			}
			continue
		}
		if strings.HasPrefix(value, ":") {
			continue
		}
		field, data, hasColon := strings.Cut(value, ":")
		if hasColon {
			data = strings.TrimPrefix(data, " ")
		}
		switch field {
		case "event":
			event = data
		case "data":
			lines = append(lines, data)
		}
	}
}

type generationState struct {
	result        *Result
	started       time.Time
	callback      func(Event) error
	key           string
	terminal      bool
	chatFinish    bool
	chatDone      bool
	anthropicStop bool
	jsonFallback  bool
	pendingText   string
	receivedText  bool
	textBuffer    strings.Builder
}

func invalidResponse(message string) error { return &Error{Code: "invalid_response", Message: message} }

func (s *generationState) text(value string) error {
	if value == "" {
		return nil
	}
	s.receivedText = true
	if s.result.FirstTextMS == nil && strings.TrimSpace(value) != "" {
		elapsed := time.Since(s.started).Milliseconds()
		s.result.FirstTextMS = &elapsed
	}
	value = s.redact(s.pendingText + value)
	s.pendingText = ""
	if s.key != "" {
		max := len(s.key) - 1
		if max > len(value) {
			max = len(value)
		}
		for size := max; size > 0; size-- {
			if strings.HasSuffix(value, s.key[:size]) {
				s.pendingText = value[len(value)-size:]
				value = value[:len(value)-size]
				break
			}
		}
	}
	return s.appendText(value)
}

func (s *generationState) redact(value string) string {
	if s.key == "" {
		return value
	}
	return strings.ReplaceAll(value, s.key, "[redacted]")
}
func (s *generationState) flushText() error {
	value := s.pendingText
	s.pendingText = ""
	return s.appendText(value)
}
func (s *generationState) appendText(value string) error {
	if value == "" {
		return nil
	}
	exceeded := s.textBuffer.Len()+len(value) > maxTextBytes
	if exceeded {
		value = value[:maxTextBytes-s.textBuffer.Len()]
		for !utf8.ValidString(value) {
			value = value[:len(value)-1]
		}
	}
	s.textBuffer.WriteString(value)
	if s.callback != nil && value != "" {
		if err := s.callback(Event{Type: "text", Text: value, ReturnedModel: s.redact(s.result.ReturnedModel), HTTPStatus: s.result.HTTPStatus}); err != nil {
			return &callbackError{err}
		}
	}
	if exceeded {
		return &Error{Code: "response_text_too_large", Message: "生成正文超过 2 MiB，已停止接收并保留部分正文"}
	}
	return nil
}

type usageWire struct {
	Prompt        *int64 `json:"prompt_tokens"`
	Completion    *int64 `json:"completion_tokens"`
	Input         *int64 `json:"input_tokens"`
	Output        *int64 `json:"output_tokens"`
	Total         *int64 `json:"total_tokens"`
	PromptDetails *struct {
		Cached *int64 `json:"cached_tokens"`
	} `json:"prompt_tokens_details"`
	InputDetails *struct {
		Cached *int64 `json:"cached_tokens"`
	} `json:"input_tokens_details"`
	CacheRead     *int64 `json:"cache_read_input_tokens"`
	CacheCreation *int64 `json:"cache_creation_input_tokens"`
}

func (s *generationState) usage(value *usageWire) error {
	if value == nil {
		return nil
	}
	usage := &s.result.Usage
	changed := false
	set := func(target **int64, next *int64) {
		if next != nil && *next >= 0 {
			copy := *next
			*target = &copy
			changed = true
		}
	}
	set(&usage.InputTokens, value.Prompt)
	set(&usage.InputTokens, value.Input)
	set(&usage.OutputTokens, value.Completion)
	set(&usage.OutputTokens, value.Output)
	set(&usage.TotalTokens, value.Total)
	if value.PromptDetails != nil {
		set(&usage.CachedInputTokens, value.PromptDetails.Cached)
	}
	if value.InputDetails != nil {
		set(&usage.CachedInputTokens, value.InputDetails.Cached)
	}
	set(&usage.CachedInputTokens, value.CacheRead)
	set(&usage.CacheCreationInputTokens, value.CacheCreation)
	if changed && s.callback != nil {
		snapshot := *usage
		if err := s.callback(Event{Type: "usage", Usage: &snapshot, HTTPStatus: s.result.HTTPStatus, ReturnedModel: s.redact(s.result.ReturnedModel)}); err != nil {
			return &callbackError{err}
		}
	}
	return nil
}

func contentText(raw json.RawMessage) string {
	var value string
	if json.Unmarshal(raw, &value) == nil {
		return value
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &blocks) != nil {
		return ""
	}
	var result strings.Builder
	for _, block := range blocks {
		if block.Type == "text" || block.Type == "output_text" {
			result.WriteString(block.Text)
		}
	}
	return result.String()
}

type chatWire struct {
	Model   string `json:"model"`
	Choices []struct {
		Index int `json:"index"`
		Delta struct {
			Content json.RawMessage `json:"content"`
		} `json:"delta"`
		Message struct {
			Content json.RawMessage `json:"content"`
		} `json:"message"`
		FinishReason *string `json:"finish_reason"`
	} `json:"choices"`
	Usage *usageWire      `json:"usage"`
	Error json.RawMessage `json:"error"`
}

func (s *generationState) consumeChat(data []byte, full bool) error {
	var chunk chatWire
	if json.Unmarshal(data, &chunk) != nil {
		return invalidResponse("Chat returned malformed JSON")
	}
	if len(chunk.Error) > 0 && string(chunk.Error) != "null" {
		return providerError(chunk.Error, s.key, s.result.HTTPStatus)
	}
	if chunk.Model != "" {
		s.result.ReturnedModel = chunk.Model
	}
	for _, choice := range chunk.Choices {
		if choice.Index != 0 {
			continue
		}
		content := choice.Delta.Content
		if full {
			content = choice.Message.Content
		}
		text := contentText(content)
		if text != "" && s.chatFinish {
			return invalidResponse("Chat returned text after its finish reason")
		}
		if err := s.text(text); err != nil {
			return err
		}
		if choice.FinishReason != nil && *choice.FinishReason != "" {
			s.result.FinishReason = *choice.FinishReason
			s.chatFinish = true
		}
	}
	if full {
		s.terminal = s.chatFinish
	}
	return s.usage(chunk.Usage)
}

type responsesWire struct {
	Type   string     `json:"type"`
	Delta  string     `json:"delta"`
	Model  string     `json:"model"`
	Status string     `json:"status"`
	Usage  *usageWire `json:"usage"`
	Output []struct {
		Type    string          `json:"type"`
		Content json.RawMessage `json:"content"`
	} `json:"output"`
	Error             json.RawMessage `json:"error"`
	IncompleteDetails *struct {
		Reason string `json:"reason"`
	} `json:"incomplete_details"`
	Response json.RawMessage `json:"response"`
}

func (s *generationState) responseObject(data []byte, emitFull bool) error {
	var object responsesWire
	if json.Unmarshal(data, &object) != nil {
		return invalidResponse("Responses returned malformed JSON")
	}
	if object.Model != "" {
		s.result.ReturnedModel = object.Model
	}
	if err := s.usage(object.Usage); err != nil {
		return err
	}
	if len(object.Error) > 0 && string(object.Error) != "null" {
		return providerError(object.Error, s.key, s.result.HTTPStatus)
	}
	if emitFull {
		for _, output := range object.Output {
			if output.Type == "message" {
				if err := s.text(contentText(output.Content)); err != nil {
					return err
				}
			}
		}
	}
	switch object.Status {
	case "completed":
		s.terminal = true
		s.result.FinishReason = "completed"
	case "incomplete":
		s.result.FinishReason = "incomplete"
		if object.IncompleteDetails != nil {
			s.result.FinishReason = object.IncompleteDetails.Reason
		}
		return s.incomplete()
	case "failed":
		return &Error{Code: "upstream_error", Message: "Responses generation failed", HTTPStatus: s.result.HTTPStatus}
	case "cancelled":
		return &Error{Code: "canceled", Message: "Responses generation was canceled", HTTPStatus: s.result.HTTPStatus}
	}
	return nil
}

func (s *generationState) consumeResponses(event string, data []byte) error {
	var chunk responsesWire
	if json.Unmarshal(data, &chunk) != nil {
		return invalidResponse("Responses stream returned malformed JSON")
	}
	typ := chunk.Type
	if typ == "" {
		typ = event
	}
	if len(chunk.Error) > 0 && string(chunk.Error) != "null" {
		return providerError(chunk.Error, s.key, s.result.HTTPStatus)
	}
	if len(chunk.Response) > 0 && string(chunk.Response) != "null" {
		var response responsesWire
		if json.Unmarshal(chunk.Response, &response) != nil {
			return invalidResponse("Responses event contains a malformed response object")
		}
		if (typ == "response.completed") != (response.Status == "completed") {
			return invalidResponse("Responses completion event and response status do not match")
		}
		if err := s.responseObject(chunk.Response, typ == "response.completed" && !s.receivedText); err != nil {
			return err
		}
	}
	switch typ {
	case "response.output_text.delta":
		if s.terminal {
			return invalidResponse("Responses returned text after completion")
		}
		return s.text(chunk.Delta)
	case "response.completed":
		if len(chunk.Response) == 0 || string(chunk.Response) == "null" {
			return invalidResponse("Responses completion is missing its response object")
		}
		if !s.terminal {
			return invalidResponse("Responses completion has no completed status")
		}
		return errStreamDone
	case "response.incomplete":
		if s.result.FinishReason == "" {
			s.result.FinishReason = "incomplete"
		}
		return s.incomplete()
	case "response.failed":
		return &Error{Code: "upstream_error", Message: "Responses generation failed", HTTPStatus: s.result.HTTPStatus}
	case "error":
		return providerError(data, s.key, s.result.HTTPStatus)
	}
	return s.usage(chunk.Usage)
}

type anthropicWire struct {
	Type       string          `json:"type"`
	Model      string          `json:"model"`
	StopReason *string         `json:"stop_reason"`
	Usage      *usageWire      `json:"usage"`
	Content    json.RawMessage `json:"content"`
	Message    *struct {
		Model string     `json:"model"`
		Usage *usageWire `json:"usage"`
	} `json:"message"`
	Delta *struct {
		Type       string  `json:"type"`
		Text       string  `json:"text"`
		StopReason *string `json:"stop_reason"`
	} `json:"delta"`
	ContentBlock *struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content_block"`
	Error json.RawMessage `json:"error"`
}

func (s *generationState) consumeAnthropic(event string, data []byte, full bool) error {
	var chunk anthropicWire
	if json.Unmarshal(data, &chunk) != nil {
		return invalidResponse("Anthropic returned malformed JSON")
	}
	if len(chunk.Error) > 0 && string(chunk.Error) != "null" {
		return providerError(chunk.Error, s.key, s.result.HTTPStatus)
	}
	typ := chunk.Type
	if typ == "" {
		typ = event
	}
	if typ == "error" {
		return providerError(data, s.key, s.result.HTTPStatus)
	}
	if chunk.Model != "" {
		s.result.ReturnedModel = chunk.Model
	}
	if chunk.Message != nil {
		if chunk.Message.Model != "" {
			s.result.ReturnedModel = chunk.Message.Model
		}
		if err := s.usage(chunk.Message.Usage); err != nil {
			return err
		}
	}
	if err := s.usage(chunk.Usage); err != nil {
		return err
	}
	if full {
		if err := s.text(contentText(chunk.Content)); err != nil {
			return err
		}
		if chunk.StopReason != nil && *chunk.StopReason != "" {
			s.result.FinishReason = *chunk.StopReason
			s.anthropicStop = true
			s.terminal = true
		}
		return nil
	}
	switch typ {
	case "content_block_start":
		if chunk.ContentBlock != nil && chunk.ContentBlock.Type == "text" {
			if s.anthropicStop || s.terminal {
				return invalidResponse("Anthropic returned text after stopping")
			}
			return s.text(chunk.ContentBlock.Text)
		}
	case "content_block_delta":
		if chunk.Delta != nil && chunk.Delta.Type == "text_delta" {
			if s.anthropicStop || s.terminal {
				return invalidResponse("Anthropic returned text after stopping")
			}
			return s.text(chunk.Delta.Text)
		}
	case "message_delta":
		if chunk.Delta != nil && chunk.Delta.StopReason != nil && *chunk.Delta.StopReason != "" {
			s.result.FinishReason = *chunk.Delta.StopReason
			s.anthropicStop = true
		}
	case "message_stop":
		s.terminal = true
		return errStreamDone
	}
	return nil
}

func (s *generationState) consumeJSON(data []byte) error {
	s.jsonFallback = true
	if !utf8.Valid(data) {
		return invalidResponse("Upstream response contains invalid UTF-8")
	}
	switch s.result.Protocol {
	case "chat":
		return s.consumeChat(data, true)
	case "responses":
		return s.responseObject(data, true)
	case "anthropic":
		return s.consumeAnthropic("", data, true)
	}
	return invalidResponse("Unsupported protocol")
}

func (s *generationState) consumeSSE(event, data string) error {
	if event == "error" {
		var payload struct {
			Error json.RawMessage `json:"error"`
		}
		if json.Unmarshal([]byte(data), &payload) == nil && len(payload.Error) > 0 {
			return providerError(payload.Error, s.key, s.result.HTTPStatus)
		}
		return providerError([]byte(data), s.key, s.result.HTTPStatus)
	}
	if data == "[DONE]" {
		if s.result.Protocol != "chat" {
			return invalidResponse("Unexpected stream sentinel")
		}
		s.chatDone = true
		s.terminal = true
		return errStreamDone
	}
	if s.chatDone {
		return invalidResponse("Chat returned data after its stream sentinel")
	}
	switch s.result.Protocol {
	case "chat":
		return s.consumeChat([]byte(data), false)
	case "responses":
		return s.consumeResponses(event, []byte(data))
	case "anthropic":
		return s.consumeAnthropic(event, []byte(data), false)
	}
	return nil
}

func (s *generationState) incomplete() error {
	reason := s.result.FinishReason
	if reason == "length" || reason == "max_tokens" || reason == "max_output_tokens" || reason == "model_context_window_exceeded" {
		return &Error{Code: "truncated", Message: "Upstream generation reached its output limit", HTTPStatus: s.result.HTTPStatus}
	}
	return &Error{Code: "incomplete", Message: "Upstream generation did not complete", HTTPStatus: s.result.HTTPStatus}
}

func (s *generationState) finish() error {
	if err := s.flushText(); err != nil {
		return err
	}
	if !s.terminal {
		return &Error{Code: "incomplete_stream", Message: "Upstream stream ended without a terminal event", HTTPStatus: s.result.HTTPStatus}
	}
	if s.result.Protocol == "chat" && (!s.chatFinish || (!s.jsonFallback && !s.chatDone)) {
		return &Error{Code: "incomplete_stream", Message: "Chat stream requires a finish reason and terminal sentinel", HTTPStatus: s.result.HTTPStatus}
	}
	if s.result.Protocol == "anthropic" && !s.anthropicStop {
		return &Error{Code: "anthropic_missing_terminal", Message: "Anthropic stream ended without message_stop/stop_reason; the response is incomplete and must not be published", HTTPStatus: s.result.HTTPStatus}
	}
	reason := s.result.FinishReason
	if reason == "length" || reason == "max_tokens" || reason == "max_output_tokens" || reason == "content_filter" || reason == "model_context_window_exceeded" {
		return s.incomplete()
	}
	if strings.TrimSpace(s.textBuffer.String()) == "" {
		return &Error{Code: "empty_output", Message: "Upstream returned no visible text", HTTPStatus: s.result.HTTPStatus}
	}
	return nil
}
