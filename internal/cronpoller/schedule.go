package cronpoller

import (
	"encoding/json"
	"time"
)

// ParseConfig is the one-time board importer boundary. Execution no longer
// rereads this file after the source has been imported into SQLite.
func ParseConfig(data []byte, approvalTopicID int64) (File, error) {
	file, _, _, err := parseFile(data, approvalTopicID)
	return file, err
}

// DueOccurrence preserves the existing current-day/current-week catch-up
// contract and optional lateness bound. The scheduled local date is identity.
func DueOccurrence(spec, timezone, maxLateness string, now time.Time) (time.Time, bool, error) {
	raw, _ := json.Marshal(File{Version: 1, Timezone: timezone, Tasks: []Task{{ID: "board", Cron: spec, CWD: "/", Prompt: "schedule", LaunchPolicy: "telegram", Enabled: true, MaxLateness: maxLateness}}})
	_, location, schedules, err := parseFile(raw, 1)
	if err != nil {
		return time.Time{}, false, err
	}
	slot, due := scheduleDue(schedules[0], now.In(location))
	if schedules[0].maxLateness > 0 && now.Sub(slot) > schedules[0].maxLateness {
		due = false
	}
	return slot, due, nil
}
