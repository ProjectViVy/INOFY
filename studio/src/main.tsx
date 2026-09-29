// 生产入口：基址为空串，全部走同源 /api（开发期由 Vite 代理到
// 本机 8377，部署时由后端一并托管静态资源）。AppTransport 同时
// 充当会话门：登录一次换 HttpOnly Cookie，令牌不落任何存储。

import { createRoot } from "react-dom/client";
import { App } from "./App";
import { AppTransport } from "./app-transport";
import "./styles.css";

const transport = new AppTransport("");

const el = document.getElementById("root");
if (!el) throw new Error("#root missing");

createRoot(el).render(
  <div style={{ width: "100vw", height: "100vh" }}>
    <App transport={transport} auth={transport} />
  </div>,
);
