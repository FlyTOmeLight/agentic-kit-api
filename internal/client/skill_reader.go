package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/FlyTOmeLight/artifact-go/sdk/go/artifactv1"
	"github.com/FlyTOmeLight/artifact-go/sdk/go/storagev1"
	"github.com/FlyTOmeLight/server-go/constants"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/FlyTOmeLight/agentic-kit-api/pkg/agent"
	"github.com/FlyTOmeLight/agentic-kit-api/pkg/memory"
)

const (
	skillArtifactType        = "agent-skill"
	skillGrantTTLSeconds     = int64(60)
	maxSkillGrantBodyBytes   = 1 << 20
	defaultSkillReadMaxBytes = 128 << 10
)

func (c *artifactClient) ResolveSkill(
	ctx context.Context,
	identity Identity,
	artifactURI string,
	shared bool,
) (ResolvedSkill, error) {
	if err := validateIdentity(identity, artifactURI); err != nil {
		return ResolvedSkill{}, err
	}
	resolved, err := c.sdk.Resolve(
		c.withScopeIdentity(ctx, identity, shared),
		&artifactv1.ResolveRequest{URI: artifactURI},
	)
	if err != nil {
		return ResolvedSkill{}, mapError(ctx, err)
	}
	if resolved.Artifact.Type != skillArtifactType {
		return ResolvedSkill{}, memory.ErrForbidden
	}
	if resolved.Version == nil || resolved.Version.VersionURI == "" {
		return ResolvedSkill{}, agent.ErrInvalid
	}
	if resolved.Version.State != artifactv1.VersionReady {
		return ResolvedSkill{}, agent.ErrConflict
	}
	var payloadMeta struct {
		Description string `json:"description"`
	}
	if len(resolved.Version.Payload) > 0 {
		_ = json.Unmarshal(resolved.Version.Payload, &payloadMeta)
	}
	files := make([]ResolvedSkillFile, 0, len(resolved.Version.Files))
	for _, f := range resolved.Version.Files {
		files = append(files, ResolvedSkillFile{
			Path:      f.Path,
			FileID:    f.FileID,
			FileURI:   f.FileURI,
			MediaType: f.ContentType,
			SizeBytes: f.Size,
		})
	}
	return ResolvedSkill{
		VersionURI:  resolved.Version.VersionURI,
		Description: strings.TrimSpace(payloadMeta.Description),
		Files:       files,
	}, nil
}

func (c *artifactClient) ReadSkillFile(
	ctx context.Context,
	identity Identity,
	shared bool,
	fileID string,
	maxBytes int64,
) ([]byte, error) {
	if err := validateIdentity(identity, fileID); err != nil {
		return nil, err
	}
	if maxBytes <= 0 {
		maxBytes = defaultSkillReadMaxBytes
	}
	cacheKey := skillCacheKey(identity, shared, fileID)
	if cached, ok := c.skillCache.get(cacheKey); ok {
		if int64(len(cached)) > maxBytes {
			return nil, fmt.Errorf("%w: skill file exceeds %d bytes", agent.ErrInvalid, maxBytes)
		}
		return cached, nil
	}
	grant, err := c.requestDownloadGrant(ctx, identity, shared, fileID)
	if err != nil {
		return nil, err
	}
	content, err := c.downloadSkillObject(ctx, grant, fileID, maxBytes)
	if err != nil {
		return nil, err
	}
	c.skillCache.put(cacheKey, content)
	return content, nil
}

func (c *artifactClient) requestDownloadGrant(
	ctx context.Context,
	identity Identity,
	shared bool,
	fileID string,
) (*storagev1.GrantResponse, error) {
	ttl := skillGrantTTLSeconds
	body, err := json.Marshal(storagev1.CreateGrantRequest{
		FileIDs:    []string{fileID},
		Scope:      storagev1.GrantScope{Preset: "download"},
		TTLSeconds: &ttl,
	})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		c.endpoint+"/v1/storage/grants",
		bytes.NewReader(body),
	)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", agent.ErrUnavailable, err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.serviceAPIKey)
	req.Header.Set(constants.HeaderTenantID, identity.TenantID)
	req.Header.Set(constants.HeaderImpersonateTenantID, identity.TenantID)
	req.Header.Set(constants.HeaderImpersonateUserID, identity.PropagatedUserID)
	if identity.ProjectID != nil && *identity.ProjectID != "" {
		req.Header.Set(constants.HeaderProjectID, *identity.ProjectID)
		req.Header.Set(constants.HeaderImpersonateProjectID, *identity.ProjectID)
	}
	if !shared {
		req.Header.Set("X-Resource-User-ID", identity.PropagatedUserID)
	}
	if identity.RequestID != "" {
		req.Header.Set("X-Request-ID", identity.RequestID)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("%w: artifact storage grant failed", agent.ErrUnavailable)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxSkillGrantBodyBytes+1))
	if err != nil {
		return nil, fmt.Errorf("%w: read storage grant response failed", agent.ErrUnavailable)
	}
	if len(raw) > maxSkillGrantBodyBytes {
		return nil, fmt.Errorf("%w: storage grant response too large", agent.ErrUnavailable)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, mapGrantHTTPStatus(resp.StatusCode)
	}
	var grant storagev1.GrantResponse
	if err = json.Unmarshal(raw, &grant); err != nil {
		return nil, fmt.Errorf("%w: decode storage grant failed", agent.ErrUnavailable)
	}
	if len(grant.Files) != 1 || grant.Files[0].FileID != fileID ||
		grant.Files[0].Bucket == "" || grant.Files[0].ObjectKey == "" ||
		!validGrantEndpoint(grant.Endpoint) ||
		grant.Credentials.AccessKeyID == "" || grant.Credentials.SecretAccessKey == "" {
		return nil, fmt.Errorf("%w: invalid storage grant response", agent.ErrUnavailable)
	}
	return &grant, nil
}

func (c *artifactClient) downloadSkillObject(
	ctx context.Context,
	grant *storagev1.GrantResponse,
	fileID string,
	maxBytes int64,
) ([]byte, error) {
	region := grant.Region
	if region == "" {
		region = "us-east-1"
	}
	endpoint := grant.Endpoint
	s3Client := s3.New(s3.Options{
		Region:       region,
		BaseEndpoint: aws.String(endpoint),
		UsePathStyle: grant.ForcePathStyle,
		Credentials: aws.NewCredentialsCache(credentials.NewStaticCredentialsProvider(
			grant.Credentials.AccessKeyID,
			grant.Credentials.SecretAccessKey,
			grant.Credentials.SessionToken,
		)),
		HTTPClient: c.httpClient,
	})
	target := grant.Files[0]
	out, err := s3Client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(target.Bucket),
		Key:    aws.String(target.ObjectKey),
	})
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		// Never echo grant credentials or raw upstream details.
		return nil, fmt.Errorf("%w: skill file %s download failed", agent.ErrUnavailable, fileID)
	}
	defer out.Body.Close()
	data, err := io.ReadAll(io.LimitReader(out.Body, maxBytes+1))
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("%w: read skill file %s failed", agent.ErrUnavailable, fileID)
	}
	if int64(len(data)) > maxBytes {
		return nil, fmt.Errorf("%w: skill file exceeds %d bytes", agent.ErrInvalid, maxBytes)
	}
	if !utf8.Valid(data) || bytes.IndexByte(data, 0) >= 0 {
		return nil, fmt.Errorf("%w: skill file is not valid UTF-8 text", agent.ErrInvalid)
	}
	return data, nil
}

func validGrantEndpoint(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && u.Host != "" && (u.Scheme == "http" || u.Scheme == "https") &&
		u.User == nil && u.Fragment == ""
}

func mapGrantHTTPStatus(status int) error {
	switch status {
	case http.StatusBadRequest, http.StatusUnprocessableEntity:
		return agent.ErrInvalid
	case http.StatusForbidden:
		return memory.ErrForbidden
	case http.StatusNotFound, http.StatusGone:
		return agent.ErrNotFound
	case http.StatusConflict:
		return agent.ErrConflict
	default:
		return agent.ErrUnavailable
	}
}

func skillCacheKey(identity Identity, shared bool, fileID string) string {
	project := ""
	if identity.ProjectID != nil {
		project = *identity.ProjectID
	}
	resourceUser := ""
	if !shared {
		resourceUser = identity.PropagatedUserID
	}
	return identity.TenantID + "\x00" + project + "\x00" + resourceUser + "\x00" + fileID
}

// skillFileCache is a small bounded FIFO cache for immutable skill file contents.
type skillFileCache struct {
	mu      sync.Mutex
	max     int
	order   []string
	entries map[string][]byte
}

func newSkillFileCache(maxEntries int) *skillFileCache {
	return &skillFileCache{
		max:     maxEntries,
		entries: make(map[string][]byte, maxEntries),
	}
}

func (c *skillFileCache) get(key string) ([]byte, bool) {
	if c == nil {
		return nil, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	v, ok := c.entries[key]
	if !ok {
		return nil, false
	}
	return append([]byte(nil), v...), true
}

func (c *skillFileCache) put(key string, data []byte) {
	if c == nil || c.max <= 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, exists := c.entries[key]; exists {
		c.entries[key] = append([]byte(nil), data...)
		return
	}
	if len(c.order) >= c.max {
		oldest := c.order[0]
		c.order = c.order[1:]
		delete(c.entries, oldest)
	}
	c.order = append(c.order, key)
	c.entries[key] = append([]byte(nil), data...)
}
