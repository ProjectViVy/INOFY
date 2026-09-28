import { createRoot } from "react-dom/client";
import { Editor } from "./Editor";
import type { Artifact } from "./schema";
import "./styles.css";

// Dev harness: production embedding (S13) mounts App.tsx with a real
// StudioTransport instead.
const demo: Artifact = {
  definition: {
    schema_version: "inofy.workflow/v1",
    graph: {
      nodes: [{ id: "start", kind: "call", type: "inofy.value@1" }],
      edges: [],
      exits: ["start"],
    },
  },
};

createRoot(document.getElementById("root")!).render(
  <div style={{ width: "100vw", height: "100vh" }}>
    <Editor artifact={demo} />
  </div>,
);
