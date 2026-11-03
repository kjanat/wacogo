package canon

import (
	"fmt"
)

// Trap is a recoverable panic value carrying a trap message and optional
// cause. Call-path entry points recover and convert to either a Go error
// (Func.Call) or a wazero trap (adapterFunc.Call).
type Trap struct {
	msg   string
	cause error
}

func (t *Trap) Error() string { return t.msg }

func (t *Trap) Unwrap() error { return t.cause }

func fmtSprintf(format string, args ...any) string { return fmt.Sprintf(format, args...) }
