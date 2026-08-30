package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/mideco-tech/codex-tg/internal/appserver"
	"github.com/mideco-tech/codex-tg/internal/model"
	"github.com/mideco-tech/codex-tg/internal/storage"
)

const chatsProjectName = "Chats"

type projectWorkspace struct {
	Key               string `json:"key,omitempty"`
	ProjectName       string `json:"project_name"`
	DirectoryName     string `json:"directory_name,omitempty"`
	CWD               string `json:"cwd"`
	LatestThread      string `json:"latest_thread_id,omitempty"`
	LatestThreadLabel string `json:"latest_thread_label,omitempty"`
	ThreadCount       int    `json:"thread_count,omitempty"`
	UpdatedAt         int64  `json:"updated_at,omitempty"`
}

type pendingNewThreadState struct {
	ProjectName   string
	DirectoryName string
	CWD           string
}

func (s *Service) projectWorkspaces(ctx context.Context) ([]projectWorkspace, error) {
	threads, err := s.store.ListThreads(ctx, 500, "")
	if err != nil {
		return nil, err
	}
	grouped := map[string]*projectWorkspace{}
	for _, thread := range threads {
		if s.isCodexChatThread(thread) {
			continue
		}
		cwdKey := model.NormalizePath(thread.CWD)
		if cwdKey == "" {
			cwdKey = "thread:" + thread.ID
		}
		workspace := grouped[cwdKey]
		if workspace == nil {
			projectName, directoryName := strings.TrimSpace(thread.ProjectName), strings.TrimSpace(thread.DirectoryName)
			if projectName == "" || directoryName == "" {
				derivedProject, derivedDirectory := model.ProjectNameFromCWD(thread.CWD)
				projectName = firstNonEmpty(projectName, derivedProject)
				directoryName = firstNonEmpty(directoryName, derivedDirectory)
			}
			workspace = &projectWorkspace{
				ProjectName: firstNonEmpty(projectName, "Shared/General"), DirectoryName: directoryName, CWD: thread.CWD,
				LatestThread: thread.ID, LatestThreadLabel: threadDisplayLabel(thread), UpdatedAt: thread.UpdatedAt,
			}
			grouped[cwdKey] = workspace
		}
		workspace.ThreadCount++
		if thread.UpdatedAt > workspace.UpdatedAt {
			workspace.UpdatedAt, workspace.LatestThread = thread.UpdatedAt, thread.ID
			workspace.LatestThreadLabel = threadDisplayLabel(thread)
		}
	}
	workspaces := make([]projectWorkspace, 0, len(grouped))
	for _, workspace := range grouped {
		workspaces = append(workspaces, *workspace)
	}
	sort.Slice(workspaces, func(i, j int) bool {
		if workspaces[i].UpdatedAt != workspaces[j].UpdatedAt {
			return workspaces[i].UpdatedAt > workspaces[j].UpdatedAt
		}
		return strings.ToLower(workspaces[i].ProjectName) < strings.ToLower(workspaces[j].ProjectName)
	})
	assignProjectWorkspaceKeys(workspaces)
	return workspaces, nil
}

func (s *Service) isCodexChatThread(thread model.Thread) bool {
	return isCodexChatsCWD(thread.CWD) || isPathUnderRoot(thread.CWD, s.cfg.CodexChatsRoot) ||
		(strings.TrimSpace(thread.CWD) == "" && strings.EqualFold(strings.TrimSpace(thread.ProjectName), chatsProjectName))
}

func isPathUnderRoot(value, root string) bool {
	normalized := strings.ToLower(strings.TrimRight(model.NormalizePath(value), "/"))
	normalizedRoot := strings.ToLower(strings.TrimRight(model.NormalizePath(root), "/"))
	return normalized != "" && normalizedRoot != "" && (normalized == normalizedRoot || strings.HasPrefix(normalized, normalizedRoot+"/"))
}

func isCodexChatsCWD(cwd string) bool {
	parts := strings.Split(strings.Trim(strings.ToLower(model.NormalizePath(cwd)), "/"), "/")
	if len(parts) >= 4 && parts[0] == "users" && parts[2] == "documents" && parts[3] == "codex" {
		return true
	}
	return len(parts) >= 5 && strings.HasSuffix(parts[0], ":") && parts[1] == "users" && parts[3] == "documents" && parts[4] == "codex"
}

func assignProjectWorkspaceKeys(workspaces []projectWorkspace) {
	seen := map[string]int{}
	for i := range workspaces {
		base := projectWorkspaceKeyBase(workspaces[i])
		seen[base]++
		workspaces[i].Key = base
		if seen[base] > 1 {
			workspaces[i].Key = fmt.Sprintf("%s-%d", base, seen[base])
		}
	}
}

func projectWorkspaceKeyBase(workspace projectWorkspace) string {
	source := strings.ToLower(firstNonEmpty(workspace.ProjectName, workspace.DirectoryName, workspace.CWD, "project"))
	var builder strings.Builder
	lastDash := false
	for _, r := range source {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			builder.WriteRune(r)
			lastDash = false
		} else if !lastDash {
			builder.WriteByte('-')
			lastDash = true
		}
	}
	if key := strings.Trim(builder.String(), "-"); key != "" {
		return key
	}
	return "project"
}

func threadDisplayLabel(thread model.Thread) string {
	return firstNonEmpty(thread.Title, thread.ShortID())
}

func projectWorkspaceButtonLabel(index int, workspace projectWorkspace) string {
	return shortButtonLabel(fmt.Sprintf("%d. %s", index, firstNonEmpty(workspace.ProjectName, workspace.DirectoryName, workspace.Key, "Project")))
}

func threadFromStartPayload(payload map[string]any, state pendingNewThreadState) model.Thread {
	thread := appserver.ThreadFromPayload(payload)
	if strings.TrimSpace(thread.CWD) == "" {
		thread.CWD = state.CWD
	}
	if strings.EqualFold(strings.TrimSpace(state.ProjectName), chatsProjectName) {
		thread.ProjectName = chatsProjectName
		thread.DirectoryName = firstNonEmpty(state.DirectoryName, thread.DirectoryName)
	}
	if strings.TrimSpace(thread.ProjectName) == "" {
		project, directory := model.ProjectNameFromCWD(thread.CWD)
		thread.ProjectName = firstNonEmpty(state.ProjectName, project)
		thread.DirectoryName = firstNonEmpty(state.DirectoryName, directory)
	}
	if strings.TrimSpace(thread.DirectoryName) == "" {
		_, directory := model.ProjectNameFromCWD(thread.CWD)
		thread.DirectoryName = firstNonEmpty(state.DirectoryName, directory)
	}
	if strings.TrimSpace(thread.Title) == "" {
		thread.Title = "New thread"
	}
	if thread.UpdatedAt == 0 {
		thread.UpdatedAt = time.Now().UTC().Unix()
	}
	if len(thread.Raw) == 0 {
		thread.Raw = json.RawMessage(storage.MustJSON(payload))
	}
	return thread
}
