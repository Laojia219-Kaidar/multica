-- 373 down: 还原 8/14 空 ProjectID 回填
UPDATE workflow_instance
SET context = jsonb_set(context, '{ProjectID}', to_jsonb(''::text))
    updated_at = now()
WHERE definition_id = 'hivecrew.project-lifecycle'
  AND context->>'ProjectID' = '75d55d55-ce39-4a7e-9de4-50479fb8ac19';
