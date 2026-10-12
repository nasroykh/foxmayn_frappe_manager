(function () {
  'use strict';
  var el = document.getElementById('job-log');
  var id = el && el.getAttribute('data-job-id');
  var scrollContainer = document.getElementById('terminal-scroll');
  var badge = document.getElementById('job-status-badge');
  var indicator = document.getElementById('live-indicator');

  function scrollToBottom() {
    if (scrollContainer) scrollContainer.scrollTop = scrollContainer.scrollHeight;
  }

  scrollToBottom();

  if (!id || !window.EventSource) return;
  var es = new EventSource('/admin/jobs/' + id + '/events');
  es.onmessage = function (ev) {
    try {
      var d = JSON.parse(ev.data);
      if (d.lines) {
        el.textContent = d.lines.join('\n');
        scrollToBottom();
      }

      if (d.status === 'succeeded' || d.status === 'failed' || d.status === 'interrupted') {
        es.close();

        if (badge) {
          if (d.status === 'succeeded') {
            badge.innerHTML = '<span class="inline-flex items-center gap-1.5 rounded-full bg-emerald-50 dark:bg-emerald-900/30 border border-emerald-200 dark:border-emerald-800/30 px-2.5 py-0.5 text-xs font-semibold text-emerald-700 dark:text-emerald-400"><span class="h-1.5 w-1.5 rounded-full bg-emerald-500"></span>Succeeded</span>';
          } else {
            badge.innerHTML = '<span class="inline-flex items-center gap-1.5 rounded-full bg-rose-50 dark:bg-rose-900/30 border border-rose-200 dark:border-rose-800/30 px-2.5 py-0.5 text-xs font-semibold text-rose-700 dark:text-rose-400"><span class="h-1.5 w-1.5 rounded-full bg-rose-500"></span>Failed</span>';
          }
        }

        if (indicator) {
          indicator.innerHTML = '<svg class="w-4 h-4 text-zinc-400" fill="none" stroke="currentColor" viewBox="0 0 24 24"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M5 13l4 4L19 7"></path></svg><span>Stream Completed</span>';
        }
      }
    } catch (e) {}
  };
})();
