// Runs before first paint (loaded in <head>) so a saved dark/light choice never flashes.
(function () {
  try {
    var t = localStorage.getItem('pandora_theme');
    if (t === 'light' || t === 'dark') document.documentElement.setAttribute('data-theme', t);
  } catch (e) {}
})();
