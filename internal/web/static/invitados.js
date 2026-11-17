// Tótem Wi-Fi · página de invitados conectados (sin dependencias).
// Búsqueda, orden por columna, paginación y desconexión de uno, varios o todos.
(function () {
  'use strict';
  var csrf = (document.querySelector('meta[name="csrf"]') || {}).content || '';
  var POR_PAGINA = 50;
  var $ = function (id) { return document.getElementById(id); };
  var tabla = $('tabla');
  var resultado = $('resultado');

  function enviar(url, datos, boton) {
    resultado.classList.remove('mal');
    resultado.textContent = 'Un momento…';
    if (boton) boton.disabled = true;
    return fetch(url, {
      method: 'POST',
      headers: { 'X-CSRF-Token': csrf, 'Content-Type': 'application/x-www-form-urlencoded' },
      body: new URLSearchParams(datos).toString(),
      credentials: 'same-origin', cache: 'no-store'
    }).then(function (r) {
      return r.json().catch(function () { return { ok: false, message: 'Respuesta inesperada (HTTP ' + r.status + ').' }; });
    }).catch(function () {
      return { ok: false, message: 'No se ha podido contactar con el servicio.' };
    }).then(function (d) {
      resultado.textContent = (d.layer ? '[' + d.layer + '] ' : '') + (d.message || '');
      resultado.classList.toggle('mal', !d.ok);
      if (boton) boton.disabled = false;
      // El resumen queda guardado en la sesión y se ve tras recargar con la lista al día.
      if (d.ok || /desconectad/.test(d.message || '')) setTimeout(function () { location.reload(); }, 900);
      return d;
    });
  }

  function desconectar(ids, boton) {
    return enviar('/admin/api/guests/disconnect', { ids: ids.join(',') }, boton);
  }

  // ---- Desconectar todos (pide la contraseña) ----
  var panel = $('panel-todos');
  $('btn-todos').addEventListener('click', function () {
    panel.hidden = !panel.hidden;
    if (!panel.hidden) $('pw-todos').focus();
  });
  $('btn-cancelar-todos').addEventListener('click', function () { panel.hidden = true; $('pw-todos').value = ''; });
  $('btn-confirmar-todos').addEventListener('click', function () {
    var pw = $('pw-todos').value;
    if (!pw) { $('pw-todos').focus(); return; }
    enviar('/admin/api/guests/disconnect-all', { password: pw, invalidar: $('invalidar').checked ? '1' : '0' }, this)
      .then(function () { $('pw-todos').value = ''; });
  });

  if (!tabla) return; // no hay invitados

  var cuerpo = tabla.tBodies[0];
  var filas = Array.prototype.slice.call(cuerpo.rows);
  var pagina = 0;
  var orden = { campo: 'conectado', desc: true };

  function visibles() {
    var q = $('buscar').value.trim().toLowerCase();
    return filas.filter(function (f) {
      if (!q) return true;
      return (f.dataset.nombre + ' ' + f.dataset.ip + ' ' + f.dataset.mac).toLowerCase().indexOf(q) >= 0;
    });
  }

  function seleccionadas() {
    return filas.filter(function (f) { var c = f.querySelector('.sel'); return c && c.checked; });
  }

  function actualizarSeleccion() {
    var n = seleccionadas().length;
    $('n-sel').textContent = n;
    $('btn-seleccionados').disabled = n === 0;
    var candidatas = visibles().filter(function (f) { return f.querySelector('.sel'); });
    var marcadas = candidatas.filter(function (f) { return f.querySelector('.sel').checked; }).length;
    $('sel-todos').checked = candidatas.length > 0 && marcadas === candidatas.length;
    $('sel-todos').indeterminate = marcadas > 0 && marcadas < candidatas.length;
  }

  function comparar(a, b) {
    var c = orden.campo, va = a.dataset[c] || '', vb = b.dataset[c] || '';
    if (c === 'nombre' && (!va || !vb) && va !== vb) return va ? -1 : 1; // sin nombre, siempre al final
    var r = (c === 'conectado' || c === 'hasta') ? (Number(va) - Number(vb))
      : va.localeCompare(vb, 'es', { numeric: true, sensitivity: 'base' });
    return orden.desc ? -r : r;
  }

  function pintar() {
    var lista = visibles().sort(comparar);
    var paginas = Math.max(1, Math.ceil(lista.length / POR_PAGINA));
    if (pagina >= paginas) pagina = paginas - 1;
    filas.forEach(function (f) { f.hidden = true; });
    lista.forEach(function (f, i) {
      cuerpo.appendChild(f); // reordena
      f.hidden = Math.floor(i / POR_PAGINA) !== pagina;
    });
    $('total').textContent = lista.length;
    $('sin-resultados').hidden = lista.length > 0;
    var pag = $('paginacion');
    pag.innerHTML = '';
    if (paginas > 1) {
      for (var p = 0; p < paginas; p++) {
        var b = document.createElement('button');
        b.type = 'button';
        b.textContent = String(p + 1);
        b.className = p === pagina ? 'activa' : '';
        b.addEventListener('click', (function (n) { return function () { pagina = n; pintar(); }; })(p));
        pag.appendChild(b);
      }
    }
    actualizarSeleccion();
  }

  $('buscar').addEventListener('input', function () { pagina = 0; pintar(); });

  Array.prototype.forEach.call(tabla.tHead.querySelectorAll('th[data-orden]'), function (th) {
    th.addEventListener('click', function () {
      var campo = th.getAttribute('data-orden');
      orden = { campo: campo, desc: orden.campo === campo ? !orden.desc : (campo === 'conectado' || campo === 'hasta') };
      Array.prototype.forEach.call(tabla.tHead.querySelectorAll('th'), function (x) { x.classList.remove('ordenada', 'asc', 'desc'); });
      th.classList.add('ordenada', orden.desc ? 'desc' : 'asc');
      pintar();
    });
  });

  // Seleccionar todos: marca todas las filas que coinciden con la búsqueda (todas las páginas).
  $('sel-todos').addEventListener('change', function () {
    var marcar = this.checked;
    visibles().forEach(function (f) { var c = f.querySelector('.sel'); if (c) c.checked = marcar; });
    actualizarSeleccion();
  });
  cuerpo.addEventListener('change', function (e) { if (e.target.classList.contains('sel')) actualizarSeleccion(); });

  cuerpo.addEventListener('click', function (e) {
    var id = e.target.getAttribute && e.target.getAttribute('data-desconectar');
    if (!id) return;
    var fila = e.target.closest('tr');
    var nombre = fila.dataset.nombre || fila.dataset.mac;
    if (window.confirm('¿Desconectar a «' + nombre + '» (' + fila.dataset.ip + ')?')) desconectar([id], e.target);
  });

  $('btn-seleccionados').addEventListener('click', function () {
    var ids = seleccionadas().map(function (f) { return f.dataset.id; });
    if (!ids.length) return;
    if (window.confirm('¿Desconectar a ' + ids.length + ' invitado' + (ids.length > 1 ? 's' : '') + '?')) desconectar(ids, this);
  });

  pintar();
})();
