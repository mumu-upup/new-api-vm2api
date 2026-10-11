package controller

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"regexp"
	"slices"
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

type channelMonitorJob struct {
	channel   *model.Channel
	group     string
	modelName string
	question  model.ChannelMonitorQuestion
}

func channelMonitorJobs(channels []*model.Channel, config model.ChannelMonitorConfig, recent []*model.ChannelMonitorArtifact, now int64, manual bool) []channelMonitorJob {
	latest := map[string]int64{}
	for _, artifact := range recent {
		key := artifact.Group + "\x00" + artifact.Model + "\x00" + artifact.PresetID
		latest[key] = max(latest[key], artifact.CreatedAt)
	}
	questions := map[string]model.ChannelMonitorQuestion{}
	for _, q := range append(model.ChannelMonitorBuiltinQuestions(), config.Questions...) {
		questions[q.ID] = q
	}
	configs := map[string]model.ChannelMonitorGroupConfig{}
	for _, g := range config.Groups {
		configs[g.Key] = g
	}
	jobs := []channelMonitorJob{}
	for _, descriptor := range model.DiscoverChannelMonitorGroups(channels) {
		group := configs[descriptor.Key]
		if !group.Enabled {
			continue
		}
		modelNames := descriptor.Models
		if group.Model != "" {
			modelNames = []string{group.Model}
		}
		for _, name := range modelNames {
			for _, id := range group.QuestionIDs {
				q, exists := questions[id]
				if !exists {
					continue
				}
				key := group.Key + "\x00" + name + "\x00" + id
				if last := latest[key]; !manual && last > 0 && now-last < int64(group.IntervalMinutes)*60 {
					continue
				}
				for _, channel := range channels {
					if !slices.Contains(descriptor.ChannelIDs, channel.Id) {
						continue
					}
					if !slices.ContainsFunc(channel.GetModels(), func(value string) bool { return strings.TrimSpace(value) == name }) {
						continue
					}
					jobs = append(jobs, channelMonitorJob{channel: channel, group: group.Key, modelName: name, question: q})
				}
			}
		}
	}
	return jobs
}

func runChannelMonitorTask(ctx context.Context, manual bool, report func(processed, total int)) (channelMonitorSummary, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	channels, err := model.GetAllChannels(0, 0, true, false)
	if err != nil {
		return channelMonitorSummary{}, err
	}
	config, err := model.LoadChannelMonitorConfig(model.DiscoverChannelMonitorGroups(channels))
	if err != nil {
		return channelMonitorSummary{}, err
	}
	now := time.Now()
	dayStart := model.ChannelMonitorDayStart(now)
	dayKey := time.Unix(dayStart, 0).In(time.FixedZone("CST", 8*60*60)).Format("2006-01-02")
	recent, err := model.LatestChannelMonitorRuns(0)
	if err != nil {
		return channelMonitorSummary{}, err
	}
	recent, err = model.PreserveChannelMonitorRuns(recent, now.Unix())
	if err != nil {
		return channelMonitorSummary{}, err
	}
	if err := model.DeleteChannelMonitorArtifactsBefore(dayStart); err != nil {
		return channelMonitorSummary{}, err
	}
	jobs := channelMonitorJobs(channels, config, recent, now.Unix(), manual)
	summary := channelMonitorSummary{}
	if report != nil {
		report(0, len(jobs))
	}
	if len(jobs) == 0 {
		return summary, nil
	}
	testUserID, err := resolveChannelTestUserID(nil)
	if err != nil {
		return summary, err
	}
	runID := fmt.Sprintf("%d", time.Now().UnixNano())
	seen := map[int]bool{}
	for index, job := range jobs {
		if err := ctx.Err(); err != nil {
			return summary, err
		}
		result := testChannelWithPrompt(ctx, job.channel, testUserID, job.modelName, "", false, job.question.Prompt)
		content := extractChannelMonitorContent(result.responseBody, job.question.ArtifactType)
		evaluation, passed := model.EvaluateChannelMonitorQuestion(job.question, content)
		status, errorText := model.ChannelMonitorStatusSuccess, ""
		if result.localErr != nil || result.newAPIError != nil {
			status, evaluation, passed = model.ChannelMonitorStatusFailed, "failed", false
			if result.localErr != nil {
				errorText = result.localErr.Error()
			} else {
				errorText = result.newAPIError.Error()
			}
		} else if !passed {
			status = model.ChannelMonitorStatusFailed
		}
		artifact := &model.ChannelMonitorArtifact{RunID: runID, DayKey: dayKey, ChannelID: job.channel.Id, ChannelName: job.channel.Name, Group: job.group, Model: job.modelName, PresetID: job.question.ID, ArtifactType: job.question.ArtifactType, Content: content, ActualModel: result.actualModel, Status: status, Evaluation: evaluation, Error: errorText, DurationMs: result.durationMs}
		if err := model.SaveChannelMonitorArtifact(artifact); err != nil {
			return summary, err
		}
		if err := model.TrimChannelMonitorArtifacts(dayKey, job.group, job.modelName, job.question.ID, 10); err != nil {
			return summary, err
		}
		seen[job.channel.Id] = true
		summary.Channels = len(seen)
		summary.Artifacts++
		if passed {
			summary.Passed++
		} else {
			summary.Failed++
		}
		if report != nil {
			report(index+1, len(jobs))
		}
	}
	return summary, nil
}

func GetChannelMonitorConfig(c *gin.Context) {
	channels, err := model.GetAllChannels(0, -1, false, false)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	groups := model.DiscoverChannelMonitorGroups(channels)
	config, err := model.LoadChannelMonitorConfig(groups)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": gin.H{"config": config, "available_groups": groups, "builtin_questions": model.ChannelMonitorBuiltinQuestions()}})
}

func UpdateChannelMonitorConfig(c *gin.Context) {
	var config model.ChannelMonitorConfig
	if err := common.DecodeJson(http.MaxBytesReader(c.Writer, c.Request.Body, 1<<20), &config); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "invalid monitor configuration"})
		return
	}
	if err := model.ValidateChannelMonitorConfig(config); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": err.Error()})
		return
	}
	channels, err := model.GetAllChannels(0, -1, false, false)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	groups := model.DiscoverChannelMonitorGroups(channels)
	for _, g := range config.Groups {
		if !g.Enabled {
			continue
		}
		availableIndex := slices.IndexFunc(groups, func(available model.ChannelMonitorGroup) bool { return available.Key == g.Key })
		if availableIndex < 0 {
			c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "monitor group is no longer available: " + g.Key})
			return
		}
		if !slices.Contains(groups[availableIndex].Models, g.Model) {
			c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "monitor model is no longer available: " + g.Model})
			return
		}
	}
	if err := model.SaveChannelMonitorConfig(config); err != nil {
		common.ApiError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true})
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
			for _, path := range []string{"choices.0.delta.content", "choices.0.message.content", "content.0.text", "candidates.0.content.parts.0.text", "output_text", "delta.text", "delta"} {
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
	availableChannels, err := model.GetAllChannels(0, -1, false, false)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	available := model.DiscoverChannelMonitorGroups(availableChannels)
	config, err := model.LoadChannelMonitorConfig(available)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	artifacts, err := model.ListChannelMonitorArtifacts(model.ChannelMonitorFilter{Model: c.Query("model"), Since: model.ChannelMonitorDayStart(time.Now())}, -1)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": channelMonitorData(config, available, artifacts, c.Query("group"), c.Query("model"), c.Query("range"), time.Now().Unix())})
}

func channelMonitorData(config model.ChannelMonitorConfig, available []model.ChannelMonitorGroup, artifacts []*model.ChannelMonitorArtifact, groupFilter, modelFilter, rangeFilter string, now int64) gin.H {
	channels := make(map[string]gin.H)
	models := make(map[string]bool)
	groups := make(map[string]bool)
	for _, g := range config.Groups {
		if !g.Enabled {
			continue
		}
		for _, descriptor := range available {
			if descriptor.Key != g.Key {
				continue
			}
			groups[g.Key] = true
			if groupFilter != "" && groupFilter != g.Key {
				continue
			}
			if modelFilter != "" && modelFilter != g.Model {
				continue
			}
			models[g.Model] = true
			cardModels := []string{g.Model}
			channels[g.Key] = gin.H{"key": g.Key, "group": g.Key, "platform": descriptor.Platform, "model": g.Model, "models": cardModels, "question_ids": g.QuestionIDs, "interval_minutes": g.IntervalMinutes, "latest": int64(0), "artifacts": []*model.ChannelMonitorArtifact{}}
		}
	}
	type modelMetric struct{ total, passed, duration int64 }
	modelMetrics := make(map[string]modelMetric)
	trend := make(map[int64]modelMetric)
	visible := make([]*model.ChannelMonitorArtifact, 0, len(artifacts))
	retained := map[string]int{}
	for _, artifact := range artifacts {
		keys := []string{artifact.Group}
		if !strings.Contains(artifact.Group, " / ") {
			keys = nil
			for _, descriptor := range available {
				if slices.Contains(descriptor.ChannelIDs, artifact.ChannelID) && slices.ContainsFunc(strings.Split(artifact.Group, ","), func(value string) bool { return strings.TrimSpace(value) == descriptor.Group }) {
					keys = append(keys, descriptor.Key)
				}
			}
		}
		for _, key := range keys {
			card, ok := channels[key]
			if modelFilter != "" && artifact.Model != modelFilter {
				continue
			}
			if !ok || !slices.Contains(card["question_ids"].([]string), artifact.PresetID) {
				continue
			}
			if configuredModel, ok := card["model"].(string); ok && configuredModel != "" && artifact.Model != configuredModel {
				continue
			}
			historyKey := key + "\x00" + artifact.Model + "\x00" + artifact.PresetID
			if retained[historyKey] >= 10 {
				continue
			}
			retained[historyKey]++
			copyArtifact := *artifact
			copyArtifact.Group = key
			visible = append(visible, &copyArtifact)
			card["artifacts"] = append(card["artifacts"].([]*model.ChannelMonitorArtifact), &copyArtifact)
			card["latest"] = max(card["latest"].(int64), artifact.CreatedAt)
		}
	}
	artifacts = visible
	if rangeFilter == "90m" {
		artifacts = slices.DeleteFunc(slices.Clone(visible), func(a *model.ChannelMonitorArtifact) bool { return a.CreatedAt < now-90*60 })
	}
	for _, artifact := range artifacts {
		metric := modelMetrics[artifact.Model]
		metric.total++
		metric.duration += artifact.DurationMs
		if artifact.Status == model.ChannelMonitorStatusSuccess {
			metric.passed++
		}
		modelMetrics[artifact.Model] = metric
		bucket := artifact.CreatedAt - artifact.CreatedAt%300
		bucketMetric := trend[bucket]
		bucketMetric.total++
		if artifact.Status == model.ChannelMonitorStatusSuccess {
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
	return gin.H{
		"questions":  append(model.ChannelMonitorBuiltinQuestions(), config.Questions...),
		"updated_at": now, "range": rangeFilter, "groups": groups, "models": models,
		"cards": channels, "detail": channelMonitorArtifactsResponse(artifacts), "model_details": modelDetails, "trend": trendData,
		"metrics": gin.H{"total": len(artifacts), "passed": countPassed(artifacts), "success_rate": percentage(int64(countPassed(artifacts)), int64(len(artifacts)))},
	}
}

func countPassed(artifacts []*model.ChannelMonitorArtifact) int {
	count := 0
	for _, artifact := range artifacts {
		if artifact.Status == model.ChannelMonitorStatusSuccess {
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
	task, created, err := service.EnqueueSystemTask(model.SystemTaskTypeChannelMonitor, channelMonitorTaskPayload{Manual: true})
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
