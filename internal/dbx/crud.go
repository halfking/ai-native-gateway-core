package dbx

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// CRUD executes registry-driven statements. Every method takes the DBTX
// handed out by a ScopeRunner transaction (or any pgx.Tx), so statement
// execution and tenant scoping compose without hidden state. Tables and
// columns that are not in a registered manifest are unreachable.
type CRUD struct {
	reg *Registry
	obs []Observer
}

type crudOptions struct {
	obs []Observer
}

// CRUDOption customizes NewCRUD.
type CRUDOption func(*crudOptions)

// WithObserver attaches an Observer (may be repeated).
func WithObserver(o Observer) CRUDOption {
	return func(co *crudOptions) {
		if o != nil {
			co.obs = append(co.obs, o)
		}
	}
}

// NewCRUD binds the CRUD layer to a registry.
func NewCRUD(reg *Registry, opts ...CRUDOption) (*CRUD, error) {
	if reg == nil {
		return nil, fmt.Errorf("dbx: NewCRUD: nil registry: %w", ErrInvalidInput)
	}
	var co crudOptions
	for _, opt := range opts {
		opt(&co)
	}
	return &CRUD{reg: reg, obs: co.obs}, nil
}

// Insert writes one row. tenantID is injected into the manifest's tenant
// column (never taken from fields); fields must all be insertable columns.
// Returns the full row via RETURNING.
func (c *CRUD) Insert(ctx context.Context, dbtx DBTX, table, tenantID string, fields map[string]any) (map[string]any, error) {
	m, err := c.manifestForWrite(table)
	if err != nil {
		return nil, err
	}
	if tenantID == "" {
		return nil, fmt.Errorf("dbx: insert %q: %w", table, ErrMissingScope)
	}
	ops, err := validateFields(m, fields, func(spec ColumnSpec) bool { return spec.Insertable })
	if err != nil {
		return nil, err
	}
	all := append([]PatchOp{{Column: m.TenantColumn, Value: tenantID}}, ops...)

	cols := make([]string, len(all))
	placeholders := make([]string, len(all))
	args := make([]any, len(all))
	for i, op := range all {
		q, qerr := QuoteIdentifier(op.Column)
		if qerr != nil {
			return nil, qerr
		}
		cols[i] = q
		placeholders[i] = fmt.Sprintf("$%d", i+1)
		args[i] = op.Value
	}
	returning, err := m.columnList()
	if err != nil {
		return nil, err
	}
	tableQ, err := QuoteIdentifier(m.Table)
	if err != nil {
		return nil, err
	}
	sqlText := fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s) RETURNING %s",
		tableQ, joinQuoted(cols), joinQuoted(placeholders), returning)
	return c.queryOne(ctx, dbtx, m, "insert", sqlText, args...)
}

// SelectByPK reads one row by primary key under the tenant predicate
// (double protection with RLS). Soft-deleted rows are invisible.
func (c *CRUD) SelectByPK(ctx context.Context, dbtx DBTX, table, tenantID string, pk any) (map[string]any, error) {
	m, err := c.reg.Lookup(table)
	if err != nil {
		return nil, err
	}
	if tenantID == "" {
		return nil, fmt.Errorf("dbx: select %q: %w", table, ErrMissingScope)
	}
	tableQ, pkQ, tenantQ, err := quoteTriple(m)
	if err != nil {
		return nil, err
	}
	cols, err := m.columnList()
	if err != nil {
		return nil, err
	}
	sqlText := fmt.Sprintf("SELECT %s FROM %s WHERE %s = $1 AND %s = $2",
		cols, tableQ, pkQ, tenantQ)
	sqlText, err = appendSoftDeleteFilter(sqlText, m)
	if err != nil {
		return nil, err
	}
	return c.queryOne(ctx, dbtx, m, "select", sqlText, pk, tenantID)
}

// appendSoftDeleteFilter hides soft-deleted rows from reads and updates.
func appendSoftDeleteFilter(sqlText string, m *TableManifest) (string, error) {
	if m.SoftDelete == nil {
		return sqlText, nil
	}
	sdQ, err := QuoteIdentifier(*m.SoftDelete)
	if err != nil {
		return "", err
	}
	return sqlText + fmt.Sprintf(" AND %s IS NULL", sdQ), nil
}

// UpdateOptions carries optional optimistic-lock expectations for updates.
type UpdateOptions struct {
	// ExpectedVersion enables a version CAS check. A zero-row result is
	// then reported as ErrConflict instead of ErrNotFound (both a missing
	// row and a stale version surface as ErrConflict; callers that need to
	// distinguish them must select first).
	ExpectedVersion *int64
}

// UpdateByPK applies a validated patch to one row identified by primary
// key + tenant. Framework-managed columns (touch, version) are set
// automatically and cannot appear in the patch.
func (c *CRUD) UpdateByPK(ctx context.Context, dbtx DBTX, table, tenantID string, pk any, patch *Patch, opt *UpdateOptions) (map[string]any, error) {
	m, err := c.manifestForWrite(table)
	if err != nil {
		return nil, err
	}
	if tenantID == "" {
		return nil, fmt.Errorf("dbx: update %q: %w", table, ErrMissingScope)
	}
	if patch == nil || len(patch.Ops) == 0 {
		return nil, fmt.Errorf("dbx: update %q: %w", table, ErrEmptyPatch)
	}
	if opt != nil && opt.ExpectedVersion != nil && m.VersionColumn == nil {
		return nil, fmt.Errorf("dbx: update %q: ExpectedVersion set but manifest has no VersionColumn: %w", table, ErrUnsupported)
	}

	setParts := make([]string, 0, len(patch.Ops)+2)
	args := make([]any, 0, len(patch.Ops)+3)
	for _, op := range patch.Ops {
		q, qerr := QuoteIdentifier(op.Column)
		if qerr != nil {
			return nil, qerr
		}
		args = append(args, op.Value)
		setParts = append(setParts, fmt.Sprintf("%s = $%d", q, len(args)))
	}
	if m.TouchColumn != nil {
		q, qerr := QuoteIdentifier(*m.TouchColumn)
		if qerr != nil {
			return nil, qerr
		}
		setParts = append(setParts, fmt.Sprintf("%s = now()", q))
	}
	if m.VersionColumn != nil {
		q, qerr := QuoteIdentifier(*m.VersionColumn)
		if qerr != nil {
			return nil, qerr
		}
		setParts = append(setParts, fmt.Sprintf("%s = %s + 1", q, q))
	}

	tableQ, pkQ, tenantQ, err := quoteTriple(m)
	if err != nil {
		return nil, err
	}
	args = append(args, pk)
	pkArg := len(args)
	args = append(args, tenantID)
	tenantArg := len(args)
	where := fmt.Sprintf("%s = $%d AND %s = $%d", pkQ, pkArg, tenantQ, tenantArg)
	if opt != nil && opt.ExpectedVersion != nil {
		vq, qerr := QuoteIdentifier(*m.VersionColumn)
		if qerr != nil {
			return nil, qerr
		}
		args = append(args, *opt.ExpectedVersion)
		where += fmt.Sprintf(" AND %s = $%d", vq, len(args))
	}
	// Soft-deleted rows are not updatable through the framework.
	where, err = appendSoftDeleteFilter(where, m)
	if err != nil {
		return nil, err
	}
	returning, err := m.columnList()
	if err != nil {
		return nil, err
	}
	sqlText := fmt.Sprintf("UPDATE %s SET %s WHERE %s RETURNING %s",
		tableQ, joinQuoted(setParts), where, returning)

	row, qerr := c.queryOne(ctx, dbtx, m, "update", sqlText, args...)
	if qerr != nil {
		if errors.Is(qerr, ErrNotFound) && opt != nil && opt.ExpectedVersion != nil {
			return nil, fmt.Errorf("dbx: update %q: %w", table, ErrConflict)
		}
		return nil, qerr
	}
	return row, nil
}

// DeleteByPK performs a soft delete (sets the SoftDelete column to now()).
// Manifests without a soft-delete column - and readonly tables - reject the
// call: the framework never falls back to a hard DELETE.
func (c *CRUD) DeleteByPK(ctx context.Context, dbtx DBTX, table, tenantID string, pk any) error {
	m, err := c.manifestForWrite(table)
	if err != nil {
		return err
	}
	if tenantID == "" {
		return fmt.Errorf("dbx: delete %q: %w", table, ErrMissingScope)
	}
	if m.SoftDelete == nil {
		return fmt.Errorf("dbx: delete %q: no soft-delete column, hard delete not offered: %w", table, ErrUnsupported)
	}
	tableQ, pkQ, tenantQ, err := quoteTriple(m)
	if err != nil {
		return err
	}
	sdQ, err := QuoteIdentifier(*m.SoftDelete)
	if err != nil {
		return err
	}
	sqlText := fmt.Sprintf("UPDATE %s SET %s = now() WHERE %s = $1 AND %s = $2 AND %s IS NULL",
		tableQ, sdQ, pkQ, tenantQ, sdQ)
	start := time.Now()
	tag, err := dbtx.Exec(ctx, sqlText, pk, tenantID)
	c.observe(QueryFact{
		QueryID: Fingerprint(sqlText), Op: "delete", Table: table,
		Duration: time.Since(start), Rows: tag.RowsAffected(), SQLState: sqlStateOf(err),
	})
	if err != nil {
		return wrapPgError("delete", table, err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("dbx: delete %q: %w", table, ErrNotFound)
	}
	return nil
}

// manifestForWrite resolves the manifest and rejects readonly tables.
func (c *CRUD) manifestForWrite(table string) (*TableManifest, error) {
	m, err := c.reg.Lookup(table)
	if err != nil {
		return nil, err
	}
	if m.Readonly {
		return nil, fmt.Errorf("dbx: %q is readonly: %w", table, ErrUnsupported)
	}
	return m, nil
}

// validateFields coerces input fields against an admission predicate
// (insertable or writable) and returns binding-ready ops in sorted order.
func validateFields(m *TableManifest, fields map[string]any, admit func(ColumnSpec) bool) ([]PatchOp, error) {
	keys := make([]string, 0, len(fields))
	for k := range fields {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	ops := make([]PatchOp, 0, len(keys))
	for _, k := range keys {
		spec, ok := m.Column(k)
		if !ok {
			return nil, fieldError(ErrUnknownField, m.Table, k)
		}
		if !admit(spec) {
			return nil, fieldError(ErrProtectedField, m.Table, k)
		}
		v, err := coerceValue(m, spec, fields[k])
		if err != nil {
			return nil, err
		}
		ops = append(ops, PatchOp{Column: k, Value: v})
	}
	if len(ops) == 0 {
		return nil, fmt.Errorf("dbx: %q: %w", m.Table, ErrEmptyPatch)
	}
	return ops, nil
}

// queryOne executes a statement expected to return at most one row and
// reads it into a name-keyed map via Values().
func (c *CRUD) queryOne(ctx context.Context, dbtx DBTX, m *TableManifest, op, sqlText string, args ...any) (map[string]any, error) {
	if dbtx == nil {
		return nil, fmt.Errorf("dbx: %s %q: nil DBTX: %w", op, m.Table, ErrInvalidInput)
	}
	start := time.Now()
	rows, err := dbtx.Query(ctx, sqlText, args...)
	if err != nil {
		c.observe(QueryFact{QueryID: Fingerprint(sqlText), Op: op, Table: m.Table, Duration: time.Since(start), SQLState: sqlStateOf(err)})
		return nil, wrapPgError(op, m.Table, err)
	}
	defer rows.Close()

	if !rows.Next() {
		rerr := rows.Err()
		c.observe(QueryFact{QueryID: Fingerprint(sqlText), Op: op, Table: m.Table, Duration: time.Since(start), SQLState: sqlStateOf(rerr)})
		if rerr != nil {
			return nil, fmt.Errorf("dbx: %s %q: %w", op, m.Table, rerr)
		}
		return nil, fmt.Errorf("dbx: %s %q: %w", op, m.Table, ErrNotFound)
	}
	values, verr := rows.Values()
	if verr != nil {
		c.observe(QueryFact{QueryID: Fingerprint(sqlText), Op: op, Table: m.Table, Duration: time.Since(start)})
		return nil, fmt.Errorf("dbx: %s %q: scan: %w", op, m.Table, verr)
	}
	fds := rows.FieldDescriptions()
	row := make(map[string]any, len(fds))
	for i := range fds {
		row[fds[i].Name] = values[i]
	}
	if rows.Next() {
		c.observe(QueryFact{QueryID: Fingerprint(sqlText), Op: op, Table: m.Table, Duration: time.Since(start)})
		return nil, fmt.Errorf("dbx: %s %q: more than one row matched: %w", op, m.Table, ErrTooManyRows)
	}
	if rerr := rows.Err(); rerr != nil {
		return nil, fmt.Errorf("dbx: %s %q: %w", op, m.Table, rerr)
	}
	c.observe(QueryFact{QueryID: Fingerprint(sqlText), Op: op, Table: m.Table, Duration: time.Since(start), Rows: 1})
	return row, nil
}

func (c *CRUD) observe(fact QueryFact) {
	for _, o := range c.obs {
		o.ObserveQuery(fact)
	}
}

func sqlStateOf(err error) string {
	if err == nil {
		return ""
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code
	}
	return ""
}

// sqlStateUniqueViolation is PostgreSQL's unique-constraint violation.
const sqlStateUniqueViolation = "23505"

// wrapPgError attaches the typed ErrUniqueViolation sentinel on SQLSTATE
// 23505 while keeping the original pgconn.PgError reachable via errors.As.
func wrapPgError(op, table string, err error) error {
	if sqlStateOf(err) == sqlStateUniqueViolation {
		return fmt.Errorf("dbx: %s %q: %w", op, table, errors.Join(fieldError(ErrUniqueViolation, table, ""), err))
	}
	return fmt.Errorf("dbx: %s %q: %w", op, table, err)
}

func quoteTriple(m *TableManifest) (tableQ, pkQ, tenantQ string, err error) {
	tableQ, err = QuoteIdentifier(m.Table)
	if err != nil {
		return
	}
	pkQ, err = QuoteIdentifier(m.PrimaryKey)
	if err != nil {
		return
	}
	tenantQ, err = QuoteIdentifier(m.TenantColumn)
	return
}

func joinQuoted(parts []string) string {
	out := ""
	for i, p := range parts {
		if i > 0 {
			out += ", "
		}
		out += p
	}
	return out
}

// compile-time checks: a live transaction satisfies the execution seam.
var _ DBTX = (pgx.Tx)(nil)
