package excelbps

import (
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/relay/channel"
	"github.com/QuantumNous/new-api/relay/channel/openai"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// Adaptor sends the OpenAI Responses shape to the subscription BPS endpoint.
// BPS accepts only streaming Responses requests for this integration.
type Adaptor struct{}

type credential struct {
	AccessToken string `json:"access_token"`
	AccountID   string `json:"account_id"`
}

func (a *Adaptor) ConvertGeminiRequest(*gin.Context, *relaycommon.RelayInfo, *dto.GeminiChatRequest) (any, error) {
	return nil, errors.New("excel/bps channel: endpoint not supported")
}

func (a *Adaptor) ConvertClaudeRequest(*gin.Context, *relaycommon.RelayInfo, *dto.ClaudeRequest) (any, error) {
	return nil, errors.New("excel/bps channel: /v1/messages endpoint not supported")
}

func (a *Adaptor) ConvertAudioRequest(*gin.Context, *relaycommon.RelayInfo, dto.AudioRequest) (io.Reader, error) {
	return nil, errors.New("excel/bps channel: endpoint not supported")
}

func (a *Adaptor) ConvertImageRequest(*gin.Context, *relaycommon.RelayInfo, dto.ImageRequest) (any, error) {
	return nil, errors.New("excel/bps channel: endpoint not supported")
}

func (a *Adaptor) Init(*relaycommon.RelayInfo) {}

func (a *Adaptor) ConvertOpenAIRequest(*gin.Context, *relaycommon.RelayInfo, *dto.GeneralOpenAIRequest) (any, error) {
	return nil, errors.New("excel/bps channel: /v1/chat/completions endpoint not supported")
}

func (a *Adaptor) ConvertRerankRequest(*gin.Context, int, dto.RerankRequest) (any, error) {
	return nil, errors.New("excel/bps channel: /v1/rerank endpoint not supported")
}

func (a *Adaptor) ConvertEmbeddingRequest(*gin.Context, *relaycommon.RelayInfo, dto.EmbeddingRequest) (any, error) {
	return nil, errors.New("excel/bps channel: /v1/embeddings endpoint not supported")
}

func (a *Adaptor) ConvertOpenAIResponsesRequest(_ *gin.Context, info *relaycommon.RelayInfo, request dto.OpenAIResponsesRequest) (any, error) {
	if request.Stream == nil || !*request.Stream {
		return nil, errors.New("excel/bps channel requires stream=true")
	}
	if info != nil && info.RelayMode != relayconstant.RelayModeResponses {
		return nil, errors.New("excel/bps channel supports only Responses requests")
	}
	if info != nil && info.ChannelMeta != nil && len(info.ParamOverride) > 0 {
		return nil, errors.New("excel/bps channel does not support parameter overrides")
	}
	if request.MaxOutputTokens != nil {
		return nil, errors.New("excel/bps channel does not support max_output_tokens")
	}
	if strings.TrimSpace(request.Model) == "" {
		return nil, errors.New("excel/bps channel requires a model")
	}
	if request.PreviousResponseID != "" || len(request.Conversation) > 0 {
		return nil, errors.New("excel/bps channel requires expanded message history")
	}
	var tools []any
	if len(request.Tools) > 0 && (common.Unmarshal(request.Tools, &tools) != nil || len(tools) > 0) {
		return nil, errors.New("excel/bps channel does not support tools")
	}
	if len(request.Text) > 0 {
		var text struct {
			Format struct {
				Type string `json:"type"`
			} `json:"format"`
		}
		if common.Unmarshal(request.Text, &text) != nil || (text.Format.Type != "" && text.Format.Type != "text") {
			return nil, errors.New("excel/bps channel does not support structured output")
		}
	}
	effort := "medium"
	if request.Reasoning != nil {
		if len(request.Reasoning.Mode) > 0 && string(request.Reasoning.Mode) != `"standard"` {
			return nil, errors.New("excel/bps channel does not support this reasoning mode")
		}
		switch strings.ToLower(strings.TrimSpace(request.Reasoning.Effort)) {
		case "", "medium":
		case "none", "minimal", "low":
			effort = "low"
		case "high":
			effort = "high"
		case "xhigh", "max", "ultra":
			effort = "xhigh"
		default:
			return nil, errors.New("excel/bps channel does not support this reasoning effort")
		}
	}
	var input any
	if common.Unmarshal(request.Input, &input) != nil {
		return nil, errors.New("excel/bps channel requires text input")
	}
	var messages []any
	if text, ok := input.(string); ok {
		messages = []any{bpsTextMessage("user", text)}
	} else if items, ok := input.([]any); ok && len(items) > 0 {
		for _, value := range items {
			item, ok := value.(map[string]any)
			if !ok || (item["type"] != nil && item["type"] != "message") {
				return nil, errors.New("excel/bps channel supports text messages only")
			}
			role, _ := item["role"].(string)
			switch role {
			case "user", "assistant", "system", "developer":
			default:
				return nil, errors.New("excel/bps channel requires a valid message role")
			}
			if text, ok := item["content"].(string); ok {
				messages = append(messages, bpsTextMessage(role, text))
				continue
			}
			parts, ok := item["content"].([]any)
			if !ok || len(parts) == 0 {
				return nil, errors.New("excel/bps channel requires text content")
			}
			var content []any
			for _, part := range parts {
				p, ok := part.(map[string]any)
				if !ok || (p["type"] != "input_text" && p["type"] != "output_text") {
					return nil, errors.New("excel/bps channel supports text content only")
				}
				text, ok := p["text"].(string)
				if !ok {
					return nil, errors.New("excel/bps channel requires text content")
				}
				content = append(content, map[string]any{"type": p["type"], "text": text})
			}
			messages = append(messages, map[string]any{"type": "message", "role": role, "content": content})
		}
	} else {
		return nil, errors.New("excel/bps channel requires text or message input")
	}
	prologue := make([]any, 0, 2)
	if len(request.Instructions) > 0 {
		var instructions string
		if common.Unmarshal(request.Instructions, &instructions) != nil {
			return nil, errors.New("excel/bps channel requires text instructions")
		}
		if instructions != "" {
			prologue = append(prologue, bpsTextMessage("developer", instructions))
		}
	}
	prologue = append(prologue, bpsTextMessage("developer", "This request comes from an external Responses client. Return assistant text. Do not call Excel, Office, workbook or connector tools."))
	// BPS accepts a narrower wire schema than public Responses. Only documented
	// BPS fields are forwarded; client tool and image bridges are not implemented.
	wire := map[string]any{
		"model": request.Model, "model_selection": "explicit", "stream": true, "store": false,
		"input": append(prologue, messages...), "reasoning_effort": effort,
		"context_management": []any{map[string]any{"type": "compaction", "compact_threshold": 920000}},
		"metadata":           map[string]any{"task_id": uuid.NewString(), "turn_id": uuid.NewString(), "agent_iteration": "1"},
	}
	return wire, nil
}

func bpsTextMessage(role, text string) map[string]any {
	kind := "input_text"
	if role == "assistant" {
		kind = "output_text"
	}
	return map[string]any{"type": "message", "role": role, "content": []any{map[string]any{"type": kind, "text": text}}}
}

func (a *Adaptor) DoRequest(c *gin.Context, info *relaycommon.RelayInfo, requestBody io.Reader) (any, error) {
	response, err := channel.DoApiRequest(a, c, info, requestBody)
	if err == nil && response != nil && response.StatusCode == http.StatusUnauthorized && service.ScheduleGPTAccountRecovery(info.ChannelId, info.ApiKey) {
		c.Set("gpt_account_recovery_channel_id", info.ChannelId)
	}
	return response, err
}

func (a *Adaptor) DoResponse(c *gin.Context, resp *http.Response, info *relaycommon.RelayInfo) (any, *types.NewAPIError) {
	if info.RelayMode != relayconstant.RelayModeResponses || !info.IsStream {
		return nil, types.NewError(errors.New("excel/bps channel requires streaming Responses"), types.ErrorCodeInvalidRequest)
	}
	return openai.OaiResponsesStreamHandler(c, info, resp)
}

func (a *Adaptor) GetModelList() []string { return ModelList }

func (a *Adaptor) GetChannelName() string { return ChannelName }

func (a *Adaptor) GetRequestURL(info *relaycommon.RelayInfo) (string, error) {
	if info == nil || info.RelayMode != relayconstant.RelayModeResponses {
		return "", errors.New("excel/bps channel supports only Responses requests")
	}
	return ResponsesURL, nil
}

func (a *Adaptor) SetupRequestHeader(c *gin.Context, req *http.Header, info *relaycommon.RelayInfo) error {
	channel.SetupApiRequestHeader(info, c, req)
	var key credential
	if err := common.Unmarshal([]byte(strings.TrimSpace(info.ApiKey)), &key); err != nil {
		return errors.New("excel/bps channel: key must be a JSON object")
	}
	if strings.TrimSpace(key.AccessToken) == "" || strings.TrimSpace(key.AccountID) == "" {
		return errors.New("excel/bps channel: access_token and account_id are required")
	}
	req.Set("Authorization", "Bearer "+strings.TrimSpace(key.AccessToken))
	req.Set("Chatgpt-Account-Id", strings.TrimSpace(key.AccountID))
	req.Set("X-Openai-Account-Id", strings.TrimSpace(key.AccountID))
	req.Set("X-Basispoints-Auth-Mode", "chatgpt")
	req.Set("X-Openai-Internal-Basispoints-Client-Product", "basispoints-excel-plugin")
	req.Set("X-Openai-Internal-Basispoints-Client-Agent-Profile", "excel")
	req.Set("Content-Type", "application/json")
	if info.IsStream {
		req.Set("Accept", "text/event-stream")
	} else {
		req.Set("Accept", "application/json")
	}
	return nil
}

var _ channel.Adaptor = (*Adaptor)(nil)
