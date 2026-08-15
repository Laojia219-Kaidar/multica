"use client";

import { useState } from "react";
import { ChevronDown, Inbox, Link2, X } from "lucide-react";
import { Button } from "@multica/ui/components/ui/button";
import { Skeleton } from "@multica/ui/components/ui/skeleton";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuTrigger,
} from "@multica/ui/components/ui/dropdown-menu";
import { cn } from "@multica/ui/lib/utils";
import type { Project, WorkInboxItem } from "@multica/core/types";

// 未登记工作收件箱（VC-05 首切）。
//
// 只读列出 /api/work/reconcile 返回的未归属动作。每个条目只提供两个动作：
// attach（关联到既有项目）与 ignore（忽略）。未归属动作不推进任何项目进度；
// 只有 attach 之后才进入项目账本。该面板复用项目页的 shadcn/Tailwind v4
// 视觉系统（rounded-lg border bg-card + oklch 蓝 brand），不使用绿色终端风。

export type WorkInboxAction =
  | { id: string; kind: "attach" | "ignore" }
  | null;

function InboxSkeletonRow() {
  return (
    <div className="flex items-center gap-3 px-3 py-2.5">
      <Skeleton className="h-4 w-72" />
      <Skeleton className="h-4 w-24" />
    </div>
  );
}

export function ProjectUnclaimedInbox({
  items,
  projects,
  isLoading = false,
  busy = null,
  onAttach,
  onIgnore,
}: {
  items: WorkInboxItem[];
  projects: Project[];
  isLoading?: boolean;
  busy?: WorkInboxAction;
  onAttach: (item: WorkInboxItem, projectId: string) => void;
  onIgnore: (item: WorkInboxItem) => void;
}) {
  const [openItemId, setOpenItemId] = useState<string | null>(null);

  return (
    <section
      aria-label="未登记工作收件箱"
      className="mx-5 mb-3 rounded-lg border bg-card shadow-sm"
    >
      <header className="flex items-center justify-between gap-3 border-b px-3 py-2">
        <div className="flex min-w-0 items-center gap-2">
          <Inbox className="size-4 shrink-0 text-muted-foreground" />
          <span className="truncate text-sm font-medium">未登记工作收件箱</span>
          <span className="shrink-0 rounded-full bg-brand/10 px-2 py-0.5 text-xs font-semibold tabular-nums text-brand">
            {items.length}
          </span>
        </div>
        <span className="shrink-0 text-[11px] text-muted-foreground">
          未归属动作不推进任何项目进度
        </span>
      </header>

      {isLoading ? (
        <div className="divide-y">
          <InboxSkeletonRow />
          <InboxSkeletonRow />
        </div>
      ) : items.length === 0 ? (
        <div className="px-3 py-6 text-center text-xs text-muted-foreground">
          没有未登记的工作动作。
        </div>
      ) : (
        <ul className="divide-y">
          {items.map((item) => {
            const isAttaching = busy?.id === item.ID && busy.kind === "attach";
            const isIgnoring = busy?.id === item.ID && busy.kind === "ignore";
            return (
              <li
                key={item.ID}
                className="flex items-center gap-3 px-3 py-2"
              >
                <code
                  className="min-w-0 flex-1 truncate font-mono text-xs"
                  title={item.WorkRef}
                >
                  {item.WorkRef}
                </code>
                <span className="hidden shrink-0 text-[10px] text-muted-foreground md:inline">
                  {item.ID}
                </span>

                <DropdownMenu
                  open={openItemId === item.ID}
                  onOpenChange={(open) => setOpenItemId(open ? item.ID : null)}
                >
                  <DropdownMenuTrigger
                    render={
                      <button
                        type="button"
                        disabled={isAttaching}
                        className={cn(
                          "inline-flex h-7 items-center gap-1 rounded-md border px-2 text-xs text-muted-foreground transition-colors hover:bg-accent hover:text-accent-foreground",
                          "data-popup-open:bg-accent data-popup-open:text-accent-foreground",
                        )}
                      >
                        <Link2 className="size-3.5" />
                        关联
                        <ChevronDown className="size-3" />
                      </button>
                    }
                  />
                  <DropdownMenuContent align="end" className="w-64">
                    <DropdownMenuLabel className="text-xs text-muted-foreground">
                      关联到既有项目
                    </DropdownMenuLabel>
                    {projects.length === 0 ? (
                      <div className="px-2 py-3 text-center text-xs text-muted-foreground">
                        暂无项目可关联
                      </div>
                    ) : (
                      projects.map((project) => (
                        <DropdownMenuItem
                          key={project.id}
                          onClick={() => onAttach(item, project.id)}
                        >
                          <span className="min-w-0 truncate">{project.title}</span>
                        </DropdownMenuItem>
                      ))
                    )}
                  </DropdownMenuContent>
                </DropdownMenu>

                <Button
                  type="button"
                  variant="ghost"
                  size="sm"
                  disabled={isIgnoring}
                  onClick={() => onIgnore(item)}
                  className="h-7 gap-1 px-2 text-xs text-muted-foreground hover:text-destructive"
                >
                  <X className="size-3.5" />
                  忽略
                </Button>
              </li>
            );
          })}
        </ul>
      )}
    </section>
  );
}
