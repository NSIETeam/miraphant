(function () {
  'use strict';

  var nav = document.querySelector('nav');
  var navToggle = document.getElementById('navToggle');
  var navLinks = document.getElementById('navLinks');

  function updateNav() {
    if (nav) nav.classList.toggle('is-scrolled', window.scrollY > 24);
  }

  updateNav();
  window.addEventListener('scroll', updateNav, { passive: true });

  if (navToggle && navLinks) {
    navToggle.setAttribute('aria-expanded', navLinks.classList.contains('open') ? 'true' : 'false');
    navToggle.addEventListener('click', function () {
      window.requestAnimationFrame(function () {
        navToggle.setAttribute('aria-expanded', navLinks.classList.contains('open') ? 'true' : 'false');
      });
    });
    document.addEventListener('keydown', function (event) {
      if (event.key === 'Escape' && navLinks.classList.contains('open')) {
        navLinks.classList.remove('open');
        navToggle.setAttribute('aria-expanded', 'false');
        navToggle.focus();
      }
    });
  }

  document.querySelectorAll('nav a[href]').forEach(function (link) {
    try {
      var target = new URL(link.href, window.location.href);
      var current = window.location.pathname.replace(/index\.html$/, '');
      var destination = target.pathname.replace(/index\.html$/, '');
      if (destination === current && target.hash === '') link.setAttribute('aria-current', 'page');
    } catch (error) {
      /* Ignore malformed external URLs. */
    }
  });

  /* Existing pages animate reveals. Keep content visible if their observer fails. */
  window.setTimeout(function () {
    document.querySelectorAll('.reveal:not(.visible)').forEach(function (element) {
      element.classList.add('visible');
    });
  }, 1200);
})();
