package agenticv1

// PageOptions uses an opaque ID cursor. Limit zero requests the server default.
type PageOptions struct {
	After string
	Limit int
}

type Page[T any] struct {
	Items []T `json:"items"`
}
