package ratio_setting

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestOpenAIStandardPricing keeps the legacy ratio tables in lockstep with the
// public OpenAI Standard pricing page. A ratio of 1.0 represents $2 per 1M
// input tokens in the gateway's historical quota unit.
func TestOpenAIStandardPricing(t *testing.T) {
	InitRatioSettings()

	tests := []struct {
		name    string
		input   float64
		output  float64
		cached  *float64
		created *float64
	}{
		{name: "gpt-daybreak-blue-latest", input: 4, output: 20, cached: floatPtr(0.1), created: floatPtr(1.25)},
		{name: "gpt-5.6-cyber", input: 12.5, output: 75, cached: floatPtr(0.1), created: floatPtr(1.25)},
		{name: "gpt-daybreak-red-latest", input: 12.5, output: 75, cached: floatPtr(0.1), created: floatPtr(1.25)},
		{name: "gpt-5.5", input: 5, output: 30, cached: floatPtr(0.1)},
		{name: "gpt-5.4-mini", input: 0.75, output: 4.5, cached: floatPtr(0.1)},
		{name: "gpt-5.2", input: 1.75, output: 14, cached: floatPtr(0.1)},
		{name: "gpt-5", input: 1.25, output: 10, cached: floatPtr(0.1)},
		{name: "gpt-4.1", input: 2, output: 8, cached: floatPtr(0.25)},
		{name: "gpt-4o", input: 2.5, output: 10, cached: floatPtr(0.5)},
		{name: "gpt-4o-mini", input: 0.15, output: 0.6, cached: floatPtr(0.5)},
		{name: "gpt-3.5-turbo-0125", input: 0.5, output: 1.5},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ratio, ok, _ := GetModelRatio(tt.name)
			require.True(t, ok)
			require.InDelta(t, tt.input/2, ratio, 1e-12)
			require.InDelta(t, tt.output/tt.input, GetCompletionRatio(tt.name), 1e-12)

			cached, found := GetCacheRatio(tt.name)
			if tt.cached == nil {
				require.False(t, found)
			} else {
				require.True(t, found)
				require.InDelta(t, *tt.cached, cached, 1e-12)
			}

			created, found := GetCreateCacheRatio(tt.name)
			if tt.created == nil {
				require.False(t, found)
				return
			}
			require.True(t, found)
			require.InDelta(t, *tt.created, created, 1e-12)
		})
	}
}

func TestOpenAIMultimodalPricing(t *testing.T) {
	InitRatioSettings()

	for _, tc := range []struct {
		name        string
		audioInput  float64
		audioOutput float64
		imageInput  float64
		imageOutput float64
		textOutput  float64
		cached      *float64
	}{
		{name: "gpt-realtime-2.1", audioInput: 32, audioOutput: 64, imageInput: 5, textOutput: 24},
		{name: "gpt-realtime-2.1-mini", audioInput: 10, audioOutput: 20, imageInput: 0.8, textOutput: 2.4},
		{name: "gpt-audio-1.5", audioInput: 32, audioOutput: 64, textOutput: 10},
		{name: "gpt-audio-mini", audioInput: 10, audioOutput: 20, textOutput: 2.4},
		{name: "gpt-image-2", imageInput: 8, imageOutput: 30, cached: floatPtr(0.25)},
		{name: "gpt-image-1.5", imageInput: 8, imageOutput: 32, cached: floatPtr(0.25)},
		{name: "gpt-image-1-mini", imageInput: 2.5, imageOutput: 8, cached: floatPtr(0.1)},
		{name: "gpt-image-1", imageInput: 10, imageOutput: 40, cached: floatPtr(0.25)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			base := GetModelRatioOrPanic(t, tc.name) * 2
			if tc.audioInput > 0 {
				require.InDelta(t, tc.audioInput/base, GetAudioRatio(tc.name), 1e-9)
				require.InDelta(t, tc.audioOutput/tc.audioInput, GetAudioCompletionRatio(tc.name), 1e-9)
			}
			if tc.imageInput > 0 {
				ratio, ok := GetImageRatio(tc.name)
				require.True(t, ok)
				require.InDelta(t, tc.imageInput/base, ratio, 1e-9)
			}
			if tc.cached != nil {
				cached, ok := GetCacheRatio(tc.name)
				require.True(t, ok)
				require.InDelta(t, *tc.cached, cached, 1e-9)
			}
			output := tc.textOutput
			if output == 0 {
				output = tc.imageOutput
			}
			require.InDelta(t, output/base, GetCompletionRatio(tc.name), 1e-9)
		})
	}
}

func floatPtr(value float64) *float64 {
	return &value
}

func GetModelRatioOrPanic(t *testing.T, name string) float64 {
	t.Helper()
	ratio, ok, _ := GetModelRatio(name)
	require.True(t, ok)
	return ratio
}
