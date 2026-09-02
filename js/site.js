/*
 * Navigation styling follows Typeless SiteHeader.tsx:11-55. The mobile menu
 * lifecycle follows MiniMax SiteHeader.tsx:344-374: Escape closes the menu,
 * desktop resize resets it, and page scroll is locked only while it is open.
 */
(function LaughingElephantSite() {
  'use strict';

  const toggle = document.getElementById('navToggle');
  const links = document.getElementById('navLinks');
  const navLabels = {
    en: { about: 'About', products: 'Products', manifesto: 'Manifesto', club: 'AI Club' },
    zh: { about: '关于我们', products: '产品', manifesto: '我们的主张', club: 'AI Club' }
  };

  function syncNavLanguage() {
    const language = document.documentElement.lang.toLowerCase().startsWith('zh') ? 'zh' : 'en';
    document.querySelectorAll('[data-nav]').forEach(function updateNavLabel(link) {
      const label = navLabels[language][link.dataset.nav];
      if (label) link.textContent = label;
    });
    document.querySelectorAll('.language-current').forEach(function updateLanguageLabel(label) {
      label.textContent = language === 'zh' ? '中文' : 'English';
    });
    document.querySelectorAll('.language-popover .lang-btn').forEach(function updateLanguageOption(button) {
      const isActive = button.dataset.lang === language;
      button.classList.toggle('active', isActive);
      button.setAttribute('aria-checked', String(isActive));
    });
  }

  function syncMenuState() {
    if (!toggle || !links) return;
    const isOpen = links.classList.contains('open');
    toggle.setAttribute('aria-expanded', String(isOpen));
    document.body.classList.toggle('nav-open', isOpen);
    document.body.style.overflow = isOpen ? 'hidden' : '';
  }

  if (toggle && links) {
    toggle.setAttribute('aria-expanded', 'false');

    toggle.addEventListener('click', function toggleMenu() {
      links.classList.toggle('open');
      syncMenuState();
    });

    links.querySelectorAll('a').forEach(function closeFromLink(link) {
      link.addEventListener('click', function closeMenu() {
        links.classList.remove('open');
        syncMenuState();
      });
    });

    window.addEventListener('keydown', function closeFromEscape(event) {
      if (event.key === 'Escape' && links.classList.contains('open')) {
        links.classList.remove('open');
        syncMenuState();
        toggle.focus();
      }
    });

    window.addEventListener('resize', function closeAtDesktop() {
      if (window.innerWidth > 840 && links.classList.contains('open')) {
        links.classList.remove('open');
        syncMenuState();
      }
    });
  }

  document.querySelectorAll('.lang-btn').forEach(function syncAfterLanguageChange(button) {
    button.addEventListener('click', function updateNavigationCopy() {
      requestAnimationFrame(syncNavLanguage);
    });
  });

  document.querySelectorAll('.language-menu').forEach(function setupLanguageMenu(menu) {
    const trigger = menu.querySelector('.language-trigger');
    const popover = menu.querySelector('.language-popover');
    if (!trigger || !popover) return;

    function closeLanguageMenu(restoreFocus) {
      menu.classList.remove('open');
      trigger.setAttribute('aria-expanded', 'false');
      if (restoreFocus) trigger.focus();
    }

    trigger.addEventListener('click', function toggleLanguageMenu() {
      const willOpen = !menu.classList.contains('open');
      document.querySelectorAll('.language-menu.open').forEach(function closeOtherMenu(otherMenu) {
        if (otherMenu !== menu) {
          otherMenu.classList.remove('open');
          const otherTrigger = otherMenu.querySelector('.language-trigger');
          if (otherTrigger) otherTrigger.setAttribute('aria-expanded', 'false');
        }
      });
      menu.classList.toggle('open', willOpen);
      trigger.setAttribute('aria-expanded', String(willOpen));
      if (willOpen) {
        const selected = popover.querySelector('.lang-btn.active');
        if (selected) selected.focus();
      }
    });

    popover.querySelectorAll('.lang-btn').forEach(function closeAfterChoice(button) {
      button.addEventListener('click', function closeLanguageChoice() {
        closeLanguageMenu(true);
      });
    });

    menu.addEventListener('keydown', function closeLanguageMenuFromEscape(event) {
      if (event.key === 'Escape' && menu.classList.contains('open')) {
        event.preventDefault();
        closeLanguageMenu(true);
        return;
      }

      if (!menu.classList.contains('open') || !['ArrowDown', 'ArrowUp', 'Home', 'End'].includes(event.key)) {
        return;
      }

      event.preventDefault();
      const options = Array.from(popover.querySelectorAll('.lang-btn'));
      const currentIndex = options.indexOf(document.activeElement);
      const nextIndex = event.key === 'Home'
        ? 0
        : event.key === 'End'
          ? options.length - 1
          : event.key === 'ArrowDown'
            ? (currentIndex + 1 + options.length) % options.length
            : (currentIndex - 1 + options.length) % options.length;
      if (options[nextIndex]) options[nextIndex].focus();
    });

    document.addEventListener('click', function closeLanguageMenuFromOutside(event) {
      if (!menu.contains(event.target)) closeLanguageMenu(false);
    });
  });

  syncNavLanguage();

  document.querySelectorAll('nav a').forEach(function markCurrentRoute(link) {
    try {
      const href = link.getAttribute('href');
      if (!href || href.includes('#')) return;
      const current = window.location.pathname.replace(/index\.html$/, '');
      const target = new URL(link.href, window.location.href).pathname.replace(/index\.html$/, '');
      if (current === target) link.setAttribute('aria-current', 'page');
    } catch (_) {
      /* Ignore links without a valid URL. */
    }
  });
})();
