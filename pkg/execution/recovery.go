package execution

import "encoding/json"

type InflightModelDispatch struct {
	Kind     string `json:"kind"`
	Sequence int64  `json:"sequence"`
}

func ReleaseInflightModel(state json.RawMessage) (json.RawMessage, json.RawMessage, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(state, &fields); err != nil || fields == nil {
		return nil, nil, ErrConflict
	}
	var version int
	if json.Unmarshal(fields["version"], &version) != nil || version != 3 {
		return nil, nil, ErrConflict
	}
	var marker struct {
		Kind     string `json:"kind"`
		Sequence *int64 `json:"sequence"`
	}
	rawMarker := fields["inflightModel"]
	if json.Unmarshal(rawMarker, &marker) != nil || marker.Sequence == nil || *marker.Sequence < 0 ||
		(marker.Kind != "model" && marker.Kind != "summary") {
		return nil, nil, ErrConflict
	}
	var pricingFailure string
	if raw := fields["pricingFailure"]; len(raw) > 0 {
		if json.Unmarshal(raw, &pricingFailure) != nil || pricingFailure != "" {
			return nil, nil, ErrConflict
		}
	}
	if raw := fields["pendingModel"]; len(raw) > 0 && string(raw) != "null" {
		return nil, nil, ErrConflict
	}
	delete(fields, "inflightModel")
	next, err := json.Marshal(fields)
	return next, rawMarker, err
}
