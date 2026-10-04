package run

type supplyResultRequest struct {
	StepID string `json:"stepID" binding:"required,max=128"`
	Result string `json:"result" binding:"required"`
}
