package inofy

import (
	"encoding/json"

	"github.com/ProjectViVy/inofy/internal/definition"
)

// DefinitionSchema returns a defensive JSON Schema projection of the
// executable Definition grammar. It helps clients author documents; callers
// must still use DecodeArtifact and ValidateDefinition before execution.
func DefinitionSchema() json.RawMessage {
	return definition.DefinitionSchema()
}
