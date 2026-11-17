// Tótem Wi-Fi · lógica de la pantalla del quiosco.
// Estados: inicio → cargando → codigo | error → inicio. Sin dependencias externas.
(function () {
  'use strict';

  var cfg = JSON.parse(document.body.getAttribute('data-config') || '{}');
  var textos = cfg.textos || {};
  var MENSAJE_GENERAL = textos.error_general || 'Ahora mismo no podemos darte un código, inténtalo en unos minutos.';
  var ERROR_SEGUNDOS = 7;      // tiempo que se muestra un error antes de volver a inicio
  var TIMEOUT_MS = 15000;      // el servidor ya corta a los 10 s; margen para la red

  var estado = 'inicio';
  var ocupado = false;
  var cuentaTimer = null, errorTimer = null;
  var fin = 0, total = 0;

  function $(id) { return document.getElementById(id); }

  function mostrar(nombre) {
    var ps = document.querySelectorAll('.pantalla');
    for (var i = 0; i < ps.length; i++) {
      ps[i].classList.toggle('activa', ps[i].id === nombre);
    }
    estado = nombre;
  }

  function ssidElegido() {
    var r = document.querySelector('input[name="ssid"]:checked');
    return r ? r.value : (document.body.getAttribute('data-ssid') || '');
  }

  // ---- Petición del código ----
  // ---- Condiciones de uso (solo si están activadas en /admin) ----
  var acepto = $('acepto');
  var btnCondiciones = $('btn-condiciones');
  var textoCondiciones = $('texto-condiciones');
  var avisoAceptar = $('aviso-aceptar');

  function aceptadas() { return !acepto || acepto.checked; }

  function actualizarBoton() {
    var b = $('btn-obtener');
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

  // Tras entregar un código, el siguiente ciudadano empieza con la casilla sin marcar.
  function reiniciarCondiciones() {
    if (!acepto) return;
    acepto.checked = false;
    mostrarCondiciones(false);
    if (avisoAceptar) avisoAceptar.hidden = true;
    actualizarBoton();
  }

  function obtener() {
    if (ocupado || estado !== 'inicio') return;
    if (!aceptadas()) {                 // pulsado sin marcar la casilla
      avisoAceptar.hidden = false;
      $('condiciones').classList.remove('resaltar');
      void $('condiciones').offsetWidth; // reinicia la animación
      $('condiciones').classList.add('resaltar');
      return;
    }
    ocupado = true;
    mostrar('cargando');

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
        else mostrarError(d.error || MENSAJE_GENERAL);
      });
    }).catch(function () {
      mostrarError(MENSAJE_GENERAL);
    }).then(function () {
      clearTimeout(corte);
      ocupado = false;
    });
  }

  // ---- Pantalla del código ----
  function mostrarCodigo(d) {
    var cont = $('codigo-valor');
    while (cont.firstChild) cont.removeChild(cont.firstChild);
    cont.appendChild(document.createTextNode(d.formatted));
    $('qr').src = d.qr_url;
    $('validez').textContent = d.valid_for || '';
    $('paso1').textContent = (textos.paso1 || '{ssid}').replace('{ssid}', d.ssid);
    mostrar('codigo');
    iniciarCuenta(d.expires_in_seconds || cfg.display_seconds || 60);
  }

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

  // Elimina el código del DOM y vuelve a inicio.
  function terminar() {
    clearInterval(cuentaTimer);
    cuentaTimer = null;
    var cont = $('codigo-valor');
    while (cont.firstChild) cont.removeChild(cont.firstChild);
    $('qr').removeAttribute('src');
    $('validez').textContent = '';
    $('cuenta-texto').textContent = '';
    reiniciarCondiciones();
    mostrar('inicio');
  }

  // ---- Error ----
  function mostrarError(msg) {
    $('error-texto').textContent = msg;
    mostrar('error');
    clearTimeout(errorTimer);
    errorTimer = setTimeout(volverDeError, ERROR_SEGUNDOS * 1000);
  }

  function volverDeError() {
    clearTimeout(errorTimer);
    if (estado === 'error') {
      $('error-texto').textContent = '';
      mostrar('inicio');
    }
  }

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
    if (k === 'Escape' && estado === 'codigo') terminar();
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

  // ---- Arranque ----
  $('btn-obtener').addEventListener('click', obtener);
  if (acepto) {
    acepto.addEventListener('change', actualizarBoton);
    btnCondiciones.addEventListener('click', function () { mostrarCondiciones(textoCondiciones.hidden); });
    actualizarBoton();
  }
  $('btn-terminado').addEventListener('click', terminar);
  $('error').addEventListener('click', volverDeError);
  programarRecarga();
})();
