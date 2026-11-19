package messages

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/RCRaceHub/EventScreenDisplay/internal/jsonx"
)

func body(t *testing.T, s string) jsonx.Object {
	t.Helper()
	o, err := jsonx.Decode([]byte(s))
	if err != nil {
		t.Fatal(err)
	}
	return o
}

func TestParseMessageDefaults(t *testing.T) {
	m, err := ParseMessage(body(t, `{"title":"Finals being calculated"}`), 10)
	if err != nil || m.Style != "hold" || m.Level != "info" || m.Text != "" || m.Until != nil {
		t.Errorf("got %+v %v", m, err)
	}
	m, err = ParseMessage(body(t, `{"title":"Finals are up","style":"announce","duration":60}`), 10)
	if err != nil || *m.Until != 70 {
		t.Errorf("got %+v %v", m, err)
	}
}

func TestParseRejects(t *testing.T) {
	for _, b := range []string{`{}`, `{"title":""}`, `{"title":"` + strings.Repeat("x", 121) + `"}`,
		`{"title":"a","style":"flash"}`, `{"title":"a","level":"red"}`, `{"title":"a","duration":0}`,
		`{"title":"a","duration":"60"}`, `{"title":5}`, `{"title":"a","duration":true}`} {
		_, err := ParseMessage(body(t, b), 0)
		var me *Error
		if !errors.As(err, &me) {
			t.Errorf("%s: got %v", b, err)
		}
	}
	if _, err := ParseBanner(body(t, `{"text":" "}`), 0); err == nil {
		t.Error("blank banner accepted")
	}
}

func TestExpiryAndPersistence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "messages.json")
	now := 1000.0
	clock := func() float64 { return now }
	msgs := New(path, clock)
	calls := 0
	msgs.OnChange(func() { calls++ })
	if _, err := msgs.SetMessage(body(t, `{"title":"Hold","duration":30}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := msgs.SetBanner(body(t, `{"text":"Car 12 to the pits"}`)); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Errorf("listeners called %d times", calls)
	}
	// Survives a restart.
	again := New(path, clock)
	if again.Message().Title != "Hold" || again.Banner().Text != "Car 12 to the pits" {
		t.Error("not restored")
	}
	now += 30
	if msgs.Message() != nil || msgs.Banner() == nil {
		t.Error("expiry wrong")
	}
	// Expired messages aren't brought back after a restart.
	if New(path, clock).Message() != nil {
		t.Error("expired message restored")
	}
	msgs.ClearBanner()
	if New(path, clock).Banner() != nil {
		t.Error("cleared banner restored")
	}
}

func TestDamagedFileIsIgnored(t *testing.T) {
	path := filepath.Join(t.TempDir(), "messages.json")
	_ = os.WriteFile(path, []byte("{not json"), 0o644)
	if New(path, nil).Message() != nil {
		t.Error("message from damaged file")
	}
}

func TestJSONShape(t *testing.T) {
	m, _ := ParseMessage(body(t, `{"title":"Hold"}`), 5)
	data, _ := json.Marshal(m)
	if string(data) != `{"style":"hold","title":"Hold","text":"","level":"info","since":5,"until":null}` {
		t.Errorf("got %s", data)
	}
}
