package service

import (
	"errors"
	"net/http"
	"regexp"
	"strings"
	"unicode"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/gin-gonic/gin"
	"golang.org/x/text/unicode/norm"
)

const ErrorCodePromptAttackDetected types.ErrorCode = "prompt_attack_detected"

// These signatures target direct, composite jailbreak instructions. They are
// defense in depth, not a semantic safety classifier: encoded, indirect,
// multilingual, multimodal, and cross-message attacks can evade signatures.
var promptAttackOverride = regexp.MustCompile(`(?:^|[.!?;\n。！？；])\s*(?:please\s+)?(?:ignore|disregard|forget|override|bypass|disable)\s+(?:(?:all|any|the|your|my)\s+)*(?:(?:previous|prior|above|system|developer|safety|security)\s+)+(?:instructions?|prompts?|rules?|polic(?:y|ies)|restrictions?|guidelines?)|(?:^|[.!?;\n。！？；])\s*(?:请|請)?(?:忽略|无视|無視|绕过|繞過|覆盖|覆蓋|取消)(?:所有|全部|之前的|此前的|以上的|你的)*(?:系统|系統|安全|之前|此前)(?:指令|提示词|提示詞|限制|规则|規則)`)
var promptAttackUnrestricted = regexp.MustCompile(`(?:without|no)\s+(?:(?:any|safety|security|ethical|moral|content)\s+)*(?:restrictions?|safety filters?|safety rules?|content filters?|ethical restrictions?)|(?:answer|respond|comply|provide)[^\n.!?]{0,100}(?:unfiltered|uncensored)|(?:never|must not|do not)\s+refuse|do anything now|(?:绕过|繞過|关闭|關閉|取消|无视|無視)(?:所有|任何)?(?:安全|道德|内容|內容)(?:限制|审查|審查|过滤|過濾|规则|規則)|(?:无条件|無條件)(?:回答|服从|服從)|(?:永远|永遠)?(?:不要|不得|不能)拒绝|(?:永遠)?(?:不要|不得|不能)拒絕`)
var promptOrdinaryConstraints = regexp.MustCompile(`(?:without|no)\s+(?:any\s+)?restrictions\s+on\s+(?:length|format|style|word count)\b`)
var promptAttackPersona = regexp.MustCompile(`(?:^|[.!?;\n。！？；])\s*(?:you are now|act as|pretend to be|from now on,? you are)\s+(?:dan\b|an?\s+(?:unrestricted|uncensored|unfiltered)\s+(?:ai|assistant|model))|(?:^|[.!?;\n。！？；])\s*(?:enable|activate|enter)\s+(?:dan|jailbreak|developer)\s+mode\b|(?:^|[.!?;\n。！？；])\s*(?:你现在是|你現在是|扮演|启用|啟用|进入|進入)(?:dan|开发者模式|開發者模式|越狱模式|越獄模式)`)
var promptQuotedData = regexp.MustCompile("(?s)```.*?```|~~~.*?~~~|`[^`]*`|\"[^\"\n]*\"|“[^”]*”|「[^」]*」|『[^』]*』")
var promptQuotedExecution = regexp.MustCompile(`(?:^|[.!?;\n])\s*(?:follow|obey|execute|apply)\s+(?:these|the following|the quoted)\s+instructions|(?:^|[。！？；\n])\s*(?:遵循|执行|執行)(?:以下|下列|引号中|引號中)(?:指令|指示)`)

// CheckPromptAttackRequest runs before billing/channel selection. Marshal only
// generation DTOs; embedding/reranking inputs are data, not model instructions.
func CheckPromptAttackRequest(c *gin.Context, request dto.Request) *types.NewAPIError {
	if !common.GetEnvOrDefaultBool("PROMPT_ATTACK_GUARD_ENABLED", true) {
		return nil
	}
	switch request.(type) {
	case *dto.GeneralOpenAIRequest, *dto.ClaudeRequest, *dto.GeminiChatRequest,
		*dto.OpenAIResponsesRequest, *dto.OpenAIResponsesCompactionRequest, *dto.ImageRequest:
	default:
		return nil
	}
	body, err := common.Marshal(request)
	if err != nil {
		return types.NewErrorWithStatusCode(errors.New("could not inspect prompt instructions"), types.ErrorCodeInvalidRequest, http.StatusBadRequest, types.ErrOptionWithSkipRetry())
	}
	return CheckPromptAttackJSON(c, body)
}

// CheckPromptAttackJSON also accepts a Realtime client event. Only explicit
// protocol instruction fields are visited: never arbitrary recursive strings,
// tool outputs, tool schemas, file data, audio, image URLs, or assistant output.
// Each text block is evaluated independently to avoid combining unrelated
// instructions/history into a false match.
func CheckPromptAttackJSON(c *gin.Context, body []byte) *types.NewAPIError {
	if !common.GetEnvOrDefaultBool("PROMPT_ATTACK_GUARD_ENABLED", true) {
		return nil
	}
	var payload map[string]any
	if err := common.Unmarshal(body, &payload); err != nil {
		return types.NewErrorWithStatusCode(errors.New("could not inspect prompt instructions"), types.ErrorCodeInvalidRequest, http.StatusBadRequest, types.ErrOptionWithSkipRetry())
	}
	return checkPromptAttackPayload(c, payload)
}

// CheckPromptAttackTaskBody accepts the host-owned task-plugin body envelope.
// Recognized JSON instruction fields and textual form prompts share the same
// guard; plugin-specific metadata and opaque file references remain data.
func CheckPromptAttackTaskBody(c *gin.Context, body any) *types.NewAPIError {
	if !common.GetEnvOrDefaultBool("PROMPT_ATTACK_GUARD_ENABLED", true) {
		return nil
	}
	envelope, _ := body.(map[string]any)
	var payload map[string]any
	switch envelope["kind"] {
	case "json":
		payload, _ = envelope["value"].(map[string]any)
	case "multipart", "form":
		fields, _ := envelope["fields"].(map[string][]string)
		payload = make(map[string]any)
		for _, key := range []string{"prompt", "instructions", "instruction", "system", "input"} {
			payload[key] = fields[key]
		}
	}
	return checkPromptAttackPayload(c, payload)
}

func checkPromptAttackPayload(c *gin.Context, payload map[string]any) *types.NewAPIError {
	rule := promptAttackInRequest(payload)
	if rule == "" {
		return nil
	}
	// IDs and a fixed rule name suffice for correlation. Do not record prompt
	// fragments, request bodies, bearer credentials, or token names.
	logger.LogWarn(c, "prompt attack blocked user_id=%d token_id=%d rule=%s",
		common.GetContextKeyInt(c, constant.ContextKeyUserId), common.GetContextKeyInt(c, constant.ContextKeyTokenId), rule)
	return types.NewErrorWithStatusCode(errors.New("prompt_attack_detected: request contains a blocked instruction override"), ErrorCodePromptAttackDetected, http.StatusForbidden,
		types.ErrOptionWithSkipRetry(), types.ErrOptionWithNoRecordErrorLog())
}

func promptAttackInRequest(payload map[string]any) string {
	for _, key := range []string{"prompt", "instructions", "instruction", "system", "input"} {
		if rule := promptAttackInContent(payload[key]); rule != "" {
			return rule
		}
	}
	for _, key := range []string{"messages", "contents"} {
		messages, _ := payload[key].([]any)
		for _, message := range messages {
			if rule := promptAttackInContent(message); rule != "" {
				return rule
			}
		}
	}
	for _, key := range []string{"systemInstruction", "system_instruction"} {
		if rule := promptAttackInContent(payload[key]); rule != "" {
			return rule
		}
	}
	requests, _ := payload["requests"].([]any)
	for _, request := range requests {
		if object, ok := request.(map[string]any); ok {
			if rule := promptAttackInRequest(object); rule != "" {
				return rule
			}
		}
	}
	// Realtime envelopes carry instructions on a session or response, and
	// conversation.item.create carries a single message.
	switch payload["type"] {
	case "session.update":
		if session, ok := payload["session"].(map[string]any); ok {
			return promptAttackInContent(session["instructions"])
		}
	case "response.create":
		if response, ok := payload["response"].(map[string]any); ok {
			return promptAttackInRequest(response)
		}
	case "conversation.item.create":
		return promptAttackInContent(payload["item"])
	}
	return ""
}

func promptAttackInContent(value any) string {
	switch content := value.(type) {
	case string:
		return promptAttackInText(content)
	case []string:
		for _, text := range content {
			if rule := promptAttackInText(text); rule != "" {
				return rule
			}
		}
	case []any:
		for _, block := range content {
			if rule := promptAttackInContent(block); rule != "" {
				return rule
			}
		}
	case map[string]any:
		role, _ := content["role"].(string)
		if role != "" && role != "user" && role != "system" && role != "developer" {
			return ""
		}
		kind, _ := content["type"].(string)
		switch kind {
		case "text", "input_text":
			if text, ok := content["text"].(string); ok {
				return promptAttackInText(text)
			}
		case "", "message":
			// Gemini text parts have no type; functionResponse/inlineData are
			// deliberately not traversed.
			if text, ok := content["text"].(string); ok {
				if rule := promptAttackInText(text); rule != "" {
					return rule
				}
			}
			for _, key := range []string{"content", "parts"} {
				if rule := promptAttackInContent(content[key]); rule != "" {
					return rule
				}
			}
		}
	}
	return ""
}

func promptAttackInText(text string) string {
	text = strings.ToLower(norm.NFKC.String(text))
	text = strings.Map(func(r rune) rune {
		if unicode.Is(unicode.Cf, r) || unicode.IsControl(r) && !unicode.IsSpace(r) {
			return -1
		}
		return r
	}, text)
	if promptQuotedExecution.MatchString(text) {
		// Quoting an instruction does not exempt a direct request to execute
		// it. Turn delimiters into boundaries so its direct verbs are checked.
		text = strings.NewReplacer("\"", "\n", "`", "\n", "“", "\n", "”", "\n", ":", "\n", "：", "\n").Replace(text)
	} else {
		text = promptQuotedData.ReplaceAllString(text, " ")
		var prose strings.Builder
		for line := range strings.SplitSeq(text, "\n") {
			if !strings.HasPrefix(strings.TrimSpace(line), ">") {
				prose.WriteString(line)
				prose.WriteByte('\n')
			}
		}
		text = prose.String()
	}
	text = promptOrdinaryConstraints.ReplaceAllString(text, " ")
	if !promptAttackUnrestricted.MatchString(text) {
		return ""
	}
	if promptAttackOverride.MatchString(text) {
		return "instruction_override"
	}
	if promptAttackPersona.MatchString(text) {
		return "unrestricted_persona"
	}
	return ""
}
