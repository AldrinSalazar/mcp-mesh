// Applies the saved theme before the first paint so dark mode doesn't flash.
// A file rather than an inline script so the hub's CSP can stay 'self'.
try {
  var t = localStorage.getItem('mcpmesh.theme')
  if (t === 'dark' || (!t && matchMedia('(prefers-color-scheme: dark)').matches)) {
    document.documentElement.dataset.theme = 'dark'
  }
} catch (e) {}
