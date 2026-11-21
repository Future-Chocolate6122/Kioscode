# Tótem Wi-Fi · compilación y empaquetado.
#   make build       binario para esta plataforma (./totem-wifi o .\totem-wifi.exe)
#   make build-all   dist/ para Windows amd64, Linux amd64 y Linux arm64
#   make release     además, paquetes .zip / .tar.gz listos para copiar a la máquina destino
#   make licenses    comprueba que cada módulo que lleva el ejecutable aparece en LICENSE
#   make test        pruebas;  make vet  análisis estático;  make run  arranca en modo desarrollo

GO      ?= go
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)
BUILD   := CGO_ENABLED=0 $(GO) build -trimpath -ldflags "$(LDFLAGS)"
PKG     := ./cmd/totem-wifi
DIST    := dist

ifeq ($(OS),Windows_NT)
  EXE := .exe
endif

.PHONY: build build-all licenses release test vet run clean

build:
	$(BUILD) -o totem-wifi$(EXE) $(PKG)

build-all:
	@mkdir -p $(DIST)
	GOOS=windows GOARCH=amd64 $(BUILD) -o $(DIST)/totem-wifi_windows_amd64.exe $(PKG)
	GOOS=linux   GOARCH=amd64 $(BUILD) -o $(DIST)/totem-wifi_linux_amd64 $(PKG)
	GOOS=linux   GOARCH=arm64 $(BUILD) -o $(DIST)/totem-wifi_linux_arm64 $(PKG)

# Cada paquete contiene el ejecutable con su nombre definitivo, los scripts de
# deploy/ de su sistema, el README, la licencia y las de terceros (licencias/).
# Cada módulo que va dentro de los ejecutables (go version -m) debe aparecer en
# LICENSE con su aviso; si falta alguno, no se genera la release.
licenses: build-all
	@set -e; falta=0; \
	for b in $(DIST)/totem-wifi_windows_amd64.exe $(DIST)/totem-wifi_linux_amd64 $(DIST)/totem-wifi_linux_arm64; do \
	  for m in $$($(GO) version -m $$b | awk '$$1=="dep"{print $$2}'); do \
	    if ! grep -qF "$$m" LICENSE; then echo "FALTA en LICENSE: $$m ($$b)"; falta=1; fi; \
	  done; \
	done; \
	if [ $$falta -ne 0 ]; then echo "Añada su licencia a LICENSE y a licencias/ (ver licencias/LEEME.md)."; exit 1; fi; \
	echo "Licencias: todos los módulos de los ejecutables aparecen en LICENSE."

release: licenses
	@set -e; \
	rm -rf $(DIST)/pkg; \
	w=$(DIST)/pkg/totem-wifi_$(VERSION)_windows_amd64; \
	mkdir -p $$w/licencias; \
	cp $(DIST)/totem-wifi_windows_amd64.exe $$w/totem-wifi.exe; \
	cp deploy/windows/*.ps1 README.md CHANGELOG.md LICENSE $$w/; \
	mkdir -p $$w/docs && cp -r docs/img $$w/docs/; \
	cp internal/web/static/fonts/OFL-*.txt licencias/* $$w/licencias/; \
	(cd $(DIST)/pkg && rm -f ../totem-wifi_$(VERSION)_windows_amd64.zip && zip -qr ../totem-wifi_$(VERSION)_windows_amd64.zip totem-wifi_$(VERSION)_windows_amd64); \
	for arch in amd64 arm64; do \
	  l=$(DIST)/pkg/totem-wifi_$(VERSION)_linux_$$arch; \
	  mkdir -p $$l/licencias; \
	  cp $(DIST)/totem-wifi_linux_$$arch $$l/totem-wifi; \
	  cp deploy/linux/*.sh README.md CHANGELOG.md LICENSE $$l/; \
	  mkdir -p $$l/docs && cp -r docs/img $$l/docs/; \
	  chmod 0755 $$l/totem-wifi $$l/*.sh; \
	  cp internal/web/static/fonts/OFL-*.txt licencias/* $$l/licencias/; \
	  tar -C $(DIST)/pkg -czf $(DIST)/totem-wifi_$(VERSION)_linux_$$arch.tar.gz totem-wifi_$(VERSION)_linux_$$arch; \
	done; \
	rm -rf $(DIST)/pkg; \
	(cd $(DIST) && sha256sum totem-wifi_$(VERSION)_* > SHA256SUMS.txt); \
	echo "Paquetes en $(DIST)/:"; ls -1 $(DIST)/totem-wifi_$(VERSION)_* $(DIST)/SHA256SUMS.txt

test:
	$(GO) test ./...

vet:
	$(GO) vet ./...
	@command -v staticcheck >/dev/null 2>&1 && staticcheck ./... || echo "(staticcheck no instalado: se omite)"

# Desarrollo: configuración y datos en ./dev (ignorado por git).
run:
	@mkdir -p dev
	$(GO) run $(PKG) -config dev/config.json

clean:
	rm -rf $(DIST) totem-wifi totem-wifi.exe
