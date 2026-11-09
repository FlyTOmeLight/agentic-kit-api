package contextbuilder

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

// SnapshotFormatV1 is the frozen context snapshot payload format.
const SnapshotFormatV1 = "context-snapshot-v1"

// MemoryMode records whether the Run asked for memory recall and whether the
// recall returned material. Off, Empty and Recall are distinct facts.
type MemoryMode string

const (
	MemoryOff    MemoryMode = "Off"
	MemoryEmpty  MemoryMode = "Empty"
	MemoryRecall MemoryMode = "Recall"
)

// Snapshot is the context payload frozen with a Run. It contains only
// normalized materials and policy; model and tool execution state stays in
// Spec and the worker checkpoint.
type Snapshot struct {
	MemoryMode MemoryMode      `json:"memoryMode"`
	Materials  FrozenMaterials `json:"materials"`
	Policy     Policy          `json:"policy"`
	Budget     Budget          `json:"budget"`
}

// Envelope carries the versioned snapshot payload and its integrity digest.
// Execution stores the envelope without interpreting Material details.
type Envelope struct {
	Format        string          `json:"format"`
	PolicyVersion string          `json:"policyVersion"`
	Digest        string          `json:"digest"`
	Payload       json.RawMessage `json:"payload"`
}

// EncodeSnapshot validates and serializes one frozen context snapshot.
func EncodeSnapshot(snapshot Snapshot) (Envelope, error) {
	if err := validateSnapshot(snapshot); err != nil {
		return Envelope{}, err
	}
	payload, err := json.Marshal(snapshot)
	if err != nil {
		return Envelope{}, err
	}
	return Envelope{
		Format:        SnapshotFormatV1,
		PolicyVersion: snapshot.Policy.Version,
		Digest:        digest(payload),
		Payload:       payload,
	}, nil
}

// DecodeSnapshot verifies the envelope and returns the frozen snapshot.
func DecodeSnapshot(envelope Envelope) (Snapshot, error) {
	if envelope.Format != SnapshotFormatV1 {
		return Snapshot{}, ErrUnsupportedSnapshotFormat
	}
	if !json.Valid(envelope.Payload) || digest(envelope.Payload) != envelope.Digest {
		return Snapshot{}, ErrSnapshotDigest
	}
	var snapshot Snapshot
	if err := json.Unmarshal(envelope.Payload, &snapshot); err != nil {
		return Snapshot{}, ErrInvalidSnapshot
	}
	if snapshot.Policy.Version != envelope.PolicyVersion {
		return Snapshot{}, ErrInvalidSnapshot
	}
	if err := validateSnapshot(snapshot); err != nil {
		return Snapshot{}, err
	}
	return snapshot, nil
}

func validateSnapshot(snapshot Snapshot) error {
	if snapshot.Policy.Version != PolicyVersionV1 && snapshot.Policy.Version != PolicyVersionV2 {
		return ErrUnsupportedPolicy
	}
	if err := validateBudget(snapshot.Budget); err != nil {
		return err
	}
	switch snapshot.MemoryMode {
	case MemoryOff, MemoryEmpty:
		if len(snapshot.Materials.Memory) != 0 {
			return ErrInvalidSnapshot
		}
	case MemoryRecall:
		if len(snapshot.Materials.Memory) == 0 {
			return ErrInvalidSnapshot
		}
	default:
		return ErrInvalidSnapshot
	}
	return validateMaterials(snapshot.Materials, make(map[string]bool))
}

func digest(payload []byte) string {
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}
