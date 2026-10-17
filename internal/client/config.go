package client

import (
	"errors"
	"fmt"

	framesdk "github.com/FlyTOmeLight/server-go/sdk"
)

// The published Forge SDK constructors do not yet accept the full framework
// config. Reject unsupported settings rather than silently ignoring them.
func serviceToken(config framesdk.Config) (string, error) {
	if config.Retry != 0 {
		return "", errors.New("published Forge SDKs do not support Retry configuration")
	}
	if config.Timeout < 0 {
		return "", errors.New("SDK timeout must not be negative")
	}
	if config.Credentials == nil {
		return "", nil
	}
	if config.Credentials.AuthType != framesdk.AuthTypeAPIKey {
		return "", fmt.Errorf("%w: %q", framesdk.ErrUnsupportedAuth, config.Credentials.AuthType)
	}
	if config.Credentials.APIKey == "" {
		return "", errors.New("SDK API key must not be empty")
	}
	return config.Credentials.APIKey, nil
}
