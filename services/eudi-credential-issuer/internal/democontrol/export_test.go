package democontrol

import "context"

// DropSchema removes the test schema.
func (p *PostgresEvents) DropSchema(ctx context.Context) {
	_, _ = p.pool.Exec(ctx, `DROP SCHEMA IF EXISTS `+p.schema+` CASCADE`)
}
