package controlapi

import (
	"context"
	"github.com/mideco-tech/codex-tg/internal/model"
	"github.com/mideco-tech/codex-tg/internal/tracker"
	"net/http"
)

func registerBoardChat(mux *http.ServeMux, b BoardBackend) {
	mux.HandleFunc("/v1/tracker/lookup", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			writeError(w, 405, "method not allowed")
			return
		}
		backend, ok := b.(interface {
			LookupTracker(context.Context, string) (tracker.Issue, error)
		})
		if !ok {
			writeError(w, 503, "Tracker unavailable")
			return
		}
		var in struct {
			Ticket string `json:"ticket"`
		}
		if !decodeCommand(w, r, &in) {
			return
		}
		value, err := backend.LookupTracker(r.Context(), in.Ticket)
		boardResult(w, 200, value, err)
	})
}
func boardChatCommand(w http.ResponseWriter, r *http.Request, b BoardBackend, id string) {
	if r.Method != "POST" {
		writeError(w, 405, "method not allowed")
		return
	}
	backend, ok := b.(interface {
		CreateBoardChat(context.Context, string, model.CreateBoardChatInput) (model.BoardChatResult, error)
	})
	if !ok {
		writeError(w, 503, "Chat creation unavailable")
		return
	}
	var in model.CreateBoardChatInput
	if !decodeCommand(w, r, &in) {
		return
	}
	value, err := backend.CreateBoardChat(r.Context(), id, in)
	boardResult(w, 200, value, err)
}

func boardNameCommand(w http.ResponseWriter, r *http.Request, b BoardBackend, id string) {
	if r.Method != "GET" && r.Method != "POST" {
		writeError(w, 405, "method not allowed")
		return
	}
	backend, ok := b.(interface {
		FixChatName(context.Context, string, bool) (model.BoardChatResult, error)
	})
	if !ok {
		writeError(w, 503, "Naming unavailable")
		return
	}
	if r.Method == "POST" {
		var input struct{}
		if !decodeCommand(w, r, &input) {
			return
		}
	}
	value, err := backend.FixChatName(r.Context(), id, r.Method == "POST")
	boardResult(w, 200, value, err)
}
