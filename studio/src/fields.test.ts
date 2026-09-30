import { describe, expect, it } from "vitest";
import { configFields, fieldsForSchema, inputFields } from "./fields";

describe("fieldsForSchema", () => {
  it("projects object properties into field specs", () => {
    const fields = fieldsForSchema({
      type: "object",
      properties: {
        task: { type: "string", description: "Task text" },
        retries: { type: "integer", default: 1 },
        verbose: { type: "boolean" },
        mode: { type: "string", enum: ["fast", "safe"] },
        extra: { type: "object" },
        freeform: {},
      },
      required: ["task"],
    });
    expect(fields.map((f) => f.name)).toEqual([
      "task",
      "retries",
      "verbose",
      "mode",
      "extra",
      "freeform",
    ]);
    expect(fields[0]).toMatchObject({
      kind: "string",
      required: true,
      description: "Task text",
    });
    expect(fields[1]).toMatchObject({ kind: "number", default: 1 });
    expect(fields[2]).toMatchObject({ kind: "boolean", required: false });
    expect(fields[3]).toMatchObject({ kind: "enum", enum: ["fast", "safe"] });
    expect(fields[4]).toMatchObject({ kind: "json" });
    expect(fields[5]).toMatchObject({ kind: "json" });
  });

  it("returns no fields for non-object schemas", () => {
    expect(fieldsForSchema(undefined)).toEqual([]);
    expect(fieldsForSchema({ type: "string" })).toEqual([]);
    expect(fieldsForSchema({ type: "object" })).toEqual([]);
  });
});

describe("descriptor projections", () => {
  const desc = {
    type_id: "acme.task@1",
    implementation_id: "acme/1",
    config_schema: {
      type: "object",
      properties: { url: { type: "string" } },
      required: ["url"],
    },
    input_schema: {
      type: "object",
      properties: { body: {}, headers: {} },
      required: ["body"],
    },
  };

  it("configFields reads config_schema", () => {
    expect(configFields(desc)).toEqual([
      { name: "url", kind: "string", required: true },
    ]);
  });

  it("inputFields marks every property a binding", () => {
    const fields = inputFields(desc);
    expect(fields.map((f) => f.name)).toEqual(["body", "headers"]);
    expect(fields[0]).toMatchObject({ kind: "binding", required: true });
    expect(fields[1]).toMatchObject({ kind: "binding", required: false });
  });
});
