package daemon

import (
	"context"
	"errors"
	"fmt"
	"github.com/mideco-tech/codex-tg/internal/appserver"
	"github.com/mideco-tech/codex-tg/internal/model"
	"github.com/mideco-tech/codex-tg/internal/tasks"
	"github.com/mideco-tech/codex-tg/internal/tracker"
	"os"
	"strings"
	"time"
)

func (s *Service) LookupTracker(ctx context.Context, input string) (tracker.Issue, error) {
	return tracker.Lookup(ctx, s.cfg.ArcanumYABin, input)
}
func chatName(template, fallback, ticket, title string) (string, error) {
	if template == "" {
		template = fallback
	}
	rest := strings.ReplaceAll(strings.ReplaceAll(template, "{ticket}", ""), "{title}", "")
	if strings.ContainsAny(rest, "{}") {
		return "", errors.New("unknown name template field")
	}
	name := strings.TrimSpace(strings.NewReplacer("{ticket}", ticket, "{title}", title).Replace(template))
	if ticket == "" {
		name = strings.Replace(name, "  ", " ", 1)
	}
	if name == "" {
		return "", errors.New("chat name is empty")
	}
	return name, nil
}
func (s *Service) CreateBoardChat(ctx context.Context, id string, in model.CreateBoardChatInput) (model.BoardChatResult, error) {
	s.boardChatMu.Lock()
	defer s.boardChatMu.Unlock()
	t, err := s.GetTask(ctx, id)
	if err != nil {
		return model.BoardChatResult{}, err
	}
	if t == nil {
		return model.BoardChatResult{}, tasks.ErrNotFound
	}
	cwd := s.cfg.BoardChatCWD
	if cwd == "" {
		cwd = s.cfg.DefaultCWD
	}
	info, err := os.Stat(cwd)
	if err != nil || !info.IsDir() {
		return model.BoardChatResult{}, fmt.Errorf("%w: chat directory unavailable", tasks.ErrInvalid)
	}
	ticket, _ := tracker.Key(t.TicketURL)
	name, err := chatName(s.cfg.BoardChatName, "ПР {ticket} {title}", ticket, t.Title)
	if err != nil {
		return model.BoardChatResult{}, err
	}
	s.taskLaunchMu.Lock()
	r, create, err := s.store.BeginBoardChat(ctx, id, in)
	s.taskLaunchMu.Unlock()
	if err != nil {
		return r, err
	}
	if !create && r.State == "prepared" {
		if err = s.store.DeleteState(ctx, "board.chat."+id); err != nil {
			return r, err
		}
		s.taskLaunchMu.Lock()
		r, create, err = s.store.BeginBoardChat(ctx, id, in)
		s.taskLaunchMu.Unlock()
		if err != nil {
			return r, err
		}
	}
	if !create {
		if r.State == "validating" && r.ThreadID != "" {
			reader, readErr := s.controlReadSession(ctx)
			if readErr != nil {
				return r, readErr
			}
			payload, readErr := reader.ThreadRead(ctx, r.ThreadID, false)
			if readErr != nil {
				return r, readErr
			}
			actual := appserver.ThreadFromPayload(payload)
			if actual.CWD != r.CWD || actual.PreferredModel != r.Model {
				r.State = "validation_failed"
				r.Error = "Recovered chat has unexpected settings; link its ID manually to accept them"
			} else {
				r.State = "created"
			}
			if err = s.store.SaveBoardChat(ctx, id, r); err != nil {
				return r, err
			}
		}
		if r.State == "sending" {
			r.State = "unknown"
			r.Error = "Creation outcome is unknown. Link the existing chat to recover."
			_ = s.store.SaveBoardChat(ctx, id, r)
		}
		if r.State == "created" && r.ThreadID != "" {
			return s.finishBoardChat(ctx, id, r, r.Name)
		}
		return r, nil
	}
	// A separate bounded context preserves the ID when the browser disconnects.
	rpcCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 45*time.Second)
	defer cancel()
	lease, err := s.syncWriter.ReserveProcess(rpcCtx, "board-chat:"+id)
	if err != nil {
		_ = s.store.DeleteState(rpcCtx, "board.chat."+id)
		return r, err
	}
	defer func() { _ = s.syncWriter.Abort(lease) }()
	modelID := s.cfg.BoardChatModel
	models, err := lease.Process.ModelList(rpcCtx, true)
	if err == nil {
		found := false
		for _, m := range models {
			if (modelID != "" && m.ID == modelID) || (modelID == "" && m.IsDefault) {
				modelID = m.ID
				found = true
				break
			}
		}
		if !found {
			err = errors.New("chat model unavailable in Codex model catalog")
		}
	}
	if err != nil {
		_ = s.store.DeleteState(rpcCtx, "board.chat."+id)
		return r, err
	}
	r.State = "sending"
	r.Name = name
	r.CWD = cwd
	r.Model = modelID
	if err = s.store.SaveBoardChat(rpcCtx, id, r); err != nil {
		return r, err
	}
	payload, err := lease.Process.ThreadStart(rpcCtx, cwd, appserver.ThreadStartOptions{Model: modelID, ApprovalPolicy: s.cfg.ExternalApprovalPolicy, ApprovalsReviewer: s.cfg.ExternalApprovalsReviewer, SandboxMode: s.cfg.ExternalSandboxMode})
	if err != nil {
		r.State = "unknown"
		r.Error = "Creation outcome is unknown. Link the existing chat to recover."
		_ = s.store.SaveBoardChat(rpcCtx, id, r)
		return r, nil
	}
	thread := appserver.ThreadFromPayload(payload)
	r.ThreadID = thread.ID
	r.State = "validating"
	r.Name = name
	if thread.ID == "" {
		r.State = "unknown"
		r.Error = "Codex returned no chat ID"
	}
	if err = s.store.SaveBoardChat(rpcCtx, id, r); err != nil {
		r.Error = "Chat created; copy its ID and link it to recover"
		return r, nil
	}
	if thread.ID == "" {
		return r, nil
	}
	lease, err = s.syncWriter.ClaimThread(lease, thread.ID)
	if err != nil {
		return r, err
	}
	if thread.CWD != cwd || thread.PreferredModel != modelID {
		r.State = "validation_failed"
		r.Error = "Codex returned an unexpected directory or model"
		_ = s.store.SaveBoardChat(rpcCtx, id, r)
		return r, nil
	}
	r.State = "created"
	if err = s.store.SaveBoardChat(rpcCtx, id, r); err != nil {
		return r, err
	}
	// Release this lease before the recovery helper claims the same thread.
	_ = s.syncWriter.Abort(lease)
	r, err = s.finishBoardChat(rpcCtx, id, r, name)
	r.Fresh = err == nil && r.State == "ready"
	return r, err
}
func (s *Service) finishBoardChat(ctx context.Context, id string, r model.BoardChatResult, name string) (model.BoardChatResult, error) {
	lease, err := s.syncWriter.Reserve(ctx, r.ThreadID)
	if err != nil {
		return r, err
	}
	defer s.syncWriter.Abort(lease)
	namer, ok := any(lease.Process).(interface {
		ThreadSetName(context.Context, string, string) (map[string]any, error)
	})
	if !ok {
		return r, errors.New("Codex naming unavailable")
	}
	if _, err = namer.ThreadSetName(ctx, r.ThreadID, name); err != nil {
		r.Error = "Chat created; retry naming"
		_ = s.store.SaveBoardChat(ctx, id, r)
		return r, nil
	}
	t, err := s.GetTask(ctx, id)
	if err != nil {
		return r, err
	}
	// The reservation prevents concurrent creation. Ordinary linkage validates identity.
	s.taskLaunchMu.Lock()
	defer s.taskLaunchMu.Unlock()
	if t.ThreadID == "" {
		_, err = s.store.LinkTaskChat(ctx, id, model.LinkTaskChatInput{ExpectedVersion: t.Version, ThreadID: r.ThreadID})
	} else if t.ThreadID != r.ThreadID {
		err = tasks.ErrConflict
	}
	if err != nil {
		r.Error = "Chat created; link its ID to recover"
		return r, nil
	}
	r.State = "ready"
	r.Error = ""
	err = s.store.SaveBoardChat(ctx, id, r)
	s.wakeSyncReconcile()
	return r, err
}
