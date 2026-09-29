// 无依赖哈希路由。地址即状态：#/workflows、#/workflows/:id、
// #/runs、#/runs/:id、#/settings。刷新与深链都由浏览器兜底，
// 不引入路由库。

import { useEffect, useState } from "react";

export type Route =
  | { name: "workflows" }
  | { name: "editor"; id: string }
  | { name: "runs"; id?: string }
  | { name: "settings" };

export function parseHash(hash: string): Route {
  const raw = hash.replace(/^#/, "");
  const path = raw.startsWith("/") ? raw.slice(1) : raw;
  const seg = path.split("/").filter(Boolean).map(decodeURIComponent);
  switch (seg[0]) {
    case "workflows":
      return seg[1] ? { name: "editor", id: seg[1] } : { name: "workflows" };
    case "runs":
      return seg[1] ? { name: "runs", id: seg[1] } : { name: "runs" };
    case "settings":
      return { name: "settings" };
    default:
      return { name: "workflows" };
  }
}

export function routeHref(route: Route): string {
  switch (route.name) {
    case "editor":
      return `#/workflows/${encodeURIComponent(route.id)}`;
    case "runs":
      return route.id ? `#/runs/${encodeURIComponent(route.id)}` : "#/runs";
    case "settings":
      return "#/settings";
    default:
      return "#/workflows";
  }
}

export function navigate(route: Route | string): void {
  const href = typeof route === "string" ? route : routeHref(route);
  if (window.location.hash === href) return;
  window.location.hash = href;
}

export function useRoute(): Route {
  const [route, setRoute] = useState(() => parseHash(window.location.hash));
  useEffect(() => {
    const onHash = () => setRoute(parseHash(window.location.hash));
    window.addEventListener("hashchange", onHash);
    onHash();
    return () => window.removeEventListener("hashchange", onHash);
  }, []);
  return route;
}
