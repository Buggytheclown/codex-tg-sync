package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"github.com/mideco-tech/codex-tg/internal/model"
)

// The task row is the reservation authority, shared by manual linkage and launch dispatch.
func (s *Store) BeginBoardChat(ctx context.Context, id string, in model.CreateBoardChatInput) (model.BoardChatResult, bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return model.BoardChatResult{}, false, err
	}
	defer tx.Rollback()
	t, err := getTaskTx(ctx, tx, id)
	if err != nil {
		return model.BoardChatResult{}, false, err
	}
	var raw string
	err = tx.QueryRowContext(ctx, `SELECT value FROM daemon_state WHERE key=?`, "board.chat."+id).Scan(&raw)
	if err == nil {
		var r model.BoardChatResult
		if err = json.Unmarshal([]byte(raw), &r); err != nil {
			return r, false, err
		}
		return r, false, nil
	}
	if err != sql.ErrNoRows {
		return model.BoardChatResult{}, false, err
	}
	if t.ThreadID != "" {
		return model.BoardChatResult{ThreadID: t.ThreadID, State: "ready"}, false, nil
	}
	if t.Version != in.ExpectedVersion {
		return model.BoardChatResult{}, false, ErrTaskConflict
	}
	if t.CardType != model.CardTask || t.IsSource() || in.AttemptID == "" {
		return model.BoardChatResult{}, false, ErrTaskInvalid
	}
	var count int
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM external_launch_requests WHERE task_id=? AND status NOT IN ('dismissed','rejected_sender','session_completed','session_interrupted','session_failed')`, id).Scan(&count); err != nil {
		return model.BoardChatResult{}, false, err
	}
	if count > 0 {
		return model.BoardChatResult{}, false, ErrTaskConflict
	}
	r := model.BoardChatResult{State: "prepared"}
	data, _ := json.Marshal(r)
	_, err = tx.ExecContext(ctx, `INSERT INTO daemon_state(key,value,updated_at) VALUES(?,?,?)`, "board.chat."+id, string(data), model.NowString())
	if err != nil {
		return r, false, err
	}
	return r, true, tx.Commit()
}
func (s *Store) SaveBoardChat(ctx context.Context, id string, r model.BoardChatResult) error {
	r.Fresh = false
	data, err := json.Marshal(r)
	if err != nil {
		return err
	}
	return s.SetState(ctx, "board.chat."+id, string(data))
}
func boardChatReservedTx(ctx context.Context, tx *sql.Tx, id string) (bool, error) {
	var raw string
	err := tx.QueryRowContext(ctx, `SELECT value FROM daemon_state WHERE key=?`, "board.chat."+id).Scan(&raw)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var r model.BoardChatResult
	err = json.Unmarshal([]byte(raw), &r)
	return r.State == "sending" || r.State == "prepared", err
}
