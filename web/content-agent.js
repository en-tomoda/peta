/*
 * Peta content agent
 *
 * /content/:id の配信時にだけ注入される。iframe は allow-same-origin なしの
 * sandbox なので親ページから中身を読めない。このスクリプトが中の座標だけを
 * 計算して postMessage で親に渡す。保存済みHTMLは書き換えない。
 *
 * 親 -> iframe : peta:mode / peta:anchors / peta:scroll-to
 * iframe -> 親 : peta:ready / peta:positions / peta:pick / peta:cancel
 */
(function () {
  'use strict';

  if (window.__petaAgent) return;
  window.__petaAgent = true;

  var parentWin = window.parent;
  if (!parentWin || parentWin === window) return;

  var anchors = [];
  var commentMode = false;
  var lastPayload = '';
  var rafId = 0;
  var styleEl = null;

  function post(message) {
    try {
      parentWin.postMessage(message, '*');
    } catch (e) {
      /* 親が閉じている場合は何もしない */
    }
  }

  /* ===== セレクタ ===== */

  function escapeIdent(value) {
    if (window.CSS && typeof window.CSS.escape === 'function') return window.CSS.escape(value);
    return value.replace(/([^\w-])/g, '\\$1');
  }

  function uniqueIDSelector(el) {
    if (!el.id) return '';
    var selector = '#' + escapeIdent(el.id);
    try {
      if (document.querySelectorAll(selector).length === 1) return selector;
    } catch (e) {
      /* 不正なidは使わない */
    }
    return '';
  }

  function buildSelector(el) {
    if (!el || el.nodeType !== 1) return '';
    if (el === document.documentElement) return 'html';

    var parts = [];
    var node = el;
    while (node && node.nodeType === 1 && node !== document.documentElement) {
      var byID = uniqueIDSelector(node);
      if (byID) {
        parts.unshift(byID);
        return parts.join('>');
      }
      var index = 1;
      var sibling = node;
      while ((sibling = sibling.previousElementSibling)) {
        if (sibling.tagName === node.tagName) index++;
      }
      parts.unshift(node.tagName.toLowerCase() + ':nth-of-type(' + index + ')');
      node = node.parentElement;
    }
    parts.unshift('html');
    return parts.join('>');
  }

  /* ===== 座標 ===== */

  function clamp01(value) {
    if (!isFinite(value)) return 0;
    return value < 0 ? 0 : value > 1 ? 1 : value;
  }

  function findElement(selector) {
    if (!selector) return null;
    try {
      return document.querySelector(selector);
    } catch (e) {
      return null;
    }
  }

  // ページ内タブの切り替えなどで隠れた要素は、座標を返さずピンごと隠す
  function isDisplayed(el) {
    if (!el.getClientRects().length) return false;
    var style = window.getComputedStyle(el);
    return style.visibility !== 'hidden' && style.display !== 'none';
  }

  function resolveAnchor(anchor) {
    var el = findElement(anchor.selector);
    if (el) {
      if (!isDisplayed(el)) return { x: 0, y: 0, resolved: true, hidden: true };

      var rect = el.getBoundingClientRect();
      return {
        x: rect.left + rect.width * clamp01(anchor.rx),
        y: rect.top + rect.height * clamp01(anchor.ry),
        resolved: true,
        hidden: false
      };
    }
    // アンカー要素そのものが無いときだけドキュメント絶対座標にフォールバック
    return {
      x: (anchor.px || 0) - window.pageXOffset,
      y: (anchor.py || 0) - window.pageYOffset,
      resolved: false,
      hidden: false
    };
  }

  function anchorFrom(el, clientX, clientY) {
    if (!el || el.nodeType !== 1) el = document.body || document.documentElement;
    var rect = el.getBoundingClientRect();
    return {
      selector: buildSelector(el),
      rx: rect.width ? clamp01((clientX - rect.left) / rect.width) : 0.5,
      ry: rect.height ? clamp01((clientY - rect.top) / rect.height) : 0.5,
      px: round(clientX + window.pageXOffset),
      py: round(clientY + window.pageYOffset)
    };
  }

  function round(value) {
    return Math.round(value * 10) / 10;
  }

  function emit() {
    rafId = 0;

    var items = [];
    for (var i = 0; i < anchors.length; i++) {
      var anchor = anchors[i];
      var point = resolveAnchor(anchor);
      items.push({
        id: anchor.id,
        x: round(point.x),
        y: round(point.y),
        resolved: point.resolved,
        hidden: !!point.hidden,
        visible: !point.hidden &&
          point.x >= 0 && point.y >= 0 &&
          point.x <= window.innerWidth && point.y <= window.innerHeight
      });
    }

    var payload = {
      type: 'peta:positions',
      items: items,
      viewport: {
        w: window.innerWidth,
        h: window.innerHeight,
        sx: round(window.pageXOffset),
        sy: round(window.pageYOffset)
      }
    };

    var serialized = JSON.stringify(payload);
    if (serialized === lastPayload) return;
    lastPayload = serialized;
    post(payload);
  }

  function schedule() {
    if (rafId) return;
    rafId = window.requestAnimationFrame ? window.requestAnimationFrame(emit) : setTimeout(emit, 16);
  }

  /* ===== コメントモード ===== */

  function ensureStyle() {
    if (styleEl) return;
    styleEl = document.createElement('style');
    styleEl.textContent =
      'html[data-peta-comment-mode], html[data-peta-comment-mode] * {' +
      ' cursor: crosshair !important; user-select: none !important;' +
      ' -webkit-user-select: none !important; }';
    (document.head || document.documentElement).appendChild(styleEl);
  }

  function setMode(on) {
    commentMode = !!on;
    if (commentMode) {
      ensureStyle();
      document.documentElement.setAttribute('data-peta-comment-mode', '');
    } else {
      document.documentElement.removeAttribute('data-peta-comment-mode');
    }
  }

  function swallow(event) {
    if (!commentMode) return;
    event.preventDefault();
    event.stopPropagation();
    if (event.stopImmediatePropagation) event.stopImmediatePropagation();
  }

  function onClick(event) {
    if (!commentMode) return;
    swallow(event);

    var el = document.elementFromPoint(event.clientX, event.clientY) || event.target;
    post({
      type: 'peta:pick',
      anchor: anchorFrom(el, event.clientX, event.clientY),
      point: { x: round(event.clientX), y: round(event.clientY) }
    });
  }

  /* ===== スクロール ===== */

  function scrollToAnchor(id) {
    for (var i = 0; i < anchors.length; i++) {
      if (anchors[i].id !== id) continue;

      var anchor = anchors[i];
      var el = findElement(anchor.selector);
      if (el && el.scrollIntoView) {
        el.scrollIntoView({ block: 'center', inline: 'center', behavior: 'smooth' });
      } else {
        window.scrollTo({
          top: Math.max(0, (anchor.py || 0) - window.innerHeight / 2),
          left: Math.max(0, (anchor.px || 0) - window.innerWidth / 2),
          behavior: 'smooth'
        });
      }
      schedule();
      return;
    }
  }

  /* ===== 監視 ===== */

  // ページ側より先に拾うため、すべて capture で受ける
  var swallowed = ['pointerdown', 'pointerup', 'mousedown', 'mouseup', 'dblclick', 'auxclick', 'submit'];
  for (var i = 0; i < swallowed.length; i++) {
    window.addEventListener(swallowed[i], swallow, true);
  }
  window.addEventListener('click', onClick, true);
  window.addEventListener('keydown', function (event) {
    if (event.key === 'Escape') post({ type: 'peta:cancel' });
  }, true);

  window.addEventListener('scroll', schedule, { capture: true, passive: true });
  window.addEventListener('resize', schedule);
  window.addEventListener('orientationchange', schedule);
  window.addEventListener('load', schedule);

  if (window.ResizeObserver) {
    var resizeObserver = new ResizeObserver(schedule);
    resizeObserver.observe(document.documentElement);
    if (document.body) resizeObserver.observe(document.body);
  }
  if (window.MutationObserver) {
    var mutationTimer = 0;
    new MutationObserver(function () {
      if (mutationTimer) return;
      mutationTimer = setTimeout(function () {
        mutationTimer = 0;
        schedule();
      }, 150);
    }).observe(document.documentElement, { childList: true, subtree: true, attributes: true });
  }
  // 画像の遅延読み込みなど、イベントが飛んでこない変化への保険
  setInterval(schedule, 500);

  window.addEventListener('message', function (event) {
    if (event.source !== parentWin) return;

    var data = event.data;
    if (!data || typeof data !== 'object') return;

    switch (data.type) {
      case 'peta:mode':
        setMode(data.on);
        break;
      case 'peta:anchors':
        anchors = Array.isArray(data.items) ? data.items : [];
        lastPayload = '';
        schedule();
        break;
      case 'peta:scroll-to':
        scrollToAnchor(data.id);
        break;
      case 'peta:resolve-point':
        post({
          type: 'peta:point-anchor',
          id: data.id,
          anchor: anchorFrom(document.elementFromPoint(data.x, data.y), data.x, data.y)
        });
        break;
    }
  });

  post({ type: 'peta:ready' });
})();
