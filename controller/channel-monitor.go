package controller

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

type channelMonitorSummary struct {
	Channels  int `json:"channels"`
	Artifacts int `json:"artifacts"`
	Passed    int `json:"passed"`
	Failed    int `json:"failed"`
}

func runChannelMonitorTask(ctx context.Context, report func(processed, total int)) (channelMonitorSummary, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	testUserID, err := resolveChannelTestUserID(nil)
	if err != nil {
		return channelMonitorSummary{}, err
	}
	channels, err := model.GetAllChannels(0, 0, true, false)
	if err != nil {
		return channelMonitorSummary{}, err
	}
	presets := model.ChannelMonitorPresets()
	dayStart := model.ChannelMonitorDayStart(time.Now())
	dayKey := time.Unix(dayStart, 0).Format("2006-01-02")
	_ = model.DeleteChannelMonitorArtifactsBefore(dayStart)
	selected := make([]*model.Channel, 0, len(channels))
	for _, channel := range channels {
		if channel == nil || channel.Status != common.ChannelStatusEnabled || len(channel.GetModels()) == 0 {
			continue
		}
		selected = append(selected, channel)
	}
	if report != nil {
		report(0, len(selected))
	}
	runID := fmt.Sprintf("%d", time.Now().UnixNano())
	summary := channelMonitorSummary{Channels: len(selected)}
	for index, channel := range selected {
		if ctx.Err() != nil {
			return summary, ctx.Err()
		}
		modelName := strings.TrimSpace(channel.GetModels()[0])
		for _, preset := range presets {
			result := testChannelWithPrompt(ctx, channel, testUserID, modelName, "", false, preset.Prompt)
			content := extractChannelMonitorContent(result.responseBody, preset.ArtifactType)
			evaluation, passed := model.EvaluateChannelMonitorArtifact(preset.ID, content)
			status := model.ChannelMonitorStatusSuccess
			errorText := ""
			if result.localErr != nil || result.newAPIError != nil {
				status = model.ChannelMonitorStatusFailed
				if result.localErr != nil {
					errorText = result.localErr.Error()
				} else {
					errorText = result.newAPIError.Error()
				}
			} else if !passed {
				status = model.ChannelMonitorStatusFailed
			}
			artifact := &model.ChannelMonitorArtifact{
				RunID:        runID,
				DayKey:       dayKey,
				ChannelID:    channel.Id,
				ChannelName:  channel.Name,
				Group:        channel.Group,
				Model:        modelName,
				PresetID:     preset.ID,
				ArtifactType: preset.ArtifactType,
				Content:      content,
				ActualModel:  result.actualModel,
				Status:       status,
				Evaluation:   evaluation,
				Error:        errorText,
				DurationMs:   result.durationMs,
			}
			if err := model.SaveChannelMonitorArtifact(artifact); err != nil {
				return summary, err
			}
			if err := model.TrimChannelMonitorArtifacts(dayKey, channel.Group, modelName, preset.ID, 10); err != nil {
				return summary, err
			}
			summary.Artifacts++
			if passed {
				summary.Passed++
			} else {
				summary.Failed++
			}
		}
		if report != nil {
			report(index+1, len(selected))
		}
	}
	return summary, nil
}

var channelMonitorHTMLPattern = regexp.MustCompile(`(?is)(<!doctype\s+html.*|<html[\s\S]*)`)

func extractChannelMonitorContent(body []byte, artifactType string) string {
	body = bytes.TrimSpace(body)
	if len(body) == 0 {
		return ""
	}
	content := string(body)
	for _, path := range []string{
		"choices.0.message.content",
		"choices.0.text",
		"output_text",
		"output.0.content.0.text",
		"content.0.text",
		"candidates.0.content.parts.0.text",
	} {
		value := gjson.GetBytes(body, path)
		if value.Exists() && value.Type == gjson.String && strings.TrimSpace(value.String()) != "" {
			content = value.String()
			break
		}
	}
	if strings.Contains(content, "data:") {
		parts := make([]string, 0)
		for line := range bytes.SplitSeq([]byte(content), []byte{'\n'}) {
			line = bytes.TrimSpace(line)
			if !bytes.HasPrefix(line, []byte("data:")) {
				continue
			}
			payload := bytes.TrimSpace(bytes.TrimPrefix(line, []byte("data:")))
			if len(payload) == 0 || bytes.Equal(payload, []byte("[DONE]")) {
				continue
			}
			for _, path := range []string{"choices.0.delta.content", "choices.0.message.content", "content.0.text", "candidates.0.content.parts.0.text", "output_text"} {
				value := gjson.GetBytes(payload, path)
				if value.Exists() && value.Type == gjson.String {
					parts = append(parts, value.String())
					break
				}
			}
		}
		if len(parts) > 0 {
			content = strings.Join(parts, "")
		}
	}
	if artifactType == model.ChannelMonitorArtifactHTML {
		if match := channelMonitorHTMLPattern.FindString(content); match != "" {
			content = match
		}
	}
	if len(content) > model.ChannelMonitorMaxContent {
		content = content[:model.ChannelMonitorMaxContent]
	}
	return strings.TrimSpace(content)
}

func channelMonitorArtifactsResponse(artifacts []*model.ChannelMonitorArtifact) gin.H {
	groups := make(map[string]gin.H)
	for _, artifact := range artifacts {
		key := artifact.Group
		if key == "" {
			key = "default"
		}
		entry, ok := groups[key]
		if !ok {
			entry = gin.H{"name": key, "artifacts": []*model.ChannelMonitorArtifact{}, "channels": map[int]string{}}
			groups[key] = entry
		}
		entry["artifacts"] = append(entry["artifacts"].([]*model.ChannelMonitorArtifact), artifact)
		channels := entry["channels"].(map[int]string)
		channels[artifact.ChannelID] = artifact.ChannelName
	}
	return gin.H{"groups": groups, "artifacts": artifacts}
}

func GetChannelMonitor(c *gin.Context) {
	since := model.ChannelMonitorDayStart(time.Now())
	if c.Query("range") == "90m" {
		since = time.Now().Add(-90 * time.Minute).Unix()
	}
	artifacts, err := model.ListChannelMonitorArtifacts(model.ChannelMonitorFilter{
		Group: c.Query("group"),
		Model: c.Query("model"),
		Since: since,
	}, 1000)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": err.Error()})
		return
	}
	channels := make(map[string]gin.H)
	models := make(map[string]bool)
	groups := make(map[string]bool)
	type modelMetric struct{ total, passed, duration int64 }
	modelMetrics := make(map[string]modelMetric)
	trend := make(map[int64]modelMetric)
	for _, artifact := range artifacts {
		groups[artifact.Group] = true
		models[artifact.Model] = true
		key := fmt.Sprintf("%s:%d", artifact.Group, artifact.ChannelID)
		card, ok := channels[key]
		if !ok {
			card = gin.H{"group": artifact.Group, "channel_id": artifact.ChannelID, "channel_name": artifact.ChannelName, "model": artifact.Model, "latest": artifact.CreatedAt, "artifacts": []*model.ChannelMonitorArtifact{}}
			channels[key] = card
		}
		card["artifacts"] = append(card["artifacts"].([]*model.ChannelMonitorArtifact), artifact)
		if artifact.CreatedAt > card["latest"].(int64) {
			card["latest"] = artifact.CreatedAt
		}
		metric := modelMetrics[artifact.Model]
		metric.total++
		metric.duration += artifact.DurationMs
		if artifact.Evaluation == "passed" {
			metric.passed++
		}
		modelMetrics[artifact.Model] = metric
		bucket := artifact.CreatedAt - artifact.CreatedAt%300
		bucketMetric := trend[bucket]
		bucketMetric.total++
		if artifact.Evaluation == "passed" {
			bucketMetric.passed++
		}
		trend[bucket] = bucketMetric
	}
	modelDetails := make([]gin.H, 0, len(modelMetrics))
	for name, metric := range modelMetrics {
		modelDetails = append(modelDetails, gin.H{"model": name, "total": metric.total, "passed": metric.passed, "success_rate": percentage(metric.passed, metric.total), "avg_duration_ms": average(metric.duration, metric.total)})
	}
	trendData := make([]gin.H, 0, len(trend))
	for bucket, metric := range trend {
		trendData = append(trendData, gin.H{"timestamp": bucket, "total": metric.total, "passed": metric.passed, "success_rate": percentage(metric.passed, metric.total)})
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": gin.H{
		"updated_at": time.Now().Unix(), "range": c.Query("range"), "groups": groups, "models": models,
		"cards": channels, "detail": channelMonitorArtifactsResponse(artifacts), "model_details": modelDetails, "trend": trendData,
		"metrics": gin.H{"total": len(artifacts), "passed": countPassed(artifacts), "success_rate": percentage(int64(countPassed(artifacts)), int64(len(artifacts)))},
	}})
}

func countPassed(artifacts []*model.ChannelMonitorArtifact) int {
	count := 0
	for _, artifact := range artifacts {
		if artifact.Evaluation == "passed" {
			count++
		}
	}
	return count
}
func percentage(value, total int64) int {
	if total <= 0 {
		return 0
	}
	return int(value * 100 / total)
}
func average(total, count int64) int64 {
	if count <= 0 {
		return 0
	}
	return total / count
}

func RunChannelMonitor(c *gin.Context) {
	task, created, err := service.EnqueueSystemTask(model.SystemTaskTypeChannelMonitor, nil)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	if !created {
		c.JSON(http.StatusConflict, gin.H{"success": false, "message": "channel monitor is already running", "data": task})
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": task})
}
