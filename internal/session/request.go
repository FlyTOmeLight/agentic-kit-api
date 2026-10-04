package session

type createRequest struct {
	AgentID string `json:"agentID" binding:"required,max=128"`
	Title   string `json:"title" binding:"max=200"`
}
type submitMessageRequest struct {
	Query string `json:"query" binding:"required"`
}
type submitMessageHeaders struct {
	IdempotencyKey string `header:"Idempotency-Key" binding:"required,max=128"`
}
