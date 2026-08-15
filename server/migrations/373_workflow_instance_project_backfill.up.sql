-- 373_workflow_instance_project_backfill
-- 8/14 通过旧版 POST /api/workflow/instances 创建的 4 个 hivecrew.project-lifecycle
-- 实例 context.ProjectID 为空。工作流运营页（workflow-scope.ts）按
-- context.project_id 过滤，无项目引用的实例被刻意隐藏，导致实例不可见。
-- 回填：把无项目绑定的 lifecycle 实例挂到工作流引擎项目 HIVE-ORCHESTRATION-V1
-- （存储侧键名为 Go 字段名大写 ProjectID；API DTO 输出小写 project_id）。
UPDATE workflow_instance
SET context = jsonb_set(context, '{ProjectID}', to_jsonb('75d55d55-ce39-4a7e-9de4-50479fb8ac19'::text)),
    updated_at = now()
WHERE definition_id = 'hivecrew.project-lifecycle'
  AND COALESCE(context->>'ProjectID', '') = '';
