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
	lease, err := s.afcWriter.ReserveProcess(ctx, "new-task:"+randomToken())
	if err != nil {
		return &DirectResponse{Text: fmt.Sprintf("AFC could not reserve the shared writer: %v", err), CallbackText: "Create failed."}, nil
	}
	s.installAFCWriterLocked(lease)
	threadPayload, startErr := lease.Process.ThreadStart(ctx, cwd)
	if startErr != nil {
		if afcDispatchAmbiguous(startErr, "") {
			_ = s.afcWriter.MarkUnknown(lease)
			return &DirectResponse{Text: "Codex thread creation outcome is unknown. The project button was consumed and will not retry automatically.", CallbackText: "Creation unknown."}, nil
		}
		_ = s.afcWriter.Abort(lease)
		return &DirectResponse{Text: fmt.Sprintf("Codex rejected thread creation: %v", startErr), CallbackText: "Create failed."}, nil
	}
	thread := threadFromStartPayload(threadPayload, pendingNewThreadState{ProjectName: projectName, DirectoryName: directoryName, CWD: cwd})
	if strings.TrimSpace(thread.ID) == "" {
		_ = s.afcWriter.MarkUnknown(lease)
		return &DirectResponse{Text: "Codex returned no thread id; creation outcome is unknown.", CallbackText: "Creation unknown."}, nil
	}
	lease, err = s.afcWriter.ClaimThread(lease, thread.ID)
	if err != nil {
		_ = s.afcWriter.MarkUnknown(lease)
		return &DirectResponse{Text: fmt.Sprintf("Created Codex thread %s, but ownership could not be confirmed: %v", thread.ID, err), CallbackText: "Partial creation."}, nil
	}
	if err := s.store.UpsertThread(ctx, thread); err != nil {
		_ = s.afcWriter.MarkUnknown(lease)
		return nil, err
	}
	forum := s.getAFCForum()
	if forum == nil {
		_ = s.afcWriter.MarkTerminal(lease)
		return &DirectResponse{Text: fmt.Sprintf("Created Codex thread %s, but Telegram transport is unavailable.", thread.ID), CallbackText: "Partial creation."}, nil
	}
	title := afcTopicTitle(thread)
	topicID, topicErr := forum.CreateAFCTopic(ctx, title)
	if topicErr != nil {
		_ = s.afcWriter.MarkTerminal(lease)
		return &DirectResponse{Text: fmt.Sprintf("Created Codex thread %s, but Telegram topic creation failed: %v", thread.ID, topicErr), CallbackText: "Partial creation."}, nil
	}
	topics, _ := s.store.ListAFCTopics(ctx, state.SessionID)
	row := model.AFCTopic{SessionID: state.SessionID, ChatID: state.ChatID, TopicID: topicID, ThreadID: thread.ID, Rank: len(topics) + 1,
		Title: title, TelegramState: model.AFCTopicConnected}
	if err := s.store.UpsertAFCTopic(ctx, row); err != nil {
		_ = forum.DeleteAFCTopic(ctx, topicID)
		_ = s.afcWriter.MarkTerminal(lease)
		return nil, err
	}
	closeErr := s.afcWriter.MarkTerminal(lease)
	if closeErr != nil {
		return &DirectResponse{Text: fmt.Sprintf("Created AFC topic %d for thread %s, but shared writer handback is unresolved: %v", topicID, thread.ID, closeErr), CallbackText: "Created with warning."}, nil
	}
	return &DirectResponse{Text: fmt.Sprintf("AFC task ready in topic %d. Open it and send the first prompt as a new message.", topicID), CallbackText: "AFC task created.", ThreadID: thread.ID}, nil
}
