package nodes

import (
	"encoding/json"

	inofy "github.com/ProjectViVy/inofy"
)

// execValue echoes the configured JSON value as node output — a pure
// constant source for graph plumbing and tests.
func (e *Executor) execValue(c inofy.NodeCall) (inofy.NodeReply, error) {
	var cfg struct {
		Value json.RawMessage `json:"value"`
	}
	if err := json.Unmarshal(c.Config, &cfg); err != nil || len(cfg.Value) == 0 {
		return inofy.NodeReply{}, &inofy.Error{
			Code:    inofy.ErrInvalidDefinition,
			Path:    c.Path,
			Message: "value node requires config.value",
		}
	}
	return inofy.NodeReply{Output: cfg.Value}, nil
}
