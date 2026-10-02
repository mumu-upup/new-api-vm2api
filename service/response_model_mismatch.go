package service

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"

	"github.com/bytedance/gopkg/util/gopool"
	"github.com/gin-gonic/gin"
)

const (
	responseModelMismatchRecentLimit = 200
	responseModelMismatchPairLimit   = 50
	responseModelMismatchTopModels   = 5
)

var responseModelMismatchCleanupOnce sync.Once

// ResponseModelMismatchStats summarizes the recorded requests whose upstream
// response declared a model matching neither the requested nor the mapped
// upstream model, i.e. requests a channel likely served with a substituted
// ("degraded") model.
type ResponseModelMismatchStats struct {
	Summary  ResponseModelMismatchSummary   `json:"summary"`
	Channels []ResponseModelMismatchChannel `json:"channels"`
	Pairs    []ResponseModelMismatchPair    `json:"pairs"`
	Trend    []ResponseModelMismatchBucket  `json:"trend"`
	Items    []ResponseModelMismatchItem    `json:"items"`
}

type ResponseModelMismatchSummary struct {
	StartTimestamp   int64 `json:"start_timestamp"`
	EndTimestamp     int64 `json:"end_timestamp"`
	RetainedSince    int64 `json:"retained_since"`
	TotalRequests    int64 `json:"total_requests"`
	MismatchRequests int64 `json:"mismatch_requests"`
	MismatchQuota    int64 `json:"mismatch_quota"`
	AffectedChannels int64 `json:"affected_channels"`
	AffectedUsers    int64 `json:"affected_users"`
	ItemLimit        int   `json:"item_limit"`
}

type ResponseModelMismatchChannel struct {
	ChannelId        int      `json:"channel_id"`
	ChannelName      string   `json:"channel_name"`
	TotalRequests    int64    `json:"total_requests"`
	MismatchRequests int64    `json:"mismatch_requests"`
	MismatchQuota    int64    `json:"mismatch_quota"`
	LastSeenAt       int64    `json:"last_seen_at"`
	ReturnedModels   []string `json:"returned_models"`
}

type ResponseModelMismatchPair struct {
	RequestedModel string `json:"requested_model"`
	ReturnedModel  string `json:"returned_model"`
	Requests       int64  `json:"requests"`
	Channels       int    `json:"channels"`
	LastSeenAt     int64  `json:"last_seen_at"`
}

// ResponseModelMismatchBucket counts mismatches in the hour starting at
// Timestamp (Unix seconds); clients regroup hours into their own time zone.
type ResponseModelMismatchBucket struct {
	Timestamp int64 `json:"timestamp"`
	Requests  int64 `json:"requests"`
}

type ResponseModelMismatchItem struct {
	model.ResponseModelMismatch
	ChannelName string `json:"channel_name"`
}

type responseModelMismatchChannelAgg struct {
	row            ResponseModelMismatchChannel
	returnedModels map[string]int64
}

type responseModelMismatchPairAgg struct {
	row      ResponseModelMismatchPair
	channels map[int]struct{}
}

// RecordResponseModelMismatch stores the consumed request when its upstream
// response declared a substituted model, using the same rule as the usage-log
// badge (relaycommon.ResponseModel.Mismatch). It is a diagnostic side record:
// failures are logged and never affect the request or its billing.
func RecordResponseModelMismatch(ctx *gin.Context, relayInfo *relaycommon.RelayInfo, params model.RecordConsumeLogParams) {
	if relayInfo == nil || !relayInfo.ResponseModel.Mismatch() {
		return
	}
	observation := relayInfo.ResponseModel
	requestedModel := observation.RequestedModel
	if requestedModel == "" {
		requestedModel = params.ModelName
	}
	record := &model.ResponseModelMismatch{
		CreatedAt:        common.GetTimestamp(),
		RequestId:        ctx.GetString(common.RequestIdKey),
		UserId:           relayInfo.UserId,
		Username:         ctx.GetString("username"),
		TokenName:        params.TokenName,
		ChannelId:        params.ChannelId,
		Group:            params.Group,
		RequestedModel:   requestedModel,
		UpstreamModel:    observation.UpstreamModel,
		ReturnedModel:    observation.ReturnedModel,
		Quota:            params.Quota,
		PromptTokens:     params.PromptTokens,
		CompletionTokens: params.CompletionTokens,
		UseTime:          params.UseTimeSeconds,
		IsStream:         params.IsStream,
	}
	if err := model.CreateResponseModelMismatch(record); err != nil {
		logger.LogError(ctx, "failed to record response model mismatch: "+err.Error())
	}
}

// responseModelMismatchWeekStart returns Monday 00:00 in now's location of
// the week containing now. Records before it belong to a previous week.
func responseModelMismatchWeekStart(now time.Time) time.Time {
	daysSinceMonday := (int(now.Weekday()) + 6) % 7
	year, month, day := now.Date()
	return time.Date(year, month, day-daysSinceMonday, 0, 0, 0, 0, now.Location())
}

// purgePreviousWeekResponseModelMismatches deletes every record created
// before the week containing now, so only the current week is retained.
func purgePreviousWeekResponseModelMismatches(now time.Time) (weekStart time.Time, deleted int64, err error) {
	weekStart = responseModelMismatchWeekStart(now)
	deleted, err = model.DeleteResponseModelMismatchesBefore(weekStart.Unix())
	return weekStart, deleted, err
}

// StartResponseModelMismatchCleanupTask clears the previous week's records
// at startup and then every Monday 00:00 (server local time) on the master
// node.
func StartResponseModelMismatchCleanupTask() {
	responseModelMismatchCleanupOnce.Do(func() {
		if !common.IsMasterNode {
			return
		}
		gopool.Go(func() {
			ctx := context.Background()
			for {
				weekStart, deleted, err := purgePreviousWeekResponseModelMismatches(time.Now())
				if err != nil {
					logger.LogWarn(ctx, "response model mismatch cleanup failed: "+err.Error())
				} else if deleted > 0 {
					logger.LogInfo(ctx, fmt.Sprintf("response model mismatch cleanup removed %d records before %s", deleted, weekStart.Format(time.RFC3339)))
				}
				// A wall clock running slightly behind can wake us just before
				// Monday; the floor keeps that retry from spinning.
				time.Sleep(max(time.Until(weekStart.AddDate(0, 0, 7)), time.Second))
			}
		})
	})
}

// GetResponseModelMismatchStats builds the degradation dashboard for
// [startTimestamp, endTimestamp] from the recorded mismatches, using consume
// logs only for each channel's total request count.
func GetResponseModelMismatchStats(startTimestamp int64, endTimestamp int64) (*ResponseModelMismatchStats, error) {
	report, err := model.GetResponseModelMismatchReport(startTimestamp, endTimestamp, responseModelMismatchRecentLimit)
	if err != nil {
		return nil, err
	}
	counts, err := model.CountConsumeLogsByChannel(startTimestamp, endTimestamp)
	if err != nil {
		return nil, err
	}

	stats := &ResponseModelMismatchStats{
		Summary: ResponseModelMismatchSummary{
			StartTimestamp:   startTimestamp,
			EndTimestamp:     endTimestamp,
			RetainedSince:    responseModelMismatchWeekStart(time.Now()).Unix(),
			MismatchRequests: report.Totals.Requests,
			MismatchQuota:    report.Totals.Quota,
			AffectedChannels: report.Totals.Channels,
			AffectedUsers:    report.Totals.Users,
			ItemLimit:        responseModelMismatchRecentLimit,
		},
		Channels: []ResponseModelMismatchChannel{},
		Pairs:    []ResponseModelMismatchPair{},
		Trend:    []ResponseModelMismatchBucket{},
		Items:    []ResponseModelMismatchItem{},
	}

	channels := make(map[int]*responseModelMismatchChannelAgg)
	pairs := make(map[[2]string]*responseModelMismatchPairAgg)
	for _, group := range report.Groups {
		channel := channels[group.ChannelId]
		if channel == nil {
			channel = &responseModelMismatchChannelAgg{
				row:            ResponseModelMismatchChannel{ChannelId: group.ChannelId},
				returnedModels: make(map[string]int64),
			}
			channels[group.ChannelId] = channel
		}
		channel.row.MismatchRequests += group.Requests
		channel.row.MismatchQuota += group.Quota
		channel.row.LastSeenAt = max(channel.row.LastSeenAt, group.LastSeenAt)
		channel.returnedModels[group.ReturnedModel] += group.Requests

		pairKey := [2]string{group.RequestedModel, group.ReturnedModel}
		pair := pairs[pairKey]
		if pair == nil {
			pair = &responseModelMismatchPairAgg{
				row: ResponseModelMismatchPair{
					RequestedModel: group.RequestedModel,
					ReturnedModel:  group.ReturnedModel,
				},
				channels: make(map[int]struct{}),
			}
			pairs[pairKey] = pair
		}
		pair.row.Requests += group.Requests
		pair.row.LastSeenAt = max(pair.row.LastSeenAt, group.LastSeenAt)
		pair.channels[group.ChannelId] = struct{}{}
	}

	for _, count := range counts {
		stats.Summary.TotalRequests += count.Total
		if channel := channels[count.ChannelId]; channel != nil {
			channel.row.TotalRequests = count.Total
		}
	}

	channelIds := make([]int, 0, len(channels))
	for id := range channels {
		if id != 0 {
			channelIds = append(channelIds, id)
		}
	}
	for _, record := range report.Recent {
		if record.ChannelId != 0 && channels[record.ChannelId] == nil {
			channelIds = append(channelIds, record.ChannelId)
		}
	}
	channelNames, err := model.GetChannelNamesByIds(channelIds)
	if err != nil {
		return nil, err
	}
	resolveChannelName := func(id int) string {
		if name := channelNames[id]; name != "" || id == 0 {
			return name
		}
		return fmt.Sprintf("channel-%d", id)
	}

	for id, channel := range channels {
		channel.row.ChannelName = resolveChannelName(id)
		returned := make([]string, 0, len(channel.returnedModels))
		for name := range channel.returnedModels {
			returned = append(returned, name)
		}
		slices.SortFunc(returned, func(a, b string) int {
			return cmp.Or(cmp.Compare(channel.returnedModels[b], channel.returnedModels[a]), cmp.Compare(a, b))
		})
		channel.row.ReturnedModels = returned[:min(len(returned), responseModelMismatchTopModels)]
		stats.Channels = append(stats.Channels, channel.row)
	}
	slices.SortFunc(stats.Channels, func(a, b ResponseModelMismatchChannel) int {
		return cmp.Or(cmp.Compare(b.MismatchRequests, a.MismatchRequests), cmp.Compare(a.ChannelId, b.ChannelId))
	})

	for _, pair := range pairs {
		pair.row.Channels = len(pair.channels)
		stats.Pairs = append(stats.Pairs, pair.row)
	}
	slices.SortFunc(stats.Pairs, func(a, b ResponseModelMismatchPair) int {
		return cmp.Or(
			cmp.Compare(b.Requests, a.Requests),
			cmp.Compare(a.RequestedModel, b.RequestedModel),
			cmp.Compare(a.ReturnedModel, b.ReturnedModel),
		)
	})
	stats.Pairs = stats.Pairs[:min(len(stats.Pairs), responseModelMismatchPairLimit)]

	for _, hour := range report.Hours {
		stats.Trend = append(stats.Trend, ResponseModelMismatchBucket{Timestamp: hour.Bucket, Requests: hour.Requests})
	}
	for _, record := range report.Recent {
		stats.Items = append(stats.Items, ResponseModelMismatchItem{
			ResponseModelMismatch: record,
			ChannelName:           resolveChannelName(record.ChannelId),
		})
	}
	return stats, nil
}
