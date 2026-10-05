package sub2api

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/constant"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGetRequestURLAlphaSearch(t *testing.T) {
	adaptor := &Adaptor{}
	info := &relaycommon.RelayInfo{
		ChannelMeta: &relaycommon.ChannelMeta{
			ChannelType:    constant.ChannelTypeSub2API,
			ChannelBaseUrl: "https://sub2api.example",
		},
		RequestURLPath: "/v1/alpha/search",
		RelayMode:      relayconstant.RelayModeAlphaSearch,
	}

	url, err := adaptor.GetRequestURL(info)
	require.NoError(t, err)
	assert.Equal(t, "https://sub2api.example/v1/alpha/search", url)
}

func TestGetRequestURLNormalizesVersionedBaseURL(t *testing.T) {
	adaptor := &Adaptor{}
	info := &relaycommon.RelayInfo{
		ChannelMeta: &relaycommon.ChannelMeta{
			ChannelType:    constant.ChannelTypeSub2API,
			ChannelBaseUrl: "https://sub2api.example/v1/",
		},
		RequestURLPath: "/v1/responses",
		RelayMode:      relayconstant.RelayModeResponses,
	}

	url, err := adaptor.GetRequestURL(info)

	require.NoError(t, err)
	assert.Equal(t, "https://sub2api.example/v1/responses", url)
}

func TestDoRequestUsesNormalizedSub2APIURL(t *testing.T) {
	service.InitHttpClient()
	for _, requestPath := range []string{
		"/v1/chat/completions",
		"/v1/responses",
		"/v1/responses/compact",
		"/v1/alpha/search",
		"/v1/messages?beta=true",
		"/v1beta/models/gemini-test:streamGenerateContent?alt=sse",
	} {
		t.Run(requestPath, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				assert.Equal(t, requestPath, r.URL.RequestURI())
				assert.Equal(t, "Bearer sub2api-key", r.Header.Get("Authorization"))
				if r.URL.RequestURI() != requestPath {
					w.WriteHeader(http.StatusNotFound)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_, err := io.WriteString(w, `{"ok":true}`)
				assert.NoError(t, err)
			}))
			t.Cleanup(server.Close)
			ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
			ctx.Request = httptest.NewRequest(http.MethodPost, requestPath, strings.NewReader(`{}`))
			ctx.Request.Header.Set("Content-Type", "application/json")
			info := &relaycommon.RelayInfo{
				ChannelMeta: &relaycommon.ChannelMeta{
					ChannelType:    constant.ChannelTypeSub2API,
					ChannelBaseUrl: server.URL + "/v1/",
					ApiKey:         "sub2api-key",
				},
				RequestURLPath: requestPath,
				RelayMode:      relayconstant.Path2RelayMode(requestPath),
			}
			adaptor := &Adaptor{}
			adaptor.Init(info)
			response, err := adaptor.DoRequest(ctx, info, strings.NewReader(`{}`))
			require.NoError(t, err)
			resp, ok := response.(*http.Response)
			require.True(t, ok)
			t.Cleanup(func() { _ = resp.Body.Close() })
			assert.Equal(t, http.StatusOK, resp.StatusCode)
			assert.Equal(t, server.URL+"/v1/", info.ChannelBaseUrl)
		})
	}
}

func TestSetupRequestHeaderUsesSub2APIAuthForCompatibleProtocols(t *testing.T) {
	tests := []struct {
		name          string
		relayFormat   types.RelayFormat
		wantBearer    string
		wantAPIKey    string
		wantGoogleKey string
	}{
		{
			name:        "openai",
			relayFormat: types.RelayFormatOpenAI,
			wantBearer:  "Bearer sub2api-key",
		},
		{
			name:        "claude",
			relayFormat: types.RelayFormatClaude,
			wantBearer:  "Bearer sub2api-key",
			wantAPIKey:  "sub2api-key",
		},
		{
			name:          "gemini",
			relayFormat:   types.RelayFormatGemini,
			wantBearer:    "Bearer sub2api-key",
			wantGoogleKey: "sub2api-key",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
			ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
			adaptor := &Adaptor{}
			info := &relaycommon.RelayInfo{
				ChannelMeta: &relaycommon.ChannelMeta{
					ChannelType: constant.ChannelTypeSub2API,
					ApiKey:      "sub2api-key",
				},
				RelayFormat: tt.relayFormat,
			}
			headers := http.Header{}

			err := adaptor.SetupRequestHeader(ctx, &headers, info)

			require.NoError(t, err)
			assert.Equal(t, tt.wantBearer, headers.Get("Authorization"))
			assert.Equal(t, tt.wantAPIKey, headers.Get("x-api-key"))
			assert.Equal(t, tt.wantGoogleKey, headers.Get("x-goog-api-key"))
			if tt.relayFormat == types.RelayFormatClaude {
				assert.Equal(t, "2023-06-01", headers.Get("anthropic-version"))
			}
		})
	}
}

func TestAdaptorInheritsNewAPIResponsesCompactSupport(t *testing.T) {
	adaptor := &Adaptor{}
	info := &relaycommon.RelayInfo{
		ChannelMeta: &relaycommon.ChannelMeta{
			ChannelType:    constant.ChannelTypeSub2API,
			ChannelBaseUrl: "https://sub2api.example",
		},
		RequestURLPath: "/v1/responses/compact",
		RelayMode:      relayconstant.RelayModeResponsesCompact,
	}

	url, err := adaptor.GetRequestURL(info)

	require.NoError(t, err)
	assert.Equal(t, "https://sub2api.example/v1/responses/compact", url)
	assert.Equal(t, "sub2api", adaptor.GetChannelName())
	assert.Empty(t, adaptor.GetModelList())
}

func TestConvertClaudeRequestPreservesAdaptiveThinkingForCompatibleModel(t *testing.T) {
	adaptor := &Adaptor{}
	maxTokens := uint(8192)
	temperature := 0.2
	topP := 0.99
	request := &dto.ClaudeRequest{
		Model:        "gpt-5.6-sol",
		MaxTokens:    &maxTokens,
		Temperature:  &temperature,
		TopP:         &topP,
		Thinking:     &dto.Thinking{Type: "adaptive", Display: "summarized"},
		OutputConfig: json.RawMessage(`{"effort":"xhigh","provider_option":true}`),
		Messages: []dto.ClaudeMessage{
			{Role: "user", Content: "hello"},
		},
	}
	info := &relaycommon.RelayInfo{
		OriginModelName: "gpt-5.6-sol",
		ChannelMeta: &relaycommon.ChannelMeta{
			ChannelType: constant.ChannelTypeSub2API,
		},
	}

	converted, err := adaptor.ConvertClaudeRequest(nil, info, request)

	require.NoError(t, err)
	assert.Same(t, request, converted)
	require.NotNil(t, request.Thinking)
	assert.Equal(t, "adaptive", request.Thinking.Type)
	assert.Equal(t, "summarized", request.Thinking.Display)
	assert.JSONEq(t, `{"effort":"xhigh","provider_option":true}`, string(request.OutputConfig))
	assert.Same(t, &temperature, request.Temperature)
	assert.Same(t, &topP, request.TopP)
	assert.Equal(t, "xhigh", info.ReasoningEffort)
	assert.Equal(t, "gpt-5.6-sol", info.UpstreamModelName)
}
