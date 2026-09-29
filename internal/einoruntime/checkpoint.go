// Checkpoint staging and interruption plumbing (architecture §8.3):
// CheckPointStore.Set buffers bytes per run checkpoint ID; only the
// host's atomic waiting commit may expose them. SuspendRequest /
// GateSuspend are executor-side signals the call lambda translates
// into Eino stateful interrupts.
package einoruntime

import (
	"context"
	"sync"

	"github.com/cloudwego/eino/compose"
)

// StagingStore buffers Eino checkpoint bytes per checkpoint ID inside
// this process. It implements compose.CheckPointStore; nothing it
// holds is durable evidence — durability comes only from a host
// RunCommit carrying the envelope.
type StagingStore struct {
	mu   sync.Mutex
	data map[string][]byte
}

func NewStagingStore() *StagingStore {
	return &StagingStore{data: map[string][]byte{}}
}

func (s *StagingStore) Get(_ context.Context, id string) ([]byte, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, ok := s.data[id]
	return b, ok, nil
}

func (s *StagingStore) Set(_ context.Context, id string, cp []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data[id] = append([]byte(nil), cp...)
	return nil
}

// Stage seeds checkpoint bytes for a resume (process reopen): the
// committed envelope payload is re-staged under the same ID so Eino
// reloads the exact committed checkpoint.
func (s *StagingStore) Stage(id string, cp []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data[id] = append([]byte(nil), cp...)
}

// Staged returns the bytes buffered under id, or nil.
func (s *StagingStore) Staged(id string) []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]byte(nil), s.data[id]...)
}

// Drop discards a staged buffer once the host no longer needs it.
func (s *StagingStore) Drop(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.data, id)
}

// SuspendRequest is the typed signal executeCall returns when a node
// produced a WaitRequest: the lambda must translate it into an Eino
// stateful interrupt carrying only the wait's opaque identity.
type SuspendRequest struct {
	RequestID       string
	ContinuationRef string
}

func (e *SuspendRequest) Error() string { return "inofy wait requested" }

// GateSuspend is returned when a run's suspension flag is already set
// and this activation must not start an effect. The lambda converts it
// into an internal continuation interrupt that is resumed together
// with the authorized human waits and never appears as a prompt.
type GateSuspend struct {
	Path string
}

func (e *GateSuspend) Error() string { return "inofy effect gate closed" }

// InterruptKind markers inside interrupt Info payloads.
const (
	InterruptWait = "inofy_wait"
	InterruptGate = "inofy_gate"
)

// InterruptPoint is a neutral view of one root-cause Eino interrupt:
// resume identity, hierarchical address and our marker payload.
type InterruptPoint struct {
	ID      string
	Address string
	Kind    string
	Ref     string // request_id (wait) or node path (gate)
}

// SuspendView is what Invoke reports when the graph suspended: the
// root-cause interruptions and nothing else — settling details live in
// the run journal.
type SuspendView struct {
	Points []InterruptPoint
}

// extractSuspension flattens the interrupt tree to deduplicated
// root-cause points (S01 G4 evidence: IDs re-mint per suspend,
// Address is the stable identity).
func extractSuspension(err error) *SuspendView {
	info, ok := compose.ExtractInterruptInfo(err)
	if !ok || info == nil {
		return nil
	}
	seen := map[string]InterruptPoint{}
	var walk func(ii *compose.InterruptInfo)
	walk = func(ii *compose.InterruptInfo) {
		for _, c := range ii.InterruptContexts {
			if !c.IsRootCause {
				continue
			}
			pt := InterruptPoint{ID: c.ID, Address: c.Address.String()}
			if m, okm := c.Info.(map[string]any); okm {
				pt.Kind, _ = m["kind"].(string)
				pt.Ref, _ = m["ref"].(string)
			}
			seen[c.ID] = pt
		}
		for _, sub := range ii.SubGraphs {
			walk(sub)
		}
	}
	walk(info)
	out := &SuspendView{}
	for _, pt := range seen {
		out.Points = append(out.Points, pt)
	}
	return out
}
