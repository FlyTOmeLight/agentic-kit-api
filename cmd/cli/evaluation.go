// eval runs a frozen single-turn suite through the existing Agentic API.
// Its checkpoint is local: the M0 entry point is not a production experiment API.
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"github.com/FlyTOmeLight/agentic-kit-api/pkg/evaluation"
	sdk "github.com/FlyTOmeLight/agentic-kit-api/sdk/go/agenticv1"
)

type trial struct {
	ID          string                     `json:"id"`
	CaseID      string                     `json:"caseID"`
	RunID       string                     `json:"runID,omitempty"`
	Observation *sdk.EvaluationObservation `json:"observation,omitempty"`
}

type checkpoint struct {
	Suite       sdk.EvaluationSuite   `json:"suite"`
	SuiteDigest string                `json:"suiteDigest"`
	Trials      []trial               `json:"trials"`
	Report      *sdk.EvaluationReport `json:"report,omitempty"`
}

func newEvaluationCommand() *cobra.Command {
	var suitePath, statePath string
	command := &cobra.Command{
		Use:   "eval",
		Short: "Run a frozen Agent evaluation suite",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			passed, err := run(cmd.Context(), suitePath, statePath)
			if err != nil {
				return err
			}
			if !passed {
				return errEvaluationFailed
			}
			return nil
		},
	}
	command.Flags().StringVar(&suitePath, "suite", "", "frozen evaluation suite JSON")
	command.Flags().StringVar(&statePath, "state", "", "private checkpoint and report JSON")
	_ = command.MarkFlagRequired("suite")
	_ = command.MarkFlagRequired("state")
	return command
}

func readJSON(path string, v any) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	d := json.NewDecoder(f)
	d.DisallowUnknownFields()
	if err = d.Decode(v); err != nil {
		return err
	}
	var trailing any
	if err = d.Decode(&trailing); err != io.EOF {
		return errors.New("expected exactly one JSON document")
	}
	return nil
}

func save(path string, state checkpoint) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".eval-state-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err = f.Chmod(0o600); err == nil {
		_, err = f.Write(b)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(f.Name(), path)
}

func run(ctx context.Context, suitePath, statePath string) (bool, error) {
	var suite sdk.EvaluationSuite
	if err := readJSON(suitePath, &suite); err != nil {
		return false, err
	}
	if suite.Scenario.Kind == "coding" {
		return false, errors.New(
			"coding scenarios require the project evaluation API for isolated environments",
		)
	}
	digest, err := evaluation.Digest(suite)
	if err != nil {
		return false, err
	}
	state := checkpoint{Suite: suite, SuiteDigest: digest}
	if _, err = os.Stat(statePath); err == nil {
		if err = readJSON(statePath, &state); err != nil {
			return false, err
		}
		if state.SuiteDigest != digest {
			return false, errors.New("suite changed: use a new experiment state file")
		}
		if len(state.Trials) != len(suite.Dataset.Cases) {
			return false, errors.New("checkpoint case count mismatch")
		}
		for i, c := range suite.Dataset.Cases {
			if state.Trials[i].CaseID != c.ID || state.Trials[i].ID == "" {
				return false, errors.New("checkpoint case identity mismatch")
			}
		}
	} else if errors.Is(err, fs.ErrNotExist) {
		for _, c := range suite.Dataset.Cases {
			var id [16]byte
			if _, err = rand.Read(id[:]); err != nil {
				return false, err
			}
			state.Trials = append(state.Trials, trial{ID: hex.EncodeToString(id[:]), CaseID: c.ID})
		}
		// Persist stable trial IDs before submitting any external Run.
		if err = save(statePath, state); err != nil {
			return false, err
		}
	} else {
		return false, err
	}
	client, err := sdk.NewClient(
		os.Getenv("AGENTIC_URL"),
		os.Getenv("AGENTIC_TOKEN"),
		&http.Client{Timeout: 20 * time.Second},
	)
	if err != nil {
		return false, err
	}
	defer client.Close()
	for i, c := range suite.Dataset.Cases {
		t := &state.Trials[i]
		if t.Observation != nil {
			continue
		}
		trialCtx, cancel := context.WithTimeout(ctx, time.Duration(suite.Scenario.TimeoutSeconds)*time.Second)
		observation, trialErr := execute(
			trialCtx,
			client,
			suite,
			c,
			t,
			func() error { return save(statePath, state) },
		)
		cancel()
		if trialErr != nil {
			return false, fmt.Errorf("trial %s: %w", c.ID, trialErr)
		}
		t.Observation = observation
		if err = save(statePath, state); err != nil {
			return false, err
		}
		fmt.Printf("case=%s run=%s status=%s\n", c.ID, observation.Run.ID, observation.Run.Status)
	}
	observations := make([]sdk.EvaluationObservation, 0, len(state.Trials))
	for _, t := range state.Trials {
		if t.Observation != nil {
			observations = append(observations, *t.Observation)
		}
	}
	state.Report, err = evaluation.Evaluate(suite, observations)
	if err != nil {
		return false, err
	}
	if err = save(statePath, state); err != nil {
		return false, err
	}
	for _, m := range state.Report.Metrics {
		fmt.Printf(
			"metric=%s scored=%d/%d coverage=%.3f passed=%t\n",
			m.ID,
			m.Scored,
			m.Planned,
			m.Coverage,
			m.Passed,
		)
	}
	return state.Report.Passed, nil
}

func execute(
	ctx context.Context,
	client *sdk.Client,
	s sdk.EvaluationSuite,
	c sdk.EvaluationCase,
	t *trial,
	persist func() error,
) (*sdk.EvaluationObservation, error) {
	var run *sdk.Run
	var err error
	if t.RunID == "" {
		run, _, err = client.Executions.Submit(
			ctx,
			"eval:"+t.ID,
			sdk.SubmitExecutionRequest{
				Spec: sdk.ExecutionSpec{
					AgentID:      s.Target.AgentID,
					AgentVersion: s.Target.AgentVersion,
					Query:        c.Input,
					Filesystem:   s.Scenario.Filesystem,
					Scope: &sdk.Scope{
						TenantID:  "evaluation-m0",
						ProjectID: "evaluation-m0",
						ActorID:   "evaluation-m0",
						TaskID:    t.ID,
						RoundID:   "1",
					},
				},
				TimeoutSeconds: s.Scenario.TimeoutSeconds,
			},
		)
		if err != nil {
			return nil, err
		}
		t.RunID = run.ID
		if err = persist(); err != nil {
			return nil, err
		}
	}
	ticker := time.NewTicker(300 * time.Millisecond)
	defer ticker.Stop()
	for {
		run, err = client.Runs.Get(ctx, t.RunID)
		if err != nil {
			return nil, err
		}
		if run.Status.Terminal() {
			break
		}
		if run.Status == sdk.WaitingClarification || run.Status == sdk.NeedsReview {
			return nil, fmt.Errorf(
				"single_turn scenario cannot automatically resolve %s; recorded run=%s",
				run.Status,
				run.ID,
			)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-ticker.C:
		}
	}
	o := &sdk.EvaluationObservation{TrialID: t.ID, CaseID: c.ID, Run: *run}
	var after int64
	for {
		page, err := client.Sessions.ListMessages(
			ctx,
			run.SessionID,
			sdk.ListMessagesOptions{After: after, Limit: 200},
		)
		if err != nil {
			return nil, err
		}
		if len(page.Items) == 0 {
			break
		}
		for _, m := range page.Items {
			if m.RunID == run.ID {
				o.Messages = append(o.Messages, m)
			}
			after = m.Seq
		}
		if len(page.Items) < 200 {
			break
		}
	}
	after = 0
	for {
		page, err := client.Runs.Trace(ctx, run.ID, after)
		if err != nil {
			return nil, err
		}
		o.Events = append(o.Events, page.Events...)
		after = page.NextSeq
		if !page.HasMore {
			break
		}
	}
	// Full final output comes from the Run, not the truncated public event preview.
	o.EvidenceComplete = true
	return o, nil
}
