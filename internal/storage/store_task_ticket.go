package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"

	"github.com/mideco-tech/codex-tg/internal/model"
)

// SetTaskTicket changes metadata only; launch and polling ownership stay intact.
func (s *Store) SetTaskTicket(ctx context.Context, id string, in model.SetTaskTicketInput) error {
	normalized, err := normalizeTaskTicketURL(in.TicketURL)
	if in.ExpectedVersion <= 0 || err != nil {
		return fmt.Errorf("%w: positive expected_version and a ticket key or HTTP(S) URL without credentials required", ErrTaskInvalid)
	}
	in.TicketURL = normalized
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

var taskTicketKeyPattern = regexp.MustCompile(`^[A-Z][A-Z0-9_]*-[1-9][0-9]*$`)

func normalizeTaskTicketURL(value string) (string, error) {
	value = strings.TrimSpace(value)
	key := strings.ToUpper(value)
	if taskTicketKeyPattern.MatchString(key) {
		return "https://st.yandex-team.ru/" + key, nil
	}
	if err := validateTaskTicketURL(value); err != nil {
		return "", err
	}
	return value, nil
}
