package excelbps

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGetRequestURLUsesBasispointsEndpoint(t *testing.T) {
	info := &relaycommon.RelayInfo{
		ChannelMeta: &relaycommon.ChannelMeta{
			ChannelType:    constant.ChannelTypeExcelBPS,
			ChannelBaseUrl: "https://example.invalid",
		},
		RelayMode: relayconstant.RelayModeResponses,
	}

	got, err := (&Adaptor{}).GetRequestURL(info)
	require.NoError(t, err)
	assert.Equal(t, ResponsesURL, got)
}

func TestSetupRequestHeaderUsesExcelBearerAndAccountHeaders(t *testing.T) {
	headers := http.Header{}
	info := &relaycommon.RelayInfo{
		ChannelMeta: &relaycommon.ChannelMeta{
			ChannelType: constant.ChannelTypeExcelBPS,
			ApiKey:      `{"access_token":"access","account_id":"acct"}`,
		},
		IsStream: true,
	}

	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	require.NoError(t, (&Adaptor{}).SetupRequestHeader(c, &headers, info))
	assert.Equal(t, "Bearer access", headers.Get("Authorization"))
	assert.Equal(t, "acct", headers.Get("Chatgpt-Account-Id"))
	assert.Equal(t, "acct", headers.Get("X-Openai-Account-Id"))
	assert.Equal(t, "chatgpt", headers.Get("X-Basispoints-Auth-Mode"))
	assert.Equal(t, "basispoints-excel-plugin", headers.Get("X-Openai-Internal-Basispoints-Client-Product"))
	assert.Equal(t, "excel", headers.Get("X-Openai-Internal-Basispoints-Client-Agent-Profile"))
	assert.Equal(t, "text/event-stream", headers.Get("Accept"))
}

func TestConvertOpenAIResponsesRequestBuildsBPSWireRequest(t *testing.T) {
	request := dto.OpenAIResponsesRequest{
		Model:        "gpt-5.6",
		Input:        []byte(`"hello"`),
		Instructions: []byte(`"Be concise"`),
		Stream:       boolPtr(true),
		Reasoning:    &dto.Reasoning{Effort: "high"},
	}
	converted, err := (&Adaptor{}).ConvertOpenAIResponsesRequest(nil, &relaycommon.RelayInfo{RelayMode: relayconstant.RelayModeResponses}, request)
	require.NoError(t, err)
	wire, ok := converted.(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "gpt-5.6", wire["model"])
	assert.Equal(t, "explicit", wire["model_selection"])
	assert.Equal(t, true, wire["stream"])
	assert.Equal(t, false, wire["store"])
	input, ok := wire["input"].([]any)
	require.True(t, ok, "BPS needs message items instead of a bare input string")
	require.Len(t, input, 3)
	assert.Equal(t, "developer", input[0].(map[string]any)["role"])
	assert.Equal(t, "user", input[2].(map[string]any)["role"])
	assert.Equal(t, "high", wire["reasoning_effort"])
	assert.NotContains(t, wire, "instructions")
	assert.NotContains(t, wire, "reasoning")
}

func TestConvertOpenAIResponsesRequestRejectsUnsupportedBPSFeatures(t *testing.T) {
	for _, tc := range []struct {
		name    string
		request dto.OpenAIResponsesRequest
	}{
		{"tools", dto.OpenAIResponsesRequest{Tools: []byte(`[{"type":"function","name":"shell"}]`)}},
		{"previous response", dto.OpenAIResponsesRequest{PreviousResponseID: "resp_1"}},
		{"image input", dto.OpenAIResponsesRequest{Input: []byte(`[{"role":"user","content":[{"type":"input_image","image_url":"https://example.com/image.png"}]}]`)}},
		{"tool history", dto.OpenAIResponsesRequest{Input: []byte(`[{"type":"function_call_output","call_id":"call_1","output":"done"}]`)}},
		{"structured output", dto.OpenAIResponsesRequest{Text: []byte(`{"format":{"type":"json_schema","schema":{"type":"object"}}}`)}},
		{"output cap", dto.OpenAIResponsesRequest{MaxOutputTokens: common.GetPointer(uint(1))}},
		{"reasoning mode", dto.OpenAIResponsesRequest{Reasoning: &dto.Reasoning{Mode: []byte(`"heavy"`)}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.request.Model, tc.request.Stream = "gpt-5.6", boolPtr(true)
			if len(tc.request.Input) == 0 {
				tc.request.Input = []byte(`"hello"`)
			}
			_, err := (&Adaptor{}).ConvertOpenAIResponsesRequest(nil, &relaycommon.RelayInfo{RelayMode: relayconstant.RelayModeResponses}, tc.request)
			require.Error(t, err)
		})
	}
}

func TestConvertOpenAIResponsesRequestRejectsBPSParameterOverrides(t *testing.T) {
	info := &relaycommon.RelayInfo{RelayMode: relayconstant.RelayModeResponses, ChannelMeta: &relaycommon.ChannelMeta{ParamOverride: map[string]any{"stream": false}}}
	_, err := (&Adaptor{}).ConvertOpenAIResponsesRequest(nil, info, dto.OpenAIResponsesRequest{Model: "gpt-5.6", Input: []byte(`"hello"`), Stream: boolPtr(true)})
	require.Error(t, err)
}

func TestConvertOpenAIResponsesRequestRejectsNonStreamingRequests(t *testing.T) {
	_, err := (&Adaptor{}).ConvertOpenAIResponsesRequest(nil, &relaycommon.RelayInfo{RelayMode: relayconstant.RelayModeResponses}, dto.OpenAIResponsesRequest{Model: "gpt-5.6", Input: []byte(`"hello"`)})
	require.Error(t, err)
	assert.ErrorContains(t, err, "stream")
}

func boolPtr(value bool) *bool { return &value }
