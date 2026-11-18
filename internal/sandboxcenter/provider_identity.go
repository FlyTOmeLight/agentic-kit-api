package sandboxcenter

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"
)

// destroyObject deletes one Kubernetes object only while its UID still matches.
// A missing object is already destroyed; a replacement with the same name is
// never deleted.
func (p *Provider) destroyObject(ctx context.Context, kind, name, uid string) error {
	if strings.TrimSpace(uid) == "" {
		return fmt.Errorf("%w: object UID is required", ErrInvalidInput)
	}
	if name == "" {
		return fmt.Errorf("%w: object name is required", ErrInvalidInput)
	}
	if p.cfg.Namespace == "" {
		return fmt.Errorf("%w: namespace is required", ErrInvalidInput)
	}
	var resource string
	switch kind {
	case "pods":
		resource = "pods"
	case "persistentvolumeclaims":
		resource = "persistentvolumeclaims"
	default:
		return fmt.Errorf("%w: unsupported object kind %q", ErrInvalidInput, kind)
	}

	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()

	currentUID, err := p.objectUID(ctx, resource, name)
	if err != nil {
		return err
	}
	if currentUID == "" {
		return nil
	}
	if currentUID != uid {
		return fmt.Errorf("%w: %s %s has UID %s, want %s", ErrConflict, kind, name, currentUID, uid)
	}

	options := struct {
		APIVersion    string `json:"apiVersion"`
		Kind          string `json:"kind"`
		Preconditions struct {
			UID string `json:"uid"`
		} `json:"preconditions"`
	}{APIVersion: "v1", Kind: "DeleteOptions"}
	options.Preconditions.UID = uid
	body, err := json.Marshal(options)
	if err != nil {
		return err
	}
	path := "/api/v1/namespaces/" + url.PathEscape(p.cfg.Namespace) + "/" + resource + "/" + url.PathEscape(name)
	if _, err := p.kubectl(ctx, body, "delete", "--raw", path, "-f", "-"); err != nil {
		currentUID, checkErr := p.objectUID(ctx, resource, name)
		if checkErr != nil {
			return err
		}
		if currentUID == "" {
			return nil
		}
		if currentUID != uid {
			return fmt.Errorf("%w: %s %s was replaced during deletion", ErrConflict, kind, name)
		}
		return err
	}

	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	for {
		currentUID, err := p.objectUID(ctx, resource, name)
		if err != nil {
			return err
		}
		if currentUID == "" {
			return nil
		}
		if currentUID != uid {
			return fmt.Errorf("%w: %s %s was replaced during deletion", ErrConflict, kind, name)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func (p *Provider) objectUID(ctx context.Context, resource, name string) (string, error) {
	out, err := p.kubectl(ctx, nil, "get", resource, name, "--ignore-not-found", "-o", "jsonpath={.metadata.uid}")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}
