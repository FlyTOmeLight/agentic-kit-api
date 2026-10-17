package agenticv1

import "time"

// Dataset drafts use revisions; published versions are immutable.
type Dataset struct {
	ID               string           `json:"id"`
	Name             string           `json:"name"`
	Revision         int64            `json:"revision"`
	PublishedVersion int64            `json:"publishedVersion"`
	Cases            []EvaluationCase `json:"cases"`
	UpdatedAt        time.Time        `json:"updatedAt"`
}

type SaveDatasetRequest struct {
	ID string `json:"-" uri:"dataset" binding:"required,max=128"`

	Name     string           `json:"name"`
	Revision int64            `json:"revision"`
	Cases    []EvaluationCase `json:"cases"`
}

type DatasetVersion struct {
	DatasetID string            `json:"datasetID"`
	Number    int64             `json:"number"`
	Snapshot  EvaluationDataset `json:"snapshot"`
	CreatedAt time.Time         `json:"createdAt"`
}
