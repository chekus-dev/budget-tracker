/* ==========================================================================
   Budget Tracker — shared client behaviour
   Loaded by nav.html, so it runs on every signed-in screen.

   Deliberately small. The app is server-rendered: adding, deleting and
   restoring are ordinary form POSTs that reload the page, so the server stays
   the single source of truth and everything works with JavaScript disabled.
   What lives here is only progressive enhancement — the navigation highlight,
   the modals, the undo toast and the slow-navigation skeleton.

   Theme handling is NOT here: head.html applies the theme before first paint
   so there is no flash of the wrong colours.
   ========================================================================== */
(function () {
  'use strict';

  /* ---------------------------------------------------------------- nav --- */
  /* The server does not tell us which page we are on, so derive it from the
     path. archive/settings have sub-paths that should keep their tab lit. */
  function markActiveNav() {
    var path = window.location.pathname;
    var match = '/';

    if (path.indexOf('/report') === 0) match = '/report';
    else if (path.indexOf('/archive') === 0) match = '/archive';
    else if (path.indexOf('/export') === 0) match = '/export';
    else if (path.indexOf('/settings') === 0) match = '/settings';

    document.querySelectorAll('.nav-item[data-nav]').forEach(function (el) {
      if (el.getAttribute('data-nav') === match) {
        el.setAttribute('aria-current', 'page');
      } else {
        el.removeAttribute('aria-current');
      }
    });
  }

  function todayLocal() {
    var d = new Date();
    var pad = function (n) { return String(n).padStart(2, '0'); };
    return d.getFullYear() + '-' + pad(d.getMonth() + 1) + '-' + pad(d.getDate());
  }

  function wireDateTriggers() {
    document.querySelectorAll('[data-date-target]').forEach(function (button) {
      button.addEventListener('click', function () {
        var targetId = button.getAttribute('data-date-target');
        if (!targetId) return;

        var input = document.getElementById(targetId);
        if (!input) return;

        if (!input.value) input.value = todayLocal();

        if (typeof input.showPicker === 'function') {
          input.showPicker();
          return;
        }

        input.focus();
        if (typeof input.click === 'function') input.click();
      });
    });
  }

  function wireArchiveSearch() {
    var form = document.querySelector('form[role="search"][action="/archive"]');
    if (!form) return;

    var searchInput = form.querySelector('#archive-q');
    var filterFields = form.querySelectorAll('select, input[type="date"]');
    var timer = null;

    function submitSearch() {
      if (timer) window.clearTimeout(timer);
      timer = window.setTimeout(function () {
        if (typeof form.requestSubmit === 'function') {
          form.requestSubmit();
          return;
        }
        form.submit();
      }, 200);
    }

    if (searchInput) {
//       searchInput.addEventListener('input', submitSearch);
    }

    filterFields.forEach(function (field) {
      field.addEventListener('change', submitSearch);
    });
  }

  /* --------------------------------------------------------------- modal --- */
  var openModalEl = null;
  var lastFocused = null;

  var FOCUSABLE = 'a[href], button:not([disabled]), input:not([disabled]), select:not([disabled]), textarea:not([disabled]), [tabindex]:not([tabindex="-1"])';

  function openModal(modal) {
    if (!modal) return;
    lastFocused = document.activeElement;
    modal.hidden = false;
    openModalEl = modal;

    var first = modal.querySelector(FOCUSABLE);
    if (first) first.focus();

    document.addEventListener('keydown', onModalKeydown);
  }

  function closeModal(modal) {
    if (!modal) return;
    modal.hidden = true;
    if (openModalEl === modal) openModalEl = null;

    document.removeEventListener('keydown', onModalKeydown);

    if (lastFocused && typeof lastFocused.focus === 'function') lastFocused.focus();
    lastFocused = null;
  }

  function onModalKeydown(e) {
    if (!openModalEl) return;

    if (e.key === 'Escape') {
      e.preventDefault();
      closeModal(openModalEl);
      return;
    }

    /* Keep Tab inside the dialog while it is open. */
    if (e.key === 'Tab') {
      var items = Array.prototype.filter.call(
        openModalEl.querySelectorAll(FOCUSABLE),
        function (el) { return el.offsetParent !== null; }
      );
      if (!items.length) return;

      var first = items[0];
      var last = items[items.length - 1];

      if (e.shiftKey && document.activeElement === first) {
        e.preventDefault();
        last.focus();
      } else if (!e.shiftKey && document.activeElement === last) {
        e.preventDefault();
        first.focus();
      }
    }
  }

  function wireModals() {
    document.querySelectorAll('[data-close-modal]').forEach(function (btn) {
      btn.addEventListener('click', function () {
        closeModal(btn.closest('.modal'));
      });
    });

    /* Clicking the backdrop (not the panel) dismisses. */
    document.querySelectorAll('.modal').forEach(function (modal) {
      modal.addEventListener('mousedown', function (e) {
        if (e.target === modal) closeModal(modal);
      });
    });
  }

  /* ---------------------------------------------------------- add expense --- */
  /* One modal, two jobs: adding and editing. Editing reuses the same form
     rather than duplicating it, so a field added to one is a field added to
     both — and the server sees the identical submission shape either way,
     differing only in the URL it posts to. */
  function wireAddExpense() {
    var fab = document.getElementById('fab-add');
    var modal = document.getElementById('add-modal');
    if (!fab || !modal) return;

    var form = modal.querySelector('#add-form');
    var select = modal.querySelector('#add-category');
    var dateInput = modal.querySelector('#add-date');
    var descInput = modal.querySelector('#add-description');
    var descLabel = modal.querySelector('#add-description-label');
    var amountInput = modal.querySelector('#add-amount');
    var monthLabel = modal.querySelector('#add-modal-month');
    var title = modal.querySelector('#add-modal-title');
    var modeLabel = modal.querySelector('#add-modal-mode');
    var submitLabel = modal.querySelector('#add-submit-label');
    var origDate = modal.querySelector('#edit-orig-date');
    var kindInput = modal.querySelector('#add-kind');
    var kindButtons = modal.querySelectorAll('[data-kind]');
    var loadedCategories = false;
    var editing = false;
    var kind = 'expense';

    /* The wording depends on two things at once — adding or editing, and
       expense or income — so it is computed from those rather than read back
       out of the markup. The template still renders the no-JavaScript case,
       which is the one a plain form POST can produce: adding an expense. */
    var WORDS = {
      expense: {
        noun: 'expense',
        article: 'an expense',
        desc: 'What did you spend on?',
        hint: 'Lunch, bus fare, data…'
      },
      income: {
        noun: 'income',
        article: 'income',
        desc: 'Where did it come from?',
        hint: 'Salary, refund, gift…'
      }
    };

    function wording(forKind, isEditing) {
      var w = WORDS[forKind] || WORDS.expense;
      return {
        title: (isEditing ? 'Edit ' : 'Add ') + w.noun,
        mode: isEditing ? 'Editing ' + w.article + ' from' : 'Adding to',
        submit: isEditing ? 'Save changes' : 'Add ' + w.noun,
        desc: w.desc,
        hint: w.hint
      };
    }

    function applyWording() {
      var w = wording(kind, editing);
      if (title) title.textContent = w.title;
      if (modeLabel) modeLabel.textContent = w.mode;
      if (submitLabel) submitLabel.textContent = w.submit;
      if (descLabel) descLabel.textContent = w.desc;
      if (descInput) descInput.setAttribute('placeholder', w.hint);
    }

    /* Which way the money went is the one field with no visible control, so
       the buttons *are* the control: they set the hidden value the form
       submits. Anything unrecognised falls back to expense, matching what the
       server does with an unreadable kind. */
    function setKind(next) {
      kind = next === 'income' ? 'income' : 'expense';
      if (kindInput) kindInput.value = kind;
      kindButtons.forEach(function (b) {
        b.classList.toggle('is-active', b.getAttribute('data-kind') === kind);
      });
      applyWording();
    }

    /* "2026-08-14" -> "August 2026". Parsed as local parts, not through
       new Date(string), which reads a bare date as UTC midnight and can name
       the wrong month west of Greenwich. */
    function monthNameOf(iso) {
      var parts = String(iso || '').split('-');
      if (parts.length < 3) return '';
      var d = new Date(Number(parts[0]), Number(parts[1]) - 1, Number(parts[2]));
      return d.toLocaleDateString(undefined, { month: 'long', year: 'numeric' });
    }

    function addOption(name) {
      for (var i = 0; i < select.options.length; i++) {
        if (select.options[i].value === name) return;
      }
      var opt = document.createElement('option');
      opt.value = name;
      opt.textContent = name;
      select.appendChild(opt);
    }

    /* Categories arrive asynchronously, so anything that needs the list has to
       wait for it. done() runs whether the fetch succeeded or failed: a failed
       load must not leave the modal unusable, it just means the select offers
       only the categories it already has. */
    function loadCategories(done) {
      if (loadedCategories) {
        if (done) done();
        return;
      }
      loadedCategories = true;

      select.setAttribute('aria-busy', 'true');

      fetch('/api/categories', { headers: { 'Accept': 'application/json' } })
        .then(function (res) {
          if (!res.ok) throw new Error('HTTP ' + res.status);
          return res.json();
        })
        .then(function (categories) {
          (categories || []).forEach(addOption);
        })
        .catch(function () {
          /* Not fatal: the expense can still be saved uncategorised, and the
             server accepts any category string. Let the user retry. */
          loadedCategories = false;
        })
        .finally(function () {
          select.removeAttribute('aria-busy');
          if (done) done();
        });
    }

    function enterAddMode() {
      /* Coming back from an edit, clear what the edit filled in — otherwise the
         next new expense silently inherits another row's values. Only on that
         transition: a half-typed expense survives closing and reopening. The
         hidden fields are deliberately left alone; they carry the month and the
         active search, which belong to the page, not to the entry. */
      if (editing) {
        descInput.value = '';
        amountInput.value = '';
        select.value = '';
        dateInput.value = todayLocal();
      }

      editing = false;
      form.setAttribute('action', '/add');
      if (origDate) origDate.value = '';
      /* Back to expense, not whatever the last edit was. The button that opens
         this is "Add expense"; resuming in income mode would make the same
         button mean different things on different days. */
      setKind('expense');
      if (monthLabel) {
        monthLabel.textContent = new Date().toLocaleDateString(undefined, {
          month: 'long', year: 'numeric'
        });
      }
    }

    /* Everything the row needs rides on the button's data-* attributes, so
       opening the editor is local — no round trip, and the row that was clicked
       is exactly the row that gets edited. */
    function enterEditMode(btn) {
      var category = btn.getAttribute('data-category') || '';
      var date = btn.getAttribute('data-date') || '';

      editing = true;
      form.setAttribute('action', '/edit/' + btn.getAttribute('data-id'));
      setKind(btn.getAttribute('data-kind') || 'expense');

      descInput.value = btn.getAttribute('data-description') || '';
      amountInput.value = btn.getAttribute('data-amount') || '';
      dateInput.value = date;
      /* The date field has no time, so the server is told which date the form
         was populated with. It rewrites created_at only when the two differ —
         which is what keeps an untouched date from being restamped with this
         afternoon's clock time. */
      if (origDate) origDate.value = date;

      if (monthLabel) monthLabel.textContent = monthNameOf(date);

      /* The row's category may have been removed from settings since it was
         recorded. Offering it back means editing an amount does not quietly
         strip the category as a side effect. */
      loadCategories(function () {
        if (category) addOption(category);
        select.value = category;
      });

      openModal(modal);
      descInput.focus();
      descInput.select();
    }

    kindButtons.forEach(function (b) {
      b.addEventListener('click', function () { setKind(b.getAttribute('data-kind')); });
    });

    fab.addEventListener('click', function () {
      enterAddMode();
      if (dateInput && !dateInput.value) dateInput.value = todayLocal();
      loadCategories();
      openModal(modal);
    });

    /* Delegated, because the expense list is replaced wholesale when the page
       refreshes its rows — a listener bound to the buttons themselves would go
       with them. */
    document.addEventListener('click', function (e) {
      var btn = e.target.closest && e.target.closest('[data-edit-expense]');
      if (btn) enterEditMode(btn);
    });

    /* Don't let an impatient double-submit create two expenses. The label is
       updated through its own span: the button also holds an icon, and setting
       textContent on the button would delete it. */
    form.addEventListener('submit', function () {
      var submit = form.querySelector('button[type="submit"]');
      if (!submit) return;
      submit.disabled = true;
      if (submitLabel) submitLabel.textContent = editing ? 'Saving…' : 'Adding…';
    });
  }

  /* -------------------------------------------------------- reset confirm --- */
  /* Shared by "reset this month" and "reset all settings": the button in the
     page submits straight through, and JavaScript upgrades that to a dialog.
     Closing the dialog is handled by wireModals() for both. */
  function wireConfirmForm(formId, modalId) {
    var form = document.getElementById(formId);
    var modal = document.getElementById(modalId);
    if (!form || !modal) return;

    form.addEventListener('submit', function (e) {
      if (form.dataset.confirmed === 'true') return;
      e.preventDefault();
      openModal(modal);
    });

    var yes = modal.querySelector('[data-confirm-yes]');
    if (yes) {
      yes.addEventListener('click', function () {
        /* Set the flag *before* submitting, or this submit would be
           intercepted and reopen the dialog. */
        form.dataset.confirmed = 'true';
        form.submit();
      });
    }
  }

  /* ------------------------------------------------------------- settings --- */
  function wireSettings() {
    var themeInput = document.getElementById('theme-input');
    if (!themeInput) return;

    /* ---- Appearance ---- */
    var buttons = document.querySelectorAll('.theme-btn');
    var prefersDark = window.matchMedia('(prefers-color-scheme: dark)');

    function applyTheme(theme) {
      var dark = theme === 'dark' || (theme === 'system' && prefersDark.matches);
      document.documentElement.classList.toggle('dark', dark);
    }

    function selectTheme(theme) {
      themeInput.value = theme;
      /* The same key head.html reads before first paint, so the choice
         survives the next navigation without a flash. */
      localStorage.setItem('bt-theme', theme);
      buttons.forEach(function (button) {
        button.setAttribute('aria-pressed', String(button.dataset.theme === theme));
      });
      applyTheme(theme);
    }

    buttons.forEach(function (button) {
      button.addEventListener('click', function () { selectTheme(button.dataset.theme); });
    });

    /* A device switching to dark should move the page with it — but only when
       the user is actually following the system. */
    prefersDark.addEventListener('change', function () {
      if (themeInput.value === 'system') applyTheme('system');
    });

    selectTheme(themeInput.value || 'system');

    /* ---- Categories ---- */
    var list = document.getElementById('category-list');
    var addBtn = document.getElementById('add-category-row');
    var rowTemplate = document.getElementById('category-row-template');
    if (!list || !addBtn || !rowTemplate) return;

    /* One delegated handler for every current and future row. */
    list.addEventListener('click', function (e) {
      var btn = e.target.closest && e.target.closest('[data-remove-category]');
      if (!btn) return;
      btn.closest('.category-item').remove();
    });

    addBtn.addEventListener('click', function () {
      var row = rowTemplate.content.cloneNode(true).firstElementChild;
      list.appendChild(row);
      row.querySelector('input').focus();
    });
  }

  /* ---------------------------------------------------------------- toast --- */
  function showToast(node, timeout) {
    var host = document.getElementById('toast-host');
    if (!host || !node) return;

    host.appendChild(node);

    if (timeout) {
      window.setTimeout(function () {
        node.style.transition = 'opacity .3s ease';
        node.style.opacity = '0';
        window.setTimeout(function () { node.remove(); }, 300);
      }, timeout);
    }
  }

  /* The server renders an <template> after a delete so the row can come back.
     Give the user a few seconds to change their mind. */
  function wireUndoToast() {
    var tpl = document.getElementById('undo-template');
    if (!tpl) return;
    showToast(tpl.content.cloneNode(true).firstElementChild, 7000);
  }

  /* ------------------------------------------------- loading / skeleton --- */
  /* Only shown if navigation is genuinely slow, so a fast load never flashes
     a skeleton at the user. */
  function wireSkeletons() {
    var container = document.getElementById('expenses-container');
    if (!container) return;

    document.addEventListener('click', function (e) {
      var link = e.target.closest && e.target.closest('a[href]');
      if (!link) return;
      if (link.target || link.hasAttribute('download')) return;
      /* A modifier-click or middle-click opens a new tab and leaves this page
         in place, so showing a skeleton here would blank the list with no
         navigation to replace it. Only the plain left-click navigates. */
      if (e.button !== 0 || e.metaKey || e.ctrlKey || e.shiftKey || e.altKey) return;

      var href = link.getAttribute('href') || '';
      if (!href.startsWith('/') || href.startsWith('/logout')) return;

      window.setTimeout(function () {
        container.innerHTML =
          '<div class="expense-list">' +
          '<div class="skeleton skeleton-row"></div>' +
          '<div class="skeleton skeleton-row"></div>' +
          '<div class="skeleton skeleton-row"></div>' +
          '</div>';
      }, 200);
    });
  }

  /* --------------------------------------------------------------- boot --- */
  function init() {
    markActiveNav();
    wireDateTriggers();
    wireArchiveSearch();
    wireModals();
    wireAddExpense();
    wireConfirmForm('clear-form', 'clear-modal');
    wireConfirmForm('reset-settings-form', 'reset-settings-modal');
    wireSettings();
    wireUndoToast();
    wireSkeletons();
  }

  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', init);
  } else {
    init();
  }
})();
