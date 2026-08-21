package daemon

import (
	"context"

	"github.com/mideco-tech/codex-tg/internal/model"
)

// ExternalSourceCursor and IngestExternalRequests form the intentionally small
// boundary used by optional source adapters. Adapters do not depend on the
// Telegram or App Server implementation owned by Service.
func (s *Service) ExternalSourceCursor(ctx context.Context, source string) (int64, error) {
	return s.store.ExternalSourceCursor(ctx, source)
}

func (s *Service) IngestExternalRequests(ctx context.Context, source string, cursor int64, requests []model.ExternalLaunchRequest) (int, error) {
	return s.store.IngestExternalRequests(ctx, source, cursor, requests)
}
