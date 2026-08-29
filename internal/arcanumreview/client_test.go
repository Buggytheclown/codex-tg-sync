package arcanumreview

import (
	"context"
	"os"
	"reflect"
	"testing"
)

type recordingCommandRunner struct {
	name   string
	args   []string
	output []byte
	err    error
}

func (r *recordingCommandRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	r.name = name
	r.args = append([]string(nil), args...)
	return append([]byte(nil), r.output...), r.err
}

func TestCLIClientListsAssignedPullRequestsWithExactQuery(t *testing.T) {
	t.Parallel()
	runner := &recordingCommandRunner{output: []byte(`{
  "review_requests": [{
    "id": 12345678,
    "url": "https://a.yandex-team.ru/review/12345678",
    "author": {"name": "alice"},
    "summary": "Example pull request"
  }],
  "has_next": false
}`)}
	client := NewCLIClient("/usr/local/bin/ya", runner)

	requests, err := client.ListAssigned(context.Background(), "reviewer-example")
	if err != nil {
		t.Fatalf("ListAssigned failed: %v", err)
	}
	wantArgs := []string{
		"tool", "gena-arcanum-cli", "--json", "pr", "search",
		"--query", "open(true);published(true);assignee(reviewer-example)",
		"--limit", "100", "--all", "--order=-updated_at",
		"--fields", "review_requests(id,url,author(name),summary),total_count",
	}
	if runner.name != "/usr/local/bin/ya" || !reflect.DeepEqual(runner.args, wantArgs) {
		t.Fatalf("command = %q %#v, want %q %#v", runner.name, runner.args, "/usr/local/bin/ya", wantArgs)
	}
	if len(requests) != 1 || requests[0].ID != 12345678 || requests[0].Author != "alice" || requests[0].Summary != "Example pull request" {
		t.Fatalf("requests = %#v", requests)
	}
}

func TestCLIClientRejectsMalformedAssignedPullRequest(t *testing.T) {
	t.Parallel()
	runner := &recordingCommandRunner{output: []byte(`{"review_requests":[{"id":12345678,"author":{},"summary":"Title"}]}`)}
	client := NewCLIClient("ya", runner)

	if _, err := client.ListAssigned(context.Background(), "reviewer-example"); err == nil {
		t.Fatal("ListAssigned succeeded with missing author")
	}
}

func TestCommandEnvironmentPrependsConfiguredYADirectoryToPath(t *testing.T) {
	t.Parallel()
	separator := string(os.PathListSeparator)
	got := commandEnvironment([]string{"HOME=/home/example", "PATH=/usr/bin:/bin"}, "/opt/arc/bin/ya")
	want := []string{"HOME=/home/example", "PATH=/opt/arc/bin" + separator + "/usr/bin:/bin"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("commandEnvironment() = %#v, want %#v", got, want)
	}
}

func TestCommandEnvironmentLeavesRelativeYABinaryUnchanged(t *testing.T) {
	t.Parallel()
	environ := []string{"HOME=/home/example", "PATH=/usr/bin:/bin"}
	if got := commandEnvironment(environ, "ya"); !reflect.DeepEqual(got, environ) {
		t.Fatalf("commandEnvironment() = %#v, want %#v", got, environ)
	}
}
