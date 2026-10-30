/* Desktop menu: a button in the top bar opens the sidebar as a drawer. */
(function () {
  'use strict';

  function init() {
    var topbar = document.querySelector('.topbar');
    var nav = document.querySelector('nav.tabbar');
    if (!topbar || !nav || document.getElementById('menu-toggle')) return;

    if (!nav.id) nav.id = 'main-nav';

    var btn = document.createElement('button');
    btn.type = 'button';
    btn.id = 'menu-toggle';
    btn.className = 'menu-toggle';
    btn.setAttribute('aria-label', 'Open menu');
    btn.setAttribute('aria-expanded', 'false');
    btn.setAttribute('aria-controls', nav.id);
    btn.innerHTML =
      '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.2" ' +
      'stroke-linecap="round" aria-hidden="true"><path d="M4 7h16M4 12h16M4 17h16"/></svg>';
    topbar.insertBefore(btn, topbar.firstChild);

    var backdrop = document.createElement('div');
    backdrop.className = 'sidebar-backdrop';
    backdrop.hidden = true;
    document.body.appendChild(backdrop);

    function setOpen(open, returnFocus) {
      document.body.classList.toggle('sidebar-open', open);
      backdrop.hidden = !open;
      btn.setAttribute('aria-expanded', String(open));
      btn.setAttribute('aria-label', open ? 'Close menu' : 'Open menu');
      if (open) {
        var first = nav.querySelector('a, button');
        if (first) first.focus();
      } else if (returnFocus) {
        btn.focus();
      }
    }

    btn.addEventListener('click', function () {
      setOpen(!document.body.classList.contains('sidebar-open'), false);
    });
    backdrop.addEventListener('click', function () { setOpen(false, true); });
    nav.addEventListener('click', function (e) {
      if (e.target.closest && e.target.closest('a')) setOpen(false, false);
    });
    document.addEventListener('keydown', function (e) {
      if (e.key === 'Escape' && document.body.classList.contains('sidebar-open')) {
        e.preventDefault();
        setOpen(false, true);
      }
    });
    window.addEventListener('resize', function () {
      if (window.innerWidth < 1024) setOpen(false, false);
    });
  }

  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', init);
  } else {
    init();
  }
})();
