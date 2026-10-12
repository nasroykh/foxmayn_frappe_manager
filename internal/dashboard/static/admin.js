(function () {
  'use strict';

  // Copy to clipboard helper
  function copyText(text, btn) {
    if (!text) return;
    function done() {
      if (btn) {
        var orig = btn.textContent;
        btn.textContent = 'Copied!';
        btn.classList.add('bg-emerald-500/10', 'text-emerald-500', 'border-emerald-500/20');
        setTimeout(function () { 
          btn.textContent = orig; 
          btn.classList.remove('bg-emerald-500/10', 'text-emerald-500', 'border-emerald-500/20');
        }, 1500);
      }
    }
    if (navigator.clipboard && navigator.clipboard.writeText) {
      navigator.clipboard.writeText(text).then(done).catch(fallback);
    } else {
      fallback();
    }
    function fallback() {
      var ta = document.createElement('textarea');
      ta.value = text;
      ta.style.position = 'fixed';
      ta.style.left = '-9999px';
      document.body.appendChild(ta);
      ta.select();
      try { document.execCommand('copy'); done(); } catch (err) {}
      document.body.removeChild(ta);
    }
  }

  // Setup click listeners for copy buttons
  document.querySelectorAll('[data-copy]').forEach(function (btn) {
    btn.addEventListener('click', function () {
      copyText(btn.getAttribute('data-copy'), btn);
    });
  });

  // CSRF token for fetch(): the layout puts it in a <meta>.
  function csrfToken() {
    var m = document.querySelector('meta[name="csrf-token"]');
    return m ? m.getAttribute('content') : '';
  }

  // Secrets are never in the page. A POST fetches one on demand; the server
  // audits it and answers with Cache-Control: no-store.
  function fetchSecret(url, field) {
    var body = new URLSearchParams();
    body.set('csrf_token', csrfToken());
    body.set('field', field);
    return fetch(url, {
      method: 'POST',
      credentials: 'same-origin',
      headers: { 'Content-Type': 'application/x-www-form-urlencoded' },
      body: body.toString()
    }).then(function (r) {
      if (!r.ok) throw new Error('HTTP ' + r.status);
      return r.json();
    }).then(function (d) { return d.value; });
  }

  // Reveal / Hide toggle: the placeholder is shown until the server is asked.
  var MASK = '••••••••';
  document.querySelectorAll('[data-reveal]').forEach(function (btn) {
    btn.addEventListener('click', function () {
      var code = document.getElementById(btn.getAttribute('data-reveal'));
      if (!code) return;
      if (btn.textContent === 'Hide') {
        code.textContent = MASK;
        code.classList.add('select-none');
        code.classList.remove('select-all');
        btn.textContent = 'Reveal';
        return;
      }
      fetchSecret(btn.getAttribute('data-reveal-url'), btn.getAttribute('data-reveal-field'))
        .then(function (value) {
          code.textContent = value;
          code.classList.remove('select-none');
          code.classList.add('select-all');
          btn.textContent = 'Hide';
        })
        .catch(function () {
          btn.textContent = 'Failed';
          setTimeout(function () { btn.textContent = 'Reveal'; }, 1500);
        });
    });
  });

  // Copy a secret without ever placing it in the DOM.
  document.querySelectorAll('[data-copy-reveal]').forEach(function (btn) {
    btn.addEventListener('click', function () {
      fetchSecret(btn.getAttribute('data-copy-reveal'), btn.getAttribute('data-reveal-field'))
        .then(function (value) { copyText(value, btn); })
        .catch(function () {});
    });
  });

  // Copy the text of another element (terminal panes).
  document.querySelectorAll('[data-copy-from]').forEach(function (btn) {
    btn.addEventListener('click', function () {
      var el = document.getElementById(btn.getAttribute('data-copy-from'));
      if (el) copyText(el.innerText, btn);
    });
  });

  // Mobile navigation drawer.
  var menuBtn = document.getElementById('mobile-menu-btn');
  var menu = document.getElementById('mobile-menu');
  if (menuBtn && menu) {
    menuBtn.addEventListener('click', function () { menu.classList.toggle('hidden'); });
  }

  // Client-side search logic for tables
  var searchInput = document.getElementById('table-search');
  if (searchInput) {
    var rows = document.querySelectorAll('[data-searchable] tbody tr');
    searchInput.addEventListener('input', function () {
      var q = searchInput.value.toLowerCase().trim();
      rows.forEach(function (tr) {
        var text = (tr.getAttribute('data-search') || tr.textContent || '').toLowerCase();
        tr.style.display = !q || text.indexOf(q) !== -1 ? '' : 'none';
      });
    });
  }

  // Confirmation dialogs: data-confirm on a form (submit) or a button/link (click).
  document.querySelectorAll('[data-confirm]').forEach(function (el) {
    var type = el.tagName === 'FORM' ? 'submit' : 'click';
    el.addEventListener(type, function (e) {
      var msg = el.getAttribute('data-confirm');
      if (msg && !window.confirm(msg)) e.preventDefault();
    });
  });

})();
