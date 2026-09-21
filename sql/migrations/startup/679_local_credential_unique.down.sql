-- Down 679: 移除本地占位凭据唯一约束（重复行不会恢复）。
DROP INDEX IF EXISTS uq_credentials_local_placeholder_per_provider;
