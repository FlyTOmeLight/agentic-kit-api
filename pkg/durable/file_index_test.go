package durable

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	"gorm.io/gorm"

	"github.com/FlyTOmeLight/agentic-kit-api/internal/table"
	"github.com/FlyTOmeLight/agentic-kit-api/pkg/contextbuilder"
	"github.com/FlyTOmeLight/agentic-kit-api/pkg/execution"
)

func TestFinishStepWithArtifactsAndCompactedSessionFileIndex(t *testing.T) {
	s := testStore(t)
	ctx := t.Context()
	session, cfg := testConversation(t, s, Spec{MaxTurns: 5})
	agentID, version := sessionPublishedFixture(t, s, session.ID)
	prepare := PrepareChatSubmission(cfg, agentID, version, nil)
	submit := func(attachments []Attachment) *Run {
		t.Helper()
		key := NewID()
		run, created, err := s.Accept(ctx, execution.SubmitCommand{
			Owner:       "owner",
			SessionID:   session.ID,
			Kind:        execution.SubmitChat,
			RequestKey:  key,
			RequestHash: key,
			Query:       "inspect files",
			TTL:         time.Hour,
			MaxAttempts: 3,
			Attachments: attachments,
		}, prepare)
		if err != nil || !created {
			t.Fatalf("accept = %+v %v %v", run, created, err)
		}
		return run
	}
	upload := Attachment{
		Kind:      AttachmentImage,
		Name:      "source.jpg",
		URI:       "https://media.test/source.jpg",
		MediaType: "image/jpeg",
		SizeBytes: 32,
	}
	first := submit([]Attachment{upload})
	worker := claim(t, s, "worker")
	if worker.ID != first.ID {
		t.Fatal("claimed the wrong run")
	}
	lease := worker.Lease()
	if err := s.RecordDecision(ctx, lease, json.RawMessage(`{}`), Message{
		Role: "assistant", ToolCalls: []ToolCall{{ID: "write", Name: "write_file"}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.BeginStep(
		ctx,
		lease,
		StepIntent{ID: "write", Input: "write files", Policy: Idempotent},
	); err != nil {
		t.Fatal(err)
	}
	artifact := FileArtifact{
		Path:      "/workspace/output.txt",
		Action:    "written",
		Provider:  "Sandbox",
		MediaType: "text/plain",
	}
	state := json.RawMessage(`{"finished":true}`)
	invalid := artifact
	invalid.Action = "unknown"
	if err := s.FinishStepWithArtifacts(
		ctx,
		lease,
		"write",
		"written",
		[]FileArtifact{invalid},
		state,
	); !errors.Is(
		err,
		ErrInvalid,
	) {
		t.Fatalf("invalid artifact error = %v", err)
	}

	if err := s.DB().
		Exec("ALTER TABLE agent_message ADD CONSTRAINT reject_tool_for_test CHECK (kind <> 'Tool')").
		Error; err != nil {
		t.Fatal(err)
	}
	if err := s.FinishStepWithArtifacts(
		ctx,
		lease,
		"write",
		"written",
		[]FileArtifact{artifact},
		state,
	); err == nil {
		t.Fatal("expected the tool message insertion to fail")
	}
	var step table.Step
	if err := s.DB().Where("run_id = ? AND step_id = ?", first.ID, "write").Take(&step).Error; err != nil {
		t.Fatal(err)
	}
	after, err := s.Get(ctx, "owner", first.ID)
	if err != nil || step.Status != "Started" || step.Result != "" ||
		string(after.State) != "{}" || after.EventSeq == 0 {
		t.Fatalf("failed write was partially committed: run=%+v step=%+v err=%v", after, step, err)
	}
	events, err := s.Events(ctx, "owner", first.ID, 0)
	if err != nil || slices.ContainsFunc(events, func(e Event) bool { return e.Kind == "ToolCompleted" }) {
		t.Fatalf("failed write left a ToolCompleted event: %v, %v", events, err)
	}
	if err := s.DB().Exec("ALTER TABLE agent_message DROP CONSTRAINT reject_tool_for_test").Error; err != nil {
		t.Fatal(err)
	}
	if err := s.FinishStepWithArtifacts(
		ctx,
		lease,
		"write",
		"written",
		[]FileArtifact{artifact},
		state,
	); err != nil {
		t.Fatal(err)
	}
	if err := s.FinishStepWithArtifacts(
		ctx,
		lease,
		"write",
		"duplicate",
		[]FileArtifact{artifact},
		state,
	); !errors.Is(
		err,
		ErrConflict,
	) {
		t.Fatalf("duplicate completion: %v", err)
	}
	after, err = s.Get(ctx, "owner", first.ID)
	if err != nil || len(after.State) == 0 {
		t.Fatalf("committed checkpoint: %+v %v", after, err)
	}
	var checkpoint struct{ Finished bool }
	if err := json.Unmarshal(after.State, &checkpoint); err != nil || !checkpoint.Finished {
		t.Fatalf("committed checkpoint: %s %v", after.State, err)
	}
	messages, err := s.Messages(ctx, "owner", session.ID, 0, 20)
	if err != nil || len(messages) != 3 || !slices.Equal(messages[0].Attachments, []Attachment{upload}) ||
		messages[2].Kind != "Tool" || messages[2].ToolCallID != "write" ||
		!slices.Equal(messages[2].Artifacts, []FileArtifact{artifact}) {
		t.Fatalf("messages = %+v %v", messages, err)
	}
	if err = s.CommitBoundary(
		ctx,
		lease,
		Boundary{Kind: BoundaryComplete, Output: "done", State: state},
	); err != nil {
		t.Fatal(err)
	}
	history, _, err := loadConversationHistory(s.DB().WithContext(ctx), "owner", session.ID)
	if err != nil || len(history) != 1 || len(history[0].Messages[0].Attachments) != 1 ||
		history[0].Messages[0].Attachments[0].URI != upload.URI {
		t.Fatalf("stored history lost attachment: %+v %v", history, err)
	}
	if err = s.DB().Model(&table.Session{}).Where("id = ?", session.ID).
		Updates(map[string]any{"compacted_through_seq": gorm.Expr("message_seq"), "compacted_summary": "past work"}).
		Error; err != nil {
		t.Fatal(err)
	}
	next := submit(nil)
	snapshot := decodeRunSnapshot(t, s, next.ID)
	files := snapshot.Materials.FileIndex
	if snapshot.Materials.BaseSummary != "past work" || len(snapshot.Materials.History) != 0 ||
		len(
			files,
		) != 2 || files[0].URI != artifact.Path || files[0].Source != "workspace" || files[0].Accessible ||
		files[1].URI != upload.URI || files[1].Source != "attachment" || files[1].Action != "uploaded" {
		t.Fatalf("compacted snapshot = %+v", snapshot.Materials)
	}
}

func TestGoalIterationKeepsFilesFromOnlyTheRestoredSandboxChain(t *testing.T) {
	s := testStore(t)
	ctx := t.Context()
	session, cfg := testConversation(t, s, Spec{MaxTurns: 5})
	cfg.Toolsets = []string{"filesystem"}
	id, version := sessionPublishedFixture(t, s, session.ID)

	// Seed completed runs on independent and connected sandbox branches.
	appendCompleted := func(sandbox, restore string, files []FileArtifact, upload []Attachment) *Run {
		t.Helper()
		runID := NewID()
		now := time.Now()
		row := table.Run{
			ID: runID, OwnerID: "owner", SessionID: session.ID,
			RequestKey: runID, RequestHash: runID, Status: Completed, Epoch: 1,
			Deadline: now.Add(time.Hour), MaxAttempts: 3,
			Spec: table.ExecutionSnapshot(execution.CompileRunSpec(
				cfg,
				id,
				version,
				"source",
				nil,
				&FilesystemBinding{
					Provider:             "Sandbox",
					URL:                  "http://sandbox.test",
					Workspace:            "/workspace",
					RestoreFromSandboxID: restore,
				},
				upload...,
			)),
			StallMark: runID,
		}
		if err := s.DB().Create(&row).Error; err != nil {
			t.Fatal(err)
		}
		run := runFromRow(row)
		if err := s.DB().Transaction(func(tx *gorm.DB) error {
			if err := appendMessage(
				tx,
				run,
				"User",
				Message{Role: "user", Content: "source", Attachments: upload},
			); err != nil {
				return err
			}
			return appendMessage(
				tx,
				run,
				"Tool",
				Message{Role: "tool", ToolCallID: "emit", Artifacts: files},
			)
		}); err != nil {
			t.Fatal(err)
		}
		if err := s.DB().Create(&table.RunEnvironment{
			ID: NewID(), RunID: runID, RequestKey: NewID(), RunEpoch: 1, ProfileID: "coding",
			SandboxID: sandbox, Status: EnvironmentReleased, SavedCheckpoint: "confirmed",
		}).Error; err != nil {
			t.Fatal(err)
		}
		return run
	}
	upload := Attachment{
		Kind:      AttachmentFile,
		URI:       "https://media.test/input",
		Name:      "input",
		MediaType: "text/plain",
	}
	appendCompleted(
		"sibling",
		"",
		[]FileArtifact{{Path: "/workspace/sibling", Action: "written", Provider: "Sandbox"}},
		nil,
	)
	appendCompleted(
		"ancestor",
		"",
		[]FileArtifact{{Path: "/workspace/ancestor", Action: "written", Provider: "Sandbox"}},
		nil,
	)
	previous := appendCompleted(
		"previous",
		"ancestor",
		[]FileArtifact{{Path: "/workspace/latest", Action: "edited", Provider: "Sandbox"}},
		[]Attachment{upload},
	)
	input := newTestGoal(session.ID, NewID(), "goal objective")
	input.Evaluator = EvaluatorSpec{Kind: EvaluatorCommand, Config: "check"}
	goal, _, err := s.CreateGoal(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.DB().Model(&table.Run{}).Where("id = ?", previous.ID).Update("goal_id", goal.ID).Error; err != nil {
		t.Fatal(err)
	}
	if err = s.DB().
		Model(&table.Goal{}).
		Where("id = ?", goal.ID).
		Update("active_run_id", previous.ID).
		Error; err != nil {
		t.Fatal(err)
	}
	if _, err = s.RecordEvaluation(
		ctx,
		"owner",
		goal.ID,
		previous.ID,
		EvaluationFailed,
		"keep working",
	); err != nil {
		t.Fatal(err)
	}
	next, err := s.QueueGoalIteration(ctx)
	if err != nil || next == nil || next.Spec.Attachments != nil ||
		next.Spec.Filesystem == nil || next.Spec.Filesystem.RestoreFromSandboxID != "previous" {
		t.Fatalf("next iteration = %+v %v", next, err)
	}
	files := decodeRunSnapshot(t, s, next.ID).Materials.FileIndex
	byURI := make(map[string]contextbuilder.FileIndexEntry, len(files))
	for _, file := range files {
		byURI[file.URI] = file
	}
	if len(byURI) != 4 || !byURI["/workspace/ancestor"].Accessible ||
		!byURI["/workspace/latest"].Accessible || byURI["/workspace/sibling"].Accessible ||
		byURI["/workspace/sibling"].Source != "workspace" || byURI[upload.URI].Source != "attachment" {
		t.Fatalf("restored file index = %+v", files)
	}
}

func TestChildRunStartsWithoutParentAttachmentsOrIndex(t *testing.T) {
	for _, await := range []bool{false, true} {
		t.Run(fmt.Sprintf("await=%v", await), func(t *testing.T) {
			s := testStore(t)
			ctx := t.Context()
			session, cfg := testConversation(t, s, Spec{MaxTurns: 5})
			cfg.SelfDelegate = true
			id, version := sessionPublishedFixture(t, s, session.ID)
			prepare := PrepareChatSubmission(cfg, id, version, nil)
			submit := func() *Run {
				t.Helper()
				key := NewID()
				run, _, err := s.Accept(ctx, execution.SubmitCommand{
					Owner:       "owner",
					SessionID:   session.ID,
					Kind:        execution.SubmitChat,
					RequestKey:  key,
					RequestHash: key,
					Query:       "inspect files",
					TTL:         time.Hour,
					MaxAttempts: 3,
					Attachments: []Attachment{
						{
							Kind:      AttachmentFile,
							URI:       "https://media.test/file",
							Name:      "file",
							MediaType: "text/plain",
						},
					},
				}, prepare)
				if err != nil {
					t.Fatal(err)
				}
				return run
			}
			submit()
			first := claim(t, s, "worker")
			if err := s.CommitBoundary(ctx, first.Lease(), Boundary{
				Kind: BoundaryComplete, State: json.RawMessage(`{}`), Output: "done",
			}); err != nil {
				t.Fatal(err)
			}
			parent := submit()
			worker := claim(t, s, "worker")
			if worker.ID != parent.ID || len(parent.Spec.Attachments) != 1 ||
				len(decodeRunSnapshot(t, s, parent.ID).Materials.FileIndex) != 1 {
				t.Fatalf("fixture parent has no attachment or files: %+v", parent)
			}
			if _, err := s.BeginStep(
				ctx,
				worker.Lease(),
				StepIntent{ID: "child", Input: "delegate", Policy: Idempotent},
			); err != nil {
				t.Fatal(err)
			}
			var child *Run
			var err error
			if await {
				child, err = s.SpawnChildAndAwait(
					ctx,
					worker.Lease(),
					ChildSpawnRequest{StepID: "child", Query: "delegate", TTL: time.Hour},
					json.RawMessage(`{}`),
					"waiting",
				)
			} else {
				child, err = s.SpawnChildRun(ctx, worker.Lease(), "child", "delegate", time.Hour)
			}
			if err != nil || child.Spec.Attachments != nil ||
				len(decodeRunSnapshot(t, s, child.ID).Materials.FileIndex) != 0 {
				t.Fatalf("spawned child = %+v %v", child, err)
			}
		})
	}
}

func TestSessionFileIndexDeduplicatesAndLimitsAIOFiles(t *testing.T) {
	s := testStore(t)
	ctx := t.Context()
	cfg := fixtureConfig(Spec{MaxTurns: 3})
	cfg.Toolsets = []string{"filesystem"}
	agent := createAgentFixture(t, s, "owner", "test", cfg)
	version := publishAgentFixture(t, s, "owner", agent.ID, agent.Revision)
	fs := &FilesystemBinding{
		Provider:  "AIO",
		URL:       "http://aio.test",
		Workspace: "/workspace",
		TokenEnv:  "AIO_TOKEN",
	}
	input := execution.ExecutionSpec{
		AgentID: agent.ID, AgentVersion: version.Number, Query: "write files", Filesystem: fs,
		Scope: &Scope{TenantID: "tenant", ActorID: "owner", TaskID: "task", RoundID: "first"},
	}
	first, _, err := submitExecutionFixture(t, s, "owner", "first", input, time.Hour, cfg)
	if err != nil {
		t.Fatal(err)
	}
	lease := claim(t, s, "worker").Lease()
	state := json.RawMessage(`{}`)
	if _, err = s.BeginStep(ctx, lease, StepIntent{ID: "batch", Policy: Idempotent}); err != nil {
		t.Fatal(err)
	}
	var files []FileArtifact
	for i := range 55 {
		files = append(
			files,
			FileArtifact{Path: fmt.Sprintf("/workspace/%02d", i), Action: "written", Provider: "AIO"},
		)
	}
	last := files[len(files)-1]
	last.Action = "edited"
	files = append(files, last)
	if err = s.FinishStepWithArtifacts(ctx, lease, "batch", "done", files, state); err != nil {
		t.Fatal(err)
	}
	if err = s.CommitBoundary(
		ctx,
		lease,
		Boundary{Kind: BoundaryComplete, State: state, Output: "done"},
	); err != nil {
		t.Fatal(err)
	}
	input.Scope.RoundID = "second"
	second, _, err := submitExecutionFixture(t, s, "owner", "second", input, time.Hour, cfg)
	if err != nil {
		t.Fatal(err)
	}
	got := decodeRunSnapshot(t, s, second.ID).Materials.FileIndex
	if second.SessionID != first.SessionID || len(got) != contextbuilder.MaxFileIndexEntries ||
		got[0].URI != last.Path || got[0].Action != "edited" || got[1].URI == last.Path ||
		slices.ContainsFunc(got, func(f contextbuilder.FileIndexEntry) bool { return !f.Accessible }) {
		t.Fatalf("AIO index: %+v", got)
	}
	other := *fs
	other.Workspace = "/other"
	got, err = loadSessionFileIndex(s.DB().WithContext(ctx), "owner", second.SessionID, &other)
	if err != nil || len(got) != contextbuilder.MaxFileIndexEntries ||
		slices.ContainsFunc(got, func(f contextbuilder.FileIndexEntry) bool { return f.Accessible }) {
		t.Fatalf("different AIO workspace index: %+v %v", got, err)
	}
}
