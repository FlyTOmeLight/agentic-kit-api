package sandboxcenter

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// ExecCommand runs a command inside an active sandbox and returns the durable
// operation receipt. It is the programmatic equivalent of POST
// /v1/sandboxes/{id}/operations: intent is persisted before dispatch, and an
// unknown dispatch outcome leaves the operation in Unknown state.
func (s *Service) ExecCommand(
	ctx context.Context,
	sandboxID string,
	requestKey string,
	script string,
	claims ...EvaluationClaim,
) (*Operation, error) {
	if strings.TrimSpace(script) == "" || requestKey == "" {
		return nil, fmt.Errorf("%w: requestKey and script are required", ErrInvalidInput)
	}
	sb, err := s.store.GetSandbox(ctx, sandboxID)
	if err != nil {
		return nil, err
	}
	if sb.Status != SandboxStatusActive {
		return nil, ErrNotReady
	}
	op, err := s.store.BeginOperation(ctx, sb.LeaseID, requestKey, "command", payloadHashOf(script), claims...)
	if err != nil {
		return nil, err
	}
	// Replay: terminal receipt already durable.
	if op.Status == OpStatusSucceeded || op.Status == OpStatusFailed {
		return op, nil
	}
	if op.Status != OpStatusPending {
		return op, fmt.Errorf("%w: operation outcome unknown; query the original operation", ErrOutcomeUnknown)
	}
	if op.Status == OpStatusPending {
		if err := s.store.MarkDispatchStarted(ctx, op.ID, "guard:"+sb.PodUID, claims...); err != nil {
			return nil, err
		}
	}
	out, _, err := s.provider.guardCall(ctx, *sb, "/command", map[string]string{
		"operationId": op.ID,
		"script":      script,
	})
	if err != nil {
		_ = s.store.MarkOperationUnknown(ctx, op.ID, "dispatch outcome unknown: "+err.Error())
		return op, fmt.Errorf("%w: dispatch outcome unknown: %v", ErrOutcomeUnknown, err)
	}
	exitCode, errStr := parseExecdOutput(string(out))
	if err := s.store.CompleteOperation(ctx, op.ID, exitCode, string(out), errStr); err != nil {
		_ = s.store.MarkOperationUnknown(ctx, op.ID, "terminal receipt not durable: "+err.Error())
		return op, fmt.Errorf("%w: terminal receipt is not durable: %v", ErrOutcomeUnknown, err)
	}
	fresh, _ := s.store.GetOperation(ctx, op.ID)
	return fresh, nil
}

type execdErrorPayload struct {
	EName      string   `json:"ename"`
	EValue     string   `json:"evalue"`
	Traceback  []string `json:"traceback"`
	ExitCode   *int     `json:"exit_code"`
	ExitCodeV1 *int     `json:"exitCode"`
}

type execdStreamFrame struct {
	Type       string             `json:"type"`
	ExitCode   *int               `json:"exit_code"`
	ExitCodeV1 *int               `json:"exitCode"`
	EName      string             `json:"ename"`
	EValue     string             `json:"evalue"`
	Traceback  []string           `json:"traceback"`
	Error      *execdErrorPayload `json:"error"`
}

// parseExecdOutput extracts the process exit code and error summary from
// execd's event stream. Successful foreground commands end with an
// execution_complete frame (exit_code 0, or omitted on legacy compat); non-zero
// exits and start failures arrive as an error frame. A truncated stream with
// neither terminal frame yields a nil exit code and an explicit error string.
func parseExecdOutput(raw string) (*int, string) {
	var (
		sawComplete  bool
		completeCode *int
		sawError     bool
		errorCode    *int
		errStr       string
	)
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "data:") {
			line = strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		}
		if !strings.HasPrefix(line, "{") {
			continue
		}
		var frame execdStreamFrame
		if err := json.Unmarshal([]byte(line), &frame); err != nil {
			continue
		}
		switch frame.Type {
		case "execution_complete":
			sawComplete = true
			if c := firstIntPtr(frame.ExitCode, frame.ExitCodeV1); c != nil {
				code := *c
				completeCode = &code
			}
		case "error":
			sawError = true
			ename := frame.EName
			evalue := frame.EValue
			traceback := frame.Traceback
			errExit := firstIntPtr(frame.ExitCode, frame.ExitCodeV1)
			if frame.Error != nil {
				ename = frame.Error.EName
				evalue = frame.Error.EValue
				traceback = frame.Error.Traceback
				if c := firstIntPtr(frame.Error.ExitCode, frame.Error.ExitCodeV1); c != nil {
					errExit = c
				}
			}
			if errExit == nil && ename == "CommandExecError" {
				if parsed, err := strconv.Atoi(strings.TrimSpace(evalue)); err == nil {
					errExit = &parsed
				}
			}
			if errExit != nil {
				code := *errExit
				errorCode = &code
			}
			if msg := formatExecdError(ename, evalue, traceback, errExit != nil); msg != "" {
				errStr = msg
			}
		}
	}

	switch {
	case errorCode != nil:
		if *errorCode != 0 && errStr == "" {
			errStr = fmt.Sprintf("exit status %d", *errorCode)
		}
		return errorCode, errStr
	case sawError:
		if errStr == "" {
			errStr = "command execution error"
		}
		return nil, errStr
	case sawComplete:
		if completeCode != nil {
			if *completeCode != 0 && errStr == "" {
				errStr = fmt.Sprintf("exit status %d", *completeCode)
			}
			return completeCode, errStr
		}
		zero := 0
		return &zero, ""
	default:
		return nil, "incomplete command stream: missing terminal event"
	}
}

func firstIntPtr(a, b *int) *int {
	if a != nil {
		return a
	}
	return b
}

func formatExecdError(ename, evalue string, traceback []string, hasExitCode bool) string {
	for i := len(traceback) - 1; i >= 0; i-- {
		if line := strings.TrimSpace(traceback[i]); line != "" {
			return line
		}
	}
	evalue = strings.TrimSpace(evalue)
	ename = strings.TrimSpace(ename)
	if evalue != "" {
		if ename != "" && !hasExitCode {
			return ename + ": " + evalue
		}
		return evalue
	}
	return ename
}

// ErrOutcomeUnknown means the dispatch result could not be determined locally.
// The caller must query the original operation receipt before retrying.
var ErrOutcomeUnknown = errors.New("outcome unknown")

func payloadHashOf(script string) string {
	return fmt.Sprintf("sha256:%x", sha256Sum(script))
}

func sha256Sum(s string) [32]byte {
	return sha256.Sum256([]byte(s))
}
