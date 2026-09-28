// Strict artifact decoding and definition validation (architecture §4).
// The artifact envelope carries exactly a semantic Definition and a
// Presentation block; everything executable is rejected on unknown
// fields while presentation unknowns are preserved in place.
package inofy

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/ProjectViVy/inofy/internal/definition"
)

// Artifact is the editor/publishable envelope: the semantic definition
// plus presentation metadata that never enters execution.
type Artifact struct {
	Definition   Definition   `json:"definition"`
	Presentation Presentation `json:"presentation"`
}

// Presentation holds title, description and layout. Unknown fields are
// preserved verbatim inside Extra; executable data is never allowed
// here (the decoder rejects executable shapes).
type Presentation struct {
	Title       string                     `json:"title,omitempty"`
	Description string                     `json:"description,omitempty"`
	Layout      Layout                     `json:"layout,omitempty"`
	Extra       map[string]json.RawMessage `json:"-"`
}

var presentationFields = map[string]bool{"title": true, "description": true, "layout": true}

// UnmarshalJSON splits known presentation fields from preserved extras.
func (p *Presentation) UnmarshalJSON(data []byte) error {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(data, &m); err != nil {
		return err
	}
	extra := map[string]json.RawMessage{}
	for k, v := range m {
		switch {
		case k == "title":
			if err := json.Unmarshal(v, &p.Title); err != nil {
				return err
			}
		case k == "description":
			if err := json.Unmarshal(v, &p.Description); err != nil {
				return err
			}
		case k == "layout":
			if err := json.Unmarshal(v, &p.Layout); err != nil {
				return err
			}
		default:
			extra[k] = v
		}
	}
	if len(extra) > 0 {
		p.Extra = extra
	}
	return nil
}

// MarshalJSON merges known fields with preserved extras.
func (p Presentation) MarshalJSON() ([]byte, error) {
	m := map[string]json.RawMessage{}
	for k, v := range p.Extra {
		if presentationFields[k] {
			return nil, fmt.Errorf("presentation extra collides with field %q", k)
		}
		m[k] = v
	}
	put := func(k string, v any, empty bool) error {
		if empty {
			return nil
		}
		b, err := json.Marshal(v)
		if err != nil {
			return err
		}
		m[k] = b
		return nil
	}
	if err := put("title", p.Title, p.Title == ""); err != nil {
		return nil, err
	}
	if err := put("description", p.Description, p.Description == ""); err != nil {
		return nil, err
	}
	layoutEmpty := len(p.Layout.Positions) == 0 && p.Layout.Viewport == nil &&
		len(p.Layout.Collapsed) == 0 && len(p.Layout.Extra) == 0
	if err := put("layout", p.Layout, layoutEmpty); err != nil {
		return nil, err
	}
	return json.Marshal(m)
}

// Layout positions nodes and viewport state for the editor.
type Layout struct {
	Positions map[string]LayoutPoint     `json:"positions,omitempty"`
	Viewport  *Viewport                  `json:"viewport,omitempty"`
	Collapsed []string                   `json:"collapsed,omitempty"`
	Extra     map[string]json.RawMessage `json:"-"`
}

// LayoutPoint is one node's canvas position.
type LayoutPoint struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
}

// Viewport stores canvas pan/zoom.
type Viewport struct {
	X    float64 `json:"x"`
	Y    float64 `json:"y"`
	Zoom float64 `json:"zoom"`
}

var layoutFields = map[string]bool{"positions": true, "viewport": true, "collapsed": true}

// UnmarshalJSON preserves unknown layout fields.
func (l *Layout) UnmarshalJSON(data []byte) error {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(data, &m); err != nil {
		return err
	}
	extra := map[string]json.RawMessage{}
	for k, v := range m {
		switch {
		case k == "positions":
			if err := json.Unmarshal(v, &l.Positions); err != nil {
				return err
			}
		case k == "viewport":
			if err := json.Unmarshal(v, &l.Viewport); err != nil {
				return err
			}
		case k == "collapsed":
			if err := json.Unmarshal(v, &l.Collapsed); err != nil {
				return err
			}
		default:
			extra[k] = v
		}
	}
	if len(extra) > 0 {
		l.Extra = extra
	}
	return nil
}

// MarshalJSON merges known layout fields with preserved extras.
func (l Layout) MarshalJSON() ([]byte, error) {
	m := map[string]json.RawMessage{}
	for k, v := range l.Extra {
		if layoutFields[k] {
			return nil, fmt.Errorf("layout extra collides with field %q", k)
		}
		m[k] = v
	}
	put := func(k string, v any, empty bool) error {
		if empty {
			return nil
		}
		b, err := json.Marshal(v)
		if err != nil {
			return err
		}
		m[k] = b
		return nil
	}
	if err := put("positions", l.Positions, len(l.Positions) == 0); err != nil {
		return nil, err
	}
	if err := put("viewport", l.Viewport, l.Viewport == nil); err != nil {
		return nil, err
	}
	if err := put("collapsed", l.Collapsed, len(l.Collapsed) == 0); err != nil {
		return nil, err
	}
	return json.Marshal(m)
}

// DecodeArtifact strictly decodes one artifact envelope. Every finding
// rejects the artifact; diagnostics carry authored JSON-Pointer paths.
func DecodeArtifact(data []byte) (Artifact, []Diagnostic, error) {
	doc, findings, err := definition.DecodeArtifactDoc(data)
	diags := toDiagnostics(findings)
	if err != nil {
		return Artifact{}, diags, decodeError(err, diags)
	}

	var out Artifact
	defBytes, err := json.Marshal(doc.Definition)
	if err != nil {
		return Artifact{}, diags, &Error{Code: ErrInvalidDefinition, Message: "definition re-encode: " + err.Error()}
	}
	if err := strictUnmarshal(defBytes, &out.Definition); err != nil {
		return Artifact{}, diags, &Error{Code: ErrInvalidDefinition, Message: "definition decode: " + err.Error()}
	}
	if doc.Presentation != nil {
		presBytes, err := json.Marshal(doc.Presentation)
		if err != nil {
			return Artifact{}, diags, &Error{Code: ErrInvalidDefinition, Message: "presentation re-encode: " + err.Error()}
		}
		if err := json.Unmarshal(presBytes, &out.Presentation); err != nil {
			return Artifact{}, diags, &Error{Code: ErrInvalidDefinition, Message: "presentation decode: " + err.Error()}
		}
	}
	return out, nil, nil
}

func decodeError(err error, diags []Diagnostic) error {
	path := ""
	if len(diags) > 0 {
		path = diags[0].Path
	}
	return &Error{Code: ErrInvalidDefinition, Message: err.Error(), Path: path}
}

func strictUnmarshal(data []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	return dec.Decode(v)
}

func toDiagnostics(fs []definition.Finding) []Diagnostic {
	if len(fs) == 0 {
		return nil
	}
	out := make([]Diagnostic, 0, len(fs))
	for _, f := range fs {
		out = append(out, Diagnostic{
			Check:   CheckKind(f.Check),
			Path:    f.Path,
			Code:    f.Code,
			Message: f.Message,
		})
	}
	return out
}

// ValidateDefinition runs the semantic checks that need the frozen
// catalog plus compile options: capability, budget and (with S02 T3)
// topology admission. Authority stays host-owned and is never emitted.
func ValidateDefinition(def Definition, catalog Catalog, opts CompileOptions) []Diagnostic {
	doc, err := defToDoc(def)
	if err != nil {
		return []Diagnostic{{
			Check: CheckSchema, Code: "encode_failed",
			Message: "definition cannot be encoded for validation: " + err.Error(),
		}}
	}
	types := make([]definition.TypeRef, 0, len(catalog.Types()))
	for _, id := range catalog.Types() {
		d, ok := catalog.Lookup(id)
		if !ok {
			continue
		}
		types = append(types, definition.TypeRef{
			TypeID:           d.TypeID,
			ImplementationID: d.ImplementationID,
			ConfigSchema:     d.ConfigSchema,
			InputSchema:      d.InputSchema,
			OutputSchema:     d.OutputSchema,
			Capabilities:     d.Capabilities,
			Replay:           string(d.Replay),
			SupportsWait:     d.SupportsWait,
		})
	}
	lim := effectiveLimits(opts.Limits)
	var findings []definition.Finding
	findings = append(findings, definition.ValidateSemanticDoc(doc, types, opts.Features)...)
	findings = append(findings, definition.ValidateTopologyDoc(doc, definition.LimitsRef{
		MaxNodes:           lim.MaxNodes,
		MaxEdges:           lim.MaxEdges,
		MaxRepeatNesting:   lim.MaxRepeatNesting,
		MaxIterations:      lim.MaxIterations,
		MaxActivations:     lim.MaxActivations,
		MaxAttemptsPerCall: lim.MaxAttemptsPerCall,
		MaxPredicateDepth:  lim.MaxPredicateDepth,
		Parallelism:        lim.Parallelism,
		MaxDefinitionBytes: lim.MaxDefinitionBytes,
	})...)
	return toDiagnostics(findings)
}

// defToDoc re-encodes a typed definition into the generic document the
// internal validators consume.
func defToDoc(def Definition) (map[string]any, error) {
	raw, err := json.Marshal(def)
	if err != nil {
		return nil, err
	}
	var doc map[string]any
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&doc); err != nil {
		return nil, err
	}
	return doc, nil
}

// effectiveLimits fills zero-valued compile ceilings with the
// standalone defaults from architecture §7.4.
func effectiveLimits(l Limits) Limits {
	d := defaultLimits()
	out := l
	out.MaxNodes = orPos(out.MaxNodes, d.MaxNodes)
	out.MaxEdges = orPos(out.MaxEdges, d.MaxEdges)
	out.MaxRepeatNesting = orPos(out.MaxRepeatNesting, d.MaxRepeatNesting)
	out.MaxIterations = orPos(out.MaxIterations, d.MaxIterations)
	out.MaxActivations = orPos(out.MaxActivations, d.MaxActivations)
	out.MaxAttemptsPerCall = orPos(out.MaxAttemptsPerCall, d.MaxAttemptsPerCall)
	out.MaxPredicateDepth = orPos(out.MaxPredicateDepth, d.MaxPredicateDepth)
	out.MaxDefinitionBytes = orPos64(out.MaxDefinitionBytes, d.MaxDefinitionBytes)
	out.MaxNodeInputBytes = orPos64(out.MaxNodeInputBytes, d.MaxNodeInputBytes)
	out.MaxNodeOutputBytes = orPos64(out.MaxNodeOutputBytes, d.MaxNodeOutputBytes)
	out.MaxOutputBytesTotal = orPos64(out.MaxOutputBytesTotal, d.MaxOutputBytesTotal)
	out.MaxCheckpointBytes = orPos64(out.MaxCheckpointBytes, d.MaxCheckpointBytes)
	out.Parallelism = orPos(out.Parallelism, d.Parallelism)
	out.MaxConcurrentRuns = orPos(out.MaxConcurrentRuns, d.MaxConcurrentRuns)
	out.MaxPendingAdmissions = orPos(out.MaxPendingAdmissions, d.MaxPendingAdmissions)
	out.MaxPredicateDepth = orPos(out.MaxPredicateDepth, d.MaxPredicateDepth)
	return out
}

func orPos(v, d int) int {
	if v <= 0 {
		return d
	}
	return v
}

func orPos64(v, d int64) int64 {
	if v <= 0 {
		return d
	}
	return v
}

// defaultLimits returns the proposed standalone ceilings (§7.4).
func defaultLimits() Limits {
	const mib = 1024 * 1024
	return Limits{
		MaxNodes:             64,
		MaxEdges:             128,
		Parallelism:          4,
		MaxRepeatNesting:     1,
		MaxIterations:        8,
		MaxActivations:       256,
		MaxAttemptsPerCall:   3,
		NodeTimeoutMS:        60_000,
		RunTimeoutMS:         600_000,
		MaxDefinitionBytes:   mib,
		MaxNodeInputBytes:    mib,
		MaxNodeOutputBytes:   mib,
		MaxOutputBytesTotal:  16 * mib,
		MaxCheckpointBytes:   16 * mib,
		MaxPredicateDepth:    8,
		MaxConcurrentRuns:    4,
		MaxPendingAdmissions: 32,
	}
}
