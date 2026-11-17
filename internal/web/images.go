package web

// Biblioteca de imágenes para el logotipo y el fondo del quiosco.
//
// Las imágenes se suben desde /admin a <datos>/imagenes/ y se eligen en el formulario.
// Cada tipo tiene su límite de tamaño y sus formatos. El contenido se comprueba de
// verdad (no basta la extensión) y los SVG con scripts o manejadores de eventos se rechazan.

import (
	"bytes"
	"encoding/binary"
	"encoding/xml"
	"errors"
	"fmt"
	"image"
	_ "image/jpeg" // registra el decodificador para leer dimensiones
	_ "image/png"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"totem-wifi/internal/config"
)

const (
	imagesDir     = "imagenes"
	MaxLogoBytes  = 1 << 20 // 1 MB
	MaxFondoBytes = 5 << 20 // 5 MB
	MaxImages     = 30
)

// imageKind describe las reglas de cada uso.
type imageKind struct {
	Name     string // "logo" o "fondo"
	Label    string
	MaxBytes int64
	Formats  map[string]bool // extensiones admitidas
}

var (
	kindLogo  = imageKind{"logo", "logotipo", MaxLogoBytes, map[string]bool{".png": true, ".svg": true, ".jpg": true, ".webp": true}}
	kindFondo = imageKind{"fondo", "fondo", MaxFondoBytes, map[string]bool{".jpg": true, ".png": true, ".webp": true}}
)

func kindByName(n string) (imageKind, bool) {
	switch n {
	case "logo":
		return kindLogo, true
	case "fondo":
		return kindFondo, true
	}
	return imageKind{}, false
}

// Allows indica si una imagen de la biblioteca sirve para este uso.
func (k imageKind) Allows(im imageInfo) bool {
	return k.Formats[im.Ext] && im.Size <= k.MaxBytes
}

// imageInfo es una imagen de la biblioteca, tal como se muestra en /admin.
type imageInfo struct {
	Name          string
	Ext           string
	Size          int64
	Width, Height int // 0 si no se conocen (SVG)
	ForLogo       bool
	ForFondo      bool
	UsedLogo      bool
	UsedFondo     bool
}

// SizeText devuelve el tamaño legible (KB o MB).
func (im imageInfo) SizeText() string {
	if im.Size >= 1<<20 {
		return fmt.Sprintf("%.1f MB", float64(im.Size)/(1<<20))
	}
	return fmt.Sprintf("%d KB", (im.Size+1023)/1024)
}

// Dims devuelve las dimensiones legibles.
func (im imageInfo) Dims() string {
	if im.Ext == ".svg" {
		return "vectorial"
	}
	if im.Width == 0 {
		return ""
	}
	return fmt.Sprintf("%d×%d px", im.Width, im.Height)
}

func (s *Server) imagesPath() string { return filepath.Join(s.dataDir, imagesDir) }

var safeImageName = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,80}\.(png|jpg|webp|svg)$`)

// listImages devuelve las imágenes de la biblioteca, ordenadas por nombre.
func (s *Server) listImages(c config.Config) []imageInfo {
	entries, err := os.ReadDir(s.imagesPath())
	if err != nil {
		return nil
	}
	var out []imageInfo
	for _, e := range entries {
		if !e.Type().IsRegular() || !safeImageName.MatchString(e.Name()) {
			continue
		}
		fi, err := e.Info()
		if err != nil {
			continue
		}
		im := imageInfo{Name: e.Name(), Ext: filepath.Ext(e.Name()), Size: fi.Size()}
		if f, err := os.Open(filepath.Join(s.imagesPath(), e.Name())); err == nil {
			head := make([]byte, 64<<10)
			n, _ := io.ReadFull(f, head)
			f.Close()
			im.Width, im.Height = imageSize(head[:n])
		}
		im.ForLogo, im.ForFondo = kindLogo.Allows(im), kindFondo.Allows(im)
		im.UsedLogo = c.Kiosk.LogoPath == im.Name
		im.UsedFondo = c.Kiosk.BackgroundImage == im.Name
		out = append(out, im)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func (s *Server) findImage(c config.Config, name string) (imageInfo, bool) {
	for _, im := range s.listImages(c) {
		if im.Name == name {
			return im, true
		}
	}
	return imageInfo{}, false
}

// detectImage averigua el formato real y comprueba que el contenido es seguro.
// Devuelve la extensión que tendrá el fichero.
func detectImage(data []byte) (string, error) {
	switch ct := http.DetectContentType(data); {
	case ct == "image/png":
		return ".png", nil
	case ct == "image/jpeg":
		return ".jpg", nil
	case ct == "image/webp":
		return ".webp", nil
	}
	if err := checkSVG(data); err != nil {
		return "", err
	}
	return ".svg", nil
}

var svgEventAttr = regexp.MustCompile(`(?i)^on`)

// checkSVG acepta solo un SVG bien formado, sin scripts, manejadores de eventos,
// enlaces javascript:, contenido HTML incrustado ni entidades.
func checkSVG(data []byte) error {
	errFormat := errors.New("el fichero no es una imagen PNG, JPG, WebP ni SVG")
	if !bytes.Contains(bytes.ToLower(data[:min(len(data), 4096)]), []byte("<svg")) {
		return errFormat
	}
	if bytes.Contains(bytes.ToUpper(data), []byte("<!ENTITY")) {
		return errors.New("el SVG contiene entidades XML; expórtelo de nuevo como SVG simple")
	}
	dec := xml.NewDecoder(bytes.NewReader(data))
	dec.Strict = true
	root := ""
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return errFormat
		}
		el, ok := tok.(xml.StartElement)
		if !ok {
			continue
		}
		if root == "" {
			root = el.Name.Local
		}
		switch strings.ToLower(el.Name.Local) {
		case "script", "foreignobject", "iframe", "object", "embed":
			return fmt.Errorf("el SVG contiene un elemento no permitido (<%s>)", el.Name.Local)
		}
		for _, a := range el.Attr {
			v := strings.ToLower(strings.TrimSpace(a.Value))
			if svgEventAttr.MatchString(a.Name.Local) || strings.HasPrefix(v, "javascript:") || strings.HasPrefix(v, "data:text/html") {
				return errors.New("el SVG contiene código (eventos o enlaces javascript:) y no se admite")
			}
		}
	}
	if root != "svg" {
		return errFormat
	}
	return nil
}

// imageSize lee las dimensiones de PNG, JPG y WebP (0, 0 si no se conocen).
func imageSize(data []byte) (int, int) {
	if cfg, _, err := image.DecodeConfig(bytes.NewReader(data)); err == nil {
		return cfg.Width, cfg.Height
	}
	if len(data) < 30 || string(data[0:4]) != "RIFF" || string(data[8:12]) != "WEBP" {
		return 0, 0
	}
	switch string(data[12:16]) {
	case "VP8 ": // con pérdida
		w := int(binary.LittleEndian.Uint16(data[26:28]) & 0x3fff)
		h := int(binary.LittleEndian.Uint16(data[28:30]) & 0x3fff)
		return w, h
	case "VP8L": // sin pérdida
		b := data[21:25]
		w := 1 + (int(b[0]) | int(b[1]&0x3f)<<8)
		h := 1 + (int(b[1])>>6 | int(b[2])<<2 | int(b[3]&0x0f)<<10)
		return w, h
	case "VP8X": // extendido
		w := 1 + (int(data[24]) | int(data[25])<<8 | int(data[26])<<16)
		h := 1 + (int(data[27]) | int(data[28])<<8 | int(data[29])<<16)
		return w, h
	}
	return 0, 0
}

var (
	nameClean = regexp.MustCompile(`[^a-z0-9_-]+`)
	multiDash = regexp.MustCompile(`-{2,}`)
)

// imageFileName crea un nombre seguro y libre a partir del nombre original.
func (s *Server) imageFileName(orig, ext string) string {
	base := strings.ToLower(strings.TrimSuffix(filepath.Base(orig), filepath.Ext(orig)))
	base = strings.NewReplacer("á", "a", "é", "e", "í", "i", "ó", "o", "ú", "u", "ü", "u", "ñ", "n", " ", "-").Replace(base)
	base = strings.Trim(nameClean.ReplaceAllString(base, "-"), "-_.")
	base = multiDash.ReplaceAllString(base, "-")
	if base == "" {
		base = "imagen"
	}
	if len(base) > 60 {
		base = base[:60]
	}
	name := base + ext
	for i := 2; ; i++ {
		if _, err := os.Stat(filepath.Join(s.imagesPath(), name)); errors.Is(err, os.ErrNotExist) {
			return name
		}
		name = fmt.Sprintf("%s-%d%s", base, i, ext)
	}
}

// limitBody limita el cuerpo antes de que nadie lo lea (también el control CSRF).
func limitBody(n int64, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, n)
		next(w, r)
	}
}

func mb(n int64) string { return fmt.Sprintf("%d MB", n>>20) }

// apiUpload sube una imagen (?tipo=logo|fondo, campo «archivo») y la deja seleccionada.
func (s *Server) apiUpload(w http.ResponseWriter, r *http.Request) {
	fail := func(msg string) { writeJSON(w, http.StatusOK, apiResult{OK: false, Message: msg}) }
	kind, ok := kindByName(r.URL.Query().Get("tipo"))
	if !ok {
		fail("Tipo de imagen no válido.")
		return
	}
	file, hdr, err := r.FormFile("archivo")
	if err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			fail(fmt.Sprintf("El fichero supera el máximo de %s para el %s.", mb(kind.MaxBytes), kind.Label))
		} else {
			fail("No se ha recibido ningún fichero.")
		}
		return
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, kind.MaxBytes+1))
	if err != nil {
		fail("No se ha podido leer el fichero.")
		return
	}
	if int64(len(data)) > kind.MaxBytes {
		fail(fmt.Sprintf("El fichero supera el máximo de %s para el %s.", mb(kind.MaxBytes), kind.Label))
		return
	}
	ext, err := detectImage(data)
	if err != nil {
		fail(err.Error() + ".")
		return
	}
	if !kind.Formats[ext] {
		fail(fmt.Sprintf("El formato %s no se admite para el %s.", strings.ToUpper(strings.TrimPrefix(ext, ".")), kind.Label))
		return
	}
	c := s.cfg.Get()
	if len(s.listImages(c)) >= MaxImages {
		fail(fmt.Sprintf("Ya hay %d imágenes en el servidor: borre alguna que no use.", MaxImages))
		return
	}
	if err := os.MkdirAll(s.imagesPath(), 0o750); err != nil {
		fail("No se puede crear la carpeta de imágenes: " + err.Error())
		return
	}
	name := s.imageFileName(hdr.Filename, ext)
	if err := writeFileAtomic(filepath.Join(s.imagesPath(), name), data, 0o640); err != nil {
		fail("No se ha podido guardar la imagen: " + err.Error())
		return
	}
	err = s.cfg.Update(func(c *config.Config) error {
		if kind.Name == "logo" {
			c.Kiosk.LogoPath = name
		} else {
			c.Kiosk.BackgroundImage = name
		}
		return nil
	})
	if err != nil {
		fail("Imagen guardada, pero no se ha podido seleccionar: " + err.Error())
		return
	}
	wd, ht := imageSize(data)
	s.log.Printf("admin: subida la imagen %s (%d bytes, %d×%d) y seleccionada como %s desde %s", name, len(data), wd, ht, kind.Label, clientIP(r))
	msg := fmt.Sprintf("Imagen «%s» subida y seleccionada como %s.", name, kind.Label)
	switch {
	case kind.Name == "logo" && ext != ".svg" && ht > 0 && ht < 64:
		msg += fmt.Sprintf(" Aviso: mide %d px de alto; se recomiendan al menos 128 px para que no se vea borrosa.", ht)
	case kind.Name == "fondo" && wd > 0 && wd < 1280 && ht < 1280:
		msg += fmt.Sprintf(" Aviso: mide %d×%d px; puede verse pixelada en una pantalla Full HD.", wd, ht)
	}
	s.sessions.setFlash(sessionFrom(r).sess, msg)
	writeJSON(w, http.StatusOK, apiResult{OK: true, Message: msg})
}

// apiImageDelete borra una imagen que no esté en uso.
func (s *Server) apiImageDelete(w http.ResponseWriter, r *http.Request) {
	name := r.FormValue("nombre")
	c := s.cfg.Get()
	im, ok := s.findImage(c, name)
	if !ok {
		writeJSON(w, http.StatusOK, apiResult{OK: false, Message: "La imagen no existe."})
		return
	}
	if im.UsedLogo || im.UsedFondo {
		writeJSON(w, http.StatusOK, apiResult{OK: false, Message: "Está en uso: elija antes otro logotipo o fondo y guarde."})
		return
	}
	if err := os.Remove(filepath.Join(s.imagesPath(), im.Name)); err != nil {
		writeJSON(w, http.StatusOK, apiResult{OK: false, Message: "No se ha podido borrar: " + err.Error()})
		return
	}
	s.log.Printf("admin: borrada la imagen %s desde %s", im.Name, clientIP(r))
	msg := fmt.Sprintf("Imagen «%s» borrada.", im.Name)
	s.sessions.setFlash(sessionFrom(r).sess, msg)
	writeJSON(w, http.StatusOK, apiResult{OK: true, Message: msg})
}

// adminImage sirve una imagen de la biblioteca para las miniaturas de /admin.
func (s *Server) adminImage(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("nombre")
	if !safeImageName.MatchString(name) {
		http.NotFound(w, r)
		return
	}
	s.serveImage(w, r, filepath.Join(s.imagesPath(), name))
}

// checkImageChoice valida en el servidor el logotipo y el fondo elegidos en el formulario.
func (s *Server) checkImageChoice(c config.Config) []string {
	var errs []string
	check := func(name string, kind imageKind) {
		if name == "" {
			return
		}
		im, ok := s.findImage(c, name)
		if !ok {
			if s.dataFile(name) != "" { // imagen copiada a mano en la carpeta de datos (versiones anteriores)
				return
			}
			errs = append(errs, fmt.Sprintf("La imagen «%s» elegida como %s no existe", name, kind.Label))
			return
		}
		if !kind.Allows(im) {
			errs = append(errs, fmt.Sprintf("La imagen «%s» no sirve como %s (formato o tamaño: máximo %s)", name, kind.Label, mb(kind.MaxBytes)))
		}
	}
	check(c.Kiosk.LogoPath, kindLogo)
	check(c.Kiosk.BackgroundImage, kindFondo)
	return errs
}

// writeFileAtomic escribe en un temporal del mismo directorio y lo renombra.
func writeFileAtomic(path string, data []byte, mode os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".subida-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), mode); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
