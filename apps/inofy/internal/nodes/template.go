package nodes

import (
	"bytes"
	"encoding/json"
	"text/template"

	inofy "github.com/ProjectViVy/inofy"
)

// execTemplate renders config.template (Go text/template) against
// the resolved input object into config.output_key — pure.
func (e *Executor) execTemplate(c inofy.NodeCall) (inofy.NodeReply, error) {
	var cfg struct {
		Template  string `json:"template"`
		OutputKey string `json:"output_key"`
	}
	if err := json.Unmarshal(c.Config, &cfg); err != nil || cfg.Template == "" || cfg.OutputKey == "" {
		return inofy.NodeReply{}, &inofy.Error{
			Code:    inofy.ErrInvalidDefinition,
			Path:    c.Path,
			Message: "template node requires config.template and config.output_key",
		}
	}
	tpl, err := template.New("node").Option("missingkey=error").Parse(cfg.Template)
	if err != nil {
		return inofy.NodeReply{}, &inofy.Error{
			Code:    inofy.ErrInvalidDefinition,
			Path:    c.Path,
			Message: "bad template: " + err.Error(),
		}
	}
	var data any
	if len(c.Input) > 0 {
		if err := json.Unmarshal(c.Input, &data); err != nil {
			return inofy.NodeReply{}, &inofy.Error{Code: inofy.ErrSchemaMismatch, Path: c.Path, Err: err}
		}
	}
	var buf bytes.Buffer
	if err := tpl.Execute(&buf, data); err != nil {
		return inofy.NodeReply{}, &inofy.Error{Code: inofy.ErrSchemaMismatch, Path: c.Path, Err: err}
	}
	out, _ := json.Marshal(map[string]string{cfg.OutputKey: buf.String()})
	return inofy.NodeReply{Output: out}, nil
}
