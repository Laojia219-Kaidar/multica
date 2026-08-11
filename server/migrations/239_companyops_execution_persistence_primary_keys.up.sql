ALTER TABLE external_work_order_link
    ADD CONSTRAINT external_work_order_link_pkey PRIMARY KEY USING INDEX external_work_order_link_identity_uidx;

ALTER TABLE assignment_dispatch_receipt
    ADD CONSTRAINT assignment_dispatch_receipt_pkey PRIMARY KEY USING INDEX assignment_dispatch_receipt_command_uidx;

ALTER TABLE execution_receipt
    ADD CONSTRAINT execution_receipt_pkey PRIMARY KEY USING INDEX execution_receipt_task_uidx;
