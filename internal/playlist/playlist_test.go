package playlist

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func decode(t *testing.T, s string) any {
	t.Helper()
	dec := json.NewDecoder(strings.NewReader(s))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		t.Fatal(err)
	}
	return v
}

func TestParseItemsTakesStringsAndObjects(t *testing.T) {
	items, err := ParseItems(decode(t, `["http://a.example/", {"url":"https://b.example/x","duration":12,"label":"B","id":"b"}]`), 30, 5)
	if err != nil {
		t.Fatal(err)
	}
	want := []Item{{"http://a.example/", 30, "", ""}, {"https://b.example/x", 12, "B", "b"}}
	if !reflect.DeepEqual(items, want) {
		t.Errorf("got %+v", items)
	}
}

func TestParseItemsRejectsBadInput(t *testing.T) {
	for _, data := range []string{
		`"not a list"`,
		`[""]`,
		`["ftp://a.example/"]`,
		`["/relative"]`,
		`["javascript:alert(1)"]`,
		`[{"url":"http://a.example/","duration":1}]`,
		`[{"url":"http://a.example/","duration":"30"}]`,
		`[{"url":"http://a.example/","duration":true}]`,
		`[42]`,
	} {
		_, err := ParseItems(decode(t, data), 30, 5)
		var pe *Error
		if !errors.As(err, &pe) {
			t.Errorf("%s: got %v", data, err)
		}
	}
}

func TestParseItemsErrorNamesTheItem(t *testing.T) {
	_, err := ParseItems(decode(t, `["http://a.example/", "nope"]`), 30, 5)
	if err == nil || !strings.Contains(err.Error(), "items[1]") {
		t.Errorf("got %v", err)
	}
}

func TestFractionalDurationIsTruncated(t *testing.T) {
	items, err := ParseItems(decode(t, `[{"url":"http://a/","duration":7.9}]`), 30, 5)
	if err != nil || items[0].Duration != 7 {
		t.Errorf("got %+v %v", items, err)
	}
}

func store(t *testing.T) Store {
	return Store{Path: filepath.Join(t.TempDir(), "sub", "playlist.json")}
}

func TestMissingFileIsEmpty(t *testing.T) {
	if pl := store(t).Load(); len(pl.Items) != 0 || pl.Items == nil {
		t.Errorf("got %+v", pl)
	}
}

func TestRoundTrip(t *testing.T) {
	s := store(t)
	pl := Replaced(Playlist{}, []Item{{URL: "http://a.example/", Duration: 10, Label: "A"}}, "event-7")
	if err := s.Save(pl); err != nil {
		t.Fatal(err)
	}
	got := s.Load()
	if !reflect.DeepEqual(got.Items, pl.Items) || got.Revision != 1 || got.Source != "event-7" || got.UpdatedAt != pl.UpdatedAt {
		t.Errorf("got %+v", got)
	}
	if _, err := os.Stat(strings.TrimSuffix(s.Path, ".json") + ".tmp"); !os.IsNotExist(err) {
		t.Error("temp file left behind")
	}
}

func TestReadsPythonAgentsFile(t *testing.T) {
	// Upgrading from the Python agent keeps the playlist.
	s := store(t)
	_ = os.MkdirAll(filepath.Dir(s.Path), 0o755)
	data := `{"items": [{"url": "http://a/", "duration": 30, "label": "", "id": ""}], "revision": 4, "updated_at": 1700000000.5, "source": "rctc"}`
	_ = os.WriteFile(s.Path, []byte(data), 0o644)
	if pl := s.Load(); pl.Revision != 4 || pl.Items[0].URL != "http://a/" || pl.Source != "rctc" {
		t.Errorf("got %+v", pl)
	}
}

func TestDamagedFileStartsEmpty(t *testing.T) {
	s := store(t)
	_ = os.MkdirAll(filepath.Dir(s.Path), 0o755)
	_ = os.WriteFile(s.Path, []byte(`{"items": [{"url": "http://a.exa`), 0o644) // cut off by a power cut
	if pl := s.Load(); len(pl.Items) != 0 {
		t.Errorf("got %+v", pl)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(s.Path), "playlist.bad")); err != nil {
		t.Errorf("damaged file not kept: %v", err)
	}
}
