package storage

import (
	"context"
	"database/sql"
	"fmt"
)

type TelegramIngress struct {
	BotID, UpdateID    int64
	Payload, SessionID string
}

func (s *Store) RecordTelegramIngress(ctx context.Context, item TelegramIngress, off bool) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer rollback(tx)
	_, err = tx.ExecContext(ctx, `INSERT INTO telegram_ingress(bot_id,update_id,payload,session_id,state,is_off) SELECT ?,?,?,coalesce((SELECT session_id FROM sync_state WHERE id=1),''),'pending',? ON CONFLICT(bot_id,update_id) DO NOTHING`, item.BotID, item.UpdateID, item.Payload, off)
	if err != nil {
		return err
	}
	if off {
		if _, err = tx.ExecContext(ctx, `UPDATE telegram_ingress SET state='done',payload='' WHERE bot_id=? AND update_id<? AND state='pending'`, item.BotID, item.UpdateID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) ClaimTelegramIngress(ctx context.Context, botID int64, updateID int64) (*TelegramIngress, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer rollback(tx)
	var item TelegramIngress
	query := `SELECT bot_id,update_id,payload,session_id FROM telegram_ingress WHERE bot_id=? AND state='pending'`
	args := []any{botID}
	if updateID > 0 {
		query += ` AND update_id=?`
		args = append(args, updateID)
	}
	if updateID == 0 {
		query += ` AND NOT EXISTS(SELECT 1 FROM telegram_ingress urgent WHERE urgent.bot_id=telegram_ingress.bot_id AND urgent.is_off=1 AND urgent.state='processing')`
	}
	query += ` ORDER BY update_id LIMIT 1`
	err = tx.QueryRowContext(ctx, query, args...).Scan(&item.BotID, &item.UpdateID, &item.Payload, &item.SessionID)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE telegram_ingress SET state='processing' WHERE bot_id=? AND update_id=?`, botID, item.UpdateID); err != nil {
		return nil, err
	}
	return &item, tx.Commit()
}
func (s *Store) FinishTelegramIngress(ctx context.Context, botID, updateID int64) error {
	_, err := s.db.ExecContext(ctx, `UPDATE telegram_ingress SET state='done',payload='' WHERE bot_id=? AND update_id=?`, botID, updateID)
	return err
}
func (s *Store) RecoverTelegramIngress(ctx context.Context, botID int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer rollback(tx)
	rows, err := tx.QueryContext(ctx, `SELECT update_id,coalesce(json_extract(payload,'$.message.chat.id'),json_extract(payload,'$.callback_query.message.chat.id'),0) FROM telegram_ingress WHERE bot_id=? AND state='processing'`, botID)
	if err != nil {
		return err
	}
	type unfinished struct{ updateID, chatID int64 }
	var items []unfinished
	for rows.Next() {
		var item unfinished
		if err = rows.Scan(&item.updateID, &item.chatID); err != nil {
			rows.Close()
			return err
		}
		items = append(items, item)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, item := range items {
		if item.chatID != 0 {
			if err = topicCreationNotice(ctx, tx, TopicCreation{ID: fmt.Sprintf("ingress:%d:%d", botID, item.updateID), ChatID: item.chatID, Title: fmt.Sprintf("Telegram update %d", item.updateID)}, "outcome unknown after restart; command will not be replayed"); err != nil {
				return err
			}
		}
	}
	if _, err = tx.ExecContext(ctx, `UPDATE telegram_ingress SET state='done',payload='' WHERE bot_id=? AND state='processing'`, botID); err != nil {
		return err
	}
	return tx.Commit()
}
