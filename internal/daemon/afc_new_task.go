package daemon

import (
	"context"
	"fmt"
	"strings"

	"github.com/mideco-tech/codex-tg/internal/model"
	"github.com/mideco-tech/codex-tg/internal/storage"
)

func (s *Service) afcProjectsMenu(ctx context.Context, controlTopicID int64) (*DirectResponse, error) {
	state, err := s.store.GetAFCState(ctx)
	if err != nil {
		return nil, err
	}
	if state.State != model.AFCStateActive {
		return &DirectResponse{Text: "AFC projects are available only while AFC is active."}, nil
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
	if len(workspaces) > afcTopicLimit {
		workspaces = workspaces[:afcTopicLimit]
	}
	lines := []string{"AFC · create an empty Codex task", "Select a project. The first prompt is sent later from the ready topic."}
	buttons := make([][]model.ButtonSpec, 0, len(workspaces))
	for index, workspace := range workspaces {
		payload := map[string]any{"session_id": state.SessionID, "control_topic_id": controlTopicID, "cwd": workspace.CWD,
			"project_name": workspace.ProjectName, "directory_name": workspace.DirectoryName}
		route := model.CallbackRoute{Token: randomToken(), Action: "afc_new_project", ThreadID: "", Status: model.CallbackStatusActive,
			PayloadJSON: storage.MustJSON(payload), CreatedAt: model.NowString()}
		if err := s.store.PutCallbackRoute(ctx, route); err != nil {
			return nil, err
		}
		buttons = append(buttons, []model.ButtonSpec{{Text: projectWorkspaceButtonLabel(index+1, workspace), CallbackData: route.Token}})
	}
	return &DirectResponse{Text: strings.Join(lines, "\n"), Buttons: buttons}, nil
}

func (s *Service) createAFCNewTaskLocked(ctx context.Context, state model.AFCState, payload map[string]any) (*DirectResponse, error) {
	if state.State != model.AFCStateActive {
		return &DirectResponse{CallbackText: "AFC is no longer active."}, nil
	}
	cwd := afcPayloadString(payload, "cwd")
	projectName := afcPayloadString(payload, "project_name")
	directoryName := afcPayloadString(payload, "directory_name")
	if cwd == "" {
		return &DirectResponse{CallbackText: "Project cwd is unavailable."}, nil
	}
	forum := s.getAFCForum()
	if forum == nil {
		return &DirectResponse{Text: "Telegram transport is unavailable; no AFC draft was created.", CallbackText: "Create failed."}, nil
	}
	title := "New task"
	topicID, topicErr := forum.CreateAFCTopic(ctx, title)
	if topicErr != nil {
		return &DirectResponse{Text: fmt.Sprintf("Telegram topic creation failed: %v", topicErr), CallbackText: "Create failed."}, nil
	}
	topics, _ := s.store.ListAFCTopics(ctx, state.SessionID)
	drafts, _ := s.store.ListAFCTopicDrafts(ctx, state.SessionID)
	draft := model.AFCTopicDraft{SessionID: state.SessionID, ChatID: state.ChatID, TopicID: topicID,
		Rank: len(topics) + len(drafts) + 1, Title: title, CWD: cwd, ProjectName: projectName, DirectoryName: directoryName}
	if err := s.store.CreateAFCTopicDraft(ctx, draft); err != nil {
		_ = forum.DeleteAFCTopic(ctx, topicID)
		return nil, err
	}
	return &DirectResponse{Text: fmt.Sprintf("AFC task ready in topic %d. Open it and send the first prompt as a new message.", topicID), CallbackText: "AFC task created."}, nil
}
