package appserver

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestThreadUnsubscribeWireContract(t *testing.T) {
	for _, status := range []string{"unsubscribed", "notSubscribed", "notLoaded", "unexpected", "", "rpc-error"} {
		t.Run(status, func(t *testing.T) {
			requests := make(chan map[string]any, 1)
			upgrader := websocket.Upgrader{}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				conn, err := upgrader.Upgrade(w, r, nil)
				if err != nil {
					return
				}
				defer conn.Close()
				for {
					var request map[string]any
					if conn.ReadJSON(&request) != nil {
						return
					}
					if request["id"] == nil {
						continue
					}
					response := map[string]any{"id": request["id"], "result": map[string]any{}}
					if request["method"] == "thread/unsubscribe" {
						requests <- request
						response["result"] = map[string]any{"status": status}
						if status == "rpc-error" {
							delete(response, "result")
							response["error"] = map[string]any{"code": -32603, "message": "unavailable"}
						}
					}
					if conn.WriteJSON(response) != nil {
						return
					}
				}
			}))
			defer server.Close()
			client := NewClientWithTransport("unused", TransportConfig{Mode: TransportWebSocket, ListenURL: "ws" + strings.TrimPrefix(server.URL, "http")}, t.TempDir(), time.Second)
			if err := client.Start(context.Background()); err != nil {
				t.Fatal(err)
			}
			defer client.Close()
			err := client.ThreadUnsubscribe(context.Background(), "thread-1")
			wantError := status == "unexpected" || status == "" || status == "rpc-error"
			if (err != nil) != wantError {
				t.Fatalf("status=%q error=%v", status, err)
			}
			select {
			case request := <-requests:
				if !reflect.DeepEqual(request["params"], map[string]any{"threadId": "thread-1"}) {
					t.Fatalf("params=%#v", request["params"])
				}
			case <-time.After(time.Second):
				t.Fatal("unsubscribe request missing")
			}
		})
	}
}
