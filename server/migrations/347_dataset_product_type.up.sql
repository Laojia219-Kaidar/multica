-- 347_dataset_product_type.up.sql — 数据知识工厂四种产品形态（RAG 知识库/员工训练包/模型微调/独立评测），
-- 与六个数据域正交。product_type 是本地执行投影字段；知识权威仍为 World Library（source_available_runtime_unavailable）。
ALTER TABLE dataset ADD COLUMN IF NOT EXISTS product_type text NOT NULL DEFAULT 'rag_kb';
