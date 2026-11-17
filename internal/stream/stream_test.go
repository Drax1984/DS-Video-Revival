package stream

import "testing"

func TestStreamIsolation(t *testing.T) {
	m := NewManager()
	s := m.Open("anna", 1, "/x.mp4", "raw", "")
	if _, err := m.Get(s.ID, "bernd"); err == nil {
		t.Fatal("fremder Benutzer darf Stream nicht nutzen")
	}
	if _, err := m.Get(s.ID, "anna"); err != nil {
		t.Fatal(err)
	}
	if m.Close(s.ID, "bernd") {
		t.Fatal("fremder Benutzer darf Stream nicht schließen")
	}
	if !m.Close(s.ID, "anna") || m.Count() != 0 {
		t.Fatal("Close")
	}
	if len(NewStreamID()) != 41 {
		t.Fatal("Stream-ID-Format")
	}
}
