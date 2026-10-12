(function () {
  'use strict';
  var el = document.getElementById('log-stream');
  if (!el) return;
  var bench = el.getAttribute('data-bench') || '';
  var svc = el.getAttribute('data-service') || '';
  var url = '/admin/benches/' + encodeURIComponent(bench) + '/logs/stream?service=' + encodeURIComponent(svc);
  var scrollContainer = document.getElementById('logs-scroll');
  var indicator = document.getElementById('logs-live-indicator');

  function scrollToBottom() {
    if (scrollContainer) scrollContainer.scrollTop = scrollContainer.scrollHeight;
  }

  var clearBtn = document.getElementById('clear-log-btn');
  if (clearBtn) {
    clearBtn.addEventListener('click', function () { el.textContent = ''; });
  }

  if (!window.EventSource) {
    el.textContent = 'Error: EventSource is not supported by this browser.';
    return;
  }

  var es = new EventSource(url);

  es.onopen = function () {
    el.textContent = '[Docker Compose Log Stream Connected]\n\n';
  };

  es.onmessage = function (ev) {
    el.textContent += ev.data + '\n';
    scrollToBottom();
  };

  es.onerror = function () {
    el.textContent += '\n\n[Log stream connection closed or timed out]\n';
    scrollToBottom();
    es.close();
    if (indicator) {
      indicator.innerHTML = '<svg class="w-4 h-4 text-zinc-400" fill="none" stroke="currentColor" viewBox="0 0 24 24"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M18.364 18.364A9 9 0 005.636 5.636m12.728 12.728A9 9 0 015.636 5.636m12.728 12.728L5.636 5.636"></path></svg><span class="text-zinc-500 font-semibold">Disconnected</span>';
    }
  };
})();
