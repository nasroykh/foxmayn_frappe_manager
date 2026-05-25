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

  // Reveal / Mask toggle helper
  document.querySelectorAll('[data-reveal]').forEach(function (btn) {
    btn.addEventListener('click', function () {
      var id = btn.getAttribute('data-reveal');
      var code = document.getElementById(id);
      if (!code) return;
      
      // If we are revealing
      if (code.classList.contains('blur-[4px]')) {
        code.classList.remove('blur-[4px]', 'select-none');
        code.classList.add('select-all');
        btn.textContent = 'Hide';
      } else {
        code.classList.add('blur-[4px]', 'select-none');
        code.classList.remove('select-all');
        btn.textContent = 'Reveal';
      }
    });
  });

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

  // Direct simple confirmation dialogs
  document.querySelectorAll('[data-confirm]').forEach(function (btn) {
    btn.addEventListener('click', function (e) {
      var msg = btn.getAttribute('data-confirm');
      if (msg && !window.confirm(msg)) e.preventDefault();
    });
  });

})();
