package daemon

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/mideco-tech/codex-tg/internal/model"
)

func (s *Service) externalRequestsOverview(ctx context.Context, filter string) (*DirectResponse, error) {
	filter = strings.ToLower(strings.TrimSpace(filter))
	if filter == "" {
		filter = "active"
	}
	var statuses []string
	switch filter {
	case "active":
		statuses = []string{
			model.ExternalLaunchPendingApproval,
			model.ExternalLaunchStarting,
			model.ExternalLaunchSessionStarted,
			model.ExternalLaunchFailed,
			model.ExternalLaunchOutcomeUnknown,
		}
	case "running":
		statuses = []string{model.ExternalLaunchStarting, model.ExternalLaunchSessionStarted}
	case "failed":
		statuses = []string{model.ExternalLaunchFailed, model.ExternalLaunchOutcomeUnknown}
	case "all":
		statuses = nil
	default:
		return &DirectResponse{Text: "Usage: /requests [active|running|failed|all]"}, nil
	}
	requests, err := s.store.ListExternalLaunchRequestsByStatus(ctx, statuses, 20)
	if err != nil {
		return nil, err
	}
	lines := []string{fmt.Sprintf("External requests · %s", filter)}
	if len(requests) == 0 {
		lines = append(lines, "", "No matching requests.")
		return &DirectResponse{Text: strings.Join(lines, "\n")}, nil
	}
	now := s.now().UTC()
	for _, request := range requests {
		age := formatExternalRequestAge(request.UpdatedAt, now)
		lines = append(lines, "", fmt.Sprintf("%s · %s · %s", externalLaunchStatusMarker(request.Status), externalLaunchStatusLabel(request.Status), age))
		lines = append(lines, request.ID)
		lines = append(lines, fmt.Sprintf("%s · %s", request.Source, strings.TrimSpace(request.Title)))
		if request.ThreadID != "" {
			lines = append(lines, "Thread: "+request.ThreadID)
		}
		if request.ErrorSummary != "" {
			lines = append(lines, "Error: "+sanitizeDiagnosticString(request.ErrorSummary))
		}
	}
	return &DirectResponse{Text: strings.Join(lines, "\n")}, nil
}

func externalLaunchStatusMarker(status string) string {
	switch status {
	case model.ExternalLaunchPendingApproval:
		return "🟡"
	case model.ExternalLaunchStarting, model.ExternalLaunchSessionStarted:
		return "🔵"
	case model.ExternalLaunchFailed:
		return "🔴"
	case model.ExternalLaunchOutcomeUnknown:
		return "🟣"
	case model.ExternalLaunchDismissed, model.ExternalLaunchRejectedSender:
		return "⚪"
	default:
		return "🟢"
	}
}

func formatExternalRequestAge(value model.TimeString, now time.Time) string {
	at, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(string(value)))
	if err != nil {
		return "unknown age"
	}
	age := now.Sub(at)
	if age < 0 {
		age = 0
	}
	return age.Round(time.Second).String() + " ago"
}
