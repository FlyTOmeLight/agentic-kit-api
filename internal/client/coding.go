package client

import (
	"context"
	"net/url"
	"strings"

	codingv1 "github.com/FlyTOmeLight/forge-coding-api/sdk/go/codingv1"

	sdk "github.com/FlyTOmeLight/agentic-kit-api/sdk/go/agenticv1"
)

// CodingEnvironment converts platform scenarios into the Coding SDK contract.
// Browser input never supplies the gateway service credential.
type CodingEnvironment struct {
	url    string
	client *codingv1.Client
}

func NewCodingEnvironment(endpoint, token string) (*CodingEnvironment, error) {
	client, err := codingv1.NewClient(endpoint, token, nil)
	if err != nil {
		return nil, err
	}
	return &CodingEnvironment{url: strings.TrimRight(endpoint, "/"), client: client}, nil
}

func (e *CodingEnvironment) Filesystem(trial string) *sdk.FilesystemBinding {
	return &sdk.FilesystemBinding{
		Provider:  "AIO",
		URL:       e.url + "/internal/v1/evaluation-environments/" + url.PathEscape(trial) + "/sandbox",
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
	evidence, err := e.client.AdvanceEvaluation(
		ctx,
		trial,
		&codingv1.EvaluationEnvironmentRequest{
			Scope:    codingv1.EvaluationScope(scope),
			Scenario: codingv1.EvaluationScenario(scenario),
			Phase:    phase,
		},
	)
	if err != nil {
		return nil, err
	}
	result := &sdk.CodingEvaluationEvidence{
		Status:   evidence.Status,
		BaseHead: evidence.BaseHead,
		Patch:    evidence.Patch,
		Changed:  evidence.Changed,
		Error:    evidence.Error,
		Stopped:  evidence.Stopped,
	}
	for _, check := range evidence.Checks {
		result.Checks = append(
			result.Checks,
			sdk.CodingEvaluationCheck{Command: check.Command, ExitCode: check.ExitCode, Log: check.Log},
		)
	}
	return result, nil
}

func (e *CodingEnvironment) Close() { _ = e.client.Close() }
