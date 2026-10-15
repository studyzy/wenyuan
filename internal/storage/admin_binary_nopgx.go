//go:build !pgx

package storage

import (
	"context"
	"fmt"
)

// RestoreBinary is unavailable in the default build: without the pgx tag no
// PostgreSQL driver is registered, so there is no backend to restore onto.
// The binary restore path needs the driver's raw connection to replay COPY
// streams byte-for-byte.
func (s *PostgresStore) RestoreBinary(ctx context.Context, loads []BinaryLoad) error {
	return fmt.Errorf("binary restore requires a build with the pgx tag")
}

// DumpBinary is unavailable in the default build, for the same reason: the
// binary backup captures the server's own COPY output, which needs the
// driver's raw connection.
func (s *PostgresStore) DumpBinary(ctx context.Context, tables []string) ([]BinaryDump, error) {
	return nil, fmt.Errorf("binary backup requires a build with the pgx tag")
}
