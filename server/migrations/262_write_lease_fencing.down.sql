-- Rollback for canonical write_lease table (HIV-410), migration 262.

DROP TABLE IF EXISTS write_lease;
