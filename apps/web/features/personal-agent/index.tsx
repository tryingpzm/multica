"use client";

/**
 * The personal agent panel page: SA's DSH sidebar panel mounted as a Multica dashboard page. Requests go
 * through Multica's ApiClient, which carries the session and the workspace; the Router's chat and a ticket's issue open
 * in Multica.
 */
import { useCallback, useEffect, useState } from "react";
import { useParams, useRouter } from "next/navigation";
import { getApi } from "@multica/core/api";
import { App } from "./client/app";
import { API_BASE, setTransport } from "./client/api";
import { en, zh } from "./client/locales";
import { translator } from "./client/translate";

setTransport((url, init) => getApi().personalAgentFetch(url, init));

export function PersonalAgentPage() {
  const { workspaceSlug } = useParams<{ workspaceSlug: string }>();
  const router = useRouter();
  const [lang, setLang] = useState<"zh" | "en">("zh");
  useEffect(() => {
    if (document.documentElement.lang.startsWith("en")) setLang("en");
  }, []);
  const ws = `/${encodeURIComponent(workspaceSlug)}`;
  // The Router's chat is a chat session; any other id the panel opens is a ticket's Multica issue.
  const openSession = useCallback(async (id: string) => {
    const response = await getApi().personalAgentFetch(`${API_BASE}state`, { method: "GET" });
    const state = (await response.json()) as { masterId?: string };
    router.push(id === state.masterId ? `${ws}/chat?session=${encodeURIComponent(id)}` : `${ws}/issues/${encodeURIComponent(id)}`);
  }, [router, ws]);
  return (
    <div className="h-full overflow-auto">
      <App t={translator(lang === "en" ? en : zh)} openSession={openSession} />
    </div>
  );
}
