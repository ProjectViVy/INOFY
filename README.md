# INOFY

The root Go package exports `DefinitionSchema()` for authoring clients. Its
JSON Schema is a projection of the strict decoder's executable vocabulary;
`DecodeArtifact`, `ValidateDefinition`, and the trusted node catalog still
govern admission. In particular, schema validation alone cannot prove
duplicate-key safety, reference scope, graph topology, or host authority.

An embeddable Go workflow engine built on Eino, with a standalone application and reusable visual editor for ViVy and Garden.

Development documents:

- [Architecture](docs/superpowers/specs/2026-09-28-inofy-architecture.md)
- [Implementation design and Story index](docs/superpowers/plans/inofy/index.md)
