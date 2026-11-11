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
// contract and optional lateness bound.
func DueOccurrence(spec, timezone, maxLateness string, now time.Time) (time.Time, bool, error) {
	schedule, location, err := parseOccurrenceSchedule(spec, timezone, maxLateness)
	if err != nil {
		return time.Time{}, false, err
	}
	slot, due := scheduleDue(schedule, now.In(location))
	if schedule.maxLateness > 0 && now.Sub(slot) > schedule.maxLateness {
		due = false
	}
	return slot, due, nil
}

// NextOccurrence reports the next future first slot of a day or week.
func NextOccurrence(spec, timezone, maxLateness string, now time.Time) (time.Time, error) {
	schedule, location, err := parseOccurrenceSchedule(spec, timezone, maxLateness)
	if err != nil {
		return time.Time{}, err
	}
	slot, periodEnd := firstPeriodOccurrence(schedule, now.In(location))
	if slot.After(now) {
		return slot, nil
	}
	return schedule.schedule.Next(periodEnd.Add(-time.Nanosecond)), nil
}

func parseOccurrenceSchedule(spec, timezone, maxLateness string) (taskSchedule, *time.Location, error) {
	raw, _ := json.Marshal(File{Version: 1, Timezone: timezone, Tasks: []Task{{ID: "board", Cron: spec, CWD: "/", Prompt: "schedule", LaunchPolicy: "telegram", Enabled: true, MaxLateness: maxLateness}}})
	_, location, schedules, err := parseFile(raw, 1)
	if err != nil {
		return taskSchedule{}, nil, err
	}
	return schedules[0], location, nil
}
