// Tótem Wi-Fi · lógica de la pantalla del quiosco. Sin dependencias externas.
//
// Pantallas: inicio → pasos → inicio.
//   inicio: condiciones de uso (si están activadas) y botón «Empezar».
//   pasos:  1. Conéctate a la red (QR)  2. Solicita tu código  3. Escribe el código.
//           El paso 2 pasa por las fases: antes → cargando → código. Si hay un error,
//           se muestra en el propio paso 2 y se puede volver a intentar.
(function () {
  'use strict';

  var cfg = JSON.parse(document.body.getAttribute('data-config') || '{}');
  var textos = cfg.textos || {};
  var MENSAJE_GENERAL = textos.error_general || 'Ahora mismo no podemos darte un código, inténtalo en unos minutos.';
  var TIMEOUT_MS = 15000;          // el servidor ya corta a los 10 s; margen para la red
  var INACTIVIDAD_MS = 120000;     // sin tocar nada antes de pedir el código: vuelta al inicio

  var estado = 'inicio';           // inicio | pasos
  var fase = 'antes';              // dentro de «pasos»: antes | cargando | codigo
  var ocupado = false;
  var cuentaTimer = null, inactividadTimer = null, esperaTimer = null;
  var fin = 0, total = 0;

  function $(id) { return document.getElementById(id); }

  function mostrar(nombre) {
    var ps = document.querySelectorAll('.pantalla');
    for (var i = 0; i < ps.length; i++) ps[i].classList.toggle('activa', ps[i].id === nombre);
    estado = nombre;
  }

  function ssidElegido() {
    var r = document.querySelector('input[name="ssid"]:checked');
    return r ? r.value : (document.body.getAttribute('data-ssid') || '');
  }

  // ---- Condiciones de uso (solo si están activadas en /admin) ----
  var acepto = $('acepto');
  var btnCondiciones = $('btn-condiciones');
  var textoCondiciones = $('texto-condiciones');
  var avisoAceptar = $('aviso-aceptar');

  function aceptadas() { return !acepto || acepto.checked; }

  function actualizarBoton() {
    var b = $('btn-empezar');
    b.classList.toggle('inactivo', !aceptadas());
    b.setAttribute('aria-disabled', aceptadas() ? 'false' : 'true');
    if (aceptadas() && avisoAceptar) avisoAceptar.hidden = true;
  }

  function mostrarCondiciones(abrir) {
    if (!textoCondiciones) return;
    textoCondiciones.hidden = !abrir;
    textoCondiciones.scrollTop = 0;
    btnCondiciones.setAttribute('aria-expanded', abrir ? 'true' : 'false');
    btnCondiciones.textContent = abrir ? textos.ocultar_condiciones : textos.ver_condiciones;
    $('inicio').classList.toggle('condiciones-abiertas', abrir);
  }

  // Al volver al inicio, la siguiente persona empieza con la casilla sin marcar.
  function reiniciarCondiciones() {
    if (!acepto) return;
    acepto.checked = false;
    mostrarCondiciones(false);
    if (avisoAceptar) avisoAceptar.hidden = true;
    actualizarBoton();
  }

  // ---- Inicio → pasos ----
  function empezar() {
    if (estado !== 'inicio') return;
    if (!aceptadas()) {                  // pulsado sin marcar la casilla
      avisoAceptar.hidden = false;
      $('condiciones').classList.remove('resaltar');
      void $('condiciones').offsetWidth; // reinicia la animación
      $('condiciones').classList.add('resaltar');
      return;
    }
    var ssid = ssidElegido();
    $('qr').src = '/api/qr.svg?ssid=' + encodeURIComponent(ssid);
    $('paso-ssid').textContent = ssid;
    ocultarError();
    ponerFase('antes');
    mostrar('pasos');
    vigilarInactividad();
  }

  function marcar(id, clase) {
    var el = $(id);
    el.classList.remove('activo', 'hecho', 'espera');
    el.classList.add(clase);
  }

  function ponerFase(f) {
    fase = f;
    $('paso2-antes').hidden = f !== 'antes';
    $('paso2-cargando').hidden = f !== 'cargando';
    $('paso2-codigo').hidden = f !== 'codigo';
    $('paso2-titulo').textContent = f === 'codigo' ? textos.paso2_titulo_codigo : textos.paso2_titulo;
    marcar('paso-1', f === 'codigo' ? 'hecho' : 'activo');
    marcar('paso-2', 'activo');
    marcar('paso-3', f === 'codigo' ? 'activo' : 'espera');
  }

  // ---- Paso 2: solicitar el código ----
  function solicitar() {
    if (ocupado || esperaTimer || estado !== 'pasos' || fase !== 'antes') return;
    ocupado = true;
    ocultarError();
    ponerFase('cargando');
    pararInactividad();

    var ctl = window.AbortController ? new AbortController() : null;
    var corte = setTimeout(function () { if (ctl) ctl.abort(); }, TIMEOUT_MS);

    fetch('/api/voucher', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ ssid: ssidElegido(), accepted: aceptadas() }),
      cache: 'no-store',
      credentials: 'same-origin',
      signal: ctl ? ctl.signal : undefined
    }).then(function (r) {
      return r.json().catch(function () { return {}; }).then(function (d) {
        if (r.ok && d.formatted) mostrarCodigo(d);
        else if (r.status === 429 && d.retry_after > 0) esperarParaSolicitar(d.retry_after);
        else mostrarError(d.error || MENSAJE_GENERAL);
      });
    }).catch(function () {
      mostrarError(MENSAJE_GENERAL);
    }).then(function () {
      clearTimeout(corte);
      ocupado = false;
    });
  }

  function mostrarCodigo(d) {
    if (estado !== 'pasos') return;      // se volvió al inicio mientras se esperaba
    var cont = $('codigo-valor');
    while (cont.firstChild) cont.removeChild(cont.firstChild);
    cont.appendChild(document.createTextNode(d.formatted));
    // Una sola línea: «Válido 4 horas · úsalo en menos de 15 minutos».
    $('validez').textContent = [d.valid_for, d.use_within].filter(Boolean).join(' · ');
    ponerFase('codigo');
    iniciarCuenta(d.expires_in_seconds || cfg.display_seconds || 60);
  }

  // El error se muestra en el propio paso 2; el botón sigue disponible para reintentar.
  function mostrarError(msg) {
    if (estado !== 'pasos') return;
    $('error-texto').textContent = msg;
    $('error-texto').hidden = false;
    ponerFase('antes');
    vigilarInactividad();
  }

  // Espera entre códigos del tótem: en lugar de un error, el propio botón muestra la
  // cuenta atrás («Podrás solicitar tu código en N s») y se reactiva solo al llegar a 0.
  function esperarParaSolicitar(segundos) {
    if (estado !== 'pasos') return;
    ponerFase('antes');
    vigilarInactividad();
    var b = $('btn-solicitar');
    var hasta = Date.now() + segundos * 1000;
    b.disabled = true;
    b.classList.add('inactivo');
    function tic() {
      var resto = Math.ceil((hasta - Date.now()) / 1000);
      if (resto <= 0) { pararEspera(); return; }
      b.textContent = (textos.boton_espera || '{s}').replace('{s}', resto);
    }
    tic();
    esperaTimer = setInterval(tic, 250);
  }

  function pararEspera() {
    clearInterval(esperaTimer);
    esperaTimer = null;
    var b = $('btn-solicitar');
    b.disabled = false;
    b.classList.remove('inactivo');
    b.textContent = textos.boton_solicitar || b.textContent;
  }

  function ocultarError() {
    $('error-texto').textContent = '';
    $('error-texto').hidden = true;
  }

  // ---- Cuenta atrás del código ----
  function iniciarCuenta(segundos) {
    total = segundos;
    fin = Date.now() + segundos * 1000;
    tic();
    cuentaTimer = setInterval(tic, 250);
  }

  function tic() {
    var resto = Math.max(0, Math.ceil((fin - Date.now()) / 1000));
    $('cuenta-texto').textContent = (textos.cuenta_atras || '{s}').replace('{s}', resto);
    $('cuenta-barra').style.width = (total ? (100 * resto / total) : 0) + '%';
    if (resto <= 0) terminar();
  }

  // ---- Vuelta al inicio: elimina el código del DOM ----
  function terminar() {
    clearInterval(cuentaTimer);
    cuentaTimer = null;
    pararInactividad();
    pararEspera();
    var cont = $('codigo-valor');
    while (cont.firstChild) cont.removeChild(cont.firstChild);
    $('validez').textContent = '';
    $('cuenta-texto').textContent = '';
    $('qr').removeAttribute('src');
    ocultarError();
    ponerFase('antes');
    reiniciarCondiciones();
    mostrar('inicio');
  }

  // ---- Inactividad en «pasos» antes de pedir el código ----
  function vigilarInactividad() {
    pararInactividad();
    inactividadTimer = setTimeout(function () {
      if (estado === 'pasos' && fase === 'antes') terminar();
    }, INACTIVIDAD_MS);
  }
  function pararInactividad() { clearTimeout(inactividadTimer); inactividadTimer = null; }
  function actividad() { if (estado === 'pasos' && fase === 'antes') vigilarInactividad(); }
  document.addEventListener('pointerdown', actividad);
  document.addEventListener('keydown', actividad);

  // ---- Bloqueos propios de un quiosco ----
  function impedir(e) { e.preventDefault(); }
  document.addEventListener('contextmenu', impedir);
  document.addEventListener('selectstart', impedir);
  document.addEventListener('dragstart', impedir);
  document.addEventListener('gesturestart', impedir);            // pellizco en Safari
  document.addEventListener('touchmove', function (e) {          // pellizco y arrastre
    // Solo se permite desplazar el selector de red y el texto de las condiciones.
    if (e.touches.length > 1 || !e.target.closest('.selector-red, .texto-condiciones')) e.preventDefault();
  }, { passive: false });
  document.addEventListener('wheel', function (e) { if (e.ctrlKey) e.preventDefault(); }, { passive: false });
  document.addEventListener('keydown', function (e) {
    var k = e.key;
    if ((e.ctrlKey || e.metaKey) && (k === '+' || k === '-' || k === '=' || k === '0' || k === 'p' || k === 's' || k === 'a')) {
      e.preventDefault();
    }
    if (k === 'Escape' && estado === 'pasos') terminar();
  });
  var ultimoToque = 0;                                           // doble toque = zoom
  document.addEventListener('touchend', function (e) {
    var ahora = Date.now();
    if (ahora - ultimoToque < 350) e.preventDefault();
    ultimoToque = ahora;
  }, { passive: false });

  // ---- Recarga diaria a las 04:00 ----
  // Solo recarga si el servidor responde (para no dejar el tótem en una página
  // de error del navegador) y si nadie está usando la pantalla.
  function programarRecarga() {
    var ahora = new Date();
    var objetivo = new Date(ahora.getFullYear(), ahora.getMonth(), ahora.getDate(), 4, 0, 0);
    if (objetivo <= ahora) objetivo.setDate(objetivo.getDate() + 1);
    setTimeout(intentarRecarga, objetivo - ahora);
  }
  function intentarRecarga() {
    if (estado !== 'inicio') { setTimeout(intentarRecarga, 30000); return; }
    fetch('/healthz', { cache: 'no-store' }).then(function () {
      window.location.reload();
    }).catch(function () {
      setTimeout(intentarRecarga, 60000);
    });
  }

  // ---- Recarga cuando cambia la configuración en /admin ----
  // Cada minuto pregunta al servidor; solo recarga en la pantalla de inicio, nunca con
  // alguien siguiendo los pasos. Si el servidor no responde, no hace nada.
  function comprobarCambios() {
    if (estado !== 'inicio' || ocupado) return;
    fetch('/api/version', { cache: 'no-store', credentials: 'same-origin' })
      .then(function (r) { return r.ok ? r.json() : null; })
      .then(function (d) {
        if (d && d.rev && cfg.rev && d.rev !== cfg.rev && estado === 'inicio' && !ocupado) {
          window.location.reload();
        }
      })
      .catch(function () {});
  }
  setInterval(comprobarCambios, 60000);

  // ---- Arranque ----
  $('btn-empezar').addEventListener('click', empezar);
  $('btn-solicitar').addEventListener('click', solicitar);
  $('btn-terminado').addEventListener('click', terminar);
  if (acepto) {
    acepto.addEventListener('change', actualizarBoton);
    btnCondiciones.addEventListener('click', function () { mostrarCondiciones(textoCondiciones.hidden); });
    actualizarBoton();
  }
  programarRecarga();
})();
