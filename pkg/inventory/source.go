package inventory

import "context"

// Source is the intentionally small adapter boundary for future MongoDB,
// Nautobot, NetBox, and GraphQL inventory integrations. Implementations own
// filtering and pagination; the renderer remains source-agnostic.
type Source interface {
	Get(ctx context.Context, id string) (Document, error)
	Query(ctx context.Context, query Query) (Page, error)
}

type Query struct {
	Filter map[string]any `json:"filter,omitempty" yaml:"filter,omitempty"`
	Limit  int            `json:"limit,omitempty" yaml:"limit,omitempty"`
	Cursor string         `json:"cursor,omitempty" yaml:"cursor,omitempty"`
}

type Page struct {
	Documents  []Document `json:"documents" yaml:"documents"`
	NextCursor string     `json:"nextCursor,omitempty" yaml:"nextCursor,omitempty"`
}
