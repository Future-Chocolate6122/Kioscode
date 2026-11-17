// Tótem Wi-Fi: servidor de quiosco que entrega vouchers del hotspot de UniFi.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"totem-wifi/internal/config"
	"totem-wifi/internal/logfile"
	"totem-wifi/internal/unifi"
	"totem-wifi/internal/voucher"
	"totem-wifi/internal/web"
)

// version se fija al compilar con -ldflags "-X main.version=…".
var version = "dev"

// app es la aplicación en marcha.
type app struct {
	configPath string
	dataDir    string
	logger     *log.Logger
	logFile    *logfile.Rotating
	srv        *http.Server
	cfg        *config.Store
	svc        *voucher.Service
	cancel     context.CancelFunc // detiene las tareas periódicas
}

// setup carga la configuración, abre el log y prepara servicio y servidor web.
func (a *app) setup() error {
	if err := os.MkdirAll(a.dataDir, 0o750); err != nil {
		return fmt.Errorf("no se puede crear el directorio de datos %s: %w", a.dataDir, err)
	}
	var out io.Writer = os.Stderr
	if lf, err := logfile.Open(filepath.Join(a.dataDir, "totem-wifi.log"), 5<<20, 5); err == nil {
		a.logFile = lf
		out = io.MultiWriter(os.Stderr, lf)
	} else {
		fmt.Fprintf(os.Stderr, "AVISO: no se puede abrir el fichero de log: %v\n", err)
	}
	a.logger = log.New(out, "", log.LstdFlags)

	cfg, created, err := config.Load(a.configPath)
	if err != nil {
		return err
	}
	a.cfg = cfg
	if created {
		a.logger.Printf("primer arranque: creada %s con los valores por defecto (modo simulación)", cfg.Path())
	}
	var prov unifi.Provider
	clientFor := func(c config.Config) unifi.Client {
		return prov.Get(c.UniFi.Mock, unifi.Config{BaseURL: c.UniFi.BaseURL, APIKey: c.UniFi.APIKey, CertSHA256: c.UniFi.CertSHA256})
	}
	a.svc = voucher.New(cfg, clientFor, filepath.Join(a.dataDir, "state.json"), a.logger)
	ws := web.New(cfg, a.svc, a.dataDir, a.logger, version)

	c := cfg.Get()
	a.srv = &http.Server{
		Addr:              c.Listen,
		Handler:           ws.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    16 << 10,
		ErrorLog:          a.logger,
	}
	return nil
}

// start arranca el servidor HTTP y la comprobación inicial de UniFi.
func (a *app) start() error {
	ln, err := net.Listen("tcp", a.srv.Addr)
	if err != nil {
		return fmt.Errorf("no se puede escuchar en %s (¿otro programa usa el puerto?): %w", a.srv.Addr, err)
	}
	c := a.cfg.Get()
	a.logger.Printf("Tótem Wi-Fi %s escuchando en http://%s/ (configuración: %s, datos: %s)", version, a.srv.Addr, a.cfg.Path(), a.dataDir)
	a.startupCheck(c)
	ctx, cancel := context.WithCancel(context.Background())
	a.cancel = cancel
	a.svc.StartCleanup(ctx) // borra cada hora los códigos del tótem sin usar (si está activado)
	go func() {
		if err := a.srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			a.logger.Printf("ERROR servidor HTTP: %v", err)
		}
	}()
	return nil
}

// startupCheck informa del modo y, en modo real, prueba UniFi en segundo plano.
func (a *app) startupCheck(c config.Config) {
	switch {
	case c.UniFi.Mock:
		a.logger.Printf("MODO SIMULACIÓN: los códigos son falsos. Configure UniFi en /admin y desactive «mock» para entregar vouchers reales")
	case c.UniFi.CertSHA256 == "":
		a.logger.Printf("AVISO: modo real sin huella del certificado: /api/voucher responderá 503. %v", unifi.ErrNoFingerprint)
	case c.UniFi.APIKey == "":
		a.logger.Printf("AVISO: modo real sin API key: /api/voucher responderá 503. %v", unifi.ErrNoAPIKey)
	default:
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), unifi.Timeout+time.Second)
			defer cancel()
			if res, err := a.svc.Check(ctx); err != nil {
				a.logger.Printf("AVISO: la comprobación inicial de UniFi ha fallado: %v", err)
			} else {
				a.logger.Printf("conexión con UniFi correcta (sitio %q, %s)", res.SiteName, res.SiteID)
			}
		}()
	}
}

// stop detiene el servidor ordenadamente.
func (a *app) stop() error {
	if a.srv == nil {
		return nil
	}
	if a.cancel != nil {
		a.cancel()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	err := a.srv.Shutdown(ctx)
	a.logger.Printf("servidor detenido")
	if a.logFile != nil {
		a.logFile.Close()
	}
	return err
}

// rutas por defecto cuando se instala como servicio.
func systemPaths() (configPath, dataDir string) {
	if runtime.GOOS == "windows" {
		pd := os.Getenv("ProgramData")
		if pd == "" {
			pd = `C:\ProgramData`
		}
		dir := filepath.Join(pd, "TotemWifi")
		return filepath.Join(dir, "config.json"), dir
	}
	return "/etc/totem-wifi/config.json", "/var/lib/totem-wifi"
}

// resolvePaths decide qué config.json y directorio de datos usar:
// -config/-data si se indican; si no, config.json junto al ejecutable si existe;
// si no, el del servicio instalado si existe; y si tampoco, junto al ejecutable.
func resolvePaths(cfgFlag, dataFlag string, preferSystem bool) (string, string) {
	sysCfg, sysData := systemPaths()
	cp, dd := cfgFlag, dataFlag
	if cp == "" {
		exeCfg := "config.json"
		if exe, err := os.Executable(); err == nil {
			exeCfg = filepath.Join(filepath.Dir(exe), "config.json")
		}
		switch {
		case preferSystem:
			cp = sysCfg
		case fileExists(exeCfg):
			cp = exeCfg
		case fileExists(sysCfg):
			cp = sysCfg
		default:
			cp = exeCfg
		}
	}
	cp, _ = filepath.Abs(cp)
	if dd == "" {
		if cp == sysCfg {
			dd = sysData
		} else {
			dd = filepath.Dir(cp)
		}
	}
	dd, _ = filepath.Abs(dd)
	return cp, dd
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

const usage = `Tótem Wi-Fi %s: entrega vouchers del hotspot de UniFi en una web de quiosco.

Uso:
  totem-wifi [-config RUTA] [-data DIR]   ejecuta en primer plano
  totem-wifi install | uninstall | start | stop | status | restart
                                          servicio del sistema (requiere administrador/root)
  totem-wifi fingerprint HOST:PUERTO      imprime la huella SHA-256 del certificado de un servidor
  totem-wifi check                        prueba la configuración y la conexión con UniFi
  totem-wifi init                         crea config.json con los valores por defecto
  totem-wifi set [-unifi-url URL] [-totems LISTA] [-admin-ips LISTA]
                                          ajusta la configuración de esta sede (lo usa install.sh)
  totem-wifi firewall-sync                ajusta ufw a las IPs permitidas (root; lo lanza systemd)
  totem-wifi allowlist                    lista puerto e IPs permitidas
  totem-wifi version                      muestra la versión

Opciones:
`

func main() {
	fs := flag.NewFlagSet("totem-wifi", flag.ExitOnError)
	cfgFlag := fs.String("config", "", "ruta de config.json")
	dataFlag := fs.String("data", "", "directorio de datos: state.json, log, logo (por defecto, el de config.json)")
	var so setOptions
	fs.StringVar(&so.unifiURL, "unifi-url", "", "con «set»: URL de UniFi OS Server de esta sede (https://IP:11443)")
	fs.StringVar(&so.totems, "totems", "", "con «set»: IPs o redes de los tótems, separadas por comas")
	fs.StringVar(&so.adminIPs, "admin-ips", "", "con «set»: IPs o redes que pueden abrir /admin, separadas por comas")
	fs.Usage = func() {
		fmt.Fprintf(fs.Output(), usage, version)
		fs.PrintDefaults()
	}
	fs.Parse(os.Args[1:]) //nolint:errcheck
	cmd, rest := "", []string(nil)
	if fs.NArg() > 0 {
		cmd = fs.Arg(0)
		fs.Parse(fs.Args()[1:]) //nolint:errcheck // permite poner las opciones después del comando
		rest = fs.Args()
	}

	switch cmd {
	case "version":
		fmt.Println("totem-wifi", version, runtime.GOOS+"/"+runtime.GOARCH)
		return
	case "fingerprint":
		os.Exit(runFingerprint(rest))
	case "help", "-h", "--help":
		fs.Usage()
		return
	}

	fs.Visit(func(f *flag.Flag) { so.given = append(so.given, f.Name) })
	cp, dd := resolvePaths(*cfgFlag, *dataFlag, isServiceCommand(cmd))
	switch cmd {
	case "":
		os.Exit(runServer(cp, dd))
	case "check":
		os.Exit(runCheck(cp))
	case "init":
		os.Exit(runInit(cp))
	case "allowlist":
		os.Exit(runAllowlist(cp))
	case "set":
		os.Exit(runSet(cp, so))
	case "firewall-sync":
		os.Exit(runFirewallSync(cp, dd))
	default:
		if isServiceCommand(cmd) {
			os.Exit(runServiceCommand(cmd, cp, dd))
		}
		fmt.Fprintf(os.Stderr, "comando desconocido: %q\n\n", cmd)
		fs.Usage()
		os.Exit(2)
	}
}
