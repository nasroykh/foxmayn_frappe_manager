// Tailwind build for the dashboard; run `make dashboard-css` from the repo root
// (content globs are relative to it). The generated static/tailwind.css is
// committed. Classes that only exist in JS (admin.js toggles bg-emerald-500/10
// and friends) are found because static/*.js is scanned too.
module.exports = {
  darkMode: 'media',
  content: [
    './internal/dashboard/templates/**/*.html',
    './internal/dashboard/static/**/*.js',
  ],
  theme: {
    extend: {
      // System stacks only: the dashboard loads nothing from another origin.
      fontFamily: {
        sans: ['system-ui', '-apple-system', 'Segoe UI', 'Roboto', 'Helvetica Neue', 'Arial', 'sans-serif'],
        mono: ['ui-monospace', 'SFMono-Regular', 'Menlo', 'Consolas', 'Liberation Mono', 'monospace'],
      },
    },
  },
};
