package downloader

import (
	"os"
	"testing"
)

// newTestDL builds a Downloader without starting the worker, so queue state is
// deterministic for assertions.
func newTestDL() *Downloader {
	return &Downloader{jobs: map[string]*Job{}, wake: make(chan struct{}, 1)}
}

func TestEnqueueDedupAndUniqueIDs(t *testing.T) {
	d := newTestDL()
	id1, ok1 := d.addLocked("https://x/1")
	id2, ok2 := d.addLocked("https://x/1") // duplicate of /1 (still queued)
	id3, ok3 := d.addLocked("https://x/2")
	if !ok1 || ok2 || !ok3 {
		t.Fatalf("dedup wrong: ok1=%v ok2=%v ok3=%v", ok1, ok2, ok3)
	}
	if id2 != id1 {
		t.Fatalf("duplicate URL should return the existing id, got %s vs %s", id2, id1)
	}
	if id1 == id3 {
		t.Fatalf("distinct jobs must get distinct ids (collision bug), both %s", id1)
	}
	if len(d.order) != 2 {
		t.Fatalf("expected 2 queued jobs, got %d", len(d.order))
	}
}

func TestValidCookieFile(t *testing.T) {
	dir := t.TempDir()
	write := func(name string, b []byte) string {
		p := dir + "/" + name
		if err := os.WriteFile(p, b, 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	cases := []struct {
		name string
		path string
		want bool
	}{
		{"header", write("ok.txt", []byte("# Netscape HTTP Cookie File\n.pornhub.com\tTRUE\t/\tTRUE\t0\tk\tv\n")), true},
		{"bare cookie line", write("bare.txt", []byte(".x.com\tTRUE\t/\tTRUE\t0\tk\tv\n")), true},
		{"null-filled (the corruption)", write("null.txt", make([]byte, 256)), false},
		{"empty", write("empty.txt", []byte("")), false},
		{"junk text", write("junk.txt", []byte("not a cookie file at all")), false},
		{"missing", dir + "/nope.txt", false},
	}
	for _, c := range cases {
		if got := validCookieFile(c.path); got != c.want {
			t.Errorf("%s: validCookieFile=%v, want %v", c.name, got, c.want)
		}
	}
}

func TestEnqueueManyDedups(t *testing.T) {
	d := newTestDL()
	added := d.EnqueueMany([]string{"a", "b", "a", "", "c", "b"}) // 3 unique, 1 blank, 2 dups
	if added != 3 {
		t.Fatalf("expected 3 newly-queued, got %d", added)
	}
	if n := len(d.Snapshot()); n != 3 {
		t.Fatalf("queue should hold 3, got %d", n)
	}
}
