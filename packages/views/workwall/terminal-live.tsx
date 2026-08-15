"use client";

import { useState } from "react";
import type { TerminalPane } from "@multica/core/api/workwall";

// Terminal 现场 — 每个宿主 tmux pane 的实时尾部输出（只读投影）。
// 数据来自宿主采集器（scripts/terminal-presence-collector.sh），10 秒心跳，
// 超过 15 分钟未心跳的 pane 由后端过滤。这里是 Owner 看数字员工
// "此刻真正在 terminal 里干什么"的现场。

function timeLabel(iso: string) {
  const d = new Date(iso);
  const diff = Math.max(0, Math.floor((Date.now() - d.getTime()) / 1000));
  if (diff < 60) return `${diff}秒前`;
  if (diff < 3600) return `${Math.floor(diff / 60)}分钟前`;
  return d.toLocaleTimeString("zh-CN", { hour12: false });
}

function PaneCard({ pane }: { pane: TerminalPane }) {
  const [open, setOpen] = useState(false);
  const lines = pane.tail_text.split("\n").filter((l) => l.trim() !== "");
  const preview = lines.slice(-3).join("\n");
  return (
    <div
      className="rounded-md border border-green-800 bg-black font-mono text-green-300"
      data-testid="terminal-live-pane"
    >
      <button
        type="button"
        onClick={() => setOpen((v) => !v)}
        className="flex w-full items-center gap-2 px-3 py-2 text-left text-xs"
        aria-expanded={open}
      >
        <span className="rounded bg-green-900 px-1.5 py-0.5 text-green-100">
          {pane.session_name}
        </span>
        <span className="text-green-500">
          {pane.host}:{pane.window_index}.{pane.pane_index}
        </span>
        {pane.agent_hint ? (
          <span className="truncate text-green-200">{pane.agent_hint}</span>
        ) : null}
        <span className="ml-auto whitespace-nowrap text-green-600">
          {pane.current_command || "idle"} · {timeLabel(pane.heartbeat_at)}
        </span>
      </button>
      <pre
        className="max-h-40 overflow-auto border-t border-green-900 px-3 py-2 text-[11px] leading-relaxed text-green-400"
        data-testid="terminal-live-tail"
      >
        {open ? pane.tail_text : preview || "（无输出）"}
      </pre>
    </div>
  );
}

export function TerminalLiveSection({ panes }: { panes: TerminalPane[] }) {
  const hosts = Array.from(new Set(panes.map((p) => p.host)));
  return (
    <section className="mt-4" data-testid="terminal-live-section">
      <div className="mb-2 flex items-center gap-2">
        <h2 className="text-sm font-semibold">Terminal 现场</h2>
        <span className="text-xs text-zinc-500">
          {panes.length} 个活跃 pane · {hosts.length} 台主机 · 采集心跳 10s
        </span>
      </div>
      {panes.length === 0 ? (
        <p className="text-xs text-zinc-500">
          暂无活跃 Terminal 现场——宿主采集器未运行或所有会话已结束。
        </p>
      ) : (
        <div className="grid grid-cols-1 gap-2 md:grid-cols-2">
          {panes.map((p) => (
            <PaneCard
              key={`${p.host}:${p.session_name}:${p.window_index}:${p.pane_index}`}
              pane={p}
            />
          ))}
        </div>
      )}
    </section>
  );
}
