package cli

import (
	"errors"

	"github.com/zachbornheimer/ai-task/internal/fault"
)

func isUsage(err error, target **usageErr) bool    { return errors.As(err, target) }
func asFault(err error, target **fault.Error) bool { return errors.As(err, target) }
