ALTER TABLE user_model_call_receipts ADD CONSTRAINT user_model_call_receipts_purpose_v2_check CHECK (purpose IN ('article', 'daily', 'connection_test', 'source_naming')) NOT VALID;
