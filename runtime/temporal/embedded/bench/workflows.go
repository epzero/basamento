package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"
)

const taskQueue = "bench"

func shortOpts() workflow.ActivityOptions {
	return workflow.ActivityOptions{StartToCloseTimeout: 10 * time.Second}
}

// Noop is the cheapest possible activity.
func Noop(_ context.Context, i int) (int, error) { return i, nil }

// Single runs one activity: end-to-end latency per start.
func Single(ctx workflow.Context) (int, error) {
	ctx = workflow.WithActivityOptions(ctx, shortOpts())
	var out int
	err := workflow.ExecuteActivity(ctx, Noop, 1).Get(ctx, &out)
	return out, err
}

// Seq runs n activities one after another.
func Seq(ctx workflow.Context, n int) (int, error) {
	ctx = workflow.WithActivityOptions(ctx, shortOpts())
	sum := 0
	for i := 0; i < n; i++ {
		var out int
		if err := workflow.ExecuteActivity(ctx, Noop, i).Get(ctx, &out); err != nil {
			return 0, err
		}
		sum += out
	}
	return sum, nil
}

// Fan runs n activities in parallel.
func Fan(ctx workflow.Context, n int) (int, error) {
	ctx = workflow.WithActivityOptions(ctx, shortOpts())
	fs := make([]workflow.Future, n)
	for i := range fs {
		fs[i] = workflow.ExecuteActivity(ctx, Noop, i)
	}
	sum := 0
	for _, f := range fs {
		var out int
		if err := f.Get(ctx, &out); err != nil {
			return 0, err
		}
		sum += out
	}
	return sum, nil
}

// Timer sleeps on a durable timer.
func Timer(ctx workflow.Context, d time.Duration) error { return workflow.Sleep(ctx, d) }

// Gate waits for a validated "approve" update (our gate node), as in REQ-FLOW-006.
func Gate(ctx workflow.Context) (string, error) {
	approvedBy := ""
	err := workflow.SetUpdateHandlerWithOptions(ctx, "approve",
		func(ctx workflow.Context, who string) (string, error) {
			approvedBy = who
			return "accepted:" + who, nil
		},
		workflow.UpdateHandlerOptions{Validator: func(ctx workflow.Context, who string) error {
			if who == "" || who == "bad" {
				return errors.New("invalid payload")
			}
			return nil
		}})
	if err != nil {
		return "", err
	}
	if err := workflow.Await(ctx, func() bool { return approvedBy != "" }); err != nil {
		return "", err
	}
	return approvedBy, nil
}

// Long runs total activities, continuing as new every batch to bound history.
func Long(ctx workflow.Context, total, batch, done int) (int, error) {
	ctx = workflow.WithActivityOptions(ctx, shortOpts())
	for i := 0; i < batch && done < total; i++ {
		if err := workflow.ExecuteActivity(ctx, Noop, done).Get(ctx, nil); err != nil {
			return 0, err
		}
		done++
	}
	if done < total {
		return 0, workflow.NewContinueAsNewError(ctx, Long, total, batch, done)
	}
	return done, nil
}

// StepLog is the activity of the crash test: it appends start/done lines to a file.
type StepLog struct{ Path string }

// Step records its start, works with heartbeats, then records completion.
func (s *StepLog) Step(ctx context.Context, i int) error {
	info := activity.GetInfo(ctx)
	appendLine(s.Path, fmt.Sprintf("start %d attempt %d", i, info.Attempt))
	deadline := time.Now().Add(250 * time.Millisecond)
	for time.Now().Before(deadline) {
		activity.RecordHeartbeat(ctx, i)
		time.Sleep(50 * time.Millisecond)
	}
	appendLine(s.Path, fmt.Sprintf("done %d", i))
	return nil
}

// Chain runs n steps sequentially (crash-recovery test).
func Chain(ctx workflow.Context, n int) (int, error) {
	ctx = workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		StartToCloseTimeout: 30 * time.Second,
		HeartbeatTimeout:    2 * time.Second,
		RetryPolicy:         &temporal.RetryPolicy{InitialInterval: 200 * time.Millisecond},
	})
	var s *StepLog
	for i := 0; i < n; i++ {
		if err := workflow.ExecuteActivity(ctx, s.Step, i).Get(ctx, nil); err != nil {
			return i, err
		}
	}
	return n, nil
}

// ChainV2 schedules a different activity: used to prove replay detects nondeterminism.
func ChainV2(ctx workflow.Context, n int) (int, error) {
	ctx = workflow.WithActivityOptions(ctx, shortOpts())
	for i := 0; i < n; i++ {
		if err := workflow.ExecuteActivity(ctx, Noop, i).Get(ctx, nil); err != nil {
			return i, err
		}
	}
	return n, nil
}

func appendLine(path, line string) {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		panic(err)
	}
	defer func() { _ = f.Close() }()
	if _, err := f.WriteString(line + "\n"); err != nil {
		panic(err)
	}
	_ = f.Sync()
}
