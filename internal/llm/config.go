package llm

import (
	"fmt"
	"net/url"
)

// Which model client a command builds
type Backend string

const (
	BackendClaude Backend = "claude" // `claude -p` and the default when the config names none
	BackendChat   Backend = "chat"   // an OpenAI compatible Chat Completions endpoint
)

// The llm key of config.json
type Config struct {
	Backend Backend `json:"backend,omitempty"`
	// The URL `/chat/completions` is joined to
	// Required for chat
	BaseURL string `json:"base_url,omitempty"`
	// The default model of the backend
	Model string `json:"model,omitempty"`
	// The env variable that holds the Bearer key when the client is built
	// The key itself is never saved
	KeyEnv string `json:"key_env,omitempty"`
}

// An absent backend reads as claude
// 1. a chat backend needs a model since an endpoint has no default of its own
// 2. a base URL with a user or a password is refused since config.json and config list would keep it in the open
func (c Config) Check() error {
	switch c.Backend {
	case "", BackendClaude:
		return nil
	case BackendChat:
		u, err := url.Parse(c.BaseURL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return fmt.Errorf("%w: %q", ErrBaseURLInvalid, c.BaseURL)
		}
		if u.User != nil {
			return ErrBaseURLCredentials
		}
		if c.Model == "" {
			return ErrModelRequired
		}
		return nil
	}
	return fmt.Errorf("%w: %q. Use claude or chat", ErrBackendUnknown, c.Backend)
}
