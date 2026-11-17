package voucher

// Invitados conectados y mantenimiento de los vouchers del tótem.

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"totem-wifi/internal/config"
	"totem-wifi/internal/unifi"
)

// paralelo es cuántas peticiones simultáneas se hacen a UniFi en las operaciones por lotes.
const paralelo = 4

// GuestRow es un invitado tal como se muestra en /admin/invitados.
type GuestRow struct {
	unifi.Guest
	ExpiresAt time.Time // fin del acceso (cero si no se conoce)
	Method    string    // VOUCHER, API u OTHER
}

// DisconnectResult resume una desconexión por lotes.
type DisconnectResult struct {
	Done        int
	Failed      []string // descripción de cada fallo
	Invalidated int      // vouchers del tótem invalidados (solo «desconectar todos»)
}

// clientSite devuelve el cliente y el sitio, detectando el sitio si hace falta.
func (s *Service) clientSite(ctx context.Context) (unifi.Client, string, error) {
	cfg := s.cfg.Get()
	c := s.client(cfg)
	site := cfg.UniFi.SiteID
	if !c.Mock() && site == "" {
		var err error
		if site, err = s.ensureSite(ctx, c); err != nil {
			return nil, "", err
		}
	}
	return c, site, nil
}

// Guests lista los invitados conectados, con la hora a la que termina su acceso.
// includePending incluye también a quienes aún no han introducido el código.
func (s *Service) Guests(ctx context.Context, includePending bool) ([]GuestRow, error) {
	c, site, err := s.clientSite(ctx)
	if err != nil {
		return nil, err
	}
	guests, err := c.ListGuests(ctx, site)
	if err != nil {
		return nil, err
	}
	rows := make([]GuestRow, 0, len(guests))
	for _, g := range guests {
		if g.Authorized || includePending {
			rows = append(rows, GuestRow{Guest: g})
		}
	}
	// La fecha de fin solo viene en la ficha de cada invitado: se piden en paralelo.
	// Si alguna falla, la fila se muestra igualmente sin esa fecha.
	var wg sync.WaitGroup
	sem := make(chan struct{}, paralelo)
	for i := range rows {
		if !rows[i].Authorized {
			continue
		}
		wg.Add(1)
		go func(r *GuestRow) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			if a, err := c.GuestDetails(ctx, site, r.ID); err == nil {
				r.ExpiresAt, r.Method = a.ExpiresAt, a.Method
			}
		}(&rows[i])
	}
	wg.Wait()
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].ConnectedAt.After(rows[j].ConnectedAt) })
	return rows, nil
}

// DisconnectGuests desconecta a los invitados indicados (por su id de UniFi).
func (s *Service) DisconnectGuests(ctx context.Context, ids []string, adminIP string) (DisconnectResult, error) {
	c, site, err := s.clientSite(ctx)
	if err != nil {
		return DisconnectResult{}, err
	}
	guests, err := c.ListGuests(ctx, site)
	if err != nil {
		return DisconnectResult{}, err
	}
	byID := map[string]unifi.Guest{}
	for _, g := range guests {
		byID[g.ID] = g
	}
	var (
		res DisconnectResult
		mu  sync.Mutex
		wg  sync.WaitGroup
		sem = make(chan struct{}, paralelo)
	)
	seen := map[string]bool{}
	for _, id := range ids {
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		g, ok := byID[id]
		if !ok {
			mu.Lock() // las desconexiones en curso también escriben en res
			res.Failed = append(res.Failed, "un dispositivo ya no estaba conectado")
			mu.Unlock()
			continue
		}
		wg.Add(1)
		go func(g unifi.Guest) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			err := c.UnauthorizeGuest(ctx, site, g.ID)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				_, hint := unifi.Explain(err)
				res.Failed = append(res.Failed, fmt.Sprintf("%s (%s): %s", guestLabel(g), g.IP, hint))
				s.log.Printf("ERROR al desconectar al invitado mac=%s ip=%s: %v", g.MAC, g.IP, err)
				return
			}
			res.Done++
			s.log.Printf("admin: invitado desconectado mac=%s ip=%s (desde %s)", g.MAC, g.IP, adminIP)
		}(g)
	}
	wg.Wait()
	sort.Strings(res.Failed)
	return res, nil
}

func guestLabel(g unifi.Guest) string {
	if g.Name != "" {
		return g.Name
	}
	return g.MAC
}

// DisconnectAll desconecta a todos los invitados autorizados y, si se pide, invalida
// después los códigos del tótem aún vigentes para que no puedan volver a entrar.
func (s *Service) DisconnectAll(ctx context.Context, invalidate bool, adminIP string) (DisconnectResult, error) {
	rows, err := s.Guests(ctx, false)
	if err != nil {
		return DisconnectResult{}, err
	}
	ids := make([]string, 0, len(rows))
	for _, r := range rows {
		ids = append(ids, r.ID)
	}
	s.log.Printf("admin: desconectar a todos los invitados (%d) desde %s; invalidar códigos del tótem: %v", len(ids), adminIP, invalidate)
	res, err := s.DisconnectGuests(ctx, ids, adminIP)
	if err != nil {
		return res, err
	}
	if invalidate {
		n, err := s.InvalidateTotemVouchers(ctx)
		if err != nil {
			_, hint := unifi.Explain(err)
			res.Failed = append(res.Failed, "no se han podido invalidar los códigos del tótem: "+hint)
		}
		res.Invalidated = n
	}
	return res, nil
}

// totemVoucherFilter selecciona los vouchers creados por este tótem: su nota y la de
// los vouchers de prueba de /admin. Los creados a mano en UniFi no coinciden.
func totemVoucherFilter(cfg config.Config) string {
	note := cfg.Voucher.Note
	return fmt.Sprintf("name.in(%s, %s)", unifi.FilterString(note), unifi.FilterString(note+" (prueba admin)"))
}

// InvalidateTotemVouchers borra en UniFi los vouchers del tótem que aún no han caducado.
func (s *Service) InvalidateTotemVouchers(ctx context.Context) (int, error) {
	c, site, err := s.clientSite(ctx)
	if err != nil {
		return 0, err
	}
	filter := fmt.Sprintf("and(%s, expired.eq(false))", totemVoucherFilter(s.cfg.Get()))
	n, err := c.DeleteVouchers(ctx, site, filter)
	if err == nil {
		s.log.Printf("admin: invalidados %d códigos del tótem que seguían vigentes", n)
	}
	return n, err
}

// CleanupUnused borra los vouchers del tótem que nadie ha usado y tienen más de
// voucher.unused_cleanup_hours horas. Devuelve cuántos ha borrado.
func (s *Service) CleanupUnused(ctx context.Context) (int, error) {
	cfg := s.cfg.Get()
	hours := cfg.Voucher.UnusedCleanupHours
	if hours <= 0 || cfg.UniFi.Mock {
		return 0, nil
	}
	c, site, err := s.clientSite(ctx)
	if err != nil {
		return 0, err
	}
	limit := s.Now().Add(-time.Duration(hours) * time.Hour)
	filter := fmt.Sprintf("and(%s, authorizedGuestCount.eq(0), createdAt.lt(%s))", totemVoucherFilter(cfg), unifi.FilterTime(limit))
	n, err := c.DeleteVouchers(ctx, site, filter)
	if err != nil {
		s.log.Printf("ERROR en la limpieza de códigos sin usar: %v", err)
		return 0, err
	}
	if n > 0 {
		s.log.Printf("limpieza: borrados %d códigos del tótem sin usar de más de %d h", n, hours)
	}
	return n, nil
}

// StartCleanup lanza la limpieza periódica (cada hora; la primera, a los 2 minutos).
func (s *Service) StartCleanup(ctx context.Context) {
	go func() {
		t := time.NewTimer(2 * time.Minute)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				cctx, cancel := context.WithTimeout(ctx, 2*unifi.Timeout)
				s.CleanupUnused(cctx) //nolint:errcheck // el error queda en el log
				cancel()
				t.Reset(time.Hour)
			}
		}
	}()
}

// SanitizeIDs limpia la lista de ids recibida del formulario (separados por comas).
func SanitizeIDs(raw []string) []string {
	var out []string
	for _, r := range raw {
		for _, id := range strings.Split(r, ",") {
			if id = strings.TrimSpace(id); id != "" && len(id) <= 64 {
				out = append(out, id)
			}
		}
	}
	return out
}
