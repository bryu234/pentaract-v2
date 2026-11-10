UPDATE sessions SET expires_at=LEAST(expires_at,created_at+interval '24 hours');
