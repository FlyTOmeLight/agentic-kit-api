package durable

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	"github.com/FlyTOmeLight/agentic-kit-api/pkg/execution"
)

// validateContextSnapshot checks the opaque envelope boundaries. Payload
// format and material semantics belong to the context builder.
func validateContextSnapshot(snapshot execution.ContextSnapshot) error {
	if snapshot.Format == "" || snapshot.PolicyVersion == "" || snapshot.Digest == "" ||
		len(snapshot.Payload) == 0 || !json.Valid(snapshot.Payload) {
		return ErrInvalid
	}
	sum := sha256.Sum256(snapshot.Payload)
	if hex.EncodeToString(sum[:]) != snapshot.Digest {
		return ErrInvalid
	}
	return nil
}
