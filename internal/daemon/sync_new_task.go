package daemon

import (
	"context"
	"fmt"
	"strings"

	"github.com/mideco-tech/codex-tg/internal/model"
	"github.com/mideco-tech/codex-tg/internal/storage"
)

const syncProjectMenuLimit = 8

func (s *Service) syncProjectsMenu(ctx context.Context, controlTopicID int64) (*DirectResponse, error) {
	state, err := s.store.GetSyncState(ctx)
	if err != nil {
		return nil, err
	}
	if state.State != model.SyncStateActive {
		return &DirectResponse{Text: "Sync projects are available only while Sync is active."}, nil
	}
	workspaces, err := s.projectWorkspaces(ctx)
	if err != nil {
		return nil, err
	}
	if len(workspaces) == 0 && strings.TrimSpace(s.cfg.DefaultCWD) != "" {
		project, directory := model.ProjectNameFromCWD(s.cfg.DefaultCWD)
		workspaces = append(workspaces, projectWorkspace{ProjectName: firstNonEmpty(project, "Default"), DirectoryName: directory, CWD: s.cfg.DefaultCWD})
	}
	if len(workspaces) == 0 {
		return &DirectResponse{Text: "No cached project workspaces are available."}, nil
	}
	if len(workspaces) > syncProjectMenuLimit {
		workspaces = workspaces[:syncProjectMenuLimit]
	}
	lines := []string{"Sync · create an empty Codex task", "Select a project. The first prompt is sent later from the ready topic."}
	buttons := make([][]model.ButtonSpec, 0, len(workspaces))
	for index, workspace := range workspaces {
		payload := map[string]any{"session_id": state.SessionID, "control_topic_id": controlTopicID, "cwd": workspace.CWD,
			"project_name": workspace.ProjectName, "directory_name": workspace.DirectoryName}
		route := model.CallbackRoute{Token: randomToken(), Action: "sync_new_project", ThreadID: "", Status: model.CallbackStatusActive,
			PayloadJSON: storage.MustJSON(payload), CreatedAt: model.NowString()}
		if err := s.store.PutCallbackRoute(ctx, route); err != nil {
			return nil, err
		}
		buttons = append(buttons, []model.ButtonSpec{{Text: projectWorkspaceButtonLabel(index+1, workspace), CallbackData: route.Token}})
	}
	return &DirectResponse{Text: strings.Join(lines, "\n"), Buttons: buttons}, nil
}

func (s *Service) createSyncNewTaskLocked(ctx context.Context, state model.SyncState, payload map[string]any) (*DirectResponse, error) {
	if state.State != model.SyncStateActive {
		return &DirectResponse{CallbackText: "Sync is no longer active."}, nil
	}
	cwd := syncPayloadString(payload, "cwd")
	projectName := syncPayloadString(payload, "project_name")
	directoryName := syncPayloadString(payload, "directory_name")
	if cwd == "" {
		return &DirectResponse{CallbackText: "Project cwd is unavailable."}, nil
	}
	forum := s.getSyncForum()
	if forum == nil {
		return &DirectResponse{Text: "Telegram transport is unavailable; no Sync draft was created.", CallbackText: "Create failed."}, nil
	}
	title := "New task"
	topicID, topicErr := forum.CreateSyncTopic(ctx, title)
	if topicErr != nil {
		return &DirectResponse{Text: fmt.Sprintf("Telegram topic creation failed: %v", topicErr), CallbackText: "Create failed."}, nil
	}
	topics, _ := s.store.ListSyncTopics(ctx, state.SessionID)
	drafts, _ := s.store.ListSyncTopicDrafts(ctx, state.SessionID)
	draft := model.SyncTopicDraft{SessionID: state.SessionID, ChatID: state.ChatID, TopicID: topicID,
		Rank: len(topics) + len(drafts) + 1, Title: title, CWD: cwd, ProjectName: projectName, DirectoryName: directoryName}
	if err := s.store.CreateSyncTopicDraft(ctx, draft); err != nil {
		_ = forum.DeleteSyncTopic(ctx, topicID)
		return nil, err
	}
	cleanupReady := s.markStaleSyncTopicsForCleanup(ctx, state.SessionID)
	return &DirectResponse{
		Text:         fmt.Sprintf("Sync task ready in topic %d. Open it and send the first prompt as a new message.", topicID),
		CallbackText: "Sync task created.", ScheduleSyncCleanup: cleanupReady,
	}, nil
}
