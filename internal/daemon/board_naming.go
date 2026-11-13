package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/mideco-tech/codex-tg/internal/appserver"
	"github.com/mideco-tech/codex-tg/internal/model"
	"github.com/mideco-tech/codex-tg/internal/tasks"
	"github.com/mideco-tech/codex-tg/internal/tracker"
)

func (s *Service) FixChatName(ctx context.Context, id string, apply bool) (model.BoardChatResult, error) {
	t, err := s.GetTask(ctx, id)
	if err != nil {
		return model.BoardChatResult{}, err
	}
	if t == nil || t.CardType != model.CardPR || t.ThreadID == "" {
		return model.BoardChatResult{}, tasks.ErrInvalid
	}
	pending, _ := s.store.GetState(ctx, "board.fix.name."+t.ThreadID)
	if pending != id {
		return model.BoardChatResult{}, nil
	}
	parent, err := s.GetTask(ctx, t.ParentTaskID)
	if err != nil {
		return model.BoardChatResult{}, err
	}
	if parent == nil {
		return model.BoardChatResult{}, nil
	}
	ticket, err := tracker.Key(parent.TicketURL)
	if err != nil {
		return model.BoardChatResult{}, nil
	}
	name, err := chatName(s.cfg.BoardFixName, "Фикс {ticket} {title}", ticket, parent.Title)
	if err != nil {
		return model.BoardChatResult{}, err
	}
	r := model.BoardChatResult{ThreadID: t.ThreadID, Name: name, State: "pending"}
	if !apply {
		return r, nil
	}
	s.syncMu.Lock()
	defer s.syncMu.Unlock()
	lease, err := s.syncWriter.Reserve(ctx, t.ThreadID)
	if err != nil {
		return r, err
	}
	defer s.syncWriter.Abort(lease)
	err = s.setFixName(ctx, t, lease.Process, name)
	return r, err
}
func (s *Service) setFixName(ctx context.Context, t *model.Task, process Session, name string) error {
	// Recheck linkage after waiting for the writer. No prompts are sent here.
	latest, err := s.GetTask(ctx, t.ID)
	if err != nil {
		return err
	}
	if latest == nil || latest.ThreadID != t.ThreadID || latest.ParentTaskID != t.ParentTaskID {
		return tasks.ErrConflict
	}
	parent, err := s.GetTask(ctx, latest.ParentTaskID)
	if err != nil || parent == nil {
		return tasks.ErrConflict
	}
	ticket, err := tracker.Key(parent.TicketURL)
	if err != nil {
		return err
	}
	expected, err := chatName(s.cfg.BoardFixName, "Фикс {ticket} {title}", ticket, parent.Title)
	if err != nil {
		return err
	}
	if expected != name {
		return tasks.ErrConflict
	}
	namer, ok := any(process).(interface {
		ThreadSetName(context.Context, string, string) (map[string]any, error)
	})
	if !ok {
		return errors.New("Codex naming unavailable")
	}
	if _, err = namer.ThreadSetName(ctx, t.ThreadID, name); err != nil {
		return err
	}
	return s.store.DeleteState(ctx, "board.fix.name."+t.ThreadID)
}
func (s *Service) reconcileFixNameLocked(ctx context.Context, current appserver.ThreadReadSnapshot) {
	id, err := s.store.GetState(ctx, "board.fix.name."+current.Thread.ID)
	if err != nil || id == "" {
		return
	}
	r, err := s.FixChatName(ctx, id, false)
	if err != nil || r.Name == "" {
		return
	}
	var raw map[string]any
	if json.Unmarshal(current.Thread.Raw, &raw) != nil {
		return
	}
	if raw["name"] == r.Name {
		_ = s.store.DeleteState(ctx, "board.fix.name."+current.Thread.ID)
		return
	}
	// Existing names keep the explicit Apply template action.
	if name, ok := raw["name"].(string); ok && name != "" {
		return
	}
	t, err := s.GetTask(ctx, id)
	if err != nil || t == nil {
		return
	}
	if lease, ok := s.syncLeases[current.Thread.ID]; ok {
		_ = s.setFixName(ctx, t, lease.Process, r.Name)
		return
	}
	lease, err := s.syncWriter.Reserve(ctx, current.Thread.ID)
	if err != nil {
		return
	}
	defer s.syncWriter.Abort(lease)
	_ = s.setFixName(ctx, t, lease.Process, r.Name)
}
