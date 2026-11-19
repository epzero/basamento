package secrets

import (
	"testing"

	"github.com/epzero/basamento/core"
)

func TestNewCodecRejectsBadConfiguration(t *testing.T) {
	t.Parallel()
	store := &memKeysetStore{data: map[string]StoredKeyset{}}
	ok := &budgetKey{id: "mk"}
	cases := []struct {
		name   string
		store  KeysetStore
		keys   []KeyService
		wantOK bool
	}{
		{name: "nil store", store: nil, keys: []KeyService{ok}, wantOK: false},
		{name: "empty keys", store: store, keys: nil, wantOK: false},
		{name: "nil key", store: store, keys: []KeyService{nil}, wantOK: false},
		{name: "duplicate ids", store: store, keys: []KeyService{&budgetKey{id: "a"}, &budgetKey{id: "a"}}, wantOK: false},
		{name: "valid", store: store, keys: []KeyService{ok}, wantOK: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := NewCodec(tc.store, tc.keys)
			if tc.wantOK {
				if err != nil {
					t.Fatalf("err=%v", err)
				}
				return
			}
			if err == nil || core.CodeOf(err) != core.CodeInvalidArgument {
				t.Fatalf("err=%v", err)
			}
		})
	}
}
