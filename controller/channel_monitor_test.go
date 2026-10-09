package controller

import (
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/model"
	"github.com/stretchr/testify/require"
)

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
