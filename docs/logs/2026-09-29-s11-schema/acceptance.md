# Acceptance

An external Go consumer can compile `inofy.DefinitionSchema()` as JSON Schema.
The full valid fixture is accepted, and unknown fields, wrong version, invalid
node shape, and malformed bindings are rejected by both schema and strict
decoder. Mutating returned bytes does not affect later calls. This is only
S11-A; no VIVY graph route has switched.
