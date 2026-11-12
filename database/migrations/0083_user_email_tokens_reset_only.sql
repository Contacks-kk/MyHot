ALTER TABLE user_email_tokens ADD CONSTRAINT user_email_tokens_reset_only CHECK (purpose = 'password_reset') NOT VALID;
