package rom

import "testing"

func TestFindRequests(t *testing.T) {
	text := []byte("BS/UART.PS.EK/0/4096/UL_START\r\n" +
		"noise\r\n" +
		"BS/UART.PS.EK/4096/55296/UL_START\r\n")
	rs := FindRequests(text)
	if len(rs) != 2 {
		t.Fatalf("got %d requests: %+v", len(rs), rs)
	}
	if rs[0] != (Request{0, 4096}) || rs[1] != (Request{4096, 55296}) {
		t.Fatalf("unexpected: %+v", rs)
	}
	last, ok := LastRequest(text)
	if !ok || last != rs[1] {
		t.Fatalf("last = %+v, %v", last, ok)
	}
}

func TestFindRequestSplitLikeConsole(t *testing.T) {
	// The BootROM concatenates status text and the EK line without a newline.
	text := []byte("WD.URPL.BS/UART.PS.EK/0/4096/UL_START\r\n")
	rs := FindRequests(text)
	if len(rs) != 1 || rs[0] != (Request{0, 4096}) {
		t.Fatalf("unexpected: %+v", rs)
	}
}

func TestValidate(t *testing.T) {
	if err := (Request{0, 4096}).Validate(303616); err != nil {
		t.Fatal(err)
	}
	bad := []Request{{-1, 1}, {0, 0}, {303000, 1000}, {400000, 1}}
	for _, r := range bad {
		if err := r.Validate(303616); err == nil {
			t.Fatalf("%+v accepted", r)
		}
	}
}
