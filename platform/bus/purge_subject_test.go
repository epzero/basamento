package bus

import (
	"context"
	"testing"
)

func TestPurgeSubjectScoped(t *testing.T) {
	st, _, _ := openTestEmbedded(t)
	ctx := context.Background()
	subA := "run.r1.msg.a"
	subB := "run.r1.msg.b"
	if _, err := st.Publish(ctx, subA, []byte("a1")); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Publish(ctx, subB, []byte("b1")); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Publish(ctx, subA, []byte("a2")); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Publish(ctx, subB, []byte("b2")); err != nil {
		t.Fatal(err)
	}
	if err := st.Purge(ctx, subA); err != nil {
		t.Fatal(err)
	}
	ch, err := st.Subscribe(ctx, subB, 0)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for i := 0; i < 2; i++ {
		m, ok := <-ch
		if !ok {
			t.Fatalf("only %d", i)
		}
		got = append(got, string(m.Data))
	}
	if got[0] != "b1" || got[1] != "b2" {
		t.Fatalf("got %v", got)
	}
}
