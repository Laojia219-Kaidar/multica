**VERDICT: PASS**

## 验收范围

- 分支: `work/hivecrew-project-lifecycle-closure`
- 候选提交: `528020fde`（HEAD `b9336919` 含 dispatch docs）
- 前端变更文件: `packages/core/types/project.ts`, `packages/core/types/index.ts`, `packages/core/api/client.ts`, `packages/views/projects/components/project-control-actions.tsx`

## 独立验证结果

### 1. typecheck ✅

```
pnpm --filter @multica/views typecheck → tsc --noEmit → exit 0
```

零类型错误。新增 `ProjectClosurePackage` 类型与后端 `ClosurePackage` JSON tag 完全对齐（所有 16 个字段一一对应）。

### 2. vitest run projects/components ✅

7 个 `projects/components` 测试文件全部通过（32 tests）：

- `projects-page.test.tsx` (5)
- `project-picker.open-state.test.tsx` (6)
- `project-picker.test.tsx` (3)
- `local-directory-hint.test.tsx` (4)
- `project-health.test.ts` (7)
- `project-date-pickers.test.tsx` (6)
- `project-issue-metrics.test.ts` (1)

全仓 2 个失败（`layout/sidebar-resize.test.tsx`、`agents/components/agent-activity-hover-content.test.tsx`）已确认在 `528020fd^` 父提交上同样失败，属于 pre-existing，与 Slice 4 无关。

### 3. 功能验证 ✅

| 合同要求 | 实现 | 结论 |
|---|---|---|
| 项目详情侧栏新增「关闭预览」按钮 | `project-control-actions.tsx` 新增 Archive 图标按钮，调用 `previewClose` → `api.projectLifecycleAction(id, "close", { preview: true })` | ✅ |
| 项目详情侧栏新增「生成成果包」按钮 | 新增 PackageCheck 图标按钮，调用 `generatePackage.mutate()` → `api.generateClosurePackage(id)` | ✅ |
| 生成成果包显示 digest | `pkg.digest.slice(0, 24)` 展示 | ✅ |
| 生成成果包显示门状态 | 展示 terminal/nonterminal issue count、outcome confirmed/total、review_required | ✅ |
| 生成成果包显示 blockers | `pkg.blockers.length > 0` 时以 `text-rose-700` 红色显示 | ✅ |
| 关闭预览在有 blockers 时禁用「确认关闭」 | `disabled={… \|\| pkg.blockers.length > 0 \|\| pkg.review_required}` | ✅ |
| 关闭预览在有 review_required 时禁用「确认关闭」 | 同上，`pkg.review_required` 参与 disabled 判断 | ✅ |
| 空态不崩溃 | `pkg` 初始 null，`{pkg && …}` 条件渲染 | ✅ |
| 加载态不崩溃 | `generatePackage.isPending` 显示 Loader2 spinner + 禁用按钮 | ✅ |
| 错误态不崩溃 | `onError` 回调 `toast.error("生成成果包失败")` | ✅ |

### 4. lint ⚠️ 非阻塞

Slice 4 新增 10 个 `i18next/no-literal-string` lint error（中文 JSX 字面量）。与 Slice 2 已有的 17 个同类 error 模式一致，属于项目级 i18n 技术债，非 Slice 4 回退。

## Findings（非阻塞）

1. **`previewClose` 无 try/catch**：与已有 `previewContinue` 模式一致（均无 try/catch），API 错误会触发 unhandled rejection。建议后续统一补充，不构成 Slice 4 阻塞。
2. **`review_required` 硬编码 `true`**：后端 `GenerateClosurePackage` 始终设置 `review_required: true`，前端据此禁用「确认关闭」。这意味着通过 UI 关闭项目需要独立复核完成后才能操作，符合合同 §5 第 5 条「Closure Package 已生成、哈希固定、独立复核、进 Outcome Center」的要求。