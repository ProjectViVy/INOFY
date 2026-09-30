// fieldsForSchema projects a node descriptor's JSON Schema (config or
// input) into flat field specs an editor can render generically — so a
// properties panel is driven by the catalog descriptor instead of a
// hardcoded per-type form. Kind collapses to the widget vocabulary:
// string/number/boolean render inputs, enum a select, everything else
// raw JSON; input-schema properties are always binding fields.
import type { JsonValue, NodeDescriptor } from "./schema";

export type FieldKind =
  | "string"
  | "number"
  | "boolean"
  | "enum"
  | "json"
  | "binding";

export interface FieldSpec {
  name: string;
  kind: FieldKind;
  required: boolean;
  enum?: JsonValue[];
  default?: JsonValue;
  description?: string;
}

const isObj = (v: JsonValue | undefined): v is { [k: string]: JsonValue } =>
  typeof v === "object" && v !== null && !Array.isArray(v);

export function fieldsForSchema(
  schema: JsonValue | undefined,
  opts: { binding?: boolean } = {},
): FieldSpec[] {
  if (!isObj(schema) || !isObj(schema.properties)) return [];
  const required = new Set(
    Array.isArray(schema.required) ? schema.required : [],
  );
  const fields: FieldSpec[] = [];
  for (const name of Object.keys(schema.properties)) {
    const p = schema.properties[name];
    const prop = isObj(p) ? p : {};
    const spec: FieldSpec = {
      name,
      kind: opts.binding ? "binding" : kindOf(prop),
      required: required.has(name),
    };
    if (!opts.binding && Array.isArray(prop.enum)) spec.enum = prop.enum;
    if (prop.default !== undefined) spec.default = prop.default;
    if (typeof prop.description === "string") spec.description = prop.description;
    fields.push(spec);
  }
  return fields;
}

function kindOf(prop: { [k: string]: JsonValue }): FieldKind {
  if (Array.isArray(prop.enum)) return "enum";
  switch (prop.type) {
    case "string":
      return "string";
    case "number":
    case "integer":
      return "number";
    case "boolean":
      return "boolean";
    default:
      return "json";
  }
}

export function configFields(d: NodeDescriptor): FieldSpec[] {
  return fieldsForSchema(d.config_schema);
}

export function inputFields(d: NodeDescriptor): FieldSpec[] {
  return fieldsForSchema(d.input_schema, { binding: true });
}
