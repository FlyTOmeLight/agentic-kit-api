package session

// Header binding is transport-specific; the SDK takes the key as a method argument.
type submitMessageHeaders struct {
	IdempotencyKey string `header:"Idempotency-Key" binding:"required,max=128"`
}
