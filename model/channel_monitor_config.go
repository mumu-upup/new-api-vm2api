package model

import (
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
)

const ChannelMonitorConfigKey = "channel_monitor.config"
const ChannelMonitorLastRunsKey = "channel_monitor.last_runs"

type ChannelMonitorQuestion struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Prompt       string `json:"prompt"`
	ArtifactType string `json:"artifact_type"`
	Matcher      string `json:"matcher"`
	Expected     string `json:"expected"`
}
type ChannelMonitorGroupConfig struct {
	Key             string   `json:"key"`
	Enabled         bool     `json:"enabled"`
	Model           string   `json:"model"`
	QuestionIDs     []string `json:"question_ids"`
	IntervalMinutes int      `json:"interval_minutes"`
}
type ChannelMonitorConfig struct {
	Groups    []ChannelMonitorGroupConfig `json:"groups"`
	Questions []ChannelMonitorQuestion    `json:"questions"`
}
type ChannelMonitorGroup struct {
	Key        string   `json:"key"`
	Platform   string   `json:"platform"`
	Group      string   `json:"group"`
	Models     []string `json:"models"`
	ChannelIDs []int    `json:"channel_ids"`
}

func ChannelMonitorGroupKey(channelType int, group string) string {
	return constant.GetChannelTypeName(channelType) + " / " + group
}

func DiscoverChannelMonitorGroups(channels []*Channel) []ChannelMonitorGroup {
	byKey := map[string]*ChannelMonitorGroup{}
	for _, channel := range channels {
		if channel == nil || channel.Status != common.ChannelStatusEnabled || len(channel.GetModels()) == 0 {
			continue
		}
		tokens := channel.GetGroups()
		if len(tokens) == 0 {
			tokens = []string{"default"}
		}
		for _, group := range tokens {
			group = strings.TrimSpace(group)
			if group == "" {
				continue
			}
			key := ChannelMonitorGroupKey(channel.Type, group)
			descriptor := byKey[key]
			if descriptor == nil {
				descriptor = &ChannelMonitorGroup{Key: key, Platform: constant.GetChannelTypeName(channel.Type), Group: group, Models: []string{}, ChannelIDs: []int{}}
				byKey[key] = descriptor
			}
			if !slices.Contains(descriptor.ChannelIDs, channel.Id) {
				descriptor.ChannelIDs = append(descriptor.ChannelIDs, channel.Id)
			}
			for _, name := range channel.GetModels() {
				name = strings.TrimSpace(name)
				if name != "" && !slices.Contains(descriptor.Models, name) {
					descriptor.Models = append(descriptor.Models, name)
				}
			}
		}
	}
	groups := make([]ChannelMonitorGroup, 0, len(byKey))
	for _, group := range byKey {
		slices.Sort(group.Models)
		slices.Sort(group.ChannelIDs)
		groups = append(groups, *group)
	}
	slices.SortFunc(groups, func(a, b ChannelMonitorGroup) int { return strings.Compare(a.Key, b.Key) })
	return groups
}

func ChannelMonitorBuiltinQuestions() []ChannelMonitorQuestion {
	questions := make([]ChannelMonitorQuestion, 0, len(channelMonitorPresets))
	for _, preset := range channelMonitorPresets {
		questions = append(questions, ChannelMonitorQuestion{ID: preset.ID, Name: preset.Label, Prompt: preset.Prompt, ArtifactType: preset.ArtifactType, Matcher: "builtin", Expected: preset.Expected})
	}
	return questions
}

func DefaultChannelMonitorConfig(groups []ChannelMonitorGroup) ChannelMonitorConfig {
	config := ChannelMonitorConfig{Groups: []ChannelMonitorGroupConfig{}, Questions: []ChannelMonitorQuestion{}}
	for _, group := range groups {
		config.Groups = append(config.Groups, ChannelMonitorGroupConfig{Key: group.Key, Enabled: true, Model: firstChannelMonitorModel(group), QuestionIDs: []string{ChannelMonitorPresetPelican, ChannelMonitorPresetCandy}, IntervalMinutes: 30})
	}
	return config
}

func firstChannelMonitorModel(group ChannelMonitorGroup) string {
	if len(group.Models) == 0 {
		return ""
	}
	return group.Models[0]
}

// New groups remain opt-in once an administrator has saved a configuration.
func LoadChannelMonitorConfig(groups []ChannelMonitorGroup) (ChannelMonitorConfig, error) {
	common.OptionMapRWMutex.RLock()
	raw := common.OptionMap[ChannelMonitorConfigKey]
	common.OptionMapRWMutex.RUnlock()
	if raw == "" {
		return DefaultChannelMonitorConfig(groups), nil
	}
	config := ChannelMonitorConfig{}
	if err := common.UnmarshalJsonStr(raw, &config); err != nil {
		return config, err
	}
	if config.Groups == nil {
		config.Groups = []ChannelMonitorGroupConfig{}
	}
	if config.Questions == nil {
		config.Questions = []ChannelMonitorQuestion{}
	}
	for _, group := range groups {
		index := slices.IndexFunc(config.Groups, func(g ChannelMonitorGroupConfig) bool { return g.Key == group.Key })
		if index < 0 {
			config.Groups = append(config.Groups, ChannelMonitorGroupConfig{Key: group.Key, Model: firstChannelMonitorModel(group), QuestionIDs: []string{ChannelMonitorPresetPelican, ChannelMonitorPresetCandy}, IntervalMinutes: 30})
			continue
		}
		if config.Groups[index].Model == "" || !slices.Contains(group.Models, config.Groups[index].Model) {
			config.Groups[index].Model = firstChannelMonitorModel(group)
		}
	}
	if err := ValidateChannelMonitorConfig(config); err != nil {
		return config, err
	}
	return config, nil
}

var channelMonitorQuestionID = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)

func ValidateChannelMonitorConfig(config ChannelMonitorConfig) error {
	if len(config.Groups) > 500 || len(config.Questions) > 50 {
		return fmt.Errorf("too many monitor groups or questions")
	}
	ids := map[string]bool{ChannelMonitorPresetPelican: true, ChannelMonitorPresetCandy: true}
	for _, q := range config.Questions {
		if !channelMonitorQuestionID.MatchString(q.ID) || ids[q.ID] {
			return fmt.Errorf("invalid or duplicate question ID: %s", q.ID)
		}
		ids[q.ID] = true
		if strings.TrimSpace(q.Name) == "" || len(q.Name) > 128 || strings.TrimSpace(q.Prompt) == "" || len(q.Prompt) > 16000 || len(q.Expected) > 4000 {
			return fmt.Errorf("invalid question name, prompt or expected value: %s", q.ID)
		}
		if q.ArtifactType != ChannelMonitorArtifactHTML && q.ArtifactType != ChannelMonitorArtifactText {
			return fmt.Errorf("invalid question type: %s", q.ID)
		}
		switch q.Matcher {
		case "none":
		case "exact", "contains":
			if strings.TrimSpace(q.Expected) == "" {
				return fmt.Errorf("expected value is required: %s", q.ID)
			}
		case "regex":
			if q.Expected == "" {
				return fmt.Errorf("expected pattern is required: %s", q.ID)
			}
			if _, err := regexp.Compile(q.Expected); err != nil {
				return fmt.Errorf("invalid regular expression: %s", q.ID)
			}
		default:
			return fmt.Errorf("invalid matcher: %s", q.ID)
		}
	}
	keys := map[string]bool{}
	for _, g := range config.Groups {
		if strings.TrimSpace(g.Key) == "" || len(g.Key) > 128 || keys[g.Key] {
			return fmt.Errorf("invalid or duplicate group key: %s", g.Key)
		}
		keys[g.Key] = true
		if g.IntervalMinutes < 1 || g.IntervalMinutes > 1440 {
			return fmt.Errorf("group interval must be 1–1440 minutes: %s", g.Key)
		}
		if g.Enabled {
			if strings.TrimSpace(g.Model) == "" || len(g.Model) > 256 {
				return fmt.Errorf("select an execution model: %s", g.Key)
			}
			if len(g.QuestionIDs) == 0 {
				return fmt.Errorf("select at least one question: %s", g.Key)
			}
		}
		selected := map[string]bool{}
		for _, id := range g.QuestionIDs {
			if !ids[id] || selected[id] {
				return fmt.Errorf("unknown or duplicate question: %s", id)
			}
			selected[id] = true
		}
	}
	return nil
}

func SaveChannelMonitorConfig(config ChannelMonitorConfig) error {
	if err := ValidateChannelMonitorConfig(config); err != nil {
		return err
	}
	encoded, err := common.Marshal(config)
	if err != nil {
		return err
	}
	// Bulk update propagates persistence failures before publishing the option.
	return UpdateOptionsBulk(map[string]string{ChannelMonitorConfigKey: string(encoded)})
}

// Polling once a minute lets every group use its own interval, even when
// configured intervals are not multiples of one another.
func ChannelMonitorInterval() time.Duration { return time.Minute }

// PreserveChannelMonitorRuns keeps small scheduling checkpoints independently
// from the generated works, which are cleared at Beijing midnight.
func PreserveChannelMonitorRuns(recent []*ChannelMonitorArtifact, now int64) ([]*ChannelMonitorArtifact, error) {
	common.OptionMapRWMutex.RLock()
	raw := common.OptionMap[ChannelMonitorLastRunsKey]
	common.OptionMapRWMutex.RUnlock()
	checkpoints := map[string]int64{}
	if raw != "" {
		if err := common.UnmarshalJsonStr(raw, &checkpoints); err != nil {
			return nil, err
		}
	}
	if checkpoints == nil {
		checkpoints = map[string]int64{}
	}
	previous := maps.Clone(checkpoints)
	for _, artifact := range recent {
		key := artifact.Group + "\x00" + artifact.Model + "\x00" + artifact.PresetID
		checkpoints[key] = max(checkpoints[key], artifact.CreatedAt)
	}
	merged := make([]*ChannelMonitorArtifact, 0, len(checkpoints))
	for key, timestamp := range checkpoints {
		group, rest, groupOK := strings.Cut(key, "\x00")
		modelName, question, questionOK := strings.Cut(rest, "\x00")
		if !groupOK || !questionOK || group == "" || modelName == "" || question == "" || timestamp <= 0 || timestamp < now-1440*60 || timestamp > now {
			delete(checkpoints, key)
			continue
		}
		merged = append(merged, &ChannelMonitorArtifact{Group: group, Model: modelName, PresetID: question, CreatedAt: timestamp})
	}
	if !maps.Equal(previous, checkpoints) {
		encoded, err := common.Marshal(checkpoints)
		if err != nil {
			return nil, err
		}
		if err := UpdateOptionsBulk(map[string]string{ChannelMonitorLastRunsKey: string(encoded)}); err != nil {
			return nil, err
		}
	}
	return merged, nil
}

func EvaluateChannelMonitorQuestion(q ChannelMonitorQuestion, content string) (string, bool) {
	if q.Matcher == "builtin" {
		return EvaluateChannelMonitorArtifact(q.ID, content)
	}
	content = strings.TrimSpace(content)
	if content == "" {
		return "failed", false
	}
	if q.ArtifactType == ChannelMonitorArtifactHTML && !strings.Contains(strings.ToLower(content), "<") {
		return "failed", false
	}
	passed := false
	switch q.Matcher {
	case "none":
		return "unscored", true
	case "exact":
		passed = content == strings.TrimSpace(q.Expected)
	case "contains":
		passed = strings.Contains(content, q.Expected)
	case "regex":
		pattern, err := regexp.Compile(q.Expected)
		passed = err == nil && pattern.MatchString(content)
	}
	if passed {
		return "passed", true
	}
	return "failed", false
}
