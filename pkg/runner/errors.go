package runner

import "fmt"

// StageError attributes a Pipeline failure to a named stage ("worker",
// "verification", "no-op", "review", "review-regression") so callers can
// give a short, stable reason without parsing prose error chains.
type StageError struct {
	Stage string
	Err   error
}

func (e *StageError) Error() string { return e.Err.Error() }
func (e *StageError) Unwrap() error { return e.Err }

// RollbackFailedError means Pipeline's own automatic rollback did not
// complete. The repository may be left partially changed — callers must not
// claim it was safely restored.
type RollbackFailedError struct {
	Cause, Rollback error
}

func (e *RollbackFailedError) Error() string {
	return fmt.Sprintf("%v; automatic rollback also failed: %v", e.Cause, e.Rollback)
}

func (e *RollbackFailedError) Unwrap() []error { return []error{e.Cause, e.Rollback} }
