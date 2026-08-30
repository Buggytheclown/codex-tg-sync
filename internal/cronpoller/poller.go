package cronpoller

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/mideco-tech/codex-tg/internal/model"
	robfigcron "github.com/robfig/cron/v3"
)

const (
	Source               = "cron"
	DefaultPollInterval  = 30 * time.Second
	resumeObservationGap = 90 * time.Second
	resumeGrace          = 4 * time.Minute
)

var taskIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)
var singleWeekdayPattern = regexp.MustCompile(`(?i)^(0|1|2|3|4|5|6|SUN|MON|TUE|WED|THU|FRI|SAT)$`)

type schedulePeriod string

const (
	periodDaily  schedulePeriod = "daily"
	periodWeekly schedulePeriod = "weekly"
)

type taskSchedule struct {
	schedule    robfigcron.Schedule
	period      schedulePeriod
	maxLateness time.Duration
}

type RequestSink interface {
	EnqueueExternalRequests(ctx context.Context, source string, requests []model.ExternalLaunchRequest) (int, error)
	NoteExternalPollStarted(ctx context.Context, source string)
	NoteExternalPollResult(ctx context.Context, source string, err error)
}

type File struct {
	Version  int    `json:"version"`
	Timezone string `json:"timezone"`
	Tasks    []Task `json:"tasks"`
}

type Task struct {
	ID              string `json:"id"`
	Cron            string `json:"cron"`
	CWD             string `json:"cwd"`
	Prompt          string `json:"prompt"`
	Model           string `json:"model"`
	ReasoningEffort string `json:"reasoning_effort"`
	LaunchPolicy    string `json:"launch_policy"`
	Enabled         bool   `json:"enabled"`
	MaxLateness     string `json:"max_lateness,omitempty"`
}

type Poller struct {
	path            string
	telegramTopicID int64
	sink            RequestSink
	now             func() time.Time
	lastRunAt       time.Time
	resumeReadyAt   time.Time
}

func New(path string, telegramTopicID int64, sink RequestSink) *Poller {
	return &Poller{path: strings.TrimSpace(path), telegramTopicID: telegramTopicID, sink: sink, now: time.Now}
}

func (p *Poller) PollOnce(ctx context.Context) (int, error) {
	if p == nil || p.sink == nil || strings.TrimSpace(p.path) == "" {
		return 0, errors.New("cron poller is not configured")
	}
	data, err := os.ReadFile(p.path)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, fmt.Errorf("read cron config: %w", err)
	}
	file, location, schedules, err := parseFile(data, p.telegramTopicID)
	if err != nil {
		return 0, err
	}
	now := p.now().In(location)
	requests := make([]model.ExternalLaunchRequest, 0, len(file.Tasks))
	for i, task := range file.Tasks {
		slot, due := scheduleDue(schedules[i], now)
		if !task.Enabled || !due {
			continue
		}
		if schedules[i].maxLateness > 0 && now.Sub(slot) > schedules[i].maxLateness {
			continue
		}
		requests = append(requests, requestForTask(task, p.telegramTopicID, now, slot))
	}
	if len(requests) == 0 {
		return 0, nil
	}
	return p.sink.EnqueueExternalRequests(ctx, Source, requests)
}

func (p *Poller) Run(ctx context.Context, interval time.Duration, onResult func(error)) {
	if interval <= 0 {
		interval = DefaultPollInterval
	}
	for {
		p.sink.NoteExternalPollStarted(ctx, Source)
		_, err := p.pollOnceAfterResume(ctx)
		p.sink.NoteExternalPollResult(ctx, Source, err)
		if onResult != nil {
			onResult(err)
		}
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

func (p *Poller) pollOnceAfterResume(ctx context.Context) (int, error) {
	now := p.now()
	if p.lastRunAt.IsZero() || now.Before(p.lastRunAt) || now.Sub(p.lastRunAt) > resumeObservationGap {
		p.resumeReadyAt = now.Add(resumeGrace)
	}
	p.lastRunAt = now
	if now.Before(p.resumeReadyAt) {
		return 0, nil
	}
	return p.PollOnce(ctx)
}

func parseFile(data []byte, telegramTopicID int64) (File, *time.Location, []taskSchedule, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var file File
	if err := decoder.Decode(&file); err != nil {
		return File{}, nil, nil, fmt.Errorf("decode cron config: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return File{}, nil, nil, errors.New("decode cron config: trailing JSON value")
	}
	if file.Version != 1 {
		return File{}, nil, nil, fmt.Errorf("cron config version must be 1, got %d", file.Version)
	}
	location, err := time.LoadLocation(strings.TrimSpace(file.Timezone))
	if err != nil {
		return File{}, nil, nil, fmt.Errorf("cron config timezone: %w", err)
	}
	parser := robfigcron.NewParser(robfigcron.Minute | robfigcron.Hour | robfigcron.Dom | robfigcron.Month | robfigcron.Dow)
	schedules := make([]taskSchedule, len(file.Tasks))
	seen := make(map[string]struct{}, len(file.Tasks))
	for i := range file.Tasks {
		task := &file.Tasks[i]
		task.ID = strings.TrimSpace(task.ID)
		task.Cron = strings.TrimSpace(task.Cron)
		task.CWD = strings.TrimSpace(task.CWD)
		task.Prompt = strings.TrimSpace(task.Prompt)
		task.Model = strings.TrimSpace(task.Model)
		task.ReasoningEffort = normalizeEffort(task.ReasoningEffort)
		task.LaunchPolicy = strings.ToLower(strings.TrimSpace(task.LaunchPolicy))
		task.MaxLateness = strings.TrimSpace(task.MaxLateness)
		if task.LaunchPolicy == "" {
			task.LaunchPolicy = "telegram"
		}
		if !taskIDPattern.MatchString(task.ID) {
			return File{}, nil, nil, fmt.Errorf("cron task %d id must use letters, digits, dot, underscore, or dash", i+1)
		}
		if _, duplicate := seen[task.ID]; duplicate {
			return File{}, nil, nil, fmt.Errorf("cron task id %q is duplicated", task.ID)
		}
		seen[task.ID] = struct{}{}
		if task.CWD == "" || task.Prompt == "" {
			return File{}, nil, nil, fmt.Errorf("cron task %q requires cwd and prompt", task.ID)
		}
		if task.LaunchPolicy != "telegram" && task.LaunchPolicy != "auto" {
			return File{}, nil, nil, fmt.Errorf("cron task %q launch_policy must be telegram or auto", task.ID)
		}
		if task.LaunchPolicy == "telegram" && telegramTopicID == 0 {
			return File{}, nil, nil, fmt.Errorf("cron task %q requires the external requests Telegram topic", task.ID)
		}
		if !validEffort(task.ReasoningEffort) {
			return File{}, nil, nil, fmt.Errorf("cron task %q has unsupported reasoning_effort %q", task.ID, task.ReasoningEffort)
		}
		fields := strings.Fields(task.Cron)
		if len(fields) != 5 || fields[2] != "*" || fields[3] != "*" {
			return File{}, nil, nil, fmt.Errorf("cron task %q must use a daily or weekly five-field cron schedule", task.ID)
		}
		period := periodDaily
		if fields[4] != "*" {
			if !singleWeekdayPattern.MatchString(fields[4]) {
				return File{}, nil, nil, fmt.Errorf("cron task %q weekly schedule must select one weekday", task.ID)
			}
			period = periodWeekly
		}
		schedule, err := parser.Parse(task.Cron)
		if err != nil {
			return File{}, nil, nil, fmt.Errorf("cron task %q schedule: %w", task.ID, err)
		}
		maxLateness := time.Duration(0)
		if task.MaxLateness != "" {
			maxLateness, err = time.ParseDuration(task.MaxLateness)
			if err != nil || maxLateness <= 0 {
				return File{}, nil, nil, fmt.Errorf("cron task %q max_lateness must be a positive duration", task.ID)
			}
		}
		schedules[i] = taskSchedule{schedule: schedule, period: period, maxLateness: maxLateness}
	}
	return file, location, schedules, nil
}

func scheduleDue(schedule taskSchedule, now time.Time) (time.Time, bool) {
	dayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	periodStart := dayStart
	periodDays := 1
	if schedule.period == periodWeekly {
		daysSinceMonday := (int(dayStart.Weekday()) + 6) % 7
		periodStart = dayStart.AddDate(0, 0, -daysSinceMonday)
		periodDays = 7
	}
	first := schedule.schedule.Next(periodStart.Add(-time.Nanosecond))
	return first, !first.After(now) && first.Before(periodStart.AddDate(0, 0, periodDays))
}

func requestForTask(task Task, telegramTopicID int64, now, slot time.Time) model.ExternalLaunchRequest {
	externalID := task.ID + ":" + slot.Format("2006-01-02")
	timestamp := model.TimeString(now.UTC().Format(time.RFC3339Nano))
	autoStart := task.LaunchPolicy == "auto"
	topicID := telegramTopicID
	if autoStart {
		topicID = 0
	}
	return model.ExternalLaunchRequest{
		ID: Source + ":" + externalID, Source: Source, ExternalID: externalID, Sender: Source,
		Title: "Scheduled task: " + task.ID, SafePreview: preview(task.Prompt, 240), Prompt: task.Prompt,
		CWD: task.CWD, Model: task.Model, ReasoningEffort: task.ReasoningEffort,
		Status: model.ExternalLaunchPendingApproval, TelegramTopicID: topicID, AutoStart: autoStart,
		CreatedAt: timestamp, UpdatedAt: timestamp,
	}
}

func normalizeEffort(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "x-high" || value == "x_high" {
		return "xhigh"
	}
	return value
}

func validEffort(value string) bool {
	switch value {
	case "", "none", "minimal", "low", "medium", "high", "xhigh", "max", "ultra":
		return true
	default:
		return false
	}
}

func preview(value string, limit int) string {
	value = strings.TrimSpace(value)
	if limit <= 0 || utf8.RuneCountInString(value) <= limit {
		return value
	}
	runes := []rune(value)
	return strings.TrimSpace(string(runes[:limit])) + "…"
}
