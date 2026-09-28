package nodes

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"

	inofy "github.com/ProjectViVy/inofy"
)

// execWait suspends the run on a durable human wait: the prompt is
// already redacted config data, the continuation ref is opaque and
// derived from the call identity (§8.3).
func (e *Executor) execWait(c inofy.NodeCall) (inofy.NodeReply, error) {
	var cfg struct {
		Prompt       string          `json:"prompt"`
		RequestID    string          `json:"request_id,omitempty"`
		Kind         string          `json:"kind,omitempty"`
		AnswerSchema json.RawMessage `json:"answer_schema,omitempty"`
	}
	if err := json.Unmarshal(c.Config, &cfg); err != nil {
		return inofy.NodeReply{}, &inofy.Error{Code: inofy.ErrInvalidDefinition, Path: c.Path, Err: err}
	}
	id := cfg.RequestID
	if id == "" {
		b := make([]byte, 8)
		_, _ = rand.Read(b)
		id = c.Path + "-" + hex.EncodeToString(b)
	}
	kind := cfg.Kind
	if kind == "" {
		kind = "human"
	}
	return inofy.NodeReply{Wait: &inofy.WaitRequest{
		RequestID:       id,
		Kind:            kind,
		Prompt:          cfg.Prompt,
		AnswerSchema:    cfg.AnswerSchema,
		ContinuationRef: c.OperationKey,
	}}, nil
}
