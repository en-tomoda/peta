/* Peta share page — テーマ・ヘッダー表示とコメントレビュー */
(function () {
  'use strict';

  var REACTION_EMOJIS = ['👍', '✅', '👀', '🎉', '❓'];
  var AUTHOR_KEY = 'peta-comment-author';
  var HIDDEN_KEY = 'peta-header-hidden';
  var DRAFT_ID = '__draft__';

  var pageID = document.body.dataset.pageId;
  var frame = document.getElementById('previewFrame');
  var pinLayer = document.getElementById('pinLayer');
  var panel = document.getElementById('commentPanel');
  var panelBody = document.getElementById('panelBody');

  var state = {
    threads: [],
    positions: {},
    commentMode: false,
    draft: null,
    openPinID: null,
    hoverPinID: null,
    filter: 'all',
    editing: null,
    picker: null,
    expanded: {}
  };

  var pinNodes = {};
  var drag = null;
  var suppressClick = false;

  /* ===== テーマ / ヘッダー ===== */

  document.documentElement.dataset.theme = localStorage.getItem('peta-theme') || 'light';

  function setHeaderHidden(hide, save) {
    document.body.classList.toggle('header-hidden', hide);
    if (save) localStorage.setItem(HIDDEN_KEY, hide ? '1' : '0');
  }
  setHeaderHidden(localStorage.getItem(HIDDEN_KEY) === '1', false);
  document.getElementById('headerToggle').addEventListener('click', function () { setHeaderHidden(true, true); });
  document.getElementById('headerTab').addEventListener('click', function () { setHeaderHidden(false, true); });

  /* ===== 小物 ===== */

  function esc(value) {
    return String(value == null ? '' : value)
      .replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;')
      .replace(/"/g, '&quot;').replace(/'/g, '&#39;');
  }

  function formatTime(value) {
    var date = new Date(value);
    if (isNaN(date.getTime())) return value || '';
    var now = new Date();
    var hhmm = String(date.getHours()).padStart(2, '0') + ':' + String(date.getMinutes()).padStart(2, '0');
    if (date.toDateString() === now.toDateString()) return hhmm;
    return (date.getMonth() + 1) + '/' + date.getDate() + ' ' + hhmm;
  }

  var toastTimer = 0;
  function toast(message) {
    var el = document.getElementById('toast');
    el.textContent = message;
    el.hidden = false;
    clearTimeout(toastTimer);
    toastTimer = setTimeout(function () { el.hidden = true; }, 2600);
  }

  function author() {
    return (localStorage.getItem(AUTHOR_KEY) || '').trim();
  }
  function setAuthor(name) {
    localStorage.setItem(AUTHOR_KEY, (name || '').trim().slice(0, 40));
    renderPanelMe();
  }
  function isMine(name) {
    var me = author();
    return !!me && name === me;
  }

  /* ===== API ===== */

  function api(method, path, body) {
    return fetch(path, {
      method: method,
      headers: body ? { 'Content-Type': 'application/json' } : undefined,
      body: body ? JSON.stringify(body) : undefined
    }).then(function (res) {
      if (!res.ok) return res.text().then(function (text) { throw new Error(text || res.status); });
      return res.status === 204 ? null : res.json();
    });
  }

  function applyThreads(list) {
    state.threads = Array.isArray(list) ? list : [];
    render();
  }

  function loadThreads() {
    return api('GET', '/page/' + pageID + '/comments')
      .then(applyThreads)
      .catch(function () { toast('コメントを読み込めませんでした'); });
  }

  function mutate(promise) {
    return promise.then(applyThreads).catch(function (err) {
      toast(String(err.message || err).trim() || '操作に失敗しました');
    });
  }

  /* ===== iframe との通信 ===== */

  function toFrame(message) {
    if (frame.contentWindow) frame.contentWindow.postMessage(message, '*');
  }

  function syncAnchors() {
    var items = [];
    state.threads.forEach(function (thread) {
      if (thread.anchor) items.push(anchorOf(thread.id, thread.anchor));
    });
    if (state.draft) items.push(anchorOf(DRAFT_ID, state.draft.anchor));
    toFrame({ type: 'peta:anchors', items: items });
  }

  function anchorOf(id, anchor) {
    return {
      id: id,
      selector: anchor.selector,
      rx: anchor.rx, ry: anchor.ry,
      px: anchor.px, py: anchor.py
    };
  }

  function setCommentMode(on) {
    state.commentMode = !!on;
    document.body.classList.toggle('comment-mode', state.commentMode);
    document.getElementById('modeToggle').classList.toggle('is-active', state.commentMode);
    toFrame({ type: 'peta:mode', on: state.commentMode });
  }

  window.addEventListener('message', function (event) {
    if (event.source !== frame.contentWindow) return;

    var data = event.data;
    if (!data || typeof data !== 'object') return;

    switch (data.type) {
      case 'peta:ready':
        syncAnchors();
        toFrame({ type: 'peta:mode', on: state.commentMode });
        break;
      case 'peta:positions':
        data.items.forEach(function (item) { state.positions[item.id] = item; });
        applyPositions();
        break;
      case 'peta:pick':
        openDraft(data.anchor);
        break;
      case 'peta:point-anchor':
        movePin(data.id, data.anchor);
        break;
      case 'peta:cancel':
        dismiss();
        break;
    }
  });

  /* ===== 下書き ===== */

  function openDraft(anchor) {
    state.draft = { anchor: anchor };
    state.openPinID = DRAFT_ID;
    // 位置が決まった時点でモードを抜ける。置き直したいときは再度コメントモードへ
    setCommentMode(false);
    render();
    var textarea = pinLayer.querySelector('.composer textarea');
    if (textarea) textarea.focus();
  }

  function dismiss() {
    if (!state.draft && !state.openPinID && !state.editing && !state.picker) return;
    state.draft = null;
    state.openPinID = null;
    state.editing = null;
    state.picker = null;
    render();
  }

  /* ===== 描画 ===== */

  function reactionsHTML(comment) {
    var buttons = (comment.reactions || []).map(function (reaction) {
      var mine = reaction.authors.indexOf(author()) >= 0;
      return '<button type="button" class="reaction' + (mine ? ' is-mine' : '') + '"' +
        ' data-act="react" data-id="' + comment.id + '" data-emoji="' + esc(reaction.emoji) + '"' +
        ' title="' + esc(reaction.authors.join(', ')) + '">' +
        esc(reaction.emoji) + '<span>' + reaction.count + '</span></button>';
    }).join('');

    var picker = state.picker === comment.id
      ? '<span class="reaction-picker">' + REACTION_EMOJIS.map(function (emoji) {
          return '<button type="button" data-act="react" data-id="' + comment.id +
            '" data-emoji="' + esc(emoji) + '">' + esc(emoji) + '</button>';
        }).join('') + '</span>'
      : '';

    return '<div class="reactions">' + buttons +
      '<button type="button" class="reaction-add" data-act="picker" data-id="' + comment.id + '">+</button>' +
      picker + '</div>';
  }

  function metaHTML(comment) {
    return '<span class="c-author">' + esc(comment.author || '匿名') + '</span>' +
      '<time>' + esc(formatTime(comment.created_at)) + '</time>' +
      (comment.updated_at ? '<span class="c-edited">編集済み</span>' : '');
  }

  function bodyHTML(comment) {
    if (state.editing === comment.id) {
      return '<div class="editor">' +
        '<textarea maxlength="2000">' + esc(comment.body) + '</textarea>' +
        '<div class="editor-actions">' +
        '<button type="button" class="btn-primary" data-act="edit-save" data-id="' + comment.id + '">保存</button>' +
        '<button type="button" class="btn-plain" data-act="edit-cancel">キャンセル</button>' +
        '</div></div>';
    }
    return '<p class="c-body">' + esc(comment.body) + '</p>';
  }

  function ownerActionsHTML(comment) {
    if (!isMine(comment.author) || state.editing === comment.id) return '';
    return '<span class="c-actions">' +
      '<button type="button" data-act="edit" data-id="' + comment.id + '">編集</button>' +
      '<button type="button" data-act="delete" data-id="' + comment.id + '">削除</button>' +
      '</span>';
  }

  function replyHTML(reply) {
    return '<li class="reply" data-id="' + reply.id + '">' +
      '<div class="c-meta">' + metaHTML(reply) + ownerActionsHTML(reply) + '</div>' +
      bodyHTML(reply) + reactionsHTML(reply) + '</li>';
  }

  function threadHTML(thread, context) {
    var resolved = !!thread.resolved_at;
    var collapsed = context === 'panel' && resolved && !state.expanded[thread.id];

    var head = '<div class="thread-head" data-act="toggle" data-id="' + thread.id + '">' +
      '<span class="thread-seq">' + thread.seq + '</span>' +
      '<div class="c-meta">' + metaHTML(thread) + '</div>' +
      '<button type="button" class="thread-resolve" data-act="resolve" data-id="' + thread.id + '"' +
      ' title="' + (resolved ? '未解決に戻す' : '解決済みにする') + '">' +
      '<svg width="11" height="11" viewBox="0 0 12 12" fill="none"><path d="M2 6.3l2.6 2.6L10 3.4"' +
      ' stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round"/></svg>' +
      '</button></div>';

    if (collapsed) {
      return '<article class="thread is-resolved is-collapsed" data-id="' + thread.id + '">' +
        head + '<p class="c-body c-body-peek">' + esc(thread.body) + '</p></article>';
    }

    var replies = (thread.replies || []).length
      ? '<ul class="replies">' + thread.replies.map(replyHTML).join('') + '</ul>'
      : '';

    var replyForm = '<form class="reply-form" data-act="reply" data-id="' + thread.id + '">' +
      '<input type="text" placeholder="返信を書く" maxlength="2000" />' +
      '<button type="submit">送信</button></form>';

    return '<article class="thread' + (resolved ? ' is-resolved' : '') + '" data-id="' + thread.id + '">' +
      head +
      '<div class="c-meta c-meta-actions">' + ownerActionsHTML(thread) + '</div>' +
      bodyHTML(thread) + reactionsHTML(thread) + replies + replyForm +
      '</article>';
  }

  function composerHTML() {
    var hasName = !!author();
    return '<div class="composer">' +
      (hasName ? '' : '<input type="text" class="composer-name" placeholder="お名前" maxlength="40" />') +
      '<textarea placeholder="コメントを書く" maxlength="2000"></textarea>' +
      '<div class="composer-actions">' +
      '<button type="button" class="btn-primary" data-act="post">投稿</button>' +
      '<button type="button" class="btn-plain" data-act="cancel">キャンセル</button>' +
      '</div></div>';
  }

  function startRename() {
    var button = document.getElementById('panelMe');
    if (!button) return;

    var input = document.createElement('input');
    input.type = 'text';
    input.className = 'panel-me panel-me-input';
    input.maxLength = 40;
    input.value = author();
    input.placeholder = 'お名前';

    function commit() {
      if (!input.parentNode) return;
      var value = input.value.trim();
      input.replaceWith(button);
      if (value !== author()) {
        setAuthor(value);
        render();
      }
      renderPanelMe();
    }

    input.addEventListener('keydown', function (event) {
      if (event.key === 'Enter') { event.preventDefault(); input.blur(); }
      if (event.key === 'Escape') { event.stopPropagation(); input.value = author(); input.blur(); }
    });
    input.addEventListener('blur', commit);

    button.replaceWith(input);
    input.focus();
    input.select();
  }

  function renderPanelMe() {
    var me = author();
    var button = document.getElementById('panelMe');
    button.textContent = me || '名前を設定';
    button.classList.toggle('is-unset', !me);
    button.dataset.act = 'rename';
  }

  function renderPanel() {
    var threads = state.threads.filter(function (thread) {
      return state.filter === 'all' || !thread.resolved_at;
    });

    document.getElementById('panelCount').textContent = state.threads.length;

    panelBody.innerHTML = threads.length
      ? threads.map(function (thread) { return threadHTML(thread, 'panel'); }).join('')
      : '<p class="panel-empty">' +
        (state.threads.length ? '未解決のコメントはありません' : 'コメントはまだありません') + '</p>';

    Array.prototype.forEach.call(panelBody.querySelectorAll('.thread'), function (node) {
      node.classList.toggle('is-active', node.dataset.id === state.openPinID);
    });
  }

  function renderPins() {
    pinNodes = {};
    pinLayer.innerHTML = '';

    state.threads.forEach(function (thread) {
      if (!thread.anchor) return;
      pinLayer.appendChild(pinNode(thread));
    });

    if (state.draft) pinLayer.appendChild(draftNode());
    applyPositions();
  }

  function pinNode(thread) {
    var open = state.openPinID === thread.id;
    var preview = !open && state.hoverPinID === thread.id;

    var node = document.createElement('div');
    node.className = 'pin' +
      (thread.resolved_at ? ' is-resolved' : '') +
      (open ? ' is-open' : '') +
      (preview ? ' is-preview' : '');
    node.dataset.id = thread.id;
    node.innerHTML =
      '<button type="button" class="pin-dot" data-act="pin" data-id="' + thread.id + '">' + thread.seq + '</button>' +
      (open || preview
        ? '<div class="pin-pop">' + (open ? threadHTML(thread, 'pin') : peekHTML(thread)) + '</div>'
        : '');

    pinNodes[thread.id] = node;
    return node;
  }

  function peekHTML(thread) {
    return '<article class="thread is-peek">' +
      '<div class="c-meta">' + metaHTML(thread) + '</div>' +
      '<p class="c-body">' + esc(thread.body) + '</p>' +
      ((thread.replies || []).length ? '<p class="peek-more">返信 ' + thread.replies.length + '件</p>' : '') +
      '</article>';
  }

  function draftNode() {
    var node = document.createElement('div');
    node.className = 'pin is-open is-draft';
    node.dataset.id = DRAFT_ID;
    node.innerHTML = '<span class="pin-dot">+</span><div class="pin-pop">' + composerHTML() + '</div>';
    pinNodes[DRAFT_ID] = node;
    return node;
  }

  function applyPositions() {
    var width = frame.clientWidth;
    var height = frame.clientHeight;

    Object.keys(pinNodes).forEach(function (id) {
      if (drag && drag.moved && drag.id === id) return;

      var node = pinNodes[id];
      var position = state.positions[id];
      if (!position) {
        node.style.visibility = 'hidden';
        return;
      }
      node.style.visibility = position.visible ? 'visible' : 'hidden';
      node.style.transform = 'translate(' + position.x + 'px,' + position.y + 'px)';
      node.classList.toggle('flip-x', position.x > width - 300);
      node.classList.toggle('flip-y', position.y > height - 220);
    });
  }

  function updateBadge() {
    var open = state.threads.filter(function (thread) { return !thread.resolved_at; }).length;
    var badge = document.getElementById('widgetBadge');
    badge.textContent = open;
    badge.hidden = open === 0;
  }

  function render() {
    renderPanel();
    renderPins();
    updateBadge();
    syncAnchors();
  }

  /* ===== 操作 ===== */

  function requireAuthor(scope) {
    var name = author();
    if (name) return name;

    var input = scope && scope.querySelector('.composer-name');
    var typed = input ? input.value.trim() : '';
    if (!typed) {
      toast('お名前を入力してください');
      if (input) input.focus();
      return '';
    }
    setAuthor(typed);
    return author();
  }

  function postDraft(scope) {
    var name = requireAuthor(scope);
    if (!name) return;

    var textarea = scope.querySelector('textarea');
    var body = textarea.value.trim();
    if (!body) {
      toast('コメントを入力してください');
      textarea.focus();
      return;
    }

    var anchor = state.draft.anchor;
    state.draft = null;
    state.openPinID = null;
    mutate(api('POST', '/page/' + pageID + '/comments', { author: name, body: body, anchor: anchor }));
  }

  var ACTIONS = {
    pin: function (id) {
      state.openPinID = state.openPinID === id ? null : id;
      state.picker = null;
      render();
      toFrame({ type: 'peta:scroll-to', id: id });
    },
    toggle: function (id, target) {
      if (target.closest('.thread-resolve')) return;
      var inPanel = !!target.closest('.comment-panel');
      if (inPanel) {
        if (state.expanded[id]) delete state.expanded[id];
        else state.expanded[id] = true;
        state.openPinID = id;
        toFrame({ type: 'peta:scroll-to', id: id });
      }
      render();
    },
    picker: function (id) {
      state.picker = state.picker === id ? null : id;
      render();
    },
    react: function (id, target) {
      var name = author();
      if (!name) { toast('先に名前を設定してください'); return; }
      state.picker = null;
      mutate(api('POST', '/comment/' + id + '/reactions', { emoji: target.dataset.emoji, author: name }));
    },
    resolve: function (id) {
      var thread = state.threads.filter(function (t) { return t.id === id; })[0];
      if (!thread) return;
      mutate(api('POST', '/comment/' + id + '/resolve', { resolved: !thread.resolved_at, author: author() }));
    },
    edit: function (id) {
      state.editing = id;
      render();
    },
    'edit-cancel': function () {
      state.editing = null;
      render();
    },
    'edit-save': function (id, target) {
      var textarea = target.closest('.editor').querySelector('textarea');
      var body = textarea.value.trim();
      if (!body) { toast('本文が空です'); return; }
      state.editing = null;
      mutate(api('PATCH', '/comment/' + id, { body: body }));
    },
    'delete': function (id) {
      if (!window.confirm('このコメントを削除しますか？')) return;
      if (state.openPinID === id) state.openPinID = null;
      mutate(api('DELETE', '/comment/' + id));
    },
    post: function (id, target) {
      postDraft(target.closest('.composer'));
    },
    cancel: function () {
      dismiss();
    },
    rename: function () {
      startRename();
    }
  };

  document.addEventListener('click', function (event) {
    if (suppressClick) {
      suppressClick = false;
      event.preventDefault();
      return;
    }

    var trigger = event.target.closest('[data-act]');
    if (trigger && ACTIONS[trigger.dataset.act]) {
      event.preventDefault();
      ACTIONS[trigger.dataset.act](trigger.dataset.id, event.target);
      return;
    }
    // ピンの外側をクリックしたら閉じる
    if (!event.target.closest('.pin') && !event.target.closest('.comment-panel') &&
        !event.target.closest('.comment-widget')) {
      dismiss();
    }
  });

  document.addEventListener('submit', function (event) {
    var form = event.target.closest('.reply-form');
    if (!form) return;
    event.preventDefault();

    var name = author();
    if (!name) { toast('先に名前を設定してください'); return; }

    var input = form.querySelector('input');
    var body = input.value.trim();
    if (!body) return;

    input.value = '';
    mutate(api('POST', '/page/' + pageID + '/comments',
      { parent_id: form.dataset.id, author: name, body: body }));
  });

  document.addEventListener('keydown', function (event) {
    if (event.key === 'Escape') {
      if (state.commentMode) setCommentMode(false);
      dismiss();
    }
  });

  document.getElementById('modeToggle').addEventListener('click', function () {
    setCommentMode(!state.commentMode);
    if (state.commentMode) dismiss();
  });

  document.getElementById('panelToggle').addEventListener('click', function () { setPanel(panel.hidden); });
  document.getElementById('panelClose').addEventListener('click', function () { setPanel(false); });

  function setPanel(open) {
    panel.hidden = !open;
    document.getElementById('panelToggle').classList.toggle('is-active', open);
    localStorage.setItem('peta-panel-open', open ? '1' : '0');
  }

  Array.prototype.forEach.call(document.querySelectorAll('.filter-btn'), function (button) {
    button.addEventListener('click', function () {
      state.filter = button.dataset.filter;
      Array.prototype.forEach.call(document.querySelectorAll('.filter-btn'), function (other) {
        other.classList.toggle('is-active', other === button);
      });
      renderPanel();
    });
  });

  pinLayer.addEventListener('mouseover', function (event) {
    var pin = event.target.closest('.pin');
    var id = pin && pin.dataset.id !== DRAFT_ID ? pin.dataset.id : null;
    if (id === state.hoverPinID) return;
    state.hoverPinID = id;
    if (state.openPinID !== id) renderPins();
  });

  pinLayer.addEventListener('mouseleave', function () {
    if (!state.hoverPinID) return;
    state.hoverPinID = null;
    renderPins();
  });

  /* ===== ピンのドラッグ移動 ===== */

  function movePin(id, anchor) {
    if (id === DRAFT_ID) {
      if (!state.draft) return;
      state.draft.anchor = anchor;
      syncAnchors();
      return;
    }
    mutate(api('PATCH', '/comment/' + id + '/anchor', { anchor: anchor }));
  }

  pinLayer.addEventListener('pointerdown', function (event) {
    if (event.button !== 0) return;

    var dot = event.target.closest('.pin-dot');
    if (!dot) return;

    var pin = dot.closest('.pin');
    var position = state.positions[pin.dataset.id];
    if (!position || !position.visible) return;

    event.preventDefault();
    drag = {
      id: pin.dataset.id, node: pin, dot: dot, pointerId: event.pointerId, moved: false,
      startX: event.clientX, startY: event.clientY,
      baseX: position.x, baseY: position.y
    };

    // カーソルがバッジを外れると iframe にマウスを奪われるので、押した時点で捕まえておく
    pinLayer.classList.add('is-capturing');
    if (dot.setPointerCapture) {
      try { dot.setPointerCapture(event.pointerId); } catch (e) { /* 未対応環境は層で受ける */ }
    }
  });

  document.addEventListener('pointermove', function (event) {
    if (!drag || event.pointerId !== drag.pointerId) return;

    var dx = event.clientX - drag.startX;
    var dy = event.clientY - drag.startY;
    if (!drag.moved) {
      if (Math.abs(dx) + Math.abs(dy) < 4) return;
      drag.moved = true;
      drag.node.classList.add('is-dragging');
    }
    drag.node.style.transform = 'translate(' + (drag.baseX + dx) + 'px,' + (drag.baseY + dy) + 'px)';
  });

  function endDrag(event) {
    if (!drag || event.pointerId !== drag.pointerId) return;

    var dropped = drag;
    drag = null;
    pinLayer.classList.remove('is-capturing');
    dropped.node.classList.remove('is-dragging');
    if (dropped.dot.releasePointerCapture) {
      try { dropped.dot.releasePointerCapture(dropped.pointerId); } catch (e) { /* 解放済み */ }
    }
    if (!dropped.moved) return;

    // ドラッグ直後のクリックでふきだしが開かないようにする
    suppressClick = true;

    var rect = frame.getBoundingClientRect();
    var x = event.clientX - rect.left;
    var y = event.clientY - rect.top;
    if (x < 0 || y < 0 || x > rect.width || y > rect.height) {
      applyPositions();
      toast('プレビューの外には置けません');
      return;
    }
    toFrame({ type: 'peta:resolve-point', id: dropped.id, x: x, y: y });
  }

  document.addEventListener('pointerup', endDrag);
  document.addEventListener('pointercancel', endDrag);

  window.addEventListener('resize', applyPositions);

  // 戻る/進むでページが復帰したときは、表示中のコメントを取り直す
  window.addEventListener('pageshow', function (event) {
    if (event.persisted) loadThreads();
  });

  /* ===== 初期化 ===== */

  renderPanelMe();
  setPanel(localStorage.getItem('peta-panel-open') === '1');
  loadThreads();
})();
