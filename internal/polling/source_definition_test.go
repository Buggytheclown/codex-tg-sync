package polling

import (
	"github.com/mideco-tech/codex-tg/internal/model"
	"strings"
	"testing"
)

func TestSourceChildWaitingMustResolveFromItsSource(t *testing.T) {
	for _, tc := range []struct {
		name, kind, id, login, author string
		wantErr                       bool
	}{
		{"PR dynamic defaults", TypeReview, "{{pr_id}}", "{{login}}", "{{author}}", false},
		{"PR changes dynamic defaults", TypeChanges, "{{pr_id}}", "reviewer", "{{author}}", false},
		{"cron cannot produce PR ID", TypeCron, "{{pr_id}}", "reviewer", "", true},
		{"cron cannot produce author", TypeCron, "123", "reviewer", "{{author}}", true},
		{"cron cannot produce login", TypeCron, "123", "{{login}}", "", true},
		{"cron explicit PR wait", TypeCron, "123", "reviewer", "known-author", false},
		{"cron invalid PR ID", TypeCron, "0", "reviewer", "", true},
		{"unsupported source variable", TypeReview, "{{missing}}", "reviewer", "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := Config{Login: "reviewer"}
			if tc.kind == TypeCron {
				cfg = Config{Cron: "0 9 * * *", Timezone: "UTC"}
			}
			wait := model.TaskPolling{Type: TypePRWait, Mode: model.PollingOnce, Config: jsonBytes(Config{PRID: tc.id, Login: tc.login, Author: tc.author, Conditions: []string{ConditionCommit, ConditionAnswered}}), Action: model.TaskPollingAction{Type: model.PollingResumeTask}}
			def := model.TaskDefinition{Title: "child", Config: model.TaskConfig{InitialPrompt: "start", ContinuationPrompt: "continue"}, Polling: &wait}
			p := model.TaskPolling{Type: tc.kind, Mode: model.PollingRepeat, Config: jsonBytes(cfg), Action: model.TaskPollingAction{Type: model.PollingCreateTask, ChildDefinition: &def}}
			err := Validate(p)
			if (err != nil) != tc.wantErr {
				t.Fatalf("Validate=%v wantErr=%v", err, tc.wantErr)
			}
			if tc.kind == TypeCron && strings.Contains(tc.id+tc.login+tc.author, "{{") && err != nil && !strings.Contains(err.Error(), "explicit") {
				t.Fatalf("cron error not actionable: %v", err)
			}
			got, _ := DecodeConfig(wait)
			if got.PRID != tc.id || got.Author != tc.author || got.Login != tc.login {
				t.Fatal("validation changed source-owned defaults")
			}
		})
	}
}
