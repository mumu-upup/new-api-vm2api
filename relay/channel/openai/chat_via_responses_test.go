package openai

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/relay/helper"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/relayconvert"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/model_setting"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func setupForcedReasoningTest(t *testing.T) {
	t.Helper()
	originalGinMode := gin.Mode()
	gin.SetMode(gin.TestMode)
	t.Cleanup(func() { gin.SetMode(originalGinMode) })
	settings := model_setting.GetGlobalSettings()
	original := *settings
	settings.PassThroughRequestEnabled = false
	settings.ThinkingModelBlacklist = nil
	settings.EffortTailModelIDs = nil
	t.Cleanup(func() { *settings = original })
}

func TestOpenAIForcedReasoningEffortFinalBody(t *testing.T) {
	setupForcedReasoningTest(t)
	for _, format := range []types.RelayFormat{types.RelayFormatOpenAI, types.RelayFormatOpenAIResponses} {
		for _, tc := range []struct {
			name        string
			model       string
			forced      string
			override    string
			channelType int
			want        string
		}{
			{name: "origin suffix", model: "gpt-6.1-sol-low", forced: "max", want: "max"},
			{name: "forced none beats suffix", model: "gpt-6.1-sol-high", forced: "none", want: "none"},
			{name: "set effort", forced: "max", override: "set", want: "max"},
			{name: "delete effort", forced: "max", override: "delete", want: "max"},
			{name: "delete reasoning", forced: "max", override: "delete_parent", want: "max"},
			{name: "legacy reasoning", forced: "max", override: "legacy", want: "max"},
			{name: "replace reasoning with array", forced: "max", override: "array", want: "max"},
			{name: "normalized policy", forced: " MAX \t", want: "max"},
			{name: "blank policy", forced: " \t", want: "low"},
			{name: "blank policy allows override", forced: " \t", override: "set", want: "medium"},
			{name: "unconfigured suffix", model: "gpt-6.1-sol-high", want: "high"},
			{name: "other channel", forced: "max", channelType: constant.ChannelTypeAzure, override: "set", want: "medium"},
		} {
			if tc.override == "delete_parent" && format == types.RelayFormatOpenAI {
				continue
			}
			t.Run(string(format)+"/"+tc.name, func(t *testing.T) {
				modelName := tc.model
				if modelName == "" {
					modelName = "gpt-6.1-sol"
				}
				channelType := tc.channelType
				if channelType == 0 {
					channelType = constant.ChannelTypeOpenAI
				}
				var request dto.Request
				effortPath := "reasoning_effort"
				if format == types.RelayFormatOpenAI {
					request = &dto.GeneralOpenAIRequest{Model: modelName, ReasoningEffort: "low"}
				} else {
					request = &dto.OpenAIResponsesRequest{Model: modelName, Reasoning: &dto.Reasoning{Effort: "low", Summary: "auto"}}
					effortPath = "reasoning.effort"
				}
				info := &relaycommon.RelayInfo{
					OriginModelName: modelName,
					Request:         request,
					RelayFormat:     format,
					ChannelMeta: &relaycommon.ChannelMeta{
						ChannelType:          channelType,
						UpstreamModelName:    modelName,
						ChannelOtherSettings: dto.ChannelOtherSettings{ForcedReasoningEffort: tc.forced},
					},
				}
				switch tc.override {
				case "set", "delete", "delete_parent":
					mode, path := tc.override, effortPath
					if mode == "delete_parent" {
						mode = "delete"
						if format == types.RelayFormatOpenAIResponses {
							path = "reasoning"
						}
					}
					info.ParamOverride = map[string]any{"operations": []any{map[string]any{"mode": mode, "path": path, "value": "medium"}}}
				case "legacy":
					info.ParamOverride = map[string]any{
						"reasoning_effort": "low",
						"reasoning":        map[string]any{"enabled": false, "effort": "low", "max_tokens": 1, "summary": "auto"},
					}
				case "array":
					info.ParamOverride = map[string]any{"reasoning": []any{}}
				}
				c, _ := gin.CreateTestContext(httptest.NewRecorder())
				c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
				require.NoError(t, helper.ApplyReasoningModelSuffix(c, info, request))
				var converted any
				var err error
				switch req := request.(type) {
				case *dto.GeneralOpenAIRequest:
					converted, err = (&Adaptor{}).ConvertOpenAIRequest(c, info, req)
				case *dto.OpenAIResponsesRequest:
					converted, err = (&Adaptor{}).ConvertOpenAIResponsesRequest(c, info, *req)
				}
				require.NoError(t, err)
				body, err := common.Marshal(converted)
				require.NoError(t, err)
				if len(info.ParamOverride) > 0 {
					body, err = relaycommon.ApplyParamOverrideWithRelayInfo(body, info)
					require.NoError(t, err)
				}
				assert.Equal(t, tc.want, gjson.GetBytes(body, effortPath).String(), string(body))
				assert.Equal(t, tc.want, info.ReasoningEffort)
				assert.Equal(t, modelName, info.OriginModelName)
				if tc.override == "legacy" {
					if format == types.RelayFormatOpenAI {
						assert.False(t, gjson.GetBytes(body, "reasoning").Exists(), string(body))
					} else {
						assert.False(t, gjson.GetBytes(body, "reasoning_effort").Exists(), string(body))
						assert.False(t, gjson.GetBytes(body, "reasoning.enabled").Exists(), string(body))
						assert.False(t, gjson.GetBytes(body, "reasoning.max_tokens").Exists(), string(body))
						assert.Equal(t, "auto", gjson.GetBytes(body, "reasoning.summary").String())
					}
				}
			})
		}
	}
}

func TestOpenAIForcedReasoningEffortChatViaResponses(t *testing.T) {
	setupForcedReasoningTest(t)
	for _, beforeConversion := range []bool{true, false} {
		name := "override after conversion"
		if beforeConversion {
			name = "override before conversion"
		}
		t.Run(name, func(t *testing.T) {
			request := &dto.GeneralOpenAIRequest{
				Model: "gpt-6.1-sol", ReasoningEffort: "low",
				Messages: []dto.Message{{Role: "user", Content: "hello"}},
			}
			info := &relaycommon.RelayInfo{
				OriginModelName: "gpt-6.1-sol", Request: request,
				RelayFormat: types.RelayFormatOpenAI,
				ChannelMeta: &relaycommon.ChannelMeta{
					ChannelType: constant.ChannelTypeOpenAI, UpstreamModelName: request.Model,
					ChannelOtherSettings: dto.ChannelOtherSettings{ForcedReasoningEffort: "max"},
					ParamOverride: map[string]any{
						"model": "gpt-6.1-sol-low", "reasoning_effort": "low",
						"reasoning": map[string]any{"effort": "low"},
					},
				},
			}
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
			require.NoError(t, helper.ApplyReasoningModelSuffix(c, info, request))
			if beforeConversion {
				body, err := common.Marshal(request)
				require.NoError(t, err)
				body, err = relaycommon.ApplyParamOverrideWithRelayInfo(body, info)
				require.NoError(t, err)
				request = &dto.GeneralOpenAIRequest{}
				require.NoError(t, common.Unmarshal(body, request))
			}
			result, err := service.ConvertRequest(c, info, types.RelayFormatOpenAIResponses, request)
			require.NoError(t, err)
			responses, ok := result.Value.(*dto.OpenAIResponsesRequest)
			require.True(t, ok)
			converted, err := (&Adaptor{}).ConvertOpenAIResponsesRequest(c, info, *responses)
			require.NoError(t, err)
			relaycommon.AppendRequestConversionFromRequest(info, converted)
			require.Equal(t, types.RelayFormatOpenAI, info.RelayFormat)
			require.Equal(t, types.RelayFormat(types.RelayFormatOpenAIResponses), info.GetFinalRequestRelayFormat())
			body, err := common.Marshal(converted)
			require.NoError(t, err)
			if !beforeConversion {
				body, err = relaycommon.ApplyParamOverrideWithRelayInfo(body, info)
				require.NoError(t, err)
			}
			assert.Equal(t, "max", gjson.GetBytes(body, "reasoning.effort").String(), string(body))
			assert.False(t, gjson.GetBytes(body, "reasoning_effort").Exists(), string(body))
			assert.Equal(t, "max", info.ReasoningEffort)
			if beforeConversion {
				assert.Equal(t, "gpt-6.1-sol", gjson.GetBytes(body, "model").String())
			}
		})
	}
}

func TestOpenAIForcedReasoningEffortBypass(t *testing.T) {
	setupForcedReasoningTest(t)
	settings := model_setting.GetGlobalSettings()
	for _, bypass := range []string{"channel passthrough", "global passthrough", "compact"} {
		t.Run(bypass, func(t *testing.T) {
			settings.PassThroughRequestEnabled = bypass == "global passthrough"
			request := &dto.OpenAIResponsesRequest{Model: "gpt-6.1-sol", Reasoning: &dto.Reasoning{Effort: "low"}}
			info := &relaycommon.RelayInfo{
				OriginModelName: request.Model, Request: request, RelayFormat: types.RelayFormatOpenAIResponses,
				ChannelMeta: &relaycommon.ChannelMeta{
					ChannelType: constant.ChannelTypeOpenAI, UpstreamModelName: request.Model,
					ChannelOtherSettings: dto.ChannelOtherSettings{ForcedReasoningEffort: "max"},
					ChannelSetting:       dto.ChannelSettings{PassThroughBodyEnabled: bypass == "channel passthrough"},
					ParamOverride:        map[string]any{"metadata": map[string]any{"unrelated": "kept"}},
				},
			}
			if bypass == "compact" {
				info.RelayMode = relayconstant.RelayModeResponsesCompact
			}
			require.NoError(t, helper.ApplyReasoningModelSuffix(nil, info, request))
			assert.Equal(t, "low", request.Reasoning.Effort)
			var converted any = request
			var err error
			if bypass == "compact" {
				converted, err = (&Adaptor{}).ConvertOpenAIResponsesRequest(nil, info, *request)
				require.NoError(t, err)
			}
			body, err := common.Marshal(converted)
			require.NoError(t, err)
			body, err = relaycommon.ApplyParamOverrideWithRelayInfo(body, info)
			require.NoError(t, err)
			assert.JSONEq(t, `{"model":"gpt-6.1-sol","reasoning":{"effort":"low"},"metadata":{"unrelated":"kept"}}`, string(body))
		})
	}
}

func newResponsesChatTestContext(t *testing.T, body string, isStream bool) (*gin.Context, *httptest.ResponseRecorder, *http.Response, *relaycommon.RelayInfo) {
	t.Helper()

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	c.Set(common.RequestIdKey, "responses-test")

	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
	}
	info := &relaycommon.RelayInfo{
		ChannelMeta:        &relaycommon.ChannelMeta{UpstreamModelName: "gpt-test"},
		IsStream:           isStream,
		RelayFormat:        types.RelayFormatOpenAI,
		ShouldIncludeUsage: true,
		DisablePing:        true,
	}
	return c, recorder, resp, info
}

func TestOaiResponsesToChatStreamHandlerConvertsSSEOrderAndUsage(t *testing.T) {
	oldMode := gin.Mode()
	gin.SetMode(gin.TestMode)
	t.Cleanup(func() { gin.SetMode(oldMode) })

	oldTimeout := constant.StreamingTimeout
	constant.StreamingTimeout = 30
	t.Cleanup(func() { constant.StreamingTimeout = oldTimeout })

	body := strings.Join([]string{
		`data: {"type":"response.created","response":{"id":"resp_1","model":"gpt-test","created_at":1710000000}}`,
		`data: {"type":"response.output_text.delta","delta":"hello"}`,
		`data: {"type":"response.output_item.added","output_index":1,"item":{"type":"function_call","id":"fc_1","call_id":"call_1","name":"lookup"}}`,
		`data: {"type":"response.function_call_arguments.delta","output_index":1,"delta":"{\"q\":\"x\"}"}`,
		`data: {"type":"response.completed","response":{"status":"completed","usage":{"input_tokens":2,"output_tokens":3,"total_tokens":5}}}`,
		`data: [DONE]`,
		``,
	}, "\n")

	c, recorder, resp, info := newResponsesChatTestContext(t, body, true)

	usage, err := OaiResponsesToChatStreamHandler(c, info, resp)
	require.Nil(t, err)
	require.NotNil(t, usage)
	require.Equal(t, 2, usage.PromptTokens)
	require.Equal(t, 3, usage.CompletionTokens)
	require.Equal(t, 5, usage.TotalTokens)

	got := recorder.Body.String()
	require.Equal(t, "text/event-stream", recorder.Header().Get("Content-Type"))
	require.Contains(t, got, `"role":"assistant"`)
	require.Contains(t, got, `"content":"hello"`)
	require.Contains(t, got, `"name":"lookup"`)
	require.Contains(t, got, `"arguments":"{\"q\":\"x\"}"`)
	require.Contains(t, got, `"finish_reason":"tool_calls"`)
	require.Contains(t, got, `"usage":{"prompt_tokens":2,"completion_tokens":3,"total_tokens":5`)
	require.Contains(t, got, `data: [DONE]`)
	requireOrderedSubstrings(t, got,
		`"role":"assistant"`,
		`"content":"hello"`,
		`"name":"lookup"`,
		`"arguments":"{\"q\":\"x\"}"`,
		`"finish_reason":"tool_calls"`,
		`"usage":{"prompt_tokens":2,"completion_tokens":3,"total_tokens":5`,
		`data: [DONE]`,
	)
}

func TestOaiResponsesToChatStreamHandlerConvertsClaudeSSETerminalsAndUsage(t *testing.T) {
	oldMode := gin.Mode()
	gin.SetMode(gin.TestMode)
	t.Cleanup(func() { gin.SetMode(oldMode) })

	oldTimeout := constant.StreamingTimeout
	constant.StreamingTimeout = 30
	t.Cleanup(func() { constant.StreamingTimeout = oldTimeout })

	body := strings.Join([]string{
		`data: {"type":"response.created","response":{"id":"resp_1","model":"gpt-test","created_at":1710000000}}`,
		`data: {"type":"response.output_text.delta","delta":"hello"}`,
		`data: {"type":"response.completed","response":{"status":"completed","usage":{"input_tokens":2,"output_tokens":3,"total_tokens":5}}}`,
		`data: [DONE]`,
		``,
	}, "\n")

	c, recorder, resp, info := newResponsesChatTestContext(t, body, true)
	info.RelayFormat = types.RelayFormatClaude

	usage, err := OaiResponsesToChatStreamHandler(c, info, resp)
	require.Nil(t, err)
	require.NotNil(t, usage)
	assert.Equal(t, 2, usage.PromptTokens)
	assert.Equal(t, 3, usage.CompletionTokens)
	assert.Equal(t, 5, usage.TotalTokens)

	got := recorder.Body.String()
	assert.Equal(t, "text/event-stream", recorder.Header().Get("Content-Type"))
	assert.Equal(t, 1, strings.Count(got, "event: message_start\n"))
	assert.Equal(t, 1, strings.Count(got, "event: content_block_stop\n"))
	assert.Equal(t, 1, strings.Count(got, "event: message_delta\n"))
	assert.Equal(t, 1, strings.Count(got, "event: message_stop\n"))

	messageDeltaFrame := ""
	for frame := range strings.SplitSeq(got, "\n\n") {
		if strings.HasPrefix(frame, "event: message_delta\n") {
			messageDeltaFrame = frame
			break
		}
	}
	require.NotEmpty(t, messageDeltaFrame)
	assert.Contains(t, messageDeltaFrame, `"type":"message_delta"`)
	assert.Contains(t, messageDeltaFrame, `"stop_reason":"end_turn"`)
	assert.Contains(t, messageDeltaFrame, `"input_tokens":2`)
	assert.Contains(t, messageDeltaFrame, `"output_tokens":3`)
	requireOrderedSubstrings(t, got,
		"event: message_start\n",
		"event: content_block_stop\n",
		"event: message_delta\n",
		"event: message_stop\n",
	)
}

func TestOaiResponsesToChatBufferedStreamHandlerReturnsJSONFromSSE(t *testing.T) {
	oldMode := gin.Mode()
	gin.SetMode(gin.TestMode)
	t.Cleanup(func() { gin.SetMode(oldMode) })

	body := strings.Join([]string{
		`data: {"type":"response.output_text.delta","delta":"buffered text"}`,
		`data: {"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","id":"fc_1","call_id":"call_1","name":"lookup"}}`,
		`data: {"type":"response.function_call_arguments.delta","output_index":0,"delta":"{\"q\":\"x\"}"}`,
		`data: {"type":"response.done","response":{"model":"gpt-test","status":"completed","usage":{"input_tokens":1,"output_tokens":2,"total_tokens":3}}}`,
		`data: [DONE]`,
		``,
	}, "\n")

	c, recorder, resp, info := newResponsesChatTestContext(t, body, false)

	usage, err := OaiResponsesToChatBufferedStreamHandler(c, info, resp)
	require.Nil(t, err)
	require.NotNil(t, usage)
	require.Equal(t, 3, usage.TotalTokens)

	got := recorder.Body.String()
	require.NotContains(t, got, `data:`)
	require.Contains(t, got, `"object":"chat.completion"`)
	require.Contains(t, got, `"content":"buffered text"`)
	require.Contains(t, got, `"name":"lookup"`)
	require.Contains(t, got, `"arguments":"{\"q\":\"x\"}"`)
	require.Contains(t, got, `"finish_reason":"tool_calls"`)
}

func TestOaiResponsesToChatBufferedStreamHandlerPreservesInterleavedClaudeContent(t *testing.T) {
	oldMode := gin.Mode()
	gin.SetMode(gin.TestMode)
	t.Cleanup(func() { gin.SetMode(oldMode) })

	body := strings.Join([]string{
		`data: {"type":"response.output_item.added","output_index":0,"item":{"type":"reasoning","id":"rs_1","summary":[]}}`,
		`data: {"type":"response.reasoning_summary_text.delta","output_index":0,"item_id":"rs_1","delta":"**Planning file inspection**"}`,
		`data: {"type":"response.output_item.added","output_index":1,"item":{"type":"message","id":"msg_1","role":"assistant","content":[]}}`,
		`data: {"type":"response.output_text.delta","output_index":1,"item_id":"msg_1","delta":"I’ll inspect the starter repository."}`,
		`data: {"type":"response.output_item.added","output_index":2,"item":{"type":"reasoning","id":"rs_2","summary":[]}}`,
		`data: {"type":"response.reasoning_summary_text.delta","output_index":2,"item_id":"rs_2","delta":"**Clarifying environment task requirements**"}`,
		`data: {"type":"response.output_item.added","output_index":3,"item":{"type":"message","id":"msg_2","role":"assistant","content":[]}}`,
		`data: {"type":"response.output_text.delta","output_index":3,"item_id":"msg_2","delta":"What would you like me to build?"}`,
		`data: {"type":"response.done","response":{"id":"resp_1","model":"gpt-test","status":"completed","usage":{"input_tokens":1,"output_tokens":2,"total_tokens":3}}}`,
		`data: [DONE]`,
		``,
	}, "\n")

	c, recorder, resp, info := newResponsesChatTestContext(t, body, false)
	info.RelayFormat = types.RelayFormatClaude

	usage, apiErr := OaiResponsesToChatBufferedStreamHandler(c, info, resp)
	require.Nil(t, apiErr)
	require.NotNil(t, usage)
	assert.Equal(t, 3, usage.TotalTokens)

	var claudeResponse dto.ClaudeResponse
	require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &claudeResponse))
	require.Len(t, claudeResponse.Content, 4)
	assert.Equal(t, []string{"thinking", "text", "thinking", "text"}, []string{
		claudeResponse.Content[0].Type,
		claudeResponse.Content[1].Type,
		claudeResponse.Content[2].Type,
		claudeResponse.Content[3].Type,
	})
	require.NotNil(t, claudeResponse.Content[0].Thinking)
	require.NotNil(t, claudeResponse.Content[2].Thinking)
	assert.Equal(t, "**Planning file inspection**", *claudeResponse.Content[0].Thinking)
	assert.Equal(t, "I’ll inspect the starter repository.", claudeResponse.Content[1].GetText())
	assert.Equal(t, "**Clarifying environment task requirements**", *claudeResponse.Content[2].Thinking)
	assert.Equal(t, "What would you like me to build?", claudeResponse.Content[3].GetText())
}

func TestOaiChatToResponsesStreamHandlerConvertsSSEOrderAndUsage(t *testing.T) {
	oldMode := gin.Mode()
	gin.SetMode(gin.TestMode)
	t.Cleanup(func() { gin.SetMode(oldMode) })

	oldTimeout := constant.StreamingTimeout
	constant.StreamingTimeout = 30
	t.Cleanup(func() { constant.StreamingTimeout = oldTimeout })

	body := strings.Join([]string{
		`data: {"id":"chatcmpl_1","object":"chat.completion.chunk","created":1710000000,"model":"gpt-test","choices":[{"index":0,"delta":{"role":"assistant"},"finish_reason":null}]}`,
		`data: {"id":"chatcmpl_1","object":"chat.completion.chunk","created":1710000000,"model":"gpt-test","choices":[{"index":0,"delta":{"content":"hello"},"finish_reason":null}]}`,
		`data: {"id":"chatcmpl_1","object":"chat.completion.chunk","created":1710000000,"model":"gpt-test","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"lookup"}}]},"finish_reason":null}]}`,
		`data: {"id":"chatcmpl_1","object":"chat.completion.chunk","created":1710000000,"model":"gpt-test","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"q\":\"x\"}"}}]},"finish_reason":null}]}`,
		`data: {"id":"chatcmpl_1","object":"chat.completion.chunk","created":1710000000,"model":"gpt-test","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
		`data: {"id":"chatcmpl_1","object":"chat.completion.chunk","created":1710000000,"model":"gpt-test","choices":[],"usage":{"prompt_tokens":2,"completion_tokens":3,"total_tokens":5}}`,
		`data: [DONE]`,
		``,
	}, "\n")

	c, recorder, resp, info := newResponsesChatTestContext(t, body, true)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)

	usage, err := OaiChatToResponsesStreamHandler(c, info, resp)
	require.Nil(t, err)
	require.NotNil(t, usage)
	require.Equal(t, 2, usage.PromptTokens)
	require.Equal(t, 3, usage.CompletionTokens)
	require.Equal(t, 5, usage.TotalTokens)

	got := recorder.Body.String()
	require.Equal(t, "text/event-stream", recorder.Header().Get("Content-Type"))
	require.Contains(t, got, `event: response.created`)
	require.Contains(t, got, `event: response.output_text.delta`)
	require.Contains(t, got, `"delta":"hello"`)
	require.Contains(t, got, `event: response.function_call_arguments.delta`)
	require.Contains(t, got, `"delta":"{\"q\":\"x\"}"`)
	require.Contains(t, got, `event: response.completed`)
	require.Contains(t, got, `"input_tokens":2`)
	require.Contains(t, got, `"output_tokens":3`)
	requireOrderedSubstrings(t, got,
		`event: response.created`,
		`event: response.output_item.added`,
		`event: response.output_text.delta`,
		`event: response.output_item.added`,
		`event: response.function_call_arguments.delta`,
		`event: response.output_text.done`,
		`event: response.function_call_arguments.done`,
		`event: response.completed`,
	)
}

func TestOaiChatToResponsesStreamHandlerRestoresCodexCustomToolCall(t *testing.T) {
	oldMode := gin.Mode()
	gin.SetMode(gin.TestMode)
	t.Cleanup(func() { gin.SetMode(oldMode) })

	oldTimeout := constant.StreamingTimeout
	constant.StreamingTimeout = 30
	t.Cleanup(func() { constant.StreamingTimeout = oldTimeout })

	body := strings.Join([]string{
		`data: {"id":"chatcmpl_1","object":"chat.completion.chunk","created":1710000000,"model":"gpt-test","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_exec","type":"function","function":{"name":"exec","arguments":"{\"input\":"}}]},"finish_reason":null}]}`,
		`data: {"id":"chatcmpl_1","object":"chat.completion.chunk","created":1710000000,"model":"gpt-test","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"ls -la\"}"}}]},"finish_reason":null}]}`,
		`data: {"id":"chatcmpl_1","object":"chat.completion.chunk","created":1710000000,"model":"gpt-test","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
		`data: [DONE]`,
		``,
	}, "\n")

	c, recorder, resp, info := newResponsesChatTestContext(t, body, true)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	tools, marshalErr := common.Marshal([]map[string]any{
		{"type": "custom", "name": "exec", "description": "Run a shell command", "format": map[string]any{"type": "text"}},
	})
	require.NoError(t, marshalErr)
	converted, convertErr := service.ConvertRequestByID(c, info, relayconvert.ConverterOpenAIResponsesToOpenAIChat, &dto.OpenAIResponsesRequest{
		Model: "gpt-test",
		Input: []byte(`"list files"`),
		Tools: tools,
	})
	require.NoError(t, convertErr)
	chatRequest, ok := converted.Value.(*dto.GeneralOpenAIRequest)
	require.True(t, ok)
	require.Len(t, chatRequest.Tools, 1)
	assert.Equal(t, "function", chatRequest.Tools[0].Type)
	assert.Equal(t, "exec", chatRequest.Tools[0].Function.Name)

	_, err := OaiChatToResponsesStreamHandler(c, info, resp)
	require.Nil(t, err)

	got := recorder.Body.String()
	assert.NotContains(t, got, `event: response.function_call_arguments`)
	assert.Contains(t, got, `"delta":"ls -la"`)
	assert.Contains(t, got, `"input":"ls -la"`)
	requireOrderedSubstrings(t, got,
		`event: response.output_item.added`,
		`"type":"custom_tool_call"`,
		`event: response.custom_tool_call_input.delta`,
		`event: response.custom_tool_call_input.done`,
		`event: response.output_item.done`,
		`event: response.completed`,
		`"type":"custom_tool_call"`,
	)
}

func requireOrderedSubstrings(t *testing.T, s string, parts ...string) {
	t.Helper()

	offset := 0
	for _, part := range parts {
		idx := strings.Index(s[offset:], part)
		require.NotEqualf(t, -1, idx, "missing %q after byte offset %d", part, offset)
		offset += idx + len(part)
	}
}
