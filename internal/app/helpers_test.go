package app_test

import (
	"errors"

	"github.com/zachbornheimer/ai-task/internal/execution"
	"github.com/zachbornheimer/ai-task/internal/fault"
)

type executionLogEntry = execution.LogEntry

func asFaultErr(err error, target **fault.Error) bool { return errors.As(err, target) }
