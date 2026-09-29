package einoruntime_test

import (
	"context"
	"encoding/json"

	"github.com/ProjectViVy/inofy/internal/definition"
	"github.com/ProjectViVy/inofy/internal/einoruntime"
)

// invoke3 keeps pre-S06 test call sites readable: output, diagnostics
// and error without suspension plumbing.
func invoke3(p *einoruntime.Program, ctx context.Context, input json.RawMessage, exec einoruntime.Executor) (json.RawMessage, []definition.Finding, error) {
	res, err := p.Invoke(ctx, input, exec, einoruntime.RunOptions{})
	return res.Output, res.Diags, err
}
