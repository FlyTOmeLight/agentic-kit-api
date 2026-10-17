package agenticv1

// Operation requests include body, path, query and request-specific headers.

type GetAgentRequest struct {
	ID string `json:"-" uri:"id" binding:"required,max=128"`
}

type ListAgentsRequest struct {
	PageOptions
}

type GetAgentVersionRequest struct {
	ID     string `json:"-" uri:"id" binding:"required,max=128"`
	Number int64  `json:"-" uri:"number" binding:"required,min=1"`
}

type GetSessionRequest struct {
	ID string `json:"-" uri:"id" binding:"required,max=128"`
}

type ListSessionsRequest struct {
	ListSessionsOptions
}

type ListMessagesRequest struct {
	SessionID string `json:"-" uri:"id" binding:"required,max=128"`
	ListMessagesOptions
}

type ListSessionRunsRequest struct {
	SessionID string `json:"-" uri:"id" binding:"required,max=128"`
	PageOptions
}

type GetRunRequest struct {
	ID string `json:"-" uri:"id" binding:"required,max=128"`
}

type GetRunTraceRequest struct {
	ID    string `json:"-" uri:"id" binding:"required,max=128"`
	After int64  `json:"-" form:"after" binding:"min=0"`
}

type CancelRunRequest struct {
	ID string `json:"-" uri:"id" binding:"required,max=128"`
}

type AnswerRunRequest struct {
	ID string `json:"-" uri:"id" binding:"required,max=128"`
	SupplyResultRequest
}

type ResolveRunRequest struct {
	ID string `json:"-" uri:"id" binding:"required,max=128"`
	SupplyResultRequest
}

type OpenRunEventsRequest struct {
	ID          string `json:"-" uri:"id" binding:"required,max=128"`
	LastEventID string `json:"-" form:"-" uri:"-" header:"Last-Event-ID"`
	After       string `json:"-" form:"after"`
}

type CheckExecutionLeaseRequest struct {
	ID    string `json:"-" uri:"id" binding:"required,max=128"`
	Epoch int64  `json:"-" form:"epoch" binding:"required,min=1"`
}

type SendMessageResponse struct {
	Run
	Created bool `json:"-"`
}

type SubmitExecutionResponse struct {
	Run
	Created bool `json:"-"`
}
