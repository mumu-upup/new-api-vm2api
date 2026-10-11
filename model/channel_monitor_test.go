package model

import (
	"github.com/QuantumNous/new-api/common"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/schema"
	"os"
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
		{name: "truncated pelican html", preset: ChannelMonitorPresetPelican, content: "<html><svg aria-label='pelican bicycle'><defs><linearGradient x2=\"", want: "failed"},
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

func TestChannelMonitorGroupConfiguration(t *testing.T) {
	channels := []*Channel{{Id: 1, Type: 1, Group: " vip, default,vip ", Models: "m1,m2", Status: 1}, {Id: 2, Type: 1, Group: "vip", Models: "m2,m3", Status: 1}}
	groups := DiscoverChannelMonitorGroups(channels)
	require.Len(t, groups, 2)
	require.Equal(t, "OpenAI / default", groups[0].Key)
	require.Equal(t, []string{"m1", "m2", "m3"}, groups[1].Models)
	config := DefaultChannelMonitorConfig(groups)
	require.Len(t, config.Groups, 2)
	require.True(t, config.Groups[0].Enabled)
	require.Equal(t, "m1", config.Groups[0].Model)
	require.Equal(t, 30, config.Groups[0].IntervalMinutes)
	require.NoError(t, ValidateChannelMonitorConfig(config))
	config.Groups[0].IntervalMinutes = 0
	require.Error(t, ValidateChannelMonitorConfig(config))
	config.Groups[0].IntervalMinutes = 30
	config.Groups[0].QuestionIDs = []string{"missing"}
	require.Error(t, ValidateChannelMonitorConfig(config))
}

func TestChannelMonitorCustomQuestions(t *testing.T) {
	for _, tc := range []struct {
		matcher, expected, content string
		passed                     bool
	}{
		{"exact", "21", " 21 ", true}, {"exact", "21", "121", false},
		{"contains", "animation", "SVG animation", true}, {"regex", `^2[01]$`, "21", true},
		{"regex", "[", "x", false}, {"none", "", "<svg></svg>", true},
	} {
		q := ChannelMonitorQuestion{ID: "custom-q", Name: "Example", Prompt: "Answer", ArtifactType: "text", Matcher: tc.matcher, Expected: tc.expected}
		_, passed := EvaluateChannelMonitorQuestion(q, tc.content)
		require.Equal(t, tc.passed, passed, tc.matcher)
	}
	config := ChannelMonitorConfig{Questions: []ChannelMonitorQuestion{{ID: "custom-q", Name: "Example", Prompt: "Answer", ArtifactType: "text", Matcher: "regex", Expected: "["}}}
	require.Error(t, ValidateChannelMonitorConfig(config))
	config.Questions[0].Matcher = "exact"
	require.NoError(t, ValidateChannelMonitorConfig(config))
	config.Questions[0].ID = ChannelMonitorPresetCandy
	require.Error(t, ValidateChannelMonitorConfig(config))
}

func TestChannelMonitorDatabaseMatrix(t *testing.T) {
	for _, dialect := range []string{"sqlite", "mysql", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			var driver gorm.Dialector
			switch dialect {
			case "sqlite":
				driver = sqlite.Open(":memory:")
			case "mysql":
				dsn := os.Getenv("TEST_MYSQL_DSN")
				if dsn == "" {
					t.Skip("TEST_MYSQL_DSN is not configured")
				}
				driver = mysql.Open(dsn)
			case "postgres":
				dsn := os.Getenv("TEST_POSTGRES_DSN")
				if dsn == "" {
					t.Skip("TEST_POSTGRES_DSN is not configured")
				}
				driver = postgres.Open(dsn)
			}
			db, err := gorm.Open(driver, &gorm.Config{NamingStrategy: schema.NamingStrategy{TablePrefix: "monitor_config_test_"}})
			require.NoError(t, err)
			sqlDB, err := db.DB()
			require.NoError(t, err)
			previousDB, previousGroup := DB, commonGroupCol
			common.OptionMapRWMutex.Lock()
			previousOptions := common.OptionMap
			common.OptionMap = map[string]string{}
			common.OptionMapRWMutex.Unlock()
			DB = db
			commonGroupCol = "`group`"
			if dialect == "postgres" {
				commonGroupCol = `"group"`
			}
			t.Cleanup(func() {
				DB = previousDB
				commonGroupCol = previousGroup
				common.OptionMapRWMutex.Lock()
				common.OptionMap = previousOptions
				common.OptionMapRWMutex.Unlock()
				require.NoError(t, db.Migrator().DropTable(&Option{}, &ChannelMonitorArtifact{}))
				require.NoError(t, sqlDB.Close())
			})
			require.NoError(t, db.AutoMigrate(&Option{}, &ChannelMonitorArtifact{}))
			var version string
			query := "SELECT version()"
			if dialect == "sqlite" {
				query = "SELECT sqlite_version()"
			}
			require.NoError(t, db.Raw(query).Scan(&version).Error)
			t.Logf("database version: %s", version)
			groups := []ChannelMonitorGroup{{Key: "OpenAI / vip", Models: []string{"m"}}}
			config := DefaultChannelMonitorConfig(groups)
			config.Groups[0].IntervalMinutes = 15
			require.NoError(t, SaveChannelMonitorConfig(config))
			loaded, err := LoadChannelMonitorConfig(groups)
			require.NoError(t, err)
			assert.Equal(t, 15, loaded.Groups[0].IntervalMinutes)
			loaded, err = LoadChannelMonitorConfig(append(groups, ChannelMonitorGroup{Key: "OpenAI / new"}))
			require.NoError(t, err)
			assert.False(t, loaded.Groups[1].Enabled)
			var option Option
			require.NoError(t, db.Where(&Option{Key: ChannelMonitorConfigKey}).First(&option).Error)
			require.NotEmpty(t, option.Value)
			for i := range 12 {
				require.NoError(t, SaveChannelMonitorArtifact(&ChannelMonitorArtifact{Group: "OpenAI / vip", Model: "m", PresetID: ChannelMonitorPresetCandy, DayKey: "2026-10-10", CreatedAt: int64(100 + i), Content: "21"}))
			}
			require.NoError(t, TrimChannelMonitorArtifacts("2026-10-10", "OpenAI / vip", "m", ChannelMonitorPresetCandy, 10))
			rows, err := ListChannelMonitorArtifacts(ChannelMonitorFilter{Group: "OpenAI / vip"}, 1000)
			require.NoError(t, err)
			assert.Len(t, rows, 10)
			recent, err := LatestChannelMonitorRuns(0)
			require.NoError(t, err)
			require.Len(t, recent, 1)
			assert.EqualValues(t, 111, recent[0].CreatedAt)
			midnight := ChannelMonitorDayStart(time.Date(2026, 10, 11, 0, 1, 0, 0, time.FixedZone("CST", 8*60*60)))
			lastRun := midnight - 60
			require.NoError(t, SaveChannelMonitorArtifact(&ChannelMonitorArtifact{Group: "OpenAI / vip", Model: "m", PresetID: ChannelMonitorPresetCandy, DayKey: "2026-10-10", CreatedAt: lastRun, Content: "21"}))
			recent, err = LatestChannelMonitorRuns(0)
			require.NoError(t, err)
			checkpoints, err := PreserveChannelMonitorRuns(recent, midnight+60)
			require.NoError(t, err)
			require.Len(t, checkpoints, 1)
			assert.Equal(t, lastRun, checkpoints[0].CreatedAt)
			require.NoError(t, DeleteChannelMonitorArtifactsBefore(midnight))
			remaining, err := LatestChannelMonitorRuns(0)
			require.NoError(t, err)
			require.Empty(t, remaining)
			var checkpointOption Option
			require.NoError(t, db.Where(&Option{Key: ChannelMonitorLastRunsKey}).First(&checkpointOption).Error)
			require.NoError(t, db.AutoMigrate(&Option{}, &ChannelMonitorArtifact{}))
			common.OptionMapRWMutex.Lock()
			common.OptionMap = map[string]string{}
			common.OptionMapRWMutex.Unlock()
			require.NoError(t, updateOptionMap(option.Key, option.Value))
			require.NoError(t, updateOptionMap(checkpointOption.Key, checkpointOption.Value))
			loaded, err = LoadChannelMonitorConfig(groups)
			require.NoError(t, err)
			assert.Equal(t, config, loaded)
			checkpoints, err = PreserveChannelMonitorRuns(remaining, midnight+120)
			require.NoError(t, err)
			require.Len(t, checkpoints, 1)
			assert.Equal(t, "OpenAI / vip", checkpoints[0].Group)
			assert.Equal(t, "m", checkpoints[0].Model)
			assert.Equal(t, ChannelMonitorPresetCandy, checkpoints[0].PresetID)
			assert.Equal(t, lastRun, checkpoints[0].CreatedAt)
			checkpoints, err = PreserveChannelMonitorRuns(nil, lastRun+1440*60+1)
			require.NoError(t, err)
			assert.Empty(t, checkpoints, "expired scheduling timestamps should not accumulate")
		})
	}
}
