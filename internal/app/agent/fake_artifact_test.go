package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"sync"
	"sync/atomic"

	"github.com/FlyTOmeLight/agentic-kit-api/internal/client"
	"github.com/FlyTOmeLight/agentic-kit-api/pkg/agent"
)

// fakeArtifactStore mirrors the idempotent request-key behavior of
// artifact-go so the service tests exercise real service code.
type fakeArtifactStore struct {
	mu        sync.Mutex
	ensures   int
	publishes int
	artifacts map[string]string // collection key → artifact ID
	versions  map[string]fakeVersion
	byID      map[string]fakeVersion

	failPublish atomic.Bool
}

type fakeVersion struct {
	key     string
	payload []byte
}

func newFakeArtifactStore() *fakeArtifactStore {
	return &fakeArtifactStore{
		artifacts: map[string]string{},
		versions:  map[string]fakeVersion{},
		byID:      map[string]fakeVersion{},
	}
}

func (f *fakeArtifactStore) EnsureCollection(
	_ context.Context,
	identity client.Identity,
	agentID string,
) (string, error) {
	if identity.TenantID == "" {
		return "", agent.ErrUnavailable
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ensures++
	key := digest(agentID)
	if id, ok := f.artifacts[key]; ok {
		return id, nil
	}
	id := "artifact-" + key[:8]
	f.artifacts[key] = id
	return id, nil
}

func (f *fakeArtifactStore) PublishVersion(
	_ context.Context,
	identity client.Identity,
	artifactID, requestKey string,
	payload []byte,
) (client.VersionRef, error) {
	if identity.TenantID == "" {
		return client.VersionRef{}, agent.ErrUnavailable
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.publishes++
	if f.failPublish.Load() {
		return client.VersionRef{}, agent.ErrUnavailable
	}
	key := artifactID + "/" + requestKey
	if existing, ok := f.versions[key]; ok {
		if string(existing.payload) != string(payload) {
			return client.VersionRef{}, agent.ErrConflict
		}
		return client.VersionRef{ID: "version-" + requestKey[:8], URI: "artifact://fake/" + requestKey[:8]}, nil
	}
	id := "version-" + requestKey[:8]
	f.versions[key] = fakeVersion{key: requestKey, payload: append([]byte(nil), payload...)}
	f.byID[id] = f.versions[key]
	return client.VersionRef{ID: id, URI: "artifact://fake/" + requestKey[:8]}, nil
}

func (f *fakeArtifactStore) GetPayload(
	_ context.Context,
	identity client.Identity,
	_, versionID string,
) ([]byte, error) {
	if identity.TenantID == "" {
		return nil, agent.ErrUnavailable
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	version, ok := f.byID[versionID]
	if !ok {
		return nil, agent.ErrNotFound
	}
	return append([]byte(nil), version.payload...), nil
}

func (f *fakeArtifactStore) Close() error { return nil }

// payloadFor returns the body stored under a publish request key.
func (f *fakeArtifactStore) payloadFor(artifactID, requestKey string) ([]byte, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	version, ok := f.versions[artifactID+"/"+requestKey]
	return version.payload, ok
}

func digest(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func (*fakeArtifactStore) ResolveAttachment(
	context.Context,
	client.Identity,
	string,
) (client.ResolvedAttachment, error) {
	return client.ResolvedAttachment{}, agent.ErrUnavailable
}

func (*fakeArtifactStore) ResolveSkill(
	context.Context,
	client.Identity,
	string,
	bool,
) (client.ResolvedSkill, error) {
	return client.ResolvedSkill{}, agent.ErrUnavailable
}

func (*fakeArtifactStore) ReadSkillFile(
	context.Context,
	client.Identity,
	bool,
	string,
	int64,
) ([]byte, error) {
	return nil, agent.ErrUnavailable
}
