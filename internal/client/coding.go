package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	sdk "github.com/FlyTOmeLight/agentic-kit-api/sdk/go/agenticv1"
)

// CodingEnvironment converts the platform scenario into the Coding service's
// isolated repository lifecycle. The browser never supplies gateway credentials.
type CodingEnvironment struct {
	url, token string
	client     *http.Client
}

func NewCodingEnvironment(endpoint, token string) *CodingEnvironment {
	return &CodingEnvironment{
		url:   strings.TrimRight(endpoint, "/"),
		token: token,
		client: &http.Client{
			Transport:     http.DefaultTransport.(*http.Transport).Clone(),
			Timeout:       200 * time.Second,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
	}
}

func (e *CodingEnvironment) Filesystem(trial string) *sdk.FilesystemBinding {
	return &sdk.FilesystemBinding{
		Provider:  "AIO",
		URL:       e.url + "/internal/v1/evaluation-environments/" + trial + "/sandbox",
		TokenEnv:  "CODING_TOOLS_TOKEN",
		Workspace: "/workspace/repo",
	}
}

func (e *CodingEnvironment) Advance(
	ctx context.Context,
	trial string,
	scope sdk.EvaluationScope,
	scenario sdk.CodingEvaluationScenario,
	phase string,
) (*sdk.CodingEvaluationEvidence, error) {
	raw, err := json.Marshal(map[string]any{"scope": scope, "scenario": scenario, "phase": phase})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(
		ctx,
		"POST",
		e.url+"/internal/v1/evaluation-environments/"+trial,
		bytes.NewReader(raw),
	)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+e.token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := e.client.Do(req)
	if err != nil {
		return nil, errors.New("Coding environment connection unavailable")
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("Coding environment HTTP %d", resp.StatusCode)
	}
	var evidence sdk.CodingEvaluationEvidence
	if err = json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&evidence); err != nil {
		return nil, err
	}
	return &evidence, nil
}

func (e *CodingEnvironment) Close() { e.client.CloseIdleConnections() }
