package contextbuilder

import (
	"encoding/json"
	"errors"
	"testing"
)

func testSnapshot(mode MemoryMode) Snapshot {
	snapshot := Snapshot{
		MemoryMode: mode,
		Materials: FrozenMaterials{
			SystemInstruction: "system",
			Tools: []ToolDefinition{{
				Name:        "lookup",
				Description: "look up a value",
				Parameters:  json.RawMessage(`{"type":"object"}`),
			}},
			History: []MessageGroup{{
				Source: Source{Kind: SourceHistory, ID: "run-1"},
				Messages: []Message{
					{Role: RoleUser, Content: "question"},
					{
						Role:      RoleAssistant,
						ToolCalls: []ToolCall{{ID: "call-1", Name: "lookup", Arguments: `{}`}},
					},
					{Role: RoleTool, ToolCallID: "call-1", Content: "result"},
					{Role: RoleAssistant, Content: "answer"},
				},
			}},
		},
		Policy: Policy{Version: PolicyVersionV1},
		Budget: Budget{MaxInputBytes: 4096, CounterVersion: "test-v1"},
	}
	if mode == MemoryRecall {
		snapshot.Materials.Memory = []ContextBlock{{
			Source:   Source{Kind: SourceMemory, ID: "memory-1", Revision: 3},
			Content:  "remembered fact",
			Priority: 1,
		}}
	}
	return snapshot
}

func TestSnapshotRoundTrip(t *testing.T) {
	for _, mode := range []MemoryMode{MemoryOff, MemoryEmpty, MemoryRecall} {
		t.Run(string(mode), func(t *testing.T) {
			envelope, err := EncodeSnapshot(testSnapshot(mode))
			if err != nil {
				t.Fatal(err)
			}
			if envelope.Format != SnapshotFormatV1 || envelope.PolicyVersion != PolicyVersionV1 ||
				envelope.Digest == "" {
				t.Fatalf("bad envelope: %+v", envelope)
			}
			decoded, err := DecodeSnapshot(envelope)
			if err != nil {
				t.Fatal(err)
			}
			if decoded.MemoryMode != mode || len(decoded.Materials.History) != 1 ||
				decoded.Budget.MaxInputBytes != 4096 {
				t.Fatalf("decoded: %+v", decoded)
			}
		})
	}
}

func TestSnapshotRejectsTampering(t *testing.T) {
	envelope, err := EncodeSnapshot(testSnapshot(MemoryOff))
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal(envelope.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	payload["memoryMode"] = MemoryRecall
	envelope.Payload, err = json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeSnapshot(envelope); !errors.Is(err, ErrSnapshotDigest) {
		t.Fatalf("error %v, want ErrSnapshotDigest", err)
	}
}

func TestSnapshotRejectsInconsistentMemoryMode(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Snapshot)
	}{
		{
			name: "off carries memory",
			mutate: func(s *Snapshot) {
				s.Materials.Memory = testSnapshot(MemoryRecall).Materials.Memory
			},
		},
		{
			name: "empty carries memory",
			mutate: func(s *Snapshot) {
				s.MemoryMode = MemoryEmpty
				s.Materials.Memory = testSnapshot(MemoryRecall).Materials.Memory
			},
		},
		{
			name: "recall without material",
			mutate: func(s *Snapshot) {
				s.MemoryMode = MemoryRecall
			},
		},
		{
			name: "unknown mode",
			mutate: func(s *Snapshot) {
				s.MemoryMode = "Forever"
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			snapshot := testSnapshot(MemoryOff)
			test.mutate(&snapshot)
			if _, err := EncodeSnapshot(snapshot); !errors.Is(err, ErrInvalidSnapshot) {
				t.Fatalf("error %v, want ErrInvalidSnapshot", err)
			}
		})
	}
}

func TestSnapshotRejectsUnknownFormatAndPolicy(t *testing.T) {
	envelope, err := EncodeSnapshot(testSnapshot(MemoryOff))
	if err != nil {
		t.Fatal(err)
	}

	unknownFormat := envelope
	unknownFormat.Format = "context-snapshot-v2"
	if _, err := DecodeSnapshot(unknownFormat); !errors.Is(err, ErrUnsupportedSnapshotFormat) {
		t.Fatalf("error %v, want ErrUnsupportedSnapshotFormat", err)
	}

	envelope, err = EncodeSnapshot(testSnapshot(MemoryOff))
	if err != nil {
		t.Fatal(err)
	}
	unknownPolicy := envelope
	unknownPolicy.PolicyVersion = "context-v9"
	if _, err := DecodeSnapshot(unknownPolicy); !errors.Is(err, ErrInvalidSnapshot) {
		t.Fatalf("error %v, want ErrInvalidSnapshot", err)
	}

	if _, err := EncodeSnapshot(Snapshot{
		MemoryMode: MemoryOff,
		Policy:     Policy{Version: "context-v9"},
	}); !errors.Is(err, ErrUnsupportedPolicy) {
		t.Fatalf("error %v, want ErrUnsupportedPolicy", err)
	}
}

func TestSnapshotRoundTripV3(t *testing.T) {
	snapshot := testSnapshot(MemoryOff)
	snapshot.Policy = Policy{Version: PolicyVersionV3, KeepRecentToolResults: 2}
	envelope, err := EncodeSnapshot(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if envelope.PolicyVersion != PolicyVersionV3 {
		t.Fatalf("policy version %q, want %q", envelope.PolicyVersion, PolicyVersionV3)
	}
	decoded, err := DecodeSnapshot(envelope)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.Policy != snapshot.Policy {
		t.Fatalf("policy %+v, want %+v", decoded.Policy, snapshot.Policy)
	}
}

func TestSnapshotIsDeterministic(t *testing.T) {
	first, err := EncodeSnapshot(testSnapshot(MemoryOff))
	if err != nil {
		t.Fatal(err)
	}
	second, err := EncodeSnapshot(testSnapshot(MemoryOff))
	if err != nil {
		t.Fatal(err)
	}
	if first.Digest != second.Digest || string(first.Payload) != string(second.Payload) {
		t.Fatal("same snapshot encoded differently")
	}
}
