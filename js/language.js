(function () {
  var STORAGE_KEY = 'miraphant-lang';
  var LEGACY_KEYS = {
    about: 'about-lang',
    manifesto: 'manifesto-lang',
    otto: 'otto-lang',
    circle: 'circle-lang',
    olivewolf: 'olivewolf-lang'
  };
  var pendingTimer;

  function normalize(lang) {
    return lang === 'zh' || lang === 'zh-CN' ? 'zh' : 'en';
  }

  function currentRoute() {
    var parts = window.location.pathname.split('/').filter(Boolean);
    return parts[parts.length - 1] || '';
  }

  function readStoredLanguage() {
    try {
      var stored = localStorage.getItem(STORAGE_KEY);
      if (stored === 'en' || stored === 'zh' || stored === 'zh-CN') return normalize(stored);

      var routeKey = LEGACY_KEYS[currentRoute()];
      var legacyOrder = routeKey
        ? [routeKey].concat(Object.values(LEGACY_KEYS).filter(function (key) { return key !== routeKey; }))
        : Object.values(LEGACY_KEYS);

      for (var i = 0; i < legacyOrder.length; i += 1) {
        var legacy = localStorage.getItem(legacyOrder[i]);
        if (legacy === 'en' || legacy === 'zh' || legacy === 'zh-CN') {
          stored = normalize(legacy);
          localStorage.setItem(STORAGE_KEY, stored);
          return stored;
        }
      }
    } catch (error) {}
    return 'en';
  }

  function clearLegacyKeys() {
    try {
      Object.values(LEGACY_KEYS).forEach(function (key) { localStorage.removeItem(key); });
    } catch (error) {}
  }

  function set(lang) {
    var normalized = normalize(lang);
    try {
      localStorage.setItem(STORAGE_KEY, normalized);
      clearLegacyKeys();
    } catch (error) {}
    document.documentElement.lang = normalized === 'zh' ? 'zh-CN' : 'en';
    return normalized;
  }

  function reveal() {
    clearTimeout(pendingTimer);
    document.documentElement.classList.remove('miraphant-language-pending');
    var pendingStyle = document.getElementById('miraphant-language-pending-style');
    if (pendingStyle) pendingStyle.remove();
  }

  var initialLanguage = readStoredLanguage();
  document.documentElement.lang = initialLanguage === 'zh' ? 'zh-CN' : 'en';

  if (initialLanguage === 'zh') {
    var style = document.createElement('style');
    style.id = 'miraphant-language-pending-style';
    style.textContent = 'html.miraphant-language-pending body{visibility:hidden}';
    document.head.appendChild(style);
    document.documentElement.classList.add('miraphant-language-pending');
    pendingTimer = setTimeout(reveal, 1500);
  }

  window.MiraphantLanguage = {
    get: readStoredLanguage,
    set: set,
    reveal: reveal,
    storageKey: STORAGE_KEY
  };
})();
