package app

import (
	"errors"

	"github.com/zachbornheimer/ai-task/internal/fault"
)

func asFault(err error, target **fault.Error) bool { return errors.As(err, target) }
