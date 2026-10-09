package model

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestEvaluateChannelMonitorArtifact(t *testing.T) {
	tests := []struct {
		name    string
		preset  string
		content string
		want    string
		ok      bool
	}{
		{name: "pelican html", preset: ChannelMonitorPresetPelican, content: "<html><svg aria-label='pelican bicycle'></svg></html>", want: "passed", ok: true},
		{name: "pelican missing svg", preset: ChannelMonitorPresetPelican, content: "pelican bicycle", want: "failed"},
		{name: "candy exact answer", preset: ChannelMonitorPresetCandy, content: "21", want: "passed", ok: true},
		{name: "candy answer in sentence", preset: ChannelMonitorPresetCandy, content: "答案是 21。", want: "passed", ok: true},
		{name: "candy wrong answer", preset: ChannelMonitorPresetCandy, content: "20", want: "failed"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			evaluation, ok := EvaluateChannelMonitorArtifact(test.preset, test.content)
			require.Equal(t, test.want, evaluation)
			require.Equal(t, test.ok, ok)
		})
	}
}

func TestChannelMonitorDayStartUsesBeijingMidnight(t *testing.T) {
	instant := time.Date(2026, 10, 10, 23, 30, 0, 0, time.FixedZone("UTC", 0))
	start := time.Unix(ChannelMonitorDayStart(instant), 0)
	require.Equal(t, "2026-10-11", start.In(time.FixedZone("CST", 8*60*60)).Format("2006-01-02"))
}

func TestChannelMonitorArtifactBeforeCreateBoundsContent(t *testing.T) {
	artifact := &ChannelMonitorArtifact{Content: string(make([]byte, ChannelMonitorMaxContent+100))}
	require.NoError(t, artifact.BeforeCreate(nil))
	require.Len(t, artifact.Content, ChannelMonitorMaxContent)
	require.NotZero(t, artifact.CreatedAt)
}
