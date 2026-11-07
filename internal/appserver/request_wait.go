package appserver

import "context"

type requestWaitKey struct{}

// RequestWaitHooks let the dispatch owner keep admission and the frame write
// serialized, while releasing its state lock during a remote response wait.
// Hooks apply only to Request, never to event readers or response callbacks.
type RequestWaitHooks struct {
	BeforeSend func(string) error
	BeforeWait func()
	AfterWait  func()
}

func WithRequestWaitHooks(ctx context.Context, hooks RequestWaitHooks) context.Context {
	return context.WithValue(ctx, requestWaitKey{}, hooks)
}
