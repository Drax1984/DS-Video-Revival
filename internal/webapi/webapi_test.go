package webapi

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestParseJSONFormat(t *testing.T) {
	r := httptest.NewRequest("POST", "/webapi/entry.cgi?api=SYNO.VideoStation2.Streaming", strings.NewReader(`version=1&method=%22open%22&file=%7B%22id%22%3A7%7D&additional=%5B%22file%22%2C%22summary%22%5D&id=%22abc_1%22`))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	q, err := Parse(r)
	if err != nil {
		t.Fatal(err)
	}
	if q.API != "SYNO.VideoStation2.Streaming" || q.Method != "open" || q.Version != 1 {
		t.Fatalf("%+v", q)
	}
	var f struct{ ID int }
	if err := q.JSON("file", &f); err != nil || f.ID != 7 {
		t.Fatal("file", err)
	}
	if l := q.StrList("additional"); len(l) != 2 || l[1] != "summary" {
		t.Fatal(l)
	}
	if q.Str("id") != "abc_1" {
		t.Fatal(q.Str("id"))
	}
}

func TestRegistryLoads(t *testing.T) {
	r := VideoStationAPIs()
	d, ok := r["SYNO.VideoStation2.Streaming"]
	if !ok || d.Path != "entry.cgi" || d.RequestFormat != "JSON" || !d.HasMethod(1, "open") {
		t.Fatalf("%+v", d)
	}
	if e := InfoEntries(r, "SYNO.VideoStation2.Info,SYNO.VideoStation.Info"); len(e) != 2 {
		t.Fatal(len(e))
	}
}
