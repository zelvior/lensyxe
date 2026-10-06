/* Lensyxe site behaviour.

   Progressive enhancement only. Every interaction here has a working baseline
   in the HTML: the install command is selectable text, the navigation is a plain
   list of links, and nothing is hidden behind JavaScript that a reader needs.
   If this file fails to load, the page still says everything it needs to say.

   Two behaviours, both small enough to not need a framework.
*/

(function () {
  'use strict';

  /* ------------------------------------------------------- copy to clipboard

     Uses the async Clipboard API where available, because execCommand is
     deprecated and blocked in some contexts. The textarea fallback exists for
     a non-secure origin, which is the normal case when the site is previewed
     from a local file or over plain http.

     The button confirms rather than assuming: execCommand can return false
     without throwing, and silently reverting the label would tell the reader
     the command is on the clipboard when it is not.
  */
  function copyText(text) {
    if (navigator.clipboard && window.isSecureContext) {
      return navigator.clipboard.writeText(text);
    }
    return new Promise(function (resolve, reject) {
      var ta = document.createElement('textarea');
      ta.value = text;
      ta.setAttribute('readonly', '');
      // Kept in the layout but invisible, so the page does not jump and iOS
      // still allows the selection to be made.
      ta.style.position = 'fixed';
      ta.style.opacity = '0';
      document.body.appendChild(ta);
      ta.select();
      var ok = false;
      try {
        ok = document.execCommand('copy');
      } catch (err) {
        ok = false;
      }
      document.body.removeChild(ta);
      ok ? resolve() : reject(new Error('copy rejected'));
    });
  }

  function wireCopyButtons() {
    var buttons = document.querySelectorAll('[data-copy]');

    Array.prototype.forEach.call(buttons, function (btn) {
      var source = document.getElementById(btn.getAttribute('data-copy'));
      if (!source) return;

      var original = btn.textContent;
      var resetTimer = null;

      btn.addEventListener('click', function () {
        var text = (source.textContent || '').trim();
        if (!text) return;

        clearTimeout(resetTimer);

        copyText(text).then(
          function () {
            btn.textContent = 'Copied';
            btn.setAttribute('data-ok', 'true');
            resetTimer = setTimeout(function () {
              btn.textContent = original;
              btn.removeAttribute('data-ok');
            }, 1600);
          },
          function () {
            // Deliberately not a silent failure. If the clipboard is
            // unavailable, say so instead of pretending it worked.
            btn.textContent = 'Select manually';
            resetTimer = setTimeout(function () {
              btn.textContent = original;
            }, 2400);
          }
        );
      });
    });
  }

  /* ------------------------------------------------------------ mobile nav

     aria-expanded on the button is what a screen reader announces, so it is
     the source of truth and the panel's visibility follows from it rather than
     being tracked separately.
  */
  function wireNav() {
    var toggle = document.querySelector('.menu-btn');
    var menu = document.getElementById('menu');
    if (!toggle || !menu) return;

    function setOpen(open) {
      toggle.setAttribute('aria-expanded', open ? 'true' : 'false');
      menu.setAttribute('data-open', open ? 'true' : 'false');
    }

    toggle.addEventListener('click', function () {
      setOpen(toggle.getAttribute('aria-expanded') !== 'true');
    });

    // Following an in-page link should close the panel, or the reader is left
    // looking at an open menu over the section they just asked for.
    menu.addEventListener('click', function (ev) {
      if (ev.target && ev.target.closest('a')) setOpen(false);
    });

    document.addEventListener('keydown', function (ev) {
      if (ev.key === 'Escape') setOpen(false);
    });

    // Crossing the breakpoint turns the menu back into a horizontal bar; leaving
    // data-open set would hide it once the CSS stops applying that rule.
    // Must match the max-width: 50rem breakpoint in styles.css. If the two drift,
// the menu stays stuck open or fails to collapse.
    var wide = window.matchMedia('(min-width: 50.0625rem)');
    var onChange = function (e) {
      if (e.matches) setOpen(false);
    };
    if (wide.addEventListener) wide.addEventListener('change', onChange);
    else if (wide.addListener) wide.addListener(onChange);
  }

  function init() {
    wireCopyButtons();
    wireNav();
  }

  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', init);
  } else {
    init();
  }
})();