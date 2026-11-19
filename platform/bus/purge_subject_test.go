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
	lastB, err := st.LastSeq(ctx, subB)
	if err != nil || lastB != 4 {
		t.Fatalf("subject B last=%d err=%v", lastB, err)
	}
	for _, tc := range []struct {
		seq  uint64
		want string
	}{
		{2, "b1"},
		{4, "b2"},
	} {
		raw, err := st.stream.GetMsg(ctx, tc.seq)
		if err != nil {
			t.Fatalf("stream seq %d missing after purge of A: %v", tc.seq, err)
		}
		if raw.Subject != wireSubject(subB) {
			t.Fatalf("stream seq %d subject %q want %q", tc.seq, raw.Subject, wireSubject(subB))
		}
		if string(raw.Data) != tc.want {
			t.Fatalf("stream seq %d data %q want %q", tc.seq, raw.Data, tc.want)
		}
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
