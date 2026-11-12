package model

import (
	"reflect"
	"testing"
)

func TestTextArrayRoundTrip(t *testing.T) {
	for _, values := range []TextArray{nil, {}, {"OpenAI"}, {"具身智能", `引号"`, `逗号,`, `反\斜杠`, "{花括号}", "", "NULL"}} {
		encoded, err := values.Value()
		if err != nil {
			t.Fatal(err)
		}
		if len(values) == 0 && encoded != "{}" {
			t.Fatalf("empty array encoded as %v", encoded)
		}
		for _, source := range []any{encoded, []byte(encoded.(string))} {
			var decoded TextArray
			if err := decoded.Scan(source); err != nil {
				t.Fatal(err)
			}
			want := values
			if want == nil {
				want = TextArray{}
			}
			if !reflect.DeepEqual(decoded, want) {
				t.Fatalf("decoded %q, want %q", decoded, want)
			}
		}
	}
}

func TestTextArrayScansPostgresValues(t *testing.T) {
	value := TextArray{"old"}
	if err := value.Scan("{OpenAI,Go}"); err != nil || !reflect.DeepEqual(value, TextArray{"OpenAI", "Go"}) {
		t.Fatalf("scan PostgreSQL literal = %q, err=%v", value, err)
	}
	if err := value.Scan(nil); err != nil || value != nil {
		t.Fatalf("scan NULL = %q, err=%v", value, err)
	}
	if err := value.Scan("not an array"); err == nil {
		t.Fatal("invalid PostgreSQL array accepted")
	}
}
