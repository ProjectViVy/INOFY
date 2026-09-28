package nodes

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/url"
	"time"

	"github.com/cloudwego/eino-ext/components/model/openai"
	"github.com/cloudwego/eino/schema"

	inofy "github.com/ProjectViVy/inofy"
)

// modelConfig is the node-facing shape: an opaque approved
// connection ID plus the request messages. The credential never
// appears here — it resolves inside the adapter only.
type modelConfig struct {
	ConnectionID string          `json:"connection_id"`
	Messages     []modelMessage  `json:"messages"`
	MaxTokens    *int            `json:"max_tokens,omitempty"`
	Temperature  *float64        `json:"temperature,omitempty"`
	Extra        json.RawMessage `json:"extra,omitempty"`
}

type modelMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// execModel resolves the opaque connection, resolves the credential
// through the secret resolver, performs the completion via the
// pinned EinoExt openai component, and classifies the effect
// conservatively (§11.2: ambiguous transport outcome →
// outcome_unknown, never a blind retry).
func (e *Executor) execModel(ctx context.Context, c inofy.NodeCall) (inofy.NodeReply, error) {
	var cfg modelConfig
	if err := json.Unmarshal(c.Config, &cfg); err != nil || cfg.ConnectionID == "" {
		return inofy.NodeReply{}, &inofy.Error{
			Code:    inofy.ErrInvalidDefinition,
			Path:    c.Path,
			Message: "model node requires config.connection_id",
		}
	}
	conn, ok := e.d.Connections[cfg.ConnectionID]
	if !ok || conn.Kind != "openai" {
		return inofy.NodeReply{}, &inofy.Error{
			Code:    inofy.ErrInvalidDefinition,
			Path:    c.Path,
			Message: "unknown or unapproved connection",
		}
	}
	// Credential resolves inside the adapter right before the
	// effect — absence fails before any provider call and never
	// echoes the env name's value.
	key, ok := e.secret(conn.SecretEnv)
	if !ok {
		return inofy.NodeReply{}, &inofy.Error{
			Code:    inofy.ErrUnsupportedFeature,
			Path:    c.Path,
			Message: "connection credential unavailable",
		}
	}
	timeout := conn.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	eff, err := e.completion(ctx, conn, key, timeout, cfg)
	if err != nil {
		return inofy.NodeReply{}, classifyModelErr(c.Path, err)
	}
	return inofy.NodeReply{Output: eff}, nil
}

func (e *Executor) secret(env string) (string, bool) {
	if e.d.Secrets == nil || env == "" {
		return "", false
	}
	return e.d.Secrets(env)
}

// completion performs the chat completion and returns the output
// document {content, role, finish_reason}.
func (e *Executor) completion(ctx context.Context, conn Connection, apiKey string, timeout time.Duration, cfg modelConfig) (json.RawMessage, error) {
	m, err := openai.NewChatModel(ctx, &openai.ChatModelConfig{
		APIKey:  apiKey,
		BaseURL: conn.BaseURL,
		Model:   conn.Model,
		Timeout: timeout,
	})
	if err != nil {
		return nil, err
	}
	msgs := make([]*schema.Message, 0, len(cfg.Messages))
	for _, mm := range cfg.Messages {
		msgs = append(msgs, &schema.Message{Role: schema.RoleType(mm.Role), Content: mm.Content})
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	resp, err := m.Generate(cctx, msgs)
	if err != nil {
		return nil, err
	}
	out := map[string]any{
		"content":       resp.Content,
		"role":          string(resp.Role),
		"finish_reason": resp.ResponseMeta.FinishReason,
	}
	return json.Marshal(out)
}

// classifyModelErr maps transport outcomes honestly: an ambiguous
// timeout or reset is outcome_unknown — the provider may or may not
// have consumed the request (no idempotency on this API).
func classifyModelErr(path string, err error) error {
	var ne net.Error
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return &inofy.Error{Code: inofy.ErrOutcomeUnknown, Path: path,
			Message: "model call outcome unknown (deadline/cancel)", Err: err}
	}
	if errors.As(err, &ne) && ne.Timeout() {
		return &inofy.Error{Code: inofy.ErrOutcomeUnknown, Path: path,
			Message: "model call outcome unknown (transport timeout)", Err: err}
	}
	var ue *url.Error
	if errors.As(err, &ue) && (ue.Timeout() || errors.Is(ue.Err, context.DeadlineExceeded)) {
		return &inofy.Error{Code: inofy.ErrOutcomeUnknown, Path: path,
			Message: "model call outcome unknown (http timeout)", Err: err}
	}
	return &inofy.Error{Code: inofy.ErrNodeFailed, Path: path,
		Message: "model call failed", Err: err}
}
