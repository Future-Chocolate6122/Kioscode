package web

import (
	"os"
	"testing"

	"golang.org/x/crypto/bcrypt"
)

func TestMain(m *testing.M) {
	bcryptCost = bcrypt.MinCost // las pruebas no necesitan un hash lento
	os.Exit(m.Run())
}
