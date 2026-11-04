package storage

// classifyDiscoveryError turns a discovery routine's failure into the answer the
// reference gives it.
//
// The reference logs a failed discovery and returns, leaving the job a no-op for
// that tick (engine/maintenance.py:282-284, 373-375, 435-437, 566-568). Nothing
// is cached on this side either: the next tick runs the routine again and logs
// again if it still fails — a deployment whose schema predates the maintenance
// migration sees one warning per job per tick, exactly as the reference does.
//
// Only the states that mean "this schema has no such routine" are swallowed, so
// a deployment that is merely behind on its migrations is not reported as
// failing; every other error is the job's error, which the loop logs and retries.
func (s *PostgresStore) classifyDiscoveryError(err error) error {
	var state interface{ SQLState() string }
	if asSQLState(err, &state) {
		// 42883 undefined_function, 42P01 undefined_table, 3F000
		// invalid_schema_name: the routine or the schema it lives in is absent.
		switch state.SQLState() {
		case "42883", "42P01", "3F000":
			return nil
		}
	}
	return err
}

// asSQLState is errors.As for anything exposing a SQLState, because the storage
// area compiles without the driver on the default build.
func asSQLState(err error, target *interface{ SQLState() string }) bool {
	for err != nil {
		if state, ok := err.(interface{ SQLState() string }); ok {
			*target = state
			return true
		}
		unwrapper, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = unwrapper.Unwrap()
	}
	return false
}
