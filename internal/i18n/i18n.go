// Paquete i18n: textos de la interfaz del quiosco.
//
// El castellano (es.json) está embebido y es la base: cualquier clave que falte en
// otro idioma se toma de él. Para añadir un idioma basta con crear <código>.json
// junto a es.json (queda embebido al compilar) o, sin recompilar, colocarlo en la
// carpeta «i18n» del directorio de datos, que tiene prioridad.
package i18n

import (
	"embed"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

//go:embed *.json
var files embed.FS

// Texts es un conjunto de textos por clave.
type Texts map[string]string

var langRe = regexp.MustCompile(`^[a-z]{2,3}(-[a-z0-9]{2,8})?$`)

func parse(data []byte, into Texts) error {
	var m map[string]string
	if err := json.Unmarshal(data, &m); err != nil {
		return err
	}
	for k, v := range m {
		into[k] = v
	}
	return nil
}

// Load devuelve los textos de lang. dataDir puede estar vacío.
// Devuelve un error solo si un fichero existe pero está mal formado
// (los textos devueltos siguen siendo utilizables).
func Load(lang, dataDir string) (Texts, error) {
	t := Texts{}
	base, _ := files.ReadFile("es.json")
	if err := parse(base, t); err != nil {
		panic("i18n: es.json embebido no válido: " + err.Error())
	}
	lang = strings.ToLower(lang)
	if !langRe.MatchString(lang) {
		return t, nil
	}
	var firstErr error
	if lang != "es" {
		if data, err := files.ReadFile(lang + ".json"); err == nil {
			if err := parse(data, t); err != nil {
				firstErr = err
			}
		}
	}
	if dataDir != "" {
		if data, err := os.ReadFile(filepath.Join(dataDir, "i18n", lang+".json")); err == nil {
			if err := parse(data, t); err != nil && firstErr == nil {
				firstErr = err
			}
		}
	}
	return t, firstErr
}

// Available lista los idiomas disponibles (embebidos y en dataDir/i18n).
func Available(dataDir string) []string {
	set := map[string]bool{}
	entries, _ := files.ReadDir(".")
	for _, e := range entries {
		set[strings.TrimSuffix(e.Name(), ".json")] = true
	}
	if dataDir != "" {
		if entries, err := os.ReadDir(filepath.Join(dataDir, "i18n")); err == nil {
			for _, e := range entries {
				if n := strings.TrimSuffix(e.Name(), ".json"); n != e.Name() && langRe.MatchString(n) {
					set[n] = true
				}
			}
		}
	}
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// T devuelve el texto de key sustituyendo {nombre} por los pares (nombre, valor).
func (t Texts) T(key string, pairs ...string) string {
	s, ok := t[key]
	if !ok {
		return key
	}
	for i := 0; i+1 < len(pairs); i += 2 {
		s = strings.ReplaceAll(s, "{"+pairs[i]+"}", pairs[i+1])
	}
	return s
}

// Duration expresa una cantidad de minutos en palabras («4 horas», «1 hora y 30 minutos»).
func (t Texts) Duration(minutes int) string {
	unit := func(n int, one, many string) string {
		if n == 1 {
			return t.T(one)
		}
		return t.T(many, "n", strconv.Itoa(n))
	}
	switch {
	case minutes >= 1440 && minutes%1440 == 0:
		return unit(minutes/1440, "dur_dia", "dur_dias")
	case minutes >= 60:
		h, m := minutes/60, minutes%60
		hs := unit(h, "dur_hora", "dur_horas")
		if m == 0 {
			return hs
		}
		return t.T("dur_y", "a", hs, "b", unit(m, "dur_minuto", "dur_minutos"))
	default:
		return unit(minutes, "dur_minuto", "dur_minutos")
	}
}
