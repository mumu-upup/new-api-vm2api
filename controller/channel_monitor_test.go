package controller

import (
	"io"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMonitorPreservesArtworkAcrossLargeSSEEnvelopes(t *testing.T) {
	content := `<html><svg aria-label="pelican bicycle">` + strings.Repeat(" ", 2048) + `</svg></html>`
	var response strings.Builder
	for _, character := range content {
		event, err := common.Marshal(map[string]any{
			"id":      "chatcmpl-monitor-pelican-animation",
			"object":  "chat.completion.chunk",
			"created": 1780000000,
			"model":   "monitor-model",
			"choices": []map[string]any{{"index": 0, "delta": map[string]string{"content": string(character)}}},
		})
		require.NoError(t, err)
		response.WriteString("data: ")
		response.Write(event)
		response.WriteString("\n\n")
	}
	response.WriteString("data: [DONE]\n\n")
	require.Greater(t, response.Len(), 256<<10)

	body, err := readTestResponseBody(io.NopCloser(strings.NewReader(response.String())), true)
	require.NoError(t, err)
	require.NoError(t, validateTestResponseBody(body, true, true))
	assert.Equal(t, content, extractChannelMonitorContent(body, model.ChannelMonitorArtifactHTML))
}

func TestMonitorRejectsIncompleteGeneration(t *testing.T) {
	for _, test := range []struct {
		name    string
		payload string
	}{
		{name: "OpenAI output limit", payload: `{"choices":[{"finish_reason":"length","delta":{}}]}`},
		{name: "Responses output limit", payload: `{"type":"response.incomplete","response":{"status":"incomplete","incomplete_details":{"reason":"max_output_tokens"}}}`},
		{name: "Responses failure", payload: `{"type":"response.failed","response":{"status":"failed"}}`},
		{name: "Claude output limit", payload: `{"type":"message_delta","delta":{"stop_reason":"max_tokens"}}`},
		{name: "Gemini output limit", payload: `{"candidates":[{"finishReason":"MAX_TOKENS"}]}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			response := []byte("data: {\"choices\":[{\"delta\":{\"content\":\"<html><svg>\"}}]}\n\ndata: " + test.payload + "\n\ndata: [DONE]\n\n")
			require.ErrorContains(t, validateTestResponseBody(response, true, true), "incomplete")
			assert.NoError(t, validateTestResponseBody(response, true, false), "connectivity tests may deliberately use a small output budget")
		})
	}
}

func TestChannelTestResponseRejectsOversizedBodies(t *testing.T) {
	for _, test := range []struct {
		name   string
		stream bool
		size   int
	}{
		{name: "JSON response", size: (256 << 10) + 1},
		{name: "SSE response", stream: true, size: (16 << 20) + 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			body, err := readTestResponseBody(io.NopCloser(strings.NewReader(strings.Repeat("x", test.size))), test.stream)
			require.ErrorContains(t, err, "response body exceeds")
			assert.Empty(t, body, "oversized responses must not return a partial successful artifact")
		})
	}
}

func TestMonitorPromptBuildsAStreamingRequest(t *testing.T) {
	request := buildTestRequestWithPrompt("gpt-5.6-sol", "", &model.Channel{}, false, "return a monitor artifact")
	chatRequest, ok := request.(*dto.GeneralOpenAIRequest)
	require.True(t, ok)
	require.NotNil(t, chatRequest.Stream)
	require.True(t, *chatRequest.Stream)
}

func TestChannelMonitorPollsEveryMinute(t *testing.T) {
	require.Equal(t, time.Minute, (channelMonitorHandler{}).Interval())
}

func TestExtractChannelMonitorContent(t *testing.T) {
	for _, test := range []struct {
		name         string
		body         string
		artifactType string
		want         string
	}{
		{
			name:         "openai message html",
			body:         `{"choices":[{"message":{"content":"<html><svg aria-label=\"pelican bicycle\"></svg></html>"}}]}`,
			artifactType: model.ChannelMonitorArtifactHTML,
			want:         `<html><svg aria-label="pelican bicycle"></svg></html>`,
		},
		{
			name:         "responses output text",
			body:         `{"output_text":"21"}`,
			artifactType: model.ChannelMonitorArtifactText,
			want:         "21",
		},
		{
			name:         "streamed delta content",
			body:         "data: {\"choices\":[{\"delta\":{\"content\":\"<html>\"}}]}\ndata: {\"choices\":[{\"delta\":{\"content\":\"<svg>pelican bicycle</svg></html>\"}}]}\ndata: [DONE]",
			artifactType: model.ChannelMonitorArtifactHTML,
			want:         `<html><svg>pelican bicycle</svg></html>`,
		},
		{
			name:         "responses streamed delta content",
			body:         "data: {\"type\":\"response.output_text.delta\",\"delta\":\"<html><svg>\"}\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"pelican bicycle</svg></html>\"}\ndata: [DONE]",
			artifactType: model.ChannelMonitorArtifactHTML,
			want:         `<html><svg>pelican bicycle</svg></html>`,
		},
		{
			name:         "anthropic streamed delta text",
			body:         "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"<html><svg>\"}}\n\nevent: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"pelican bicycle</svg></html>\"}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n",
			artifactType: model.ChannelMonitorArtifactHTML,
			want:         `<html><svg>pelican bicycle</svg></html>`,
		},
		{
			name:         "plain text fallback",
			body:         "答案是 21。",
			artifactType: model.ChannelMonitorArtifactText,
			want:         "答案是 21。",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			require.Equal(t, test.want, extractChannelMonitorContent([]byte(test.body), test.artifactType))
		})
	}
}

func TestExtractChannelMonitorContentBoundsArtifacts(t *testing.T) {
	body := []byte(`{"output_text":"` + strings.Repeat("x", model.ChannelMonitorMaxContent+1024) + `"}`)
	content := extractChannelMonitorContent(body, model.ChannelMonitorArtifactText)
	require.Len(t, content, model.ChannelMonitorMaxContent)
}

func TestChannelMonitorJobsRespectGroupQuestionsAndFrequency(t *testing.T) {
	channels := []*model.Channel{{Id: 1, Type: 1, Status: 1, Group: "vip,default", Models: "m1,m2"}}
	config := model.DefaultChannelMonitorConfig(model.DiscoverChannelMonitorGroups(channels))
	config.Groups[0].Enabled = false
	config.Groups[1].Model = "m2"
	config.Groups[1].QuestionIDs = []string{model.ChannelMonitorPresetCandy}
	config.Groups[1].IntervalMinutes = 10
	recent := []*model.ChannelMonitorArtifact{{Group: "OpenAI / vip", Model: "m1", PresetID: model.ChannelMonitorPresetCandy, CreatedAt: 900}}
	jobs := channelMonitorJobs(channels, config, recent, 1000, false)
	require.Len(t, jobs, 1)
	require.Equal(t, "m2", jobs[0].modelName)
	require.Equal(t, "OpenAI / vip", jobs[0].group)
	require.Len(t, channelMonitorJobs(channels, config, recent, 1500, false), 1)
	manualJobs := channelMonitorJobs(channels, config, recent, 1000, true)
	require.Len(t, manualJobs, 1)
	for _, job := range manualJobs {
		require.Equal(t, "OpenAI / vip", job.group)
		require.Equal(t, model.ChannelMonitorPresetCandy, job.question.ID)
		require.Equal(t, "m2", job.modelName)
	}
}

func TestValidateChannelMonitorConfigRejectsMissingExecutionModel(t *testing.T) {
	groups := []model.ChannelMonitorGroup{{Key: "OpenAI / vip", Models: []string{"m1"}}}
	config := model.DefaultChannelMonitorConfig(groups)
	config.Groups[0].Model = ""
	require.Error(t, model.ValidateChannelMonitorConfig(config))
}

func TestChannelMonitorCardsSeparateHistoryFromMetrics(t *testing.T) {
	channels := []*model.Channel{{Id: 1, Type: 1, Status: 1, Group: "vip,hidden", Models: "m1,m2"}}
	groups := model.DiscoverChannelMonitorGroups(channels)
	config := model.DefaultChannelMonitorConfig(groups)
	config.Groups[0].Enabled = false
	records := []*model.ChannelMonitorArtifact{{ID: 1, ChannelID: 1, Group: "vip,hidden", Model: "m1", PresetID: model.ChannelMonitorPresetCandy, CreatedAt: 100, Content: "21", Evaluation: "passed"}}
	data := channelMonitorData(config, groups, records, "", "", "90m", 10000)
	cards := data["cards"].(map[string]gin.H)
	require.Len(t, cards, 1)
	require.Equal(t, []string{"m1"}, cards["OpenAI / vip"]["models"])
	works := cards["OpenAI / vip"]["artifacts"].([]*model.ChannelMonitorArtifact)
	require.Len(t, works, 1)
	require.Equal(t, "OpenAI / vip", works[0].Group)
	require.Empty(t, data["detail"].(gin.H)["artifacts"])
	require.Empty(t, channelMonitorData(config, groups, records, "OpenAI / hidden", "", "today", 10000)["cards"])
	filtered := channelMonitorData(config, groups, records, "", "m2", "today", 10000)["cards"].(map[string]gin.H)
	require.Empty(t, filtered)
}

func TestChannelMonitorMetricsCountSuccessfulPreviews(t *testing.T) {
	channels := []*model.Channel{{Id: 1, Type: 1, Status: 1, Group: "vip", Models: "m1"}}
	groups := model.DiscoverChannelMonitorGroups(channels)
	config := model.DefaultChannelMonitorConfig(groups)
	config.Questions = []model.ChannelMonitorQuestion{{ID: "preview", Name: "Preview", Prompt: "Create HTML", ArtifactType: "html", Matcher: "none"}}
	config.Groups[0].QuestionIDs = []string{"preview"}
	records := []*model.ChannelMonitorArtifact{
		{ID: 2, ChannelID: 1, Group: "OpenAI / vip", Model: "m1", PresetID: "preview", CreatedAt: 100, Status: model.ChannelMonitorStatusSuccess, Evaluation: "unscored"},
		{ID: 1, ChannelID: 1, Group: "OpenAI / vip", Model: "m1", PresetID: "preview", CreatedAt: 90, Status: model.ChannelMonitorStatusFailed, Evaluation: "failed"},
	}
	data := channelMonitorData(config, groups, records, "", "", "today", 100)
	metrics := data["metrics"].(gin.H)
	require.Equal(t, 1, metrics["passed"])
	require.Equal(t, 50, metrics["success_rate"])
	details := data["model_details"].([]gin.H)
	require.Len(t, details, 1)
	require.Equal(t, 50, details[0]["success_rate"])
	trend := data["trend"].([]gin.H)
	require.Len(t, trend, 1)
	require.Equal(t, 50, trend[0]["success_rate"])
}
