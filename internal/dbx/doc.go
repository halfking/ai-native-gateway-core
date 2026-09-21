// Package dbx is a self-contained, pgx-first data access framework.
//
// Independence rules (hard constraints, see
// docs/03-design/04-data-design/数据库处理框架方案.md §2):
//
//  1. This package must never import host business packages (db/, domains/,
//     admin/, other internal/ packages). Allowed dependencies: Go stdlib,
//     pgx/v5, and test-only pgxmock/testcontainers.
//  2. It contains mechanisms only: no business tables, no entity structs, no
//     business SQL. Project conventions (RLS GUC names, super-admin values)
//     are injected via ScopeConfig by the host.
//  3. All framework code and tests live under this single directory tree.
package dbx
