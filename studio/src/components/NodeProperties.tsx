import { useState } from "react";
import type { NodeDescriptor, Node, JsonValue } from "../schema";
import { t } from "../i18n";

interface Props {
  node: Node;
  descriptor?: NodeDescriptor;
  onChange(node: Node): void;
  readOnly?: boolean;
}

type SchemaProp = { name: string; type: string; required: boolean };

// Schema-driven config fields where the catalog advertises a plain
// object schema; anything richer (or missing) falls back to a raw
// JSON textarea — the backend stays the final validator.
function schemaProps(desc?: NodeDescriptor): SchemaProp[] | null {
  const s = desc?.config_schema as
    | { type?: string; properties?: Record<string, { type?: string }>; required?: string[] }
    | undefined;
  if (!s || s.type !== "object" || !s.properties) return null;
  const req = new Set(s.required ?? []);
  const props = Object.entries(s.properties).map(([name, p]) => ({
    name,
    type: p?.type ?? "string",
    required: req.has(name),
  }));
  return props.every((p) => ["string", "number", "integer", "boolean"].includes(p.type))
    ? props
    : null;
}

export function NodeProperties({ node, descriptor, onChange, readOnly }: Props) {
  const [jsonErr, setJsonErr] = useState<string | null>(null);
  const props = schemaProps(descriptor);
  const config = (node.config ?? {}) as Record<string, JsonValue>;

  const setConfig = (next: Record<string, JsonValue>) =>
    onChange({ ...node, config: next });

  const applyJSON = (field: "config" | "inputs", text: string) => {
    try {
      const v = JSON.parse(text);
      setJsonErr(null);
      onChange({ ...node, [field]: v });
    } catch {
      setJsonErr(t("prop.json.invalid"));
    }
  };

  return (
    <aside className="node-props" aria-label={t("prop.node")}>
      <h3>
        {node.id} <small>{node.kind}</small>
      </h3>
      {node.kind === "call" && (
        <>
          <fieldset disabled={readOnly || undefined}>
            <legend>{props ? t("prop.config") : t("prop.config.raw")}</legend>
            {props ? (
              props.map((p) => (
                <label key={p.name}>
                  {p.name}
                  {p.required ? " *" : ""}
                  {p.type === "boolean" ? (
                    <input
                      type="checkbox"
                      checked={config[p.name] === true}
                      onChange={(e) =>
                        setConfig({ ...config, [p.name]: e.target.checked })
                      }
                    />
                  ) : (
                    <input
                      type={p.type === "string" ? "text" : "number"}
                      value={
                        config[p.name] == null ? "" : String(config[p.name])
                      }
                      onChange={(e) => {
                        const next = { ...config };
                        if (p.type === "string") {
                          next[p.name] = e.target.value;
                        } else if (e.target.value === "") {
                          delete next[p.name];
                        } else {
                          next[p.name] = Number(e.target.value);
                        }
                        setConfig(next);
                      }}
                    />
                  )}
                </label>
              ))
            ) : (
              <textarea
                aria-label={t("prop.config.raw")}
                defaultValue={JSON.stringify(node.config ?? {}, null, 2)}
                onBlur={(e) => applyJSON("config", e.target.value)}
              />
            )}
            <label>
              {t("prop.inputs")}
              <textarea
                aria-label={t("prop.inputs")}
                defaultValue={JSON.stringify(node.inputs ?? {}, null, 2)}
                onBlur={(e) => applyJSON("inputs", e.target.value)}
              />
            </label>
          </fieldset>
          {jsonErr && <p role="alert">{jsonErr}</p>}
        </>
      )}
      {node.kind !== "call" && (
        <pre data-testid="node-json">{JSON.stringify(node, null, 2)}</pre>
      )}
    </aside>
  );
}
