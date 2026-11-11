package controlapi

import (
	"context"
	"github.com/mideco-tech/codex-tg/internal/model"
	"github.com/mideco-tech/codex-tg/internal/storage"
	"github.com/mideco-tech/codex-tg/internal/tasks"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/mideco-tech/codex-tg/internal/arcanumreview"
)

func TestBoardPresenceUsesTrustBoundaryAndDoesNotMutateTasks(t *testing.T) {
	p := arcanumreview.NewMetadataPresence()
	b := &fakeBoard{}
	h := NewHandler("test", &fakeBackend{}, Options{Board: b, ListenAddress: "127.0.0.1:8099", PRMetadataPresence: p})
	for _, tc := range []struct {
		method, host, origin, ct, body string
		want                           int
	}{
		{"POST", "evil.example:8099", "", "application/json", `{"client_id":"page","sequence":1,"active":true}`, 403},
		{"POST", "127.0.0.1:8099", "https://evil.example", "application/json", `{"client_id":"page","sequence":1,"active":true}`, 403},
		{"GET", "127.0.0.1:8099", "", "", "", 405},
		{"POST", "127.0.0.1:8099", "", "text/plain", `{}`, 415},
		{"POST", "127.0.0.1:8099", "", "application/json", `{"client_id":"page","sequence":1}`, 400},
		{"POST", "127.0.0.1:8099", "", "application/json", `{"client_id":"page","sequence":1,"active":true,"extra":1}`, 400},
		{"POST", "127.0.0.1:8099", "", "application/json", `{"client_id":"","sequence":1,"active":true}`, 400},
	} {
		w := boardRequest(h, tc.method, "/v1/board-presence", tc.body, tc.host, tc.origin, tc.ct)
		if w.Code != tc.want {
			t.Fatalf("response=%d want=%d: %s", w.Code, tc.want, w.Body.String())
		}
		if active, _ := p.State(time.Now()); active {
			t.Fatal("rejected presence reached worker")
		}
	}
	for seq, active := range []bool{true, false} {
		body := `{"client_id":"page","sequence":1,"active":true}`
		if !active {
			body = `{"client_id":"page","sequence":2,"active":false}`
		}
		w := boardRequest(h, http.MethodPost, "/v1/board-presence", body, "127.0.0.1:8099", "http://127.0.0.1:8099", "application/json")
		if w.Code != 204 {
			t.Fatalf("seq %d: %d %s", seq, w.Code, w.Body.String())
		}
		if got, _ := p.State(time.Now()); got != active {
			t.Fatalf("presence=%v want=%v", got, active)
		}
	}
	if b.mutations != 0 || b.fullLists != 0 {
		t.Fatal("presence accessed tasks")
	}
}

type presenceMetadataReader struct {
	reads   chan int64
	release chan struct{}
}

func (r *presenceMetadataReader) ReadPRMetadata(ctx context.Context, id int64) (arcanumreview.PRSnapshot, error) {
	select {
	case r.reads <- id:
	case <-ctx.Done():
		return arcanumreview.PRSnapshot{}, ctx.Err()
	}
	select {
	case <-r.release:
		return arcanumreview.PRSnapshot{ID: id, Status: "merged"}, nil
	case <-ctx.Done():
		return arcanumreview.PRSnapshot{}, ctx.Err()
	}
}

func TestPresenceHTTPDrivesWorkerAndFreshCacheWithoutChangingCard(t *testing.T) {
	store, err := storage.Open(filepath.Join(t.TempDir(), "board.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	card, err := store.CreateTask(ctx, model.CreateTaskInput{Title: "PR", CardType: model.CardPR, SourceURL: "42", Column: model.TaskInbox})
	if err != nil {
		t.Fatal(err)
	}
	if err = store.RecordPRMetadata(ctx, "42", "open", "waiting_for_review", true, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	p := arcanumreview.NewMetadataPresence()
	h := NewHandler("test", &fakeBackend{}, Options{Board: tasks.New(store), PRMetadataPresence: p, ListenAddress: "127.0.0.1:8099"})
	reader := &presenceMetadataReader{reads: make(chan int64, 2), release: make(chan struct{})}
	done := make(chan struct{})
	go func() {
		defer close(done)
		arcanumreview.RunPRMetadata(ctx, store, reader, p, func(err error) { t.Errorf("worker: %v", err) })
	}()
	defer func() {
		cancel()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Error("worker did not stop")
		}
	}()
	select {
	case <-reader.reads:
		t.Fatal("read with no active page")
	case <-time.After(25 * time.Millisecond):
	}
	w := boardRequest(h, "POST", "/v1/board-presence", `{"client_id":"page","sequence":1,"active":true}`, "127.0.0.1:8099", "", "application/json")
	if w.Code != 204 {
		t.Fatal(w.Code, w.Body.String())
	}
	select {
	case id := <-reader.reads:
		if id != 42 {
			t.Fatal(id)
		}
	case <-time.After(time.Second):
		t.Fatal("activation did not refresh fresh cache")
	}
	w = boardRequest(h, "POST", "/v1/board-presence", `{"client_id":"page","sequence":2,"active":false}`, "127.0.0.1:8099", "", "application/json")
	if w.Code != 204 {
		t.Fatal(w.Code, w.Body.String())
	}
	close(reader.release)
	deadline := time.Now().Add(time.Second)
	for {
		got, err := store.GetTaskCard(ctx, card.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got.PRStatus != nil && got.PRStatus.Label == "Merged" {
			if got.Version != card.Version || got.Column != card.Column || len(got.Requests) != 0 {
				t.Fatalf("passive read changed card: %#v", got)
			}
			events, err := store.ListTaskEvents(ctx, card.ID)
			if err != nil || len(events) != 1 {
				t.Fatalf("history changed: %v %v", events, err)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("in-flight read did not finish after hiding")
		}
		time.Sleep(time.Millisecond)
	}
}
