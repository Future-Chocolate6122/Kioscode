package i18n

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCastellano(t *testing.T) {
	tx, err := Load("es", "")
	if err != nil {
		t.Fatal(err)
	}
	if got := tx.T("validez", "duracion", "4 horas"); got != "Válido durante 4 horas" {
		t.Error(got)
	}
	if tx.T("no_existe") != "no_existe" {
		t.Error("una clave desconocida se devuelve tal cual")
	}
}

func TestDuracion(t *testing.T) {
	tx, _ := Load("es", "")
	casos := map[int]string{1: "1 minuto", 45: "45 minutos", 60: "1 hora", 240: "4 horas", 90: "1 hora y 30 minutos", 1440: "1 día", 4320: "3 días", 1500: "25 horas"}
	for m, want := range casos {
		if got := tx.Duration(m); got != want {
			t.Errorf("%d: %q, want %q", m, got, want)
		}
	}
}

func TestIdiomaDesdeDirectorioDeDatos(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "i18n"), 0o755)
	os.WriteFile(filepath.Join(dir, "i18n", "en.json"), []byte(`{"boton_empezar":"Start"}`), 0o644)
	tx, err := Load("en", dir)
	if err != nil {
		t.Fatal(err)
	}
	if tx.T("boton_empezar") != "Start" {
		t.Error("no se ha aplicado el idioma del directorio de datos")
	}
	if tx.T("boton_terminado") != "He terminado" {
		t.Error("las claves que faltan deben tomarse del castellano")
	}
	av := Available(dir)
	if len(av) != 2 || av[0] != "en" || av[1] != "es" {
		t.Errorf("Available = %v", av)
	}
	// Un código de idioma con rutas no debe leer ficheros fuera.
	if tx, _ := Load("../x", dir); tx.T("boton_empezar") != "Empezar" {
		t.Error("código de idioma no válido")
	}
}
