"use client";

import { useState } from "react";
import type { A2Pane } from "@multica/core/api/workwall";
import {
  Card,
  CardContent,
  CardHeader,
} from "@multica/ui/components/ui/card";
import { Badge } from "@multica/ui/components/ui/badge";
import { ScrollArea } from "@multica/ui/components/ui/scroll-area";

// Terminal 现场 — A2 终端 pane 的独立列表视图。
// 与工作墙上的内联 pane 不同：这里展示全部 terminal kind 的 pane，
// 按 session_id 映射到真实 TerminalPane.session_name。
// 绝不伪造终端 — 只渲染后端返回的真实 pane。

function timeLabel(iso: string) {
  const d = new Date(iso);
  const diff = Math.max(0, Math.floor((Date.now() - d.getTime()) / 1000));
  if (diff < 60) return `${diff} 秒前`;
  if (diff < 3600) return `${Math.floor(diff / 60)} 分钟前`;
  return d.toLocaleTimeString("zh-CN", { hour12: false });
}

function PaneCard({ pane }: { pane: A2Pane }) {
  const [expanded, setExpanded] = useState(false);
  const lines = pane.tail_text.split("\n").filter((l) => l.trim() !== "");
  const preview = lines.slice(-3).join("\n");

  return (
    <Card className="flex flex-col overflow-hidden" data-testid="terminal-live-pane">
      <CardHeader className="flex flex-row items-center gap-2 py-2">
        <Badge variant="outline" className="font-mono">
          {pane.session_id}
        </Badge>
        <span className="truncate text-xs text-muted-foreground">
          {pane.display_name}
        </span>
        <span className="ml-auto text-[10px] text-muted-foreground">
          {timeLabel(pane.observed_at)}
        </span>
        <button
          type="button"
          onClick={() => setExpanded((v) => !v)}
          className="rounded border border-border px-2 py-0.5 text-[10px] text-muted-foreground hover:bg-muted"
          aria-expanded={expanded}
        >
          {expanded ? "收起" : "展开"}
        </button>
      </CardHeader>
      <CardContent className="pb-2 pt-0">
        <ScrollArea
          className={`rounded-md border border-surface-border bg-surface p-2 font-mono text-[11px] leading-relaxed ${
            expanded ? "h-48" : "h-20"
          }`}
          data-testid="terminal-live-tail"
        >
          <pre className="whitespace-pre-wrap break-words text-surface-foreground/80">
            {expanded ? pane.tail_text : preview || "（无输出）"}
          </pre>
        </ScrollArea>
      </CardContent>
    </Card>
  );
}

export function TerminalLiveSection({ panes }: { panes: A2Pane[] }) {
  // Only show terminal-kind panes here; event_console lives elsewhere.
  const terminalPanes = panes.filter((p) => p.kind === "terminal");
  const hosts = Array.from(
    new Set(terminalPanes.map((p) => p.session_id.split(":")[0] ?? "")),
  );

  return (
    <section className="mt-4" data-testid="terminal-live-section">
      <div className="mb-2 flex items-center gap-2">
        <h2 className="text-sm font-medium">Terminal 现场</h2>
        <span className="text-xs text-muted-foreground">
          {terminalPanes.length} 个活跃 pane · {hosts.length} 台主机 · 采集心跳 5s
        </span>
      </div>
      {terminalPanes.length === 0 ? (
        <p className="rounded-md border border-dashed border-border px-3 py-4 text-center text-xs text-muted-foreground">
          暂无活跃 Terminal 现场——宿主采集器未运行或所有会话已结束。
        </p>
      ) : (
        <div className="grid grid-cols-1 gap-2 md:grid-cols-2 lg:grid-cols-3">
          {terminalPanes.map((p) => (
            <PaneCard key={`${p.pane_id}:${p.session_id}`} pane={p} />
          ))}
        </div>
      )}
    </section>
  );
}
