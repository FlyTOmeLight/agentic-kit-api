// Package workspace holds scope and reference values shared by the API,
// worker and SDK without transport or persistence dependencies.
package workspace

// Scope is asserted by a trusted application from its verified identity and task.
type Scope struct {
	TenantID  string `json:"tenantID"`
	ProjectID string `json:"projectID"`
	ActorID   string `json:"actorID"`
	TaskID    string `json:"taskID"`
	RoundID   string `json:"roundID"`
}
