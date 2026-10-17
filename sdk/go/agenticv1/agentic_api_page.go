package agenticv1

// PageOptions uses an opaque ID cursor. Limit zero requests the server default.
type PageOptions struct {
	After string `json:"-" form:"after"`
	Limit int    `json:"-" form:"limit,default=50" binding:"min=1,max=100"`
}

type Page[T any] struct {
	Items []T `json:"items"`
}
