package jsonx

import (
	"testing"
)

func TestDecode(t *testing.T) {
	for _, bad := range []string{"[1]", "nope", `{"a":1} {}`, `"x"`} {
		if _, err := Decode([]byte(bad)); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	if o, err := Decode([]byte("  ")); err != nil || len(o) != 0 {
		t.Errorf("empty body: %v %v", o, err)
	}
}

func TestIntAndFloatFollowPythonRules(t *testing.T) {
	o, _ := Decode([]byte(`{"i":3,"f":1.5,"e":1e3,"b":true,"s":"3","neg":-2}`))
	if v, ok := Int(o["i"]); !ok || v != 3 {
		t.Error("int")
	}
	if v, ok := Int(o["neg"]); !ok || v != -2 {
		t.Error("negative int")
	}
	for _, k := range []string{"f", "e", "b", "s"} {
		if _, ok := Int(o[k]); ok {
			t.Errorf("%s taken as an int", k)
		}
	}
	if v, ok := Float(o["f"]); !ok || v != 1.5 {
		t.Error("float")
	}
	if _, ok := Float(o["b"]); ok {
		t.Error("bool taken as a number")
	}
}

func TestStr(t *testing.T) {
	o, _ := Decode([]byte(`{"s":"x","n":7,"b":false,"z":null,"l":[1]}`))
	want := map[string]string{"s": "x", "n": "7", "b": "False", "z": "None", "l": "[1]"}
	for k, w := range want {
		if got := Str(o[k]); got != w {
			t.Errorf("%s: %q want %q", k, got, w)
		}
	}
}
