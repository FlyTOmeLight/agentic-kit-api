package client

import (
	"context"
	"net/url"
	"strings"
	"time"

	codingv1 "github.com/FlyTOmeLight/forge-coding-api/sdk/go/codingv1"
	framesdk "github.com/FlyTOmeLight/server-go/sdk"

	sdk "github.com/FlyTOmeLight/agentic-kit-api/sdk/go/agenticv1"
)

// CodingEnvironment converts platform scenarios into the Coding SDK contract.
// Browser input never supplies the gateway service credential.
type CodingEnvironment struct {
	url       string
	client    *codingv1.Client
	transport *framesdk.Client
	timeout   time.Duration
}

func NewCodingEnvironment(config framesdk.Config) (*CodingEnvironment, error) {
	token, err := serviceToken(config)
	if err != nil {
		return nil, err
	}
	if config.Timeout == 0 {
		config.Timeout = 20
	}
	transport, err := framesdk.NewClientWithConfig(&config)
	if err != nil {
		return nil, err
	}
	client, err := codingv1.NewClient(config.Endpoint, token, transport.Client.Client())
	if err != nil {
		_ = transport.Close()
		return nil, err
	}
	return &CodingEnvironment{url: strings.TrimRight(config.Endpoint, "/"), client: client, transport: transport, timeout: time.Duration(config.Timeout) * time.Second}, nil
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
	ctx, cancel := context.WithTimeout(ctx, e.timeout)
	defer cancel()
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

func (e *CodingEnvironment) Close() {
	_ = e.client.Close()
	_ = e.transport.Close()
}
