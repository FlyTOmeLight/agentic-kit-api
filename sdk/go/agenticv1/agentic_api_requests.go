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

type GetEvaluationRequest struct {
	ID string `json:"-" uri:"id" binding:"required,max=128"`
}

type ListEvaluationsRequest struct {
	ListEvaluationsOptions
}

type CancelEvaluationRequest struct {
	ID string `json:"-" uri:"id" binding:"required,max=128"`
}

type CompareEvaluationsRequest struct {
	CandidateID string `json:"-" uri:"id" binding:"required,max=128"`
	BaselineID  string `json:"-" form:"baselineID" binding:"required,max=128"`
}

type ListProjectAgentsRequest struct {
	PageOptions
}

type GetProjectAgentRequest struct {
	ID string `json:"-" uri:"agent" binding:"required,max=128"`
}

type PublishProjectAgentRequest struct {
	ID       string `json:"-" uri:"agent" binding:"required,max=128"`
	Revision int64  `json:"revision" binding:"required,min=1"`
}

type GetProjectAgentVersionRequest struct {
	ID     string `json:"-" uri:"agent" binding:"required,max=128"`
	Number int64  `json:"-" uri:"version" binding:"required,min=1"`
}

type ListProjectEvaluationsRequest struct {
	PageOptions
}

type GetProjectTrialTraceRequest struct {
	ID      string `json:"-" uri:"experiment" binding:"required,max=128"`
	TrialID string `json:"-" uri:"trial" binding:"required,max=128"`
	After   int64  `json:"-" form:"after" binding:"min=0"`
}

type ListEvaluationPlansRequest struct {
	PageOptions
}

type GetEvaluationPlanRequest struct {
	ID string `json:"-" uri:"plan" binding:"required,max=128"`
}

type GetProjectTrialRequest struct {
	AgentID string `json:"-" uri:"agent" binding:"required,max=128"`
	ID      string `json:"-" uri:"run" binding:"required,max=128"`
}

type CancelProjectTrialRequest struct {
	AgentID string `json:"-" uri:"agent" binding:"required,max=128"`
	ID      string `json:"-" uri:"run" binding:"required,max=128"`
}

type ListProjectTemplatesRequest struct{}

type ListDatasetsRequest struct {
	PageOptions
}

type GetDatasetRequest struct {
	ID string `json:"-" uri:"dataset" binding:"required,max=128"`
}

type PublishDatasetRequest struct {
	ID       string `json:"-" uri:"dataset" binding:"required,max=128"`
	Revision int64  `json:"revision" binding:"required,min=1"`
}

type GetDatasetVersionRequest struct {
	ID     string `json:"-" uri:"dataset" binding:"required,max=128"`
	Number int64  `json:"-" uri:"version" binding:"required,min=1"`
}

type SendMessageResponse struct {
	Run
	Created bool `json:"-"`
}

type SubmitExecutionResponse struct {
	Run
	Created bool `json:"-"`
}

type StartEvaluationResponse struct {
	EvaluationExperiment
	Created bool `json:"-"`
}

type StartProjectEvaluationResponse struct {
	EvaluationExperiment
	Created bool `json:"-"`
}

type StartProjectTrialResponse struct {
	ProjectTrial
	Created bool `json:"-"`
}

// Project evaluation paths use experiment rather than the runtime evaluation ID.
type GetProjectEvaluationRequest struct {
	ID string `json:"-" uri:"experiment" binding:"required,max=128"`
}

type CancelProjectEvaluationRequest struct {
	ID string `json:"-" uri:"experiment" binding:"required,max=128"`
}

type CompareProjectEvaluationsRequest struct {
	CandidateID string `json:"-" uri:"experiment" binding:"required,max=128"`
	BaselineID  string `json:"-" form:"baselineID" binding:"required,max=128"`
}
