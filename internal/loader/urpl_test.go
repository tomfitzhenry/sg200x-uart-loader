package loader

import (
	"io"
	"testing"
)

func testLoader(t *testing.T) *Loader {
	t.Helper()
	log, err := NewLogger("", false, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	return New(Options{Log: log})
}

func TestObserveURPLDoesNotAnswerEarly(t *testing.T) {
	l := testLoader(t)
	st := &state{}
	l.observeText([]byte("B.SCS/0/0.WD.URPL."), st)
	if !st.urplSeen || st.firstURPL.IsZero() {
		t.Fatalf("early URPL not recorded: %+v", st)
	}
	if st.answerURPL {
		t.Fatal("early URPL must not be answered before the eMMC reset")
	}
}

func TestResetMarkerArmsAndForgetsEarlyURPL(t *testing.T) {
	l := testLoader(t)
	st := &state{}
	l.observeText([]byte("B.SCS/0/0.WD.URPL."), st)
	l.observeText([]byte(" E:Boot failed (8).\r\n E:RESET:plat/mars/platform.c:114\r\n"), st)
	if !st.answerURPL {
		t.Fatal("reset marker did not arm the next prompt")
	}
	if st.urplSeen || !st.firstURPL.IsZero() {
		t.Fatalf("stale prompt survived the reset: %+v", st)
	}
	l.observeText([]byte("B.SCS/0/0.WD.URPL."), st)
	if !st.urplSeen || !st.answerURPL {
		t.Fatalf("post-reset prompt not ready: %+v", st)
	}
}
