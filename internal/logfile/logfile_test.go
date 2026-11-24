package logfile

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRotacion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "kioscode.log")
	r, err := Open(path, 100, 2)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	line := strings.Repeat("x", 39) + "\n" // 40 bytes
	for i := 0; i < 10; i++ {
		if _, err := r.Write([]byte(line)); err != nil {
			t.Fatal(err)
		}
	}
	for _, p := range []string{path, path + ".1", path + ".2"} {
		fi, err := os.Stat(p)
		if err != nil {
			t.Fatalf("falta %s", p)
		}
		if fi.Size() > 100 {
			t.Errorf("%s supera el tamaño máximo: %d", p, fi.Size())
		}
	}
	if _, err := os.Stat(path + ".3"); err == nil {
		t.Error("solo deben conservarse 2 ficheros antiguos")
	}
}
