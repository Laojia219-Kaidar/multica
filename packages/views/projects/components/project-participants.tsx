"use client";

import { Users } from "lucide-react";
import { Skeleton } from "@multica/ui/components/ui/skeleton";
import { cn } from "@multica/ui/lib/utils";
import { ACTOR_TYPE_LABELS, isParticipantFieldPending } from "@multica/core/work-entry";
import type {
  ProjectParticipantsData,
  WorkParticipant,
} from "@multica/core/types";

// 参与者 / 执行者面板（VC-04 首切）。
//
// 目标：以项目为枢轴展示 actor_type / employee_id / carrier / runtime /
// model / base / host / session / 下一动作。当前项目级聚合端点待后端部署，
// 首切复用 companyops workforce_base_runtime 员工读模型（registered_employee
// 子集）；external_agent 与 carrier/host/session/next_action 维度按“待后端部署
// 后接通”标注。视觉复用 shadcn/Tailwind v4（oklch 蓝 brand），无绿色终端风。

const FIELD_COLS = [
  ["carrier", "载体"],
  ["runtime", "运行时"],
  ["model", "模型"],
  ["base", "基地"],
  ["host", "主机"],
  ["session", "会话"],
  ["next_action", "下一动作"],
] as const;

function fieldValue(p: WorkParticipant, key: string): string | undefined {
  switch (key) {
    case "carrier":
      return p.carrier_id;
    case "runtime":
      return p.runtime_id;
    case "model":
      return p.model_ref;
    case "base":
      return p.base_id;
    case "host":
      return p.host_id;
    case "session":
      return p.session_id;
    case "next_action":
      return p.next_action;
    default:
      return undefined;
  }
}

function ParticipantField({ label, value }: { label: string; value?: string }) {
  const pending = isParticipantFieldPending(value);
  return (
    <div className="flex min-w-0 items-baseline gap-1.5">
      <span className="shrink-0 text-[10px] text-muted-foreground">{label}</span>
      <span
        className={cn(
          "min-w-0 truncate text-xs",
          pending ? "text-muted-foreground/50" : "text-foreground",
        )}
        title={pending ? undefined : value}
      >
        {pending ? "—" : value}
      </span>
    </div>
  );
}

function ParticipantRow({ participant }: { participant: WorkParticipant }) {
  return (
    <li className="px-3 py-2.5">
      <div className="flex flex-wrap items-center gap-2">
        <span className="inline-flex items-center gap-1 rounded bg-brand/10 px-1.5 py-0.5 text-[11px] font-medium text-brand">
          {ACTOR_TYPE_LABELS[participant.actor_type] ?? participant.actor_type}
        </span>
        {participant.employee_id ? (
          <span
            className="rounded bg-muted px-1.5 py-0.5 text-[11px] tabular-nums text-muted-foreground"
            title="员工编号"
          >
            {participant.employee_id}
          </span>
        ) : null}
        <code className="min-w-0 truncate font-mono text-[11px] text-muted-foreground">
          {participant.actor_id}
        </code>
      </div>
      <div className="mt-2 grid grid-cols-2 gap-x-3 gap-y-1.5 @2xl:grid-cols-3">
        {FIELD_COLS.map(([key, label]) => (
          <ParticipantField
            key={key}
            label={label}
            value={fieldValue(participant, key)}
          />
        ))}
      </div>
    </li>
  );
}

export function ProjectParticipants({
  data,
  isLoading = false,
}: {
  data?: ProjectParticipantsData;
  isLoading?: boolean;
}) {
  const participants = data?.participants ?? [];
  const pendingProjectScope = data?.pending_project_scope ?? false;

  return (
    <section
      aria-label="参与者 / 执行者"
      className="rounded-lg border bg-card shadow-sm"
    >
      <header className="flex items-center justify-between gap-2 border-b px-3 py-2">
        <div className="flex min-w-0 items-center gap-2">
          <Users className="size-4 shrink-0 text-muted-foreground" />
          <span className="truncate text-sm font-medium">参与者 / 执行者</span>
        </div>
        <span className="shrink-0 rounded-full bg-brand/10 px-2 py-0.5 text-xs font-semibold tabular-nums text-brand">
          {participants.length}
        </span>
      </header>

      {isLoading ? (
        <div className="space-y-2 px-3 py-3">
          <Skeleton className="h-4 w-40" />
          <Skeleton className="h-4 w-64" />
        </div>
      ) : participants.length === 0 ? (
        <div className="px-3 py-5 text-center text-xs text-muted-foreground">
          暂无参与者数据。
        </div>
      ) : (
        <ul className="max-h-80 divide-y overflow-y-auto">
          {participants.map((p) => (
            <ParticipantRow key={`${p.actor_type}:${p.actor_id}`} participant={p} />
          ))}
        </ul>
      )}

      {pendingProjectScope && (
        <p className="border-t px-3 py-2 text-[11px] leading-relaxed text-muted-foreground">
          现数据源：workforce_base_runtime 员工读模型（员工 → 智能体 → 运行时 →
          基地）；项目级聚合与外部智能体 / 载体 / 主机 / 会话 / 下一动作维度
          待后端聚合端点部署后接通。
        </p>
      )}
    </section>
  );
}
