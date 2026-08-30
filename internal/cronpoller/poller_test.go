package cronpoller

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mideco-tech/codex-tg/internal/model"
)

type fakeRequestSink struct {
	requests map[string]model.ExternalLaunchRequest
}

func (*fakeRequestSink) NoteExternalPollStarted(context.Context, string)       {}
func (*fakeRequestSink) NoteExternalPollResult(context.Context, string, error) {}

func (s *fakeRequestSink) EnqueueExternalRequests(_ context.Context, source string, requests []model.ExternalLaunchRequest) (int, error) {
	if s.requests == nil {
		s.requests = make(map[string]model.ExternalLaunchRequest)
	}
	created := 0
	for _, request := range requests {
		key := source + "|" + request.ExternalID
		if _, exists := s.requests[key]; exists {
			continue
		}
		s.requests[key] = request
		created++
	}
	return created, nil
}

func TestPollOnceEnqueuesOneTelegramApprovalRequestAfterDailyCronTime(t *testing.T) {
	path := writeCronConfig(t, `{
  "version": 1,
  "timezone": "Europe/Minsk",
  "tasks": [{
    "id": "morning-reading",
    "cron": "0 10 * * *",
    "cwd": "/project",
    "prompt": "Use the reading skill.",
    "model": "gpt-5.6-luna",
    "reasoning_effort": "high",
    "launch_policy": "telegram",
    "enabled": true
  }]
}`)
	sink := &fakeRequestSink{}
	poller := New(path, 77, sink)
	poller.now = func() time.Time { return time.Date(2026, 8, 24, 10, 1, 0, 0, time.FixedZone("test", 3*60*60)) }

	created, err := poller.PollOnce(context.Background())
	if err != nil || created != 1 {
		t.Fatalf("first poll created=%d err=%v", created, err)
	}
	created, err = poller.PollOnce(context.Background())
	if err != nil || created != 0 {
		t.Fatalf("second poll created=%d err=%v, want same-day dedupe", created, err)
	}
	request := sink.requests[Source+"|morning-reading:2026-08-24"]
	if request.Source != Source || request.Sender != "cron" || request.CWD != "/project" || request.Prompt != "Use the reading skill." {
		t.Fatalf("request=%#v", request)
	}
	if request.AutoStart || request.TelegramTopicID != 77 {
		t.Fatalf("launch policy request=%#v, want Telegram approval", request)
	}
	if request.Model != "gpt-5.6-luna" || request.ReasoningEffort != "high" {
		t.Fatalf("execution settings request=%#v", request)
	}
}

func TestPollOnceAfterFiveMissedDaysCreatesOnlyCurrentDayRequest(t *testing.T) {
	path := writeCronConfig(t, dailyConfig("telegram"))
	sink := &fakeRequestSink{}
	poller := New(path, 77, sink)
	poller.now = func() time.Time { return time.Date(2026, 8, 29, 15, 0, 0, 0, time.UTC) }

	created, err := poller.PollOnce(context.Background())
	if err != nil || created != 1 || len(sink.requests) != 1 {
		t.Fatalf("created=%d requests=%#v err=%v", created, sink.requests, err)
	}
	if _, ok := sink.requests[Source+"|morning-reading:2026-08-29"]; !ok {
		t.Fatalf("requests=%#v, want only current local day", sink.requests)
	}
}

func TestPollOnceHonorsOptionalMaxLateness(t *testing.T) {
	path := writeCronConfig(t, strings.Replace(dailyConfig("telegram"), `"enabled": true`, `"max_lateness": "2h",
    "enabled": true`, 1))
	sink := &fakeRequestSink{}
	poller := New(path, 77, sink)
	poller.now = func() time.Time { return time.Date(2026, 8, 24, 12, 1, 0, 0, time.UTC) }

	created, err := poller.PollOnce(context.Background())
	if err != nil || created != 0 {
		t.Fatalf("late poll created=%d err=%v, want skipped", created, err)
	}
	poller.now = func() time.Time { return time.Date(2026, 8, 25, 11, 59, 0, 0, time.UTC) }
	created, err = poller.PollOnce(context.Background())
	if err != nil || created != 1 {
		t.Fatalf("in-window poll created=%d err=%v, want 1", created, err)
	}
}

func TestResumeGateWaitsForContinuousRuntimeBeforeCronCatchup(t *testing.T) {
	path := writeCronConfig(t, dailyConfig("telegram"))
	sink := &fakeRequestSink{}
	poller := New(path, 77, sink)
	base := time.Date(2026, 8, 24, 10, 5, 0, 0, time.UTC)

	poller.now = func() time.Time { return base }
	created, err := poller.pollOnceAfterResume(context.Background())
	if err != nil || created != 0 {
		t.Fatalf("first resumed poll created=%d err=%v, want warm-up", created, err)
	}
	for elapsed := 30 * time.Second; elapsed < resumeGrace; elapsed += 30 * time.Second {
		poller.now = func() time.Time { return base.Add(elapsed) }
		created, err = poller.pollOnceAfterResume(context.Background())
		if err != nil || created != 0 {
			t.Fatalf("warm-up at %s created=%d err=%v", elapsed, created, err)
		}
	}
	poller.now = func() time.Time { return base.Add(resumeGrace) }
	created, err = poller.pollOnceAfterResume(context.Background())
	if err != nil || created != 1 {
		t.Fatalf("stable runtime created=%d err=%v, want 1", created, err)
	}
}

func TestPollOnceBeforeDailyCronTimeWaitsForToday(t *testing.T) {
	path := writeCronConfig(t, dailyConfig("telegram"))
	sink := &fakeRequestSink{}
	poller := New(path, 77, sink)
	poller.now = func() time.Time { return time.Date(2026, 8, 24, 9, 59, 0, 0, time.FixedZone("test", 3*60*60)) }

	created, err := poller.PollOnce(context.Background())
	if err != nil || created != 0 || len(sink.requests) != 0 {
		t.Fatalf("created=%d requests=%#v err=%v", created, sink.requests, err)
	}
}

func TestPollOnceWeeklyScheduleCatchesUpOnceForCurrentWeek(t *testing.T) {
	path := writeCronConfig(t, weeklyConfig("telegram"))
	sink := &fakeRequestSink{}
	poller := New(path, 77, sink)
	poller.now = func() time.Time { return time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC) }

	created, err := poller.PollOnce(context.Background())
	if err != nil || created != 1 {
		t.Fatalf("first poll created=%d err=%v", created, err)
	}
	created, err = poller.PollOnce(context.Background())
	if err != nil || created != 0 {
		t.Fatalf("second poll created=%d err=%v, want same-week dedupe", created, err)
	}
	request, ok := sink.requests[Source+"|weekly-radar:2026-08-24"]
	if !ok {
		t.Fatalf("requests=%#v, want Monday occurrence identity", sink.requests)
	}
	if request.CWD != "/project" || request.Prompt != "Scan the internal radar." || request.Model != "gpt-5.6-sol" || request.ReasoningEffort != "high" || request.AutoStart {
		t.Fatalf("weekly request=%#v", request)
	}
	poller.now = func() time.Time { return time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC) }
	created, err = poller.PollOnce(context.Background())
	if err != nil || created != 1 || len(sink.requests) != 2 {
		t.Fatalf("poll after five missed weeks created=%d requests=%#v err=%v", created, sink.requests, err)
	}
	if _, ok := sink.requests[Source+"|weekly-radar:2026-09-28"]; !ok {
		t.Fatalf("requests=%#v, want only current week's Monday occurrence", sink.requests)
	}
}

func TestPollOnceWeeklyScheduleWaitsForCurrentWeekSlot(t *testing.T) {
	path := writeCronConfig(t, weeklyConfig("telegram"))
	sink := &fakeRequestSink{}
	poller := New(path, 77, sink)
	poller.now = func() time.Time { return time.Date(2026, 8, 24, 9, 59, 0, 0, time.UTC) }

	created, err := poller.PollOnce(context.Background())
	if err != nil || created != 0 || len(sink.requests) != 0 {
		t.Fatalf("created=%d requests=%#v err=%v", created, sink.requests, err)
	}
}

func TestPollOnceAutoPolicySkipsTelegramLaunchApproval(t *testing.T) {
	path := writeCronConfig(t, dailyConfig("auto"))
	sink := &fakeRequestSink{}
	poller := New(path, 0, sink)
	poller.now = func() time.Time { return time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC) }

	created, err := poller.PollOnce(context.Background())
	if err != nil || created != 1 {
		t.Fatalf("created=%d err=%v", created, err)
	}
	request := sink.requests[Source+"|morning-reading:2026-08-24"]
	if !request.AutoStart || request.TelegramTopicID != 0 {
		t.Fatalf("request=%#v, want auto start without approval topic", request)
	}
}

func TestPollOnceFailsClosedForInvalidOrUnsupportedSchedule(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{name: "invalid json", body: `{`, want: "cron config"},
		{name: "trailing json", body: dailyConfig("telegram") + `{}`, want: "cron config"},
		{name: "monthly schedule", body: strings.Replace(dailyConfig("telegram"), "0 10 * * *", "0 10 1 * *", 1), want: "daily or weekly"},
		{name: "multiple weekdays", body: strings.Replace(dailyConfig("telegram"), "0 10 * * *", "0 10 * * 1,3", 1), want: "one weekday"},
		{name: "bad policy", body: strings.Replace(dailyConfig("telegram"), `"telegram"`, `"sometimes"`, 1), want: "launch_policy"},
		{name: "bad max lateness", body: strings.Replace(dailyConfig("telegram"), `"enabled": true`, `"max_lateness": "later",
    "enabled": true`, 1), want: "max_lateness"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := writeCronConfig(t, test.body)
			sink := &fakeRequestSink{}
			poller := New(path, 77, sink)
			poller.now = func() time.Time { return time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC) }
			if _, err := poller.PollOnce(context.Background()); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("err=%v, want %q", err, test.want)
			}
			if len(sink.requests) != 0 {
				t.Fatalf("invalid config enqueued %#v", sink.requests)
			}
		})
	}
}

func TestPollOnceMissingConfigIsDisabled(t *testing.T) {
	poller := New(filepath.Join(t.TempDir(), "missing.json"), 77, &fakeRequestSink{})
	created, err := poller.PollOnce(context.Background())
	if err != nil || created != 0 {
		t.Fatalf("created=%d err=%v", created, err)
	}
}

func dailyConfig(policy string) string {
	return strings.Replace(`{
  "version": 1,
  "timezone": "UTC",
  "tasks": [{
    "id": "morning-reading",
    "cron": "0 10 * * *",
    "cwd": "/project",
    "prompt": "Read.",
    "model": "gpt-5.6-luna",
    "reasoning_effort": "high",
    "launch_policy": "POLICY",
    "enabled": true
  }]
}`, "POLICY", policy, 1)
}

func weeklyConfig(policy string) string {
	return strings.Replace(`{
  "version": 1,
  "timezone": "UTC",
  "tasks": [{
    "id": "weekly-radar",
    "cron": "0 10 * * 1",
    "cwd": "/project",
    "prompt": "Scan the internal radar.",
    "model": "gpt-5.6-sol",
    "reasoning_effort": "high",
    "launch_policy": "POLICY",
    "enabled": true
  }]
}`, "POLICY", policy, 1)
}

func writeCronConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "cron.json")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
