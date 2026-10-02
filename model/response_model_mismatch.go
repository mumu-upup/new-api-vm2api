package model

import "gorm.io/gorm"

// ResponseModelMismatch records a consumed request whose upstream response
// declared a model that matches neither the requested model nor the mapped
// upstream model, i.e. a request the channel likely served with a substituted
// ("degraded") model. Rows are written when the consume log is recorded and
// are cleared weekly, so the table only holds the current week.
type ResponseModelMismatch struct {
	Id               int    `json:"id"`
	CreatedAt        int64  `json:"created_at" gorm:"bigint;index"`
	RequestId        string `json:"request_id" gorm:"type:varchar(64);default:''"`
	UserId           int    `json:"user_id"`
	Username         string `json:"username" gorm:"default:''"`
	TokenName        string `json:"token_name" gorm:"default:''"`
	ChannelId        int    `json:"channel_id"`
	Group            string `json:"group" gorm:"column:use_group;default:''"`
	RequestedModel   string `json:"requested_model" gorm:"default:''"`
	UpstreamModel    string `json:"upstream_model" gorm:"default:''"`
	ReturnedModel    string `json:"returned_model" gorm:"default:''"`
	Quota            int    `json:"quota" gorm:"default:0"`
	PromptTokens     int    `json:"prompt_tokens" gorm:"default:0"`
	CompletionTokens int    `json:"completion_tokens" gorm:"default:0"`
	UseTime          int    `json:"use_time" gorm:"default:0"`
	IsStream         bool   `json:"is_stream"`
}

func CreateResponseModelMismatch(record *ResponseModelMismatch) error {
	return DB.Create(record).Error
}

// DeleteResponseModelMismatchesBefore removes records created before cutoff
// (Unix seconds) and returns how many were deleted.
func DeleteResponseModelMismatchesBefore(cutoff int64) (int64, error) {
	result := DB.Where("created_at < ?", cutoff).Delete(&ResponseModelMismatch{})
	return result.RowsAffected, result.Error
}

type ResponseModelMismatchTotals struct {
	Requests int64 `gorm:"column:requests"`
	Quota    int64 `gorm:"column:quota"`
	Channels int64 `gorm:"column:channels"`
	Users    int64 `gorm:"column:users"`
}

// ResponseModelMismatchGroup aggregates records sharing a channel and a
// requested/returned model pair.
type ResponseModelMismatchGroup struct {
	ChannelId      int    `gorm:"column:channel_id"`
	RequestedModel string `gorm:"column:requested_model"`
	ReturnedModel  string `gorm:"column:returned_model"`
	Requests       int64  `gorm:"column:requests"`
	Quota          int64  `gorm:"column:quota"`
	LastSeenAt     int64  `gorm:"column:last_seen_at"`
}

type ResponseModelMismatchHour struct {
	Bucket   int64 `gorm:"column:bucket"`
	Requests int64 `gorm:"column:requests"`
}

// ResponseModelMismatchReport is the raw material for the degradation
// dashboard within one time range.
type ResponseModelMismatchReport struct {
	Totals ResponseModelMismatchTotals
	Groups []ResponseModelMismatchGroup
	Hours  []ResponseModelMismatchHour
	Recent []ResponseModelMismatch
}

// GetResponseModelMismatchReport aggregates the records created within
// [startTimestamp, endTimestamp] and returns up to recentLimit newest rows.
func GetResponseModelMismatchReport(startTimestamp int64, endTimestamp int64, recentLimit int) (*ResponseModelMismatchReport, error) {
	inRange := DB.Model(&ResponseModelMismatch{}).
		Where("created_at >= ? AND created_at <= ?", startTimestamp, endTimestamp)
	report := &ResponseModelMismatchReport{}

	err := inRange.Session(&gorm.Session{}).
		Select("COUNT(*) AS requests, COALESCE(SUM(quota), 0) AS quota, " +
			"COUNT(DISTINCT channel_id) AS channels, COUNT(DISTINCT user_id) AS users").
		Scan(&report.Totals).Error
	if err != nil {
		return nil, err
	}
	err = inRange.Session(&gorm.Session{}).
		Select("channel_id, requested_model, returned_model, COUNT(*) AS requests, " +
			"COALESCE(SUM(quota), 0) AS quota, MAX(created_at) AS last_seen_at").
		Group("channel_id, requested_model, returned_model").
		Scan(&report.Groups).Error
	if err != nil {
		return nil, err
	}
	err = inRange.Session(&gorm.Session{}).
		Select("created_at - created_at % 3600 AS bucket, COUNT(*) AS requests").
		Group("created_at - created_at % 3600").
		Order("bucket ASC").
		Scan(&report.Hours).Error
	if err != nil {
		return nil, err
	}
	err = inRange.Session(&gorm.Session{}).
		Order("created_at DESC, id DESC").
		Limit(recentLimit).
		Find(&report.Recent).Error
	if err != nil {
		return nil, err
	}
	return report, nil
}
