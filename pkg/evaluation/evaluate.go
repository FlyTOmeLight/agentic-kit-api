// Package evaluation scores recorded public evidence without executing agents.
package evaluation

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"path"
	"regexp"
	"strings"

	sdk "github.com/FlyTOmeLight/agentic-kit-api/sdk/go/agenticv1"
)

func Validate(s sdk.EvaluationSuite) error {
	if s.SchemaVersion != 1 || strings.TrimSpace(s.Name) == "" || strings.TrimSpace(s.Version) == "" ||
		s.Target.Kind != "Agentic" ||
		s.Target.AgentID == "" ||
		s.Target.AgentVersion < 1 ||
		(s.Scenario.Kind != "single_turn" && s.Scenario.Kind != "coding") ||
		s.Scenario.TimeoutSeconds < 1 ||
		s.Scenario.TimeoutSeconds > 86400 ||
		s.Dataset.Name == "" ||
		s.Dataset.Version == "" ||
		len(s.Dataset.Cases) == 0 ||
		len(s.Evaluators) == 0 ||
		len(s.Metrics) == 0 {
		return errors.New("invalid evaluation suite identity, target, scenario, or empty collection")
	}
	capabilities := map[string]bool{}
	for _, c := range s.Target.Capabilities {
		if c == "" || capabilities[c] {
			return errors.New("duplicate or empty capability")
		}
		capabilities[c] = true
	}
	if s.Scenario.Kind == "coding" {
		c := s.Scenario.Coding
		if c == nil || !regexp.MustCompile(`^[0-9a-f]{40}$`).MatchString(c.BaseHead) ||
			len(c.AllowedPaths) == 0 ||
			len(c.AllowedPaths) > 20 ||
			len(c.Checks) == 0 ||
			len(c.Checks) > 10 ||
			s.Scenario.Filesystem != nil ||
			!capabilities["filesystem"] ||
			!capabilities["checks"] {
			return errors.New(
				"coding requires a fixed commit, allowed paths, checks and isolated filesystem",
			)
		}
		for _, p := range c.AllowedPaths {
			if p == "" || path.IsAbs(p) || path.Clean(p) == "." || strings.Contains(p, "\\") {
				return errors.New("invalid allowed path")
			}
			for _, segment := range strings.Split(p, "/") {
				if segment == ".." {
					return errors.New("invalid allowed path")
				}
			}
		}
		for _, command := range c.Checks {
			if strings.TrimSpace(command) == "" || len(command) > 500 ||
				strings.ContainsAny(command, "\r\n") {
				return errors.New("invalid check command")
			}
		}
	} else if s.Scenario.Coding != nil || capabilities["checks"] {
		return errors.New("coding evidence requires coding scenario")
	}
	if capabilities["filesystem"] && s.Scenario.Filesystem == nil && s.Scenario.Kind != "coding" {
		return errors.New("filesystem capability requires trusted scenario binding")
	}
	if s.Scenario.Filesystem != nil &&
		(!capabilities["filesystem"] || s.Scenario.Filesystem.Provider != "AIO" || s.Scenario.Filesystem.URL == "" || s.Scenario.Filesystem.TokenEnv == "" || !strings.HasPrefix(s.Scenario.Filesystem.Workspace, "/")) {
		return errors.New("invalid trusted filesystem binding")
	}
	if !capabilities["output"] {
		return errors.New("target must declare output capability")
	}
	cases := map[string]bool{}
	for _, c := range s.Dataset.Cases {
		if c.ID == "" || strings.TrimSpace(c.Input) == "" || cases[c.ID] {
			return fmt.Errorf("duplicate or invalid case: %s", c.ID)
		}
		cases[c.ID] = true
	}
	evaluators := map[string]bool{}
	for _, e := range s.Evaluators {
		if e.ID == "" || e.Version == "" || evaluators[e.ID] || len(e.Requires) != 1 ||
			(e.Kind != "checks_passed" && (e.Scope != "output" || e.Requires[0] != "output")) ||
			(e.Kind == "checks_passed" && (e.Scope != "checks" || e.Requires[0] != "checks" || s.Scenario.Kind != "coding")) {
			return fmt.Errorf("invalid evaluator identity or evidence scope: %s", e.ID)
		}
		switch e.Kind {
		case "contains_all":
			if len(e.Values) == 0 {
				return errors.New("contains_all needs nonempty values")
			}
			for _, value := range e.Values {
				if value == "" {
					return errors.New("contains_all cannot contain empty value")
				}
			}
		case "exact", "json_valid", "checks_passed":
			if len(e.Values) != 0 {
				return fmt.Errorf("%s does not accept values", e.Kind)
			}
		default:
			return fmt.Errorf("unsupported evaluator kind: %s", e.Kind)
		}
		evaluators[e.ID] = true
	}
	metrics := map[string]bool{}
	for _, m := range s.Metrics {
		if m.ID == "" || metrics[m.ID] || !evaluators[m.Evaluator] || m.Aggregate != "pass_rate" ||
			math.IsNaN(m.MinimumScore) ||
			math.IsInf(m.MinimumScore, 0) ||
			m.MinimumScore < 0 ||
			m.MinimumScore > 1 ||
			math.IsNaN(m.MinimumCoverage) ||
			math.IsInf(m.MinimumCoverage, 0) ||
			m.MinimumCoverage <= 0 ||
			m.MinimumCoverage > 1 {
			return fmt.Errorf("invalid metric: %s", m.ID)
		}
		metrics[m.ID] = true
	}
	return nil
}

func Digest(s sdk.EvaluationSuite) (string, error) {
	if err := Validate(s); err != nil {
		return "", err
	}
	b, err := json.Marshal(s)
	if err != nil {
		return "", err
	}
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:]), nil
}

// Evaluate uses every scheduled case as the denominator. Unscored cases cannot
// increase pass rate. Coverage independently states how much was actually scored.
func Evaluate(s sdk.EvaluationSuite, observations []sdk.EvaluationObservation) (*sdk.EvaluationReport, error) {
	digest, err := Digest(s)
	if err != nil {
		return nil, err
	}
	knownCases := map[string]bool{}
	for _, c := range s.Dataset.Cases {
		knownCases[c.ID] = true
	}
	byCase := map[string]sdk.EvaluationObservation{}
	for _, o := range observations {
		if !knownCases[o.CaseID] || o.TrialID == "" {
			return nil, errors.New("unknown case or empty trial")
		}
		if _, exists := byCase[o.CaseID]; exists {
			return nil, fmt.Errorf("duplicate observation for case: %s", o.CaseID)
		}
		if o.Run.AgentID != s.Target.AgentID || o.Run.ConfigVersion != fmt.Sprint(s.Target.AgentVersion) {
			return nil, fmt.Errorf("target version mismatch for case: %s", o.CaseID)
		}
		byCase[o.CaseID] = o
	}
	report := &sdk.EvaluationReport{SuiteDigest: digest, Passed: true}
	for _, c := range s.Dataset.Cases {
		o, exists := byCase[c.ID]
		for _, e := range s.Evaluators {
			score := sdk.EvaluationScore{
				TrialID:   o.TrialID,
				CaseID:    c.ID,
				Evaluator: e.ID,
				Status:    "Incomplete",
				Reason:    "trial or required evidence not available",
			}
			if exists && o.Run.Status.Terminal() && o.Run.Status != sdk.Completed {
				score.Status, score.Reason = "ExecutionError", string(o.Run.Status)
			} else if exists && o.Run.Status == sdk.Completed && o.EvidenceComplete {
				if e.Kind == "checks_passed" &&
					(o.Coding == nil || o.Coding.Status != "Completed" || len(o.Coding.Checks) != len(s.Scenario.Coding.Checks)) {
					score.Status, score.Reason = "ExecutionError", "independent verification evidence unavailable"
					if o.Coding != nil && o.Coding.Error != "" {
						score.Reason = o.Coding.Error
					}
					report.Scores = append(report.Scores, score)
					continue
				}
				passed := false
				switch e.Kind {
				case "checks_passed":
					passed = true
					for i, check := range o.Coding.Checks {
						if check.ExitCode != 0 || check.Command != s.Scenario.Coding.Checks[i] {
							passed = false
						}
					}
				case "contains_all":
					passed = true
					for _, value := range e.Values {
						if !strings.Contains(o.Run.Output, value) {
							passed = false
							break
						}
					}
				case "exact":
					passed = o.Run.Output == c.Expected
				case "json_valid":
					passed = json.Valid([]byte(o.Run.Output))
				}
				value := 0.0
				if passed {
					value = 1
				}
				score.Status, score.Value, score.Reason = "Scored", &value, "configured output check"
				if e.Kind == "checks_passed" {
					score.Reason = "independent verification exit codes"
				}
			}
			report.Scores = append(report.Scores, score)
		}
	}
	for _, m := range s.Metrics {
		result := sdk.EvaluationMetricResult{ID: m.ID, Planned: len(s.Dataset.Cases)}
		sum := 0.0
		for _, score := range report.Scores {
			if score.Evaluator == m.Evaluator && score.Status == "Scored" {
				result.Scored++
				sum += *score.Value
			}
		}
		result.Coverage = float64(result.Scored) / float64(result.Planned)
		if result.Scored > 0 {
			value := sum / float64(result.Planned)
			result.Value = &value
		}
		result.Passed = result.Value != nil && *result.Value >= m.MinimumScore &&
			result.Coverage >= m.MinimumCoverage
		if !result.Passed {
			report.Passed = false
		}
		report.Metrics = append(report.Metrics, result)
	}
	return report, nil
}
