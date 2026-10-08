package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"

	"github.com/mideco-tech/codex-tg/internal/model"
)

// SetTaskTicket changes metadata only; launch and polling ownership stay intact.
func (s *Store) SetTaskTicket(ctx context.Context, id string, in model.SetTaskTicketInput) error {
	if in.ExpectedVersion <= 0 || validateTaskTicketURL(in.TicketURL) != nil {
		return fmt.Errorf("%w: positive expected_version and an absolute HTTP(S) ticket URL without credentials required", ErrTaskInvalid)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var version int64
	if err := tx.QueryRowContext(ctx, `SELECT version FROM tasks WHERE id=?`, id).Scan(&version); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrTaskNotFound
		}
		return err
	}
	if version != in.ExpectedVersion {
		return ErrTaskConflict
	}
	if _, err := tx.ExecContext(ctx, `UPDATE tasks SET ticket_url=?,version=version+1,updated_at=? WHERE id=?`, in.TicketURL, model.NowString(), id); err != nil {
		return err
	}
	if err := addTaskEventTx(ctx, tx, id, "ticket_link_changed", map[string]string{"ticket_url": in.TicketURL}); err != nil {
		return err
	}
	return tx.Commit()
}

func validateTaskTicketURL(value string) error {
	if value == "" {
		return nil
	}
	parsed, err := url.Parse(value)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.Hostname() == "" || parsed.User != nil || parsed.Opaque != "" {
		return fmt.Errorf("%w: ticket_url must be an absolute HTTP(S) URL without credentials", ErrTaskInvalid)
	}
	return nil
}
