package jump

import "testing"

func TestITerm2FocusByTTY_ReportsLocation(t *testing.T) {
	stubOsascript(t, "ok|2|3|1|4\n", nil) // window #2, tab 3, pane 1 of 4
	loc, found, err := ITerm2FocusLocate("/dev/ttys003")
	if err != nil || !found {
		t.Fatalf("found=%v err=%v", found, err)
	}
	if loc != "tab 3 · pane 1 of 4" {
		t.Errorf("loc = %q", loc)
	}
	stubOsascript(t, "missing\n", nil)
	if _, found, _ := ITerm2FocusLocate("/dev/ttys009"); found {
		t.Error("missing pane reported as found")
	}
}
