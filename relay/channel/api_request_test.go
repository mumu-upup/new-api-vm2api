package channel

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type requestCancellationAdaptor struct {
	Adaptor
	url string
}

func (a *requestCancellationAdaptor) GetRequestURL(*relaycommon.RelayInfo) (string, error) {
	return a.url, nil
}

func (*requestCancellationAdaptor) SetupRequestHeader(c *gin.Context, header *http.Header, _ *relaycommon.RelayInfo) error {
	header.Set("Content-Type", c.Request.Header.Get("Content-Type"))
	return nil
}

func TestAPIRequestsCancelUpstreamBeforeResponseHeaders(t *testing.T) {
	service.InitHttpClient()
	previousAutoDisable := common.AutomaticDisableChannelEnabled
	previousDisableRanges := operation_setting.AutomaticDisableStatusCodeRanges
	previousRetryRanges := operation_setting.AutomaticRetryStatusCodeRanges
	common.AutomaticDisableChannelEnabled = true
	operation_setting.AutomaticDisableStatusCodeRanges = []operation_setting.StatusCodeRange{{Start: 500, End: 502}}
	operation_setting.AutomaticRetryStatusCodeRanges = []operation_setting.StatusCodeRange{{Start: 500, End: 502}}
	t.Cleanup(func() {
		common.AutomaticDisableChannelEnabled = previousAutoDisable
		operation_setting.AutomaticDisableStatusCodeRanges = previousDisableRanges
		operation_setting.AutomaticRetryStatusCodeRanges = previousRetryRanges
	})
	for _, test := range []struct {
		name        string
		contentType string
		body        string
		stream      bool
		request     func(Adaptor, *gin.Context, *relaycommon.RelayInfo, io.Reader) (*http.Response, error)
	}{
		{
			name:        "JSON stream",
			contentType: "application/json",
			body:        `{"model":"test-model","input":"hello","stream":true}`,
			stream:      true,
			request:     DoApiRequest,
		},
		{
			name:        "multipart",
			contentType: "multipart/form-data; boundary=relay-test",
			body:        "--relay-test\r\nContent-Disposition: form-data; name=\"model\"\r\n\r\ntest-model\r\n--relay-test--\r\n",
			request:     DoFormRequest,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			type receivedRequest struct {
				body        string
				contentType string
				err         error
			}
			received := make(chan receivedRequest, 1)
			upstreamCanceled := make(chan struct{})
			release := make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
				body, err := io.ReadAll(r.Body)
				received <- receivedRequest{body: string(body), contentType: r.Header.Get("Content-Type"), err: err}
				select {
				case <-r.Context().Done():
					close(upstreamCanceled)
				case <-release:
				}
			}))
			defer server.Close()
			defer close(release)

			requestContext, cancel := context.WithCancel(context.Background())
			defer cancel()
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/relay", strings.NewReader(test.body)).WithContext(requestContext)
			c.Request.Header.Set("Content-Type", test.contentType)
			info := &relaycommon.RelayInfo{
				IsStream:    test.stream,
				DisablePing: true,
				ChannelMeta: &relaycommon.ChannelMeta{},
			}
			finished := make(chan error, 1)
			go func() {
				resp, err := test.request(&requestCancellationAdaptor{url: server.URL}, c, info, strings.NewReader(test.body))
				if resp != nil {
					_ = resp.Body.Close()
				}
				finished <- err
			}()

			select {
			case request := <-received:
				require.NoError(t, request.err)
				assert.Equal(t, test.body, request.body)
				assert.Equal(t, test.contentType, request.contentType)
			case <-time.After(5 * time.Second):
				t.Fatal("upstream did not receive the request")
			}
			cancel()

			select {
			case err := <-finished:
				var apiError *types.NewAPIError
				require.ErrorAs(t, err, &apiError)
				assert.Equal(t, types.ErrorCodeDoRequestFailed, apiError.GetErrorCode())
				assert.EqualError(t, apiError, "upstream error: do request failed")
				assert.True(t, types.IsSkipRetryError(apiError))
				decision := service.DecideRelayRetry(c, apiError, 1)
				assert.Equal(t, service.PolicyDecision{Action: "stop", Reason: "request_canceled", Source: "client"}, decision)
				assert.False(t, service.ShouldDisableChannel(apiError))
			case <-time.After(2 * time.Second):
				t.Fatal("relay kept waiting for upstream response headers after client cancellation")
			}
			select {
			case <-upstreamCanceled:
			case <-time.After(2 * time.Second):
				t.Fatal("upstream request was not canceled")
			}
		})
	}

	t.Run("uncanceled upstream failure remains retryable", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
			_, _ = io.Copy(io.Discard, r.Body)
			panic(http.ErrAbortHandler)
		}))
		defer server.Close()
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest(http.MethodPost, "/relay", strings.NewReader(`{}`))
		info := &relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{}}
		_, err := DoApiRequest(&requestCancellationAdaptor{url: server.URL}, c, info, strings.NewReader(`{}`))
		var apiError *types.NewAPIError
		require.ErrorAs(t, err, &apiError)
		require.NoError(t, c.Request.Context().Err())
		assert.Equal(t, types.ErrorCodeDoRequestFailed, apiError.GetErrorCode())
		assert.EqualError(t, apiError, "upstream error: do request failed")
		assert.False(t, types.IsSkipRetryError(apiError))
		assert.Equal(t, "retry", service.DecideRelayRetry(c, apiError, 1).Action)
		assert.True(t, service.ShouldDisableChannel(apiError))
	})

	for _, test := range []struct {
		name             string
		cancelBeforeRead bool
		decode           bool
		upstreamTimeout  bool
		wantRetry        string
		wantSource       string
		wantReason       string
		wantHealth       string
	}{
		{name: "client cancels JSON body", cancelBeforeRead: true, wantRetry: "stop", wantSource: "client", wantReason: "request_canceled", wantHealth: "unchanged"},
		{name: "client cancels JSON decoder", cancelBeforeRead: true, decode: true, wantRetry: "stop", wantSource: "client", wantReason: "request_canceled", wantHealth: "unchanged"},
		{name: "upstream disconnects JSON body", wantRetry: "retry", wantSource: "upstream", wantReason: "upstream_failure", wantHealth: "channel_disable_requested"},
		{name: "upstream body times out with active caller", upstreamTimeout: true, wantRetry: "retry", wantSource: "upstream", wantReason: "upstream_failure", wantHealth: "channel_disable_requested"},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, `{"id":`)
				w.(http.Flusher).Flush()
				<-r.Context().Done()
			}))
			defer server.Close()
			defer server.CloseClientConnections()
			if test.upstreamTimeout {
				client := service.GetHttpClient()
				previousTimeout := client.Timeout
				client.Timeout = time.Second
				defer func() { client.Timeout = previousTimeout }()
			}
			requestContext, cancel := context.WithCancel(context.Background())
			defer cancel()
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{}`)).WithContext(requestContext)
			c.Set("auto_ban", true)
			info := &relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{}}
			resp, err := DoApiRequest(&requestCancellationAdaptor{url: server.URL}, c, info, strings.NewReader(`{}`))
			require.NoError(t, err)
			defer resp.Body.Close()
			assert.Equal(t, http.StatusOK, resp.StatusCode)
			started, finished := make(chan struct{}), make(chan error, 1)
			go func() {
				close(started)
				if test.decode {
					var payload any
					finished <- common.DecodeJson(resp.Body, &payload)
					return
				}
				_, readErr := io.ReadAll(resp.Body)
				finished <- readErr
			}()
			<-started
			if test.cancelBeforeRead {
				cancel()
			} else if !test.upstreamTimeout {
				server.CloseClientConnections()
			}
			var readErr error
			select {
			case readErr = <-finished:
				require.Error(t, readErr)
			case <-time.After(5 * time.Second):
				t.Fatal("reading the upstream JSON body did not stop")
			}
			if test.cancelBeforeRead {
				require.ErrorIs(t, readErr, context.Canceled)
			} else {
				require.NoError(t, requestContext.Err())
			}
			if test.upstreamTimeout {
				require.ErrorIs(t, readErr, context.DeadlineExceeded)
			}

			// Buffered Responses handlers turn this real body-read failure into
			// the same API error before shared retry and channel-health handling.
			errorCode, status := types.ErrorCodeReadResponseBodyFailed, http.StatusInternalServerError
			if test.decode {
				errorCode, status = types.ErrorCodeBadResponseBody, http.StatusBadGateway
			}
			apiError := types.NewOpenAIError(readErr, errorCode, status)
			decision := service.DecideRelayRetry(c, apiError, 1)
			assert.Equal(t, test.wantRetry, decision.Action)
			assert.Equal(t, test.wantHealth == "channel_disable_requested", service.ShouldDisableChannelForRequest(c, apiError))
			service.RecordPolicyFailure(c, 7, apiError, decision)
			events := service.RequestPolicy(c).Events()
			require.NotEmpty(t, events)
			assert.Equal(t, test.wantSource, events[0].ErrorSource)
			assert.Equal(t, test.wantReason, events[0].Decision.Reason)
			assert.Equal(t, test.wantHealth, events[len(events)-1].Health)
		})
	}

	t.Run("completed upstream rejection keeps health policy after client cancellation", func(t *testing.T) {
		previous := operation_setting.AutomaticDisableStatusCodeRanges
		operation_setting.AutomaticDisableStatusCodeRanges = []operation_setting.StatusCodeRange{{Start: 401, End: 401}, {Start: 429, End: 429}}
		defer func() { operation_setting.AutomaticDisableStatusCodeRanges = previous }()
		for _, status := range []int{http.StatusUnauthorized, http.StatusTooManyRequests} {
			requestContext, cancel := context.WithCancel(context.Background())
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil).WithContext(requestContext)
			c.Set("auto_ban", true)
			apiError := types.NewOpenAIError(errors.New("upstream rejected request"), types.ErrorCodeBadResponseStatusCode, status)
			cancel()
			decision := service.DecideRelayRetry(c, apiError, 1)
			assert.Equal(t, "stop", decision.Action)
			assert.True(t, service.ShouldDisableChannelForRequest(c, apiError))
			service.RecordPolicyFailure(c, 7, apiError, decision)
			events := service.RequestPolicy(c).Events()
			require.Len(t, events, 2)
			assert.Equal(t, "upstream", events[0].ErrorSource)
			assert.Equal(t, "upstream_failure", events[0].Decision.Reason)
			assert.Equal(t, "channel_disable_requested", events[1].Health)
		}
	})
}

func TestNewTaskAPIRequestInheritsClientCancellation(t *testing.T) {
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	requestContext, cancel := context.WithCancel(context.Background())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil).WithContext(requestContext)

	upstream, err := newTaskAPIRequest(c, "https://provider.example/tasks", nil)
	require.NoError(t, err)
	cancel()

	require.ErrorIs(t, upstream.Context().Err(), context.Canceled)
}

func TestProcessHeaderOverride_ChannelTestSkipsPassthroughRules(t *testing.T) {
	t.Parallel()

	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	ctx.Request.Header.Set("X-Trace-Id", "trace-123")

	info := &relaycommon.RelayInfo{
		IsChannelTest: true,
		ChannelMeta: &relaycommon.ChannelMeta{
			HeadersOverride: map[string]any{
				"*": "",
			},
		},
	}

	headers, err := processHeaderOverride(info, ctx)
	require.NoError(t, err)
	require.Empty(t, headers)
}

func TestProcessHeaderOverride_ChannelTestSkipsClientHeaderPlaceholder(t *testing.T) {
	t.Parallel()

	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	ctx.Request.Header.Set("X-Trace-Id", "trace-123")

	info := &relaycommon.RelayInfo{
		IsChannelTest: true,
		ChannelMeta: &relaycommon.ChannelMeta{
			HeadersOverride: map[string]any{
				"X-Upstream-Trace": "{client_header:X-Trace-Id}",
			},
		},
	}

	headers, err := processHeaderOverride(info, ctx)
	require.NoError(t, err)
	_, ok := headers["x-upstream-trace"]
	require.False(t, ok)
}

func TestProcessHeaderOverride_NonTestKeepsClientHeaderPlaceholder(t *testing.T) {
	t.Parallel()

	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	ctx.Request.Header.Set("X-Trace-Id", "trace-123")

	info := &relaycommon.RelayInfo{
		IsChannelTest: false,
		ChannelMeta: &relaycommon.ChannelMeta{
			HeadersOverride: map[string]any{
				"X-Upstream-Trace": "{client_header:X-Trace-Id}",
			},
		},
	}

	headers, err := processHeaderOverride(info, ctx)
	require.NoError(t, err)
	require.Equal(t, "trace-123", headers["x-upstream-trace"])
}

func TestProcessHeaderOverride_RuntimeOverrideIsFinalHeaderMap(t *testing.T) {
	t.Parallel()

	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)

	info := &relaycommon.RelayInfo{
		IsChannelTest:             false,
		UseRuntimeHeadersOverride: true,
		RuntimeHeadersOverride: map[string]any{
			"x-static":  "runtime-value",
			"x-runtime": "runtime-only",
		},
		ChannelMeta: &relaycommon.ChannelMeta{
			HeadersOverride: map[string]any{
				"X-Static": "legacy-value",
				"X-Legacy": "legacy-only",
			},
		},
	}

	headers, err := processHeaderOverride(info, ctx)
	require.NoError(t, err)
	require.Equal(t, "runtime-value", headers["x-static"])
	require.Equal(t, "runtime-only", headers["x-runtime"])
	_, exists := headers["x-legacy"]
	require.False(t, exists)
}

func TestProcessHeaderOverride_PassthroughSkipsAcceptEncoding(t *testing.T) {
	t.Parallel()

	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	ctx.Request.Header.Set("X-Trace-Id", "trace-123")
	ctx.Request.Header.Set("Accept-Encoding", "gzip")

	info := &relaycommon.RelayInfo{
		IsChannelTest: false,
		ChannelMeta: &relaycommon.ChannelMeta{
			HeadersOverride: map[string]any{
				"*": "",
			},
		},
	}

	headers, err := processHeaderOverride(info, ctx)
	require.NoError(t, err)
	require.Equal(t, "trace-123", headers["x-trace-id"])

	_, hasAcceptEncoding := headers["accept-encoding"]
	require.False(t, hasAcceptEncoding)
}

func TestProcessHeaderOverride_PassHeadersTemplateSetsRuntimeHeaders(t *testing.T) {
	t.Parallel()

	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	ctx.Request.Header.Set("Originator", "Codex CLI")
	ctx.Request.Header.Set("Session_id", "sess-123")

	info := &relaycommon.RelayInfo{
		IsChannelTest: false,
		RequestHeaders: map[string]string{
			"Originator": "Codex CLI",
			"Session_id": "sess-123",
		},
		ChannelMeta: &relaycommon.ChannelMeta{
			ParamOverride: map[string]any{
				"operations": []any{
					map[string]any{
						"mode":  "pass_headers",
						"value": []any{"Originator", "Session_id", "X-Codex-Beta-Features"},
					},
				},
			},
			HeadersOverride: map[string]any{
				"X-Static": "legacy-value",
			},
		},
	}

	_, err := relaycommon.ApplyParamOverrideWithRelayInfo([]byte(`{"model":"gpt-4.1"}`), info)
	require.NoError(t, err)
	require.True(t, info.UseRuntimeHeadersOverride)
	require.Equal(t, "Codex CLI", info.RuntimeHeadersOverride["originator"])
	require.Equal(t, "sess-123", info.RuntimeHeadersOverride["session_id"])
	_, exists := info.RuntimeHeadersOverride["x-codex-beta-features"]
	require.False(t, exists)
	require.Equal(t, "legacy-value", info.RuntimeHeadersOverride["x-static"])

	headers, err := processHeaderOverride(info, ctx)
	require.NoError(t, err)
	require.Equal(t, "Codex CLI", headers["originator"])
	require.Equal(t, "sess-123", headers["session_id"])
	_, exists = headers["x-codex-beta-features"]
	require.False(t, exists)

	upstreamReq := httptest.NewRequest(http.MethodPost, "https://example.com/v1/responses", nil)
	applyHeaderOverrideToRequest(upstreamReq, headers)
	require.Equal(t, "Codex CLI", upstreamReq.Header.Get("Originator"))
	require.Equal(t, "sess-123", upstreamReq.Header.Get("Session_id"))
	require.Empty(t, upstreamReq.Header.Get("X-Codex-Beta-Features"))
}

func TestToWebSocketURL(t *testing.T) {
	for input, want := range map[string]string{
		"https://api.openai.com/v1/responses":             "wss://api.openai.com/v1/responses",
		"http://127.0.0.1:3000/v1/responses":              "ws://127.0.0.1:3000/v1/responses",
		"wss://chatgpt.com/backend-api/codex/responses":   "wss://chatgpt.com/backend-api/codex/responses",
		"ws://127.0.0.1:3000/backend-api/codex/responses": "ws://127.0.0.1:3000/backend-api/codex/responses",
	} {
		assert.Equal(t, want, toWebSocketURL(input), input)
	}
}
