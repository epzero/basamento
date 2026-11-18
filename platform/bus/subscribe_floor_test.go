package bus

import (
	"errors"
	"testing"

	"github.com/epzero/basamento/core"
)

func TestFloorAfterConsumerRejectsRaisedFloor(t *testing.T) {
	err := floorAfterConsumer(2, func() (uint64, error) { return 5, nil })
	if err == nil {
		t.Fatal("expected purged")
	}
	if string(core.CodeOf(err)) != string(core.CodeNotFound) {
		t.Fatalf("code %s", core.CodeOf(err))
	}
}

func TestFloorAfterConsumerAcceptsAtFloor(t *testing.T) {
	if err := floorAfterConsumer(5, func() (uint64, error) { return 5, nil }); err != nil {
		t.Fatal(err)
	}
	if err := floorAfterConsumer(0, func() (uint64, error) { return 5, nil }); err != nil {
		t.Fatal(err)
	}
}

func TestFloorAfterConsumerPropagatesReadError(t *testing.T) {
	want := errors.New("read failed")
	err := floorAfterConsumer(1, func() (uint64, error) { return 0, want })
	if !errors.Is(err, want) {
		t.Fatalf("got %v", err)
	}
}
