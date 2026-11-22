package temporal

import (
	"context"
	"errors"
	"strings"
	"testing"

	enumspb "go.temporal.io/api/enums/v1"
	"go.temporal.io/api/serviceerror"

	"github.com/epzero/basamento/core"
)

func TestServeSearchAttributeTable(t *testing.T) {
	t.Run("absent-adds-keyword", func(t *testing.T) {
		fc := &fakeClient{}
		fc.opSvc = &fakeOperatorService{parent: fc}
		fw := &fakeWorker{started: make(chan struct{})}
		rt := testServeRuntime(t, fc, fw)
		ctx, cancel := context.WithCancel(context.Background())
		started := fw.started
		done := make(chan error, 1)
		go func() { done <- rt.Serve(ctx) }()
		waitWorkerStarted(t, started, done)
		if fc.operatorAddCalls() != 1 {
			t.Fatalf("add calls: %d", fc.operatorAddCalls())
		}
		list := fc.lastListSearchAttributes()
		add := fc.lastAddSearchAttributes()
		if list == nil || list.Namespace != rt.clientNamespace() {
			t.Fatalf("list namespace: %#v", list)
		}
		if add == nil || add.Namespace != rt.clientNamespace() {
			t.Fatalf("add namespace: %#v", add)
		}
		want := map[string]enumspb.IndexedValueType{FlowSearchAttribute: enumspb.INDEXED_VALUE_TYPE_KEYWORD}
		if len(add.SearchAttributes) != 1 || add.SearchAttributes[FlowSearchAttribute] != want[FlowSearchAttribute] {
			t.Fatalf("add attrs: %#v", add.SearchAttributes)
		}
		cancel()
		if err := <-done; err != nil {
			t.Fatalf("Serve: %v", err)
		}
	})

	t.Run("present-keyword-skip-add", func(t *testing.T) {
		fc := &fakeClient{}
		fc.opSvc = &fakeOperatorService{parent: fc, custom: map[string]enumspb.IndexedValueType{
			FlowSearchAttribute: enumspb.INDEXED_VALUE_TYPE_KEYWORD,
		}}
		fw := &fakeWorker{started: make(chan struct{})}
		rt := testServeRuntime(t, fc, fw)
		ctx, cancel := context.WithCancel(context.Background())
		started := fw.started
		done := make(chan error, 1)
		go func() { done <- rt.Serve(ctx) }()
		waitWorkerStarted(t, started, done)
		if fc.operatorAddCalls() != 0 {
			t.Fatalf("add calls: %d", fc.operatorAddCalls())
		}
		cancel()
		if err := <-done; err != nil {
			t.Fatalf("Serve: %v", err)
		}
	})

	t.Run("wrong-type-conflict", func(t *testing.T) {
		fc := &fakeClient{}
		fc.opSvc = &fakeOperatorService{parent: fc, custom: map[string]enumspb.IndexedValueType{
			FlowSearchAttribute: enumspb.INDEXED_VALUE_TYPE_INT,
		}}
		fw := &fakeWorker{started: make(chan struct{})}
		rt := testServeRuntime(t, fc, fw)
		started := fw.started
		done := make(chan error, 1)
		go func() { done <- rt.Serve(context.Background()) }()
		var err error
		select {
		case err = <-done:
		case <-started:
			t.Fatal("worker started with wrong search attribute type")
		}
		if core.CodeOf(err) != core.CodeConflict {
			t.Fatalf("got %v", err)
		}
		if fc.CloseCount() != 1 {
			t.Fatalf("close %d", fc.CloseCount())
		}
	})

	t.Run("add-already-exists-continues", func(t *testing.T) {
		fc := &fakeClient{}
		fc.opSvc = &fakeOperatorService{parent: fc, addErr: &serviceerror.AlreadyExists{}}
		fw := &fakeWorker{started: make(chan struct{})}
		rt := testServeRuntime(t, fc, fw)
		ctx, cancel := context.WithCancel(context.Background())
		started := fw.started
		done := make(chan error, 1)
		go func() { done <- rt.Serve(ctx) }()
		select {
		case err := <-done:
			t.Fatalf("Serve should tolerate AlreadyExists, got %v", err)
		case <-started:
		}
		cancel()
		if err := <-done; err != nil {
			t.Fatalf("Serve: %v", err)
		}
	})

	t.Run("add-permission-denied", func(t *testing.T) {
		fc := &fakeClient{}
		fc.opSvc = &fakeOperatorService{parent: fc, addErr: &serviceerror.PermissionDenied{}}
		fw := &fakeWorker{}
		rt := testServeRuntime(t, fc, fw)
		err := rt.Serve(context.Background())
		if core.CodeOf(err) != core.CodePermissionDenied {
			t.Fatalf("got %v", err)
		}
		var pe *core.Error
		if !errors.As(err, &pe) {
			t.Fatal(err)
		}
		if !strings.Contains(pe.Message, "Keyword") {
			t.Fatalf("message: %q", pe.Message)
		}
		_, _, _, _, starts, _ := fw.snapshot()
		if starts != 0 {
			t.Fatal("worker started")
		}
	})

	t.Run("list-error-mapped", func(t *testing.T) {
		fc := &fakeClient{}
		fc.opSvc = &fakeOperatorService{parent: fc, listErr: &serviceerror.Unavailable{}}
		fw := &fakeWorker{}
		rt := testServeRuntime(t, fc, fw)
		err := rt.Serve(context.Background())
		if core.CodeOf(err) != core.CodeUnavailable {
			t.Fatalf("got %v", err)
		}
		_, _, _, _, starts, _ := fw.snapshot()
		if starts != 0 {
			t.Fatal("worker started")
		}
	})
}
