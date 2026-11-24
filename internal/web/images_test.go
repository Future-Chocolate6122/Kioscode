package web

import (
	"bytes"
	"encoding/json"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"kioscode/internal/config"
)

func pngDe(w, h int) []byte {
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	img.Set(0, 0, color.NRGBA{R: 7, G: 155, B: 226, A: 255})
	var b bytes.Buffer
	png.Encode(&b, img) //nolint:errcheck
	return b.Bytes()
}

func jpgDe(w, h int) []byte {
	var b bytes.Buffer
	jpeg.Encode(&b, image.NewRGBA(image.Rect(0, 0, w, h)), nil) //nolint:errcheck
	return b.Bytes()
}

// webpVP8X fabrica la cabecera de un WebP extendido con las dimensiones dadas.
func webpVP8X(w, h int) []byte {
	b := make([]byte, 40)
	copy(b, "RIFF")
	copy(b[8:], "WEBPVP8X")
	w, h = w-1, h-1
	b[24], b[25], b[26] = byte(w), byte(w>>8), byte(w>>16)
	b[27], b[28], b[29] = byte(h), byte(h>>8), byte(h>>16)
	return b
}

const svgBueno = `<?xml version="1.0"?><svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 10 10"><rect width="10" height="10" fill="#079BE2"/></svg>`

func TestDetectImage(t *testing.T) {
	casos := []struct {
		nombre string
		datos  []byte
		ext    string // "" = debe rechazarse
	}{
		{"png", pngDe(4, 4), ".png"},
		{"jpg", jpgDe(4, 4), ".jpg"},
		{"webp", webpVP8X(1920, 1080), ".webp"},
		{"svg", []byte(svgBueno), ".svg"},
		{"svg con script", []byte(`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`), ""},
		{"svg con onload", []byte(`<svg xmlns="http://www.w3.org/2000/svg" onload="alert(1)"/>`), ""},
		{"svg con javascript:", []byte(`<svg xmlns="http://www.w3.org/2000/svg"><a href="javascript:alert(1)"><rect/></a></svg>`), ""},
		{"svg con foreignObject", []byte(`<svg xmlns="http://www.w3.org/2000/svg"><foreignObject><p>x</p></foreignObject></svg>`), ""},
		{"svg con entidades", []byte(`<!DOCTYPE svg [<!ENTITY x "y">]><svg xmlns="http://www.w3.org/2000/svg">&x;</svg>`), ""},
		{"html", []byte(`<html><body><svg></svg></body></html>`), ""},
		{"texto", []byte("hola, esto no es una imagen"), ""},
	}
	for _, c := range casos {
		ext, err := detectImage(c.datos)
		if c.ext == "" && err == nil {
			t.Errorf("%s: debería rechazarse (detectado %s)", c.nombre, ext)
		}
		if c.ext != "" && (err != nil || ext != c.ext) {
			t.Errorf("%s: ext=%q err=%v, want %s", c.nombre, ext, err, c.ext)
		}
	}
}

func TestImageSize(t *testing.T) {
	if w, h := imageSize(pngDe(640, 128)); w != 640 || h != 128 {
		t.Errorf("png: %d×%d", w, h)
	}
	if w, h := imageSize(jpgDe(320, 200)); w != 320 || h != 200 {
		t.Errorf("jpg: %d×%d", w, h)
	}
	if w, h := imageSize(webpVP8X(1920, 1080)); w != 1920 || h != 1080 {
		t.Errorf("webp VP8X: %d×%d", w, h)
	}
	// VP8L de 300×17: ancho-1=299 (0x12B), alto-1=16.
	l := make([]byte, 30)
	copy(l, "RIFF")
	copy(l[8:], "WEBPVP8L")
	w1, h1 := 299, 16
	l[21] = byte(w1)
	l[22] = byte(w1>>8)&0x3f | byte(h1<<6)
	l[23] = byte(h1 >> 2)
	l[24] = byte(h1>>10) & 0x0f
	if w, h := imageSize(l); w != 300 || h != 17 {
		t.Errorf("webp VP8L: %d×%d", w, h)
	}
	if w, h := imageSize([]byte(svgBueno)); w != 0 || h != 0 {
		t.Errorf("svg: %d×%d", w, h)
	}
}

// subir envía una imagen al endpoint de subida como lo haría admin.js.
func (n *navegador) subir(tipo, nombre string, datos []byte) apiResult {
	n.t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	fw, _ := mw.CreateFormFile("archivo", nombre)
	fw.Write(datos) //nolint:errcheck
	mw.Close()
	w := n.e.peticion("POST", "/admin/api/upload?tipo="+tipo, n.ip, body.String(), map[string]string{
		"Content-Type": mw.FormDataContentType(), "X-CSRF-Token": n.csrf, "Cookie": cookieName + "=" + n.cookie,
	})
	if w.Code == http.StatusRequestEntityTooLarge {
		return apiResult{OK: false, Message: "413"}
	}
	var r apiResult
	if err := json.Unmarshal(w.Body.Bytes(), &r); err != nil {
		n.t.Fatalf("subida: %d %s", w.Code, w.Body)
	}
	return r
}

func TestSubirLogoYFondo(t *testing.T) {
	e := nuevoEntorno(t, nil)
	n := entrar(t, e)
	revAntes := e.srv.kioskRevision(e.cfg.Get())

	// Logo PNG con nombre raro: se guarda con nombre seguro y queda seleccionado.
	r := n.subir("logo", "Logo Oficial (versión 2).PNG", pngDe(640, 128))
	if !r.OK {
		t.Fatalf("subir logo: %+v", r)
	}
	k := e.cfg.Get().Kiosk
	if k.LogoPath != "logo-oficial-version-2.png" {
		t.Errorf("nombre/selección del logo: %q", k.LogoPath)
	}
	if _, err := os.Stat(filepath.Join(e.dir, "imagenes", k.LogoPath)); err != nil {
		t.Errorf("el fichero no está en <datos>/imagenes: %v", err)
	}
	if w := e.peticion("GET", "/logo", "127.0.0.1", "", nil); w.Code != 200 || w.Header().Get("Content-Type") != "image/png" || w.Header().Get("Cache-Control") != "no-cache" {
		t.Errorf("/logo: %d %v", w.Code, w.Header())
	}
	if e.srv.kioskRevision(e.cfg.Get()) == revAntes {
		t.Error("cambiar el logo debe cambiar la huella para que el quiosco recargue")
	}
	if !strings.Contains(e.logs.String(), "subida la imagen logo-oficial-version-2.png") {
		t.Error("la subida debe quedar en el log")
	}
	// Mismo nombre otra vez: no se pisa el anterior.
	n.subir("logo", "Logo Oficial (versión 2).png", pngDe(640, 128))
	if e.cfg.Get().Kiosk.LogoPath != "logo-oficial-version-2-2.png" {
		t.Errorf("nombre duplicado: %q", e.cfg.Get().Kiosk.LogoPath)
	}

	// Fondo JPG.
	if r := n.subir("fondo", "fondo.jpg", jpgDe(1920, 1080)); !r.OK || e.cfg.Get().Kiosk.BackgroundImage != "fondo.jpg" {
		t.Fatalf("subir fondo: %+v", r)
	}
	if w := e.peticion("GET", "/background", "127.0.0.1", "", nil); w.Code != 200 {
		t.Errorf("/background: %d", w.Code)
	}
	if !strings.Contains(e.peticion("GET", "/", "127.0.0.1", "", nil).Body.String(), "con-fondo") {
		t.Error("el quiosco debe mostrar el fondo")
	}

	// La página de /admin lista las imágenes y marca las que están en uso.
	body := n.hacer("GET", "/admin", nil, nil).Body.String()
	for _, want := range []string{"Imágenes en el servidor (3)", "logo-oficial-version-2.png", "fondo.jpg", "1920×1080 px", "Subir logotipo"} {
		if !strings.Contains(body, want) {
			t.Errorf("falta %q en /admin", want)
		}
	}
}

func TestSubidaLimitesYFormatos(t *testing.T) {
	e := nuevoEntorno(t, nil)
	n := entrar(t, e)
	grande := append(pngDe(10, 10), make([]byte, MaxLogoBytes)...) // PNG válido de más de 1 MB
	if r := n.subir("logo", "grande.png", grande); r.OK || !strings.Contains(r.Message, "1 MB") {
		t.Errorf("logo de más de 1 MB: %+v", r)
	}
	if r := n.subir("fondo", "grande.png", grande); !r.OK {
		t.Errorf("el mismo fichero sí vale como fondo (máx. 5 MB): %+v", r)
	}
	enorme := append(pngDe(10, 10), make([]byte, MaxFondoBytes)...)
	if r := n.subir("fondo", "enorme.png", enorme); r.OK {
		t.Errorf("fondo de más de 5 MB: %+v", r)
	}
	if r := n.subir("fondo", "logo.svg", []byte(svgBueno)); r.OK || !strings.Contains(r.Message, "SVG") {
		t.Errorf("un SVG no vale como fondo: %+v", r)
	}
	if r := n.subir("logo", "malo.svg", []byte(`<svg xmlns="http://www.w3.org/2000/svg" onload="x()"/>`)); r.OK {
		t.Error("un SVG con código debe rechazarse")
	}
	if r := n.subir("logo", "foto.png", []byte("no soy un png")); r.OK {
		t.Error("un fichero que no es imagen debe rechazarse aunque se llame .png")
	}
	if r := n.subir("otro", "x.png", pngDe(2, 2)); r.OK {
		t.Error("tipo de imagen desconocido")
	}
	// Sin sesión no se puede subir.
	anon := &navegador{t: t, e: e, ip: "127.0.0.1"}
	if r := anon.subir("logo", "x.png", pngDe(2, 2)); r.OK {
		t.Error("subida sin sesión")
	}
}

func TestBorrarImagenYEleccion(t *testing.T) {
	e := nuevoEntorno(t, nil)
	n := entrar(t, e)
	n.subir("logo", "a.png", pngDe(10, 10))
	n.subir("logo", "b.svg", []byte(svgBueno)) // queda en uso b.svg
	if r := n.api("/admin/api/image-delete", url.Values{"nombre": {"b.svg"}}); r.OK {
		t.Error("no se debe poder borrar una imagen en uso")
	}
	if r := n.api("/admin/api/image-delete", url.Values{"nombre": {"a.png"}}); !r.OK {
		t.Errorf("borrar imagen sin uso: %+v", r)
	}
	if _, err := os.Stat(filepath.Join(e.dir, "imagenes", "a.png")); err == nil {
		t.Error("la imagen sigue en disco")
	}
	if r := n.api("/admin/api/image-delete", url.Values{"nombre": {"../config.json"}}); r.OK {
		t.Error("no debe poder borrarse nada fuera de la biblioteca")
	}

	// Elegir en el formulario un SVG como fondo: se rechaza al guardar.
	form := formularioActual(e.cfg.Get(), n.csrf)
	form.Set("kiosk_logo_path", "b.svg")
	form.Set("kiosk_background_image", "b.svg")
	if w := n.hacer("POST", "/admin", form, nil); w.Code != 400 || !strings.Contains(w.Body.String(), "no sirve como fondo") {
		t.Errorf("SVG como fondo: %d", w.Code)
	}
	form.Set("kiosk_background_image", "")
	if w := n.hacer("POST", "/admin", form, nil); w.Code != 303 {
		t.Errorf("logo SVG sin fondo: %d", w.Code)
	}
	form.Set("kiosk_logo_path", "no-existe.png")
	if w := n.hacer("POST", "/admin", form, nil); w.Code != 400 {
		t.Errorf("imagen inexistente: %d", w.Code)
	}
}

func TestMiniaturasSoloConSesion(t *testing.T) {
	e := nuevoEntorno(t, func(c *config.Config) {})
	n := entrar(t, e)
	n.subir("logo", "a.png", pngDe(10, 10))
	if w := n.hacer("GET", "/admin/imagenes/a.png", nil, nil); w.Code != 200 {
		t.Errorf("miniatura con sesión: %d", w.Code)
	}
	if w := e.peticion("GET", "/admin/imagenes/a.png", "127.0.0.1", "", nil); w.Code == 200 {
		t.Error("miniatura sin sesión")
	}
	if w := n.hacer("GET", "/admin/imagenes/..%2fconfig.json", nil, nil); w.Code == 200 {
		t.Error("no debe servir ficheros fuera de la biblioteca")
	}
}

func TestVersionDelQuiosco(t *testing.T) {
	e := nuevoEntorno(t, nil)
	var v1, v2 map[string]string
	json.Unmarshal(e.peticion("GET", "/api/version", "127.0.0.1", "", nil).Body.Bytes(), &v1)
	if len(v1["rev"]) != 12 || !strings.Contains(e.peticion("GET", "/", "127.0.0.1", "", nil).Body.String(), v1["rev"]) {
		t.Fatalf("la página debe llevar la misma huella que /api/version: %v", v1)
	}
	e.cfg.Update(func(c *config.Config) error { c.Kiosk.Title = "Otro título"; return nil })
	json.Unmarshal(e.peticion("GET", "/api/version", "127.0.0.1", "", nil).Body.Bytes(), &v2)
	if v1["rev"] == v2["rev"] {
		t.Error("cambiar un texto debe cambiar la huella")
	}
	if w := e.peticion("GET", "/api/version", "203.0.113.50", "", nil); w.Code != 403 {
		t.Errorf("/api/version fuera de la lista de tótems: %d", w.Code)
	}
}
