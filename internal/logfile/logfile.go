// Paquete logfile: fichero de log con rotación por tamaño, sin dependencias.
package logfile

import (
	"fmt"
	"os"
	"sync"
)

// Rotating escribe en path y, al superar MaxBytes, lo renombra a path.1
// (y path.1 a path.2, etc.), conservando Keep ficheros antiguos.
type Rotating struct {
	mu       sync.Mutex
	path     string
	MaxBytes int64
	Keep     int
	f        *os.File
	size     int64
}

// Open abre (o crea) el fichero de log.
func Open(path string, maxBytes int64, keep int) (*Rotating, error) {
	r := &Rotating{path: path, MaxBytes: maxBytes, Keep: keep}
	if err := r.open(); err != nil {
		return nil, err
	}
	return r, nil
}

func (r *Rotating) open() error {
	f, err := os.OpenFile(r.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o640)
	if err != nil {
		return err
	}
	fi, err := f.Stat()
	if err != nil {
		f.Close()
		return err
	}
	r.f, r.size = f, fi.Size()
	return nil
}

func (r *Rotating) rotate() error {
	r.f.Close()
	for i := r.Keep - 1; i >= 1; i-- {
		os.Rename(fmt.Sprintf("%s.%d", r.path, i), fmt.Sprintf("%s.%d", r.path, i+1)) //nolint:errcheck
	}
	if r.Keep > 0 {
		os.Rename(r.path, r.path+".1") //nolint:errcheck
	} else {
		os.Remove(r.path) //nolint:errcheck
	}
	return r.open()
}

// Write implementa io.Writer.
func (r *Rotating) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.f == nil {
		return 0, os.ErrClosed
	}
	if r.size+int64(len(p)) > r.MaxBytes && r.size > 0 {
		if err := r.rotate(); err != nil {
			return 0, err
		}
	}
	n, err := r.f.Write(p)
	r.size += int64(n)
	return n, err
}

// Close cierra el fichero.
func (r *Rotating) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.f == nil {
		return nil
	}
	err := r.f.Close()
	r.f = nil
	return err
}
