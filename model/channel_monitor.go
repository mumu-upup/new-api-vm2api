package model

import (
	"errors"
	"regexp"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
)

const (
	ChannelMonitorPresetPelican = "pelican-svg-animation"
	ChannelMonitorPresetCandy   = "candy-shape-matching"
	ChannelMonitorArtifactHTML  = "html"
	ChannelMonitorArtifactText  = "text"
	ChannelMonitorStatusSuccess = "success"
	ChannelMonitorStatusFailed  = "failed"
	ChannelMonitorStatusSkipped = "skipped"
	ChannelMonitorMaxContent    = 128 << 10
)

type ChannelMonitorArtifact struct {
	ID           int64  `json:"id" gorm:"primaryKey"`
	RunID        string `json:"run_id" gorm:"size:64;index"`
	DayKey       string `json:"day_key" gorm:"size:16;index"`
	ChannelID    int    `json:"channel_id" gorm:"index"`
	ChannelName  string `json:"channel_name" gorm:"size:128"`
	Group        string `json:"group" gorm:"column:group;size:128;index"`
	Model        string `json:"model" gorm:"size:128;index"`
	PresetID     string `json:"preset_id" gorm:"size:64;index"`
	ArtifactType string `json:"artifact_type" gorm:"size:16"`
	Content      string `json:"content" gorm:"type:text"`
	ActualModel  string `json:"actual_model" gorm:"size:128"`
	Status       string `json:"status" gorm:"size:16;index"`
	Evaluation   string `json:"evaluation" gorm:"size:16"`
	Error        string `json:"error,omitempty" gorm:"type:text"`
	DurationMs   int64  `json:"duration_ms"`
	CreatedAt    int64  `json:"created_at" gorm:"bigint;index"`
	SyncedAt     int64  `json:"synced_at" gorm:"bigint"`
}

func (ChannelMonitorArtifact) TableName() string { return "channel_monitor_artifacts" }

func (artifact *ChannelMonitorArtifact) BeforeCreate(_ *gorm.DB) error {
	now := common.GetTimestamp()
	if artifact.CreatedAt == 0 {
		artifact.CreatedAt = now
	}
	if artifact.SyncedAt == 0 {
		artifact.SyncedAt = now
	}
	if len(artifact.Content) > ChannelMonitorMaxContent {
		artifact.Content = strings.ToValidUTF8(artifact.Content[:ChannelMonitorMaxContent], "")
	}
	return nil
}

func SaveChannelMonitorArtifact(artifact *ChannelMonitorArtifact) error {
	if artifact == nil {
		return errors.New("channel monitor artifact is nil")
	}
	if len(artifact.Content) > ChannelMonitorMaxContent {
		artifact.Content = strings.ToValidUTF8(artifact.Content[:ChannelMonitorMaxContent], "")
	}
	return DB.Create(artifact).Error
}

type ChannelMonitorFilter struct {
	ChannelID int
	Group     string
	Model     string
	Since     int64
}

func ListChannelMonitorArtifacts(filter ChannelMonitorFilter, limit int) ([]*ChannelMonitorArtifact, error) {
	if limit == 0 || limit > 1000 {
		limit = 1000
	}
	query := DB.Model(&ChannelMonitorArtifact{}).Order("created_at desc, id desc").Limit(limit)
	if filter.ChannelID > 0 {
		query = query.Where("channel_id = ?", filter.ChannelID)
	}
	if filter.Group != "" {
		query = query.Where(commonGroupCol+" = ?", filter.Group)
	}
	if filter.Model != "" {
		query = query.Where("model = ?", filter.Model)
	}
	if filter.Since > 0 {
		query = query.Where("created_at >= ?", filter.Since)
	}
	var artifacts []*ChannelMonitorArtifact
	return artifacts, query.Find(&artifacts).Error
}

func DeleteChannelMonitorArtifactsBefore(cutoff int64) error {
	if cutoff <= 0 {
		return nil
	}
	return DB.Where("created_at < ?", cutoff).Delete(&ChannelMonitorArtifact{}).Error
}

func TrimChannelMonitorArtifacts(dayKey, group, modelName, presetID string, keep int) error {
	if keep <= 0 {
		keep = 10
	}
	query := DB.Model(&ChannelMonitorArtifact{}).Where("day_key = ? AND "+commonGroupCol+" = ? AND model = ? AND preset_id = ?", dayKey, group, modelName, presetID)
	var keepIDs []int64
	if err := query.Order("created_at desc, id desc").Limit(keep).Pluck("id", &keepIDs).Error; err != nil {
		return err
	}
	if len(keepIDs) == 0 {
		return nil
	}
	return query.Where("id NOT IN ?", keepIDs).Delete(&ChannelMonitorArtifact{}).Error
}

type ChannelMonitorPreset struct {
	ID           string
	Label        string
	Prompt       string
	ArtifactType string
	Expected     string
}

var channelMonitorPresets = []ChannelMonitorPreset{
	{
		ID:           ChannelMonitorPresetPelican,
		Label:        "SVG pelican animation",
		Prompt:       "Create a complete, directly runnable HTML page containing a 2D animation of a pelican riding a bicycle drawn with SVG. Return only the HTML.",
		ArtifactType: ChannelMonitorArtifactHTML,
	},
	{
		ID:           ChannelMonitorPresetCandy,
		Label:        "Candy shape matching",
		Prompt:       "A black bag contains apple, peach, and watermelon candies. Each flavor has round and five-pointed-star shapes. Apple: round 7, star 7. Peach: round 9, star 6. Watermelon: round 8, star 4. What is the minimum number of candies that guarantees holding different shapes of both apple and peach candies? Answer only the number.",
		ArtifactType: ChannelMonitorArtifactText,
		Expected:     "21",
	},
}

func ChannelMonitorPresets() []ChannelMonitorPreset {
	return append([]ChannelMonitorPreset(nil), channelMonitorPresets...)
}

var candyAnswerPattern = regexp.MustCompile(`(?m)(^|\D)21(\D|$)`)

func EvaluateChannelMonitorArtifact(presetID, content string) (evaluation string, ok bool) {
	content = strings.TrimSpace(content)
	switch presetID {
	case ChannelMonitorPresetPelican:
		lower := strings.ToLower(content)
		if strings.Contains(lower, "<svg") && strings.Contains(lower, "pelican") && (strings.Contains(lower, "bicycle") || strings.Contains(lower, "bike")) {
			return "passed", true
		}
		return "failed", false
	case ChannelMonitorPresetCandy:
		if candyAnswerPattern.MatchString(content) {
			return "passed", true
		}
		return "failed", false
	default:
		return "unscored", false
	}
}

func ChannelMonitorDayStart(now time.Time) int64 {
	location, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		location = time.FixedZone("CST", 8*60*60)
	}
	local := now.In(location)
	return time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, location).Unix()
}

// LatestChannelMonitorRuns avoids loading generated HTML into the scheduler.
func LatestChannelMonitorRuns(since int64) ([]*ChannelMonitorArtifact, error) {
	var rows []*ChannelMonitorArtifact
	err := DB.Model(&ChannelMonitorArtifact{}).Select(commonGroupCol+", model, preset_id, MAX(created_at) AS created_at").Where("created_at >= ?", since).Group(commonGroupCol + ", model, preset_id").Find(&rows).Error
	return rows, err
}
