// Package nodes is the App's governed node catalog (architecture
// §5.2, S09): pure value/template nodes, a human wait node, and an
// OpenAI-compatible model binding whose credential resolves only at
// effect time inside the App adapter — never in config, events, or
// checkpoints. Model effects are classified conservatively: an
// ambiguous timeout is outcome_unknown, never a silent retry.
package nodes

import (
	"context"
	"time"

	inofy "github.com/ProjectViVy/inofy"
	"github.com/ProjectViVy/inofy/apps/inofy/internal/storage"
)

// Connection is a host-approved provider binding. The secret lives
// behind an env name resolved through Secrets — the raw key never
// appears in the definition or the durable record.
type Connection struct {
	Kind      string        `json:"kind"`
	BaseURL   string        `json:"base_url"`
	Model     string        `json:"model"`
	SecretEnv string        `json:"secret_env"`
	Timeout   time.Duration `json:"timeout,omitempty"`
}

// Dependencies wire the App adapter: approved connections, a secret
// resolver, and the durable ledger consulted before effects.
type Dependencies struct {
	Connections *Registry
	Secrets     func(env string) (string, bool)
	Ledger      *storage.Store
}

// Executor implements inofy.NodeExecutor for every catalog type.
type Executor struct {
	d Dependencies
}

// New returns the App executor.
func New(d Dependencies) *Executor { return &Executor{d: d} }

// Execute dispatches on the node's type ID.
func (e *Executor) Execute(ctx context.Context, c inofy.NodeCall) (inofy.NodeReply, error) {
	// Ledger consult before any effect: a committed output for this
	// operation key replays instead of re-executing.
	if e.d.Ledger != nil && c.OperationKey != "" {
		if rep, ok, err := e.replay(ctx, c); err != nil {
			return inofy.NodeReply{}, err
		} else if ok {
			return rep, nil
		}
	}
	switch c.TypeID {
	case "inofy.value@1":
		return e.execValue(c)
	case "inofy.template@1":
		return e.execTemplate(c)
	case "inofy.model.openai@1":
		return e.execModel(ctx, c)
	case "inofy.wait@1":
		return e.execWait(c)
	default:
		return inofy.NodeReply{}, &inofy.Error{
			Code:    inofy.ErrUnsupportedFeature,
			Path:    c.Path,
			Message: "no app executor for node type " + c.TypeID,
		}
	}
}

// replay asks the committed ledger whether this operation already
// produced output — the run's effect boundary makes the same commit
// identity visible, so a repeat call must not repeat the effect.
func (e *Executor) replay(ctx context.Context, c inofy.NodeCall) (inofy.NodeReply, bool, error) {
	out, ok, err := e.d.Ledger.CommittedOutput(ctx, c.Ref.RunID, c.Path, c.Attempt)
	if err != nil || !ok {
		return inofy.NodeReply{}, ok, err
	}
	return inofy.NodeReply{Output: out}, true, nil
}

// Catalog returns the App's frozen node catalog with pinned
// implementation identities and replay classes (§5.2).
func Catalog(d Dependencies) inofy.Catalog {
	descs := []inofy.NodeDescriptor{
		{
			TypeID:           "inofy.value@1",
			ImplementationID: "inofy-app/value/go1",
			Replay:           inofy.ReplayPure,
			Display:          inofy.DisplayMeta{Title: "Value"},
		},
		{
			TypeID:           "inofy.template@1",
			ImplementationID: "inofy-app/template/go1",
			Replay:           inofy.ReplayPure,
			Display:          inofy.DisplayMeta{Title: "Template"},
		},
		{
			TypeID:           "inofy.model.openai@1",
			ImplementationID: "inofy-app/model/openai-einoext-v0.1.13",
			Replay:           inofy.ReplayNonReplayable,
			Display:          inofy.DisplayMeta{Title: "OpenAI-compatible model"},
		},
		{
			TypeID:           "inofy.wait@1",
			ImplementationID: "inofy-app/wait/go1",
			SupportsWait:     true,
			Replay:           inofy.ReplayPure,
			Display:          inofy.DisplayMeta{Title: "Human wait"},
		},
	}
	cat, err := inofy.NewCatalog(descs)
	if err != nil {
		panic("nodes.Catalog: " + err.Error())
	}
	return cat
}
