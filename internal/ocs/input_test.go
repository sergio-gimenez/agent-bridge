package ocs

import (
	"os"
	"testing"
	"time"
)

// After stop, the reader must not take input meant for whatever reads next.
func TestReadKeysStopsReading(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()
	input, stop := readKeys(int(r.Fd()))
	if _, err := w.WriteString("a"); err != nil {
		t.Fatal(err)
	}
	select {
	case chunk := <-input:
		if string(chunk) != "a" {
			t.Fatalf("got %q", chunk)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no input read")
	}
	stop()
	if _, err := w.WriteString("next line\n"); err != nil {
		t.Fatal(err)
	}
	buffer := make([]byte, 64)
	n, err := r.Read(buffer)
	if err != nil || string(buffer[:n]) != "next line\n" {
		t.Fatalf("the next reader got %q, %v", buffer[:n], err)
	}
}
