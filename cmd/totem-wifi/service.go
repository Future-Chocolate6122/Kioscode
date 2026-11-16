package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/kardianos/service"
)

const serviceName = "TotemWifi"

// Unidad systemd endurecida. @RW@ se sustituye por los directorios escribibles
// (configuración y datos); el resto lo rellena kardianos/service.
const systemdUnit = `[Unit]
Description={{.Description}}
After=network-online.target
Wants=network-online.target
ConditionFileIsExecutable={{.Path|cmdEscape}}

[Service]
ExecStart={{.Path|cmdEscape}}{{range .Arguments}} {{.|cmd}}{{end}}
{{if .UserName}}User={{.UserName}}
Group={{.UserName}}{{end}}
Restart=always
RestartSec=5
UMask=0027
NoNewPrivileges=yes
ProtectSystem=strict
ProtectHome=yes
PrivateTmp=yes
PrivateDevices=yes
ProtectKernelTunables=yes
ProtectKernelModules=yes
ProtectControlGroups=yes
RestrictAddressFamilies=AF_INET AF_INET6 AF_UNIX
ReadWritePaths=@RW@

[Install]
WantedBy=multi-user.target
`

// program adapta app a la interfaz de kardianos/service.
type program struct {
	a *app
}

func (p *program) Start(service.Service) error {
	if err := p.a.setup(); err != nil {
		return err
	}
	return p.a.start() // no bloquea: el servidor HTTP corre en su goroutine
}

func (p *program) Stop(service.Service) error { return p.a.stop() }

func isServiceCommand(cmd string) bool {
	switch cmd {
	case "install", "uninstall", "start", "stop", "status", "restart":
		return true
	}
	return false
}

func newService(configPath, dataDir string, a *app) (service.Service, error) {
	cfg := &service.Config{
		Name:        serviceName,
		DisplayName: "Tótem Wi-Fi",
		Description: "Tótem Wi-Fi: entrega vouchers del hotspot de UniFi en una web de quiosco",
		Arguments:   []string{"-config", configPath, "-data", dataDir},
		Option:      service.KeyValue{},
	}
	switch runtime.GOOS {
	case "windows":
		cfg.Option["StartType"] = "automatic"
		cfg.Option["OnFailure"] = "restart"
		cfg.Option["OnFailureDelayDuration"] = "10s"
		cfg.Option["OnFailureResetPeriod"] = 3600
	case "linux":
		rw := []string{dataDir}
		if d := filepath.Dir(configPath); d != dataDir {
			rw = append(rw, d)
		}
		cfg.Option["SystemdScript"] = strings.ReplaceAll(systemdUnit, "@RW@", strings.Join(rw, " "))
		// Usuario sin privilegios creado por install.sh; si no existe, se ejecuta como root.
		if userExists("totem") {
			cfg.UserName = "totem"
		}
	}
	return service.New(&program{a: a}, cfg)
}

func userExists(name string) bool {
	data, err := os.ReadFile("/etc/passwd")
	if err != nil {
		return false
	}
	for _, l := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(l, name+":") {
			return true
		}
	}
	return false
}

// runServiceCommand gestiona el servicio del sistema.
func runServiceCommand(cmd, configPath, dataDir string) int {
	s, err := newService(configPath, dataDir, nil)
	if err != nil {
		fmt.Fprintln(os.Stderr, "ERROR:", err)
		return 1
	}
	if cmd == "status" {
		st, err := s.Status()
		switch {
		case errors.Is(err, service.ErrNotInstalled):
			fmt.Println("El servicio no está instalado.")
			return 3
		case err != nil:
			fmt.Fprintf(os.Stderr, "ERROR: no se puede consultar el servicio con %s: %v\n", service.ChosenSystem(), err)
			return 1
		case st == service.StatusRunning:
			fmt.Println("El servicio está en marcha.")
			return 0
		case st == service.StatusStopped:
			fmt.Println("El servicio está instalado pero detenido.")
			return 3
		default:
			fmt.Println("Estado del servicio desconocido.")
			return 4
		}
	}
	if err := service.Control(s, cmd); err != nil {
		msg := err.Error()
		if strings.Contains(strings.ToLower(msg), "access") || strings.Contains(strings.ToLower(msg), "permission") {
			msg += "\n(ejecute el comando como administrador / root)"
		}
		fmt.Fprintf(os.Stderr, "ERROR al ejecutar «%s»: %s\n", cmd, msg)
		return 1
	}
	switch cmd {
	case "install":
		fmt.Printf("Servicio %s instalado.\n  Configuración: %s\n  Datos y log:   %s\n", serviceName, configPath, dataDir)
	case "uninstall":
		fmt.Printf("Servicio %s desinstalado (la configuración y los datos se conservan).\n", serviceName)
	default:
		fmt.Printf("Servicio %s: %s correcto.\n", serviceName, cmd)
	}
	return 0
}

// runServer ejecuta la aplicación: en primer plano si se lanza desde una consola,
// o bajo el gestor de servicios (servicio de Windows / systemd).
func runServer(configPath, dataDir string) int {
	a := &app{configPath: configPath, dataDir: dataDir}
	s, err := newService(configPath, dataDir, a)
	if err != nil {
		fmt.Fprintln(os.Stderr, "ERROR:", err)
		return 1
	}
	if err := s.Run(); err != nil {
		if a.logger != nil {
			a.logger.Printf("ERROR: %v", err)
		} else {
			fmt.Fprintln(os.Stderr, "ERROR:", err)
		}
		return 1
	}
	return 0
}
