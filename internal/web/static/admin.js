// Kioscode · acciones de la página de administración (sin dependencias).
(function () {
  'use strict';
  var meta = document.querySelector('meta[name="csrf"]');
  if (!meta) return;
  var csrf = meta.getAttribute('content');

  function llamar(url, datos, salida, boton) {
    salida.textContent = 'Un momento…';
    salida.classList.remove('mal');
    boton.disabled = true;
    var cuerpo = new URLSearchParams(datos || {});
    return fetch(url, {
      method: 'POST',
      headers: { 'X-CSRF-Token': csrf, 'Content-Type': 'application/x-www-form-urlencoded' },
      body: cuerpo.toString(),
      credentials: 'same-origin',
      cache: 'no-store'
    }).then(function (r) {
      return r.json().catch(function () { return { ok: false, message: 'Respuesta inesperada (HTTP ' + r.status + ').' }; });
    }).catch(function () {
      return { ok: false, message: 'No se ha podido contactar con el servicio.' };
    }).then(function (d) {
      var texto = (d.layer ? '[' + d.layer + '] ' : '') + (d.message || '');
      salida.textContent = texto;
      salida.classList.toggle('mal', !d.ok);
      boton.disabled = false;
      return d;
    });
  }

  var probar = document.getElementById('btn-probar');
  var prueba = document.getElementById('btn-prueba-voucher');
  var huella = document.getElementById('btn-huella');
  var resAcc = document.getElementById('res-acciones');

  if (probar) probar.addEventListener('click', function () {
    llamar('/admin/api/check', null, resAcc, probar);
  });
  if (prueba) prueba.addEventListener('click', function () {
    if (!window.confirm('¿Generar un voucher de prueba? Si el modo real está activo, será un código válido y contará en el límite diario.')) return;
    llamar('/admin/api/test-voucher', null, resAcc, prueba);
  });
  // Cambios en «Acceso»: pedir la contraseña de administración.
  var confirmar = document.getElementById('confirmar-acceso');
  ['ac', 'aac', 'listen'].forEach(function (id) {
    var el = document.getElementById(id);
    if (!el || !confirmar) return;
    var inicial = el.value;
    el.addEventListener('input', function () {
      if (el.value !== inicial) confirmar.hidden = false;
    });
  });

  // Estado del cortafuegos: si hay cambios pendientes, consultar hasta que se apliquen.
  var fwEstado = document.getElementById('fw-estado');
  var fwTexto = document.getElementById('fw-texto');
  var etiquetas = { ok: 'Actualizado', pendiente: 'Aplicando…', inactivo: 'Desactivado', error: 'Error' };
  function consultarCortafuegos(intentos) {
    fetch('/admin/api/firewall', { credentials: 'same-origin', cache: 'no-store' })
      .then(function (r) { return r.json(); })
      .then(function (d) {
        fwEstado.textContent = etiquetas[d.state] || d.state;
        fwEstado.className = d.state === 'ok' ? 'ok' : (d.state === 'error' ? 'ko' : '');
        fwTexto.textContent = d.text;
        fwTexto.classList.toggle('mal', d.state === 'error');
        if (d.state === 'pendiente' && intentos > 0) {
          setTimeout(function () { consultarCortafuegos(intentos - 1); }, 2000);
        } else if (d.state === 'pendiente') {
          fwTexto.textContent = 'El cortafuegos no se ha actualizado. Compruebe en el servidor: systemctl status kioscode-firewall';
          fwTexto.classList.add('mal');
        }
      })
      .catch(function () {});
  }
  if (fwEstado && fwEstado.getAttribute('data-state') === 'pendiente') {
    setTimeout(function () { consultarCortafuegos(10); }, 1500);
  }

  // ---- Imágenes: subir (queda seleccionada) y borrar ----
  var resImg = document.getElementById('res-imagenes');
  function mostrarResultado(d) {
    resImg.textContent = d.message || '';
    resImg.classList.toggle('mal', !d.ok);
    if (d.ok) setTimeout(function () { window.location.reload(); }, 600);
  }
  Array.prototype.forEach.call(document.querySelectorAll('[data-subir]'), function (btn) {
    btn.addEventListener('click', function () {
      var input = document.getElementById(btn.getAttribute('data-archivo'));
      if (!input.files || !input.files[0]) { input.click(); return; }
      var datos = new FormData();
      datos.append('archivo', input.files[0]);
      btn.disabled = true;
      resImg.classList.remove('mal');
      resImg.textContent = 'Subiendo…';
      fetch('/admin/api/upload?tipo=' + btn.getAttribute('data-subir'), {
        method: 'POST', body: datos, credentials: 'same-origin', cache: 'no-store',
        headers: { 'X-CSRF-Token': csrf }
      }).then(function (r) {
        if (r.status === 413) return { ok: false, message: 'El fichero es demasiado grande.' };
        return r.json().catch(function () { return { ok: false, message: 'Respuesta inesperada (HTTP ' + r.status + ').' }; });
      }).catch(function () {
        return { ok: false, message: 'No se ha podido subir el fichero.' };
      }).then(function (d) { btn.disabled = false; mostrarResultado(d); });
    });
  });
  // Al elegir un fichero, el botón pasa a «Subir …»; sin fichero, abre el selector.
  Array.prototype.forEach.call(document.querySelectorAll('.subida input[type=file]'), function (input) {
    input.addEventListener('change', function () { resImg.textContent = ''; });
  });
  Array.prototype.forEach.call(document.querySelectorAll('[data-borrar]'), function (btn) {
    btn.addEventListener('click', function () {
      var nombre = btn.getAttribute('data-borrar');
      if (!window.confirm('¿Borrar la imagen «' + nombre + '» del servidor?')) return;
      llamar('/admin/api/image-delete', { nombre: nombre }, resImg, btn).then(function (d) {
        if (d.ok) setTimeout(function () { window.location.reload(); }, 600);
      });
    });
  });

  if (huella) huella.addEventListener('click', function () {
    var url = document.getElementById('base_url').value;
    llamar('/admin/api/fingerprint', { base_url: url }, document.getElementById('res-huella'), huella).then(function (d) {
      if (d.ok && d.sha256) document.getElementById('cert').value = d.sha256;
    });
  });
})();
