/* LocalDrop · app.js — 电脑端管理页逻辑（index.html）。
 * 视图：加载骨架 / 设置 / 分享 / 手机配对 / 无权限，另含浏览文件弹窗与确认弹层。 */
(function () {
  'use strict';

  var LD = window.LD;
  function $(id) { return document.getElementById(id); }

  LD.captureToken();
  var platform = LD.platform();

  var state = {
    session: null,        // 当前投送会话（管理员版 JSON）
    paths: [],            // 设置视图已选路径 [{path,name,dir}]
    warnings: [],         // 设置视图本地警告
    rows: new Map(),      // 文件 id -> {row,bar,pct,speedEl,stateEl}
    qrCode: null,         // 已渲染二维码对应的投送码
    browse: null,         // 浏览弹窗状态
    modalOpen: null,      // 当前打开的弹层元素
    confirmCb: null,
    replacing: false,     // 正在替换旧投送，抑制“已停止”切换
    es: null,
    browseSeq: 0,         // 浏览请求序号，丢弃过期响应
    picking: false,       // 系统选择窗口打开中
    dropDepth: 0,         // dragenter/dragleave 计数
    dropping: false       // 拖入批次处理中
  };

  /* ------------------------------------------------------------ 视图切换 */

  var VIEWS = ['view-loading', 'view-setup', 'view-share', 'view-pair', 'view-denied'];
  function showView(id) {
    VIEWS.forEach(function (v) { $(v).hidden = v !== id; });
    if (id !== 'view-loading') $('view-loading').setAttribute('aria-busy', 'false');
  }

  function setSvcStatus(ok) {
    $('svc-dot').className = 'dot ' + (ok ? 'dot-green' : 'dot-amber');
    $('svc-text').textContent = ok ? '服务运行中' : '正在重连…';
  }

  /* ---------------------------------------------------------------- 启动 */

  function boot() {
    LD.api('/api/session').then(function (s) {
      if (s && s.active) {
        state.session = s;
        renderShare(true);
        showView('view-share');
      } else {
        showView('view-setup');
        renderSelection();
      }
      openAdminEvents();
    }).catch(function (err) {
      if (err.status === 401) {
        if (platform.mobile) {
          showView('view-pair');
          setTimeout(function () { $('pair-input').focus(); }, 60);
        } else {
          showView('view-denied');
        }
      } else {
        showView('view-setup');
        renderSelection();
        LD.toast(err.message);
      }
    });
  }

  /* -------------------------------------------------------------- 事件流 */

  function openAdminEvents() {
    if (state.es) return;
    var t = LD.getToken();
    var es = new EventSource('/api/events' + (t ? ('?t=' + encodeURIComponent(t)) : ''));
    state.es = es;
    es.addEventListener('open', function () { setSvcStatus(true); });
    es.addEventListener('error', function () { setSvcStatus(false); });
    es.onmessage = function (ev) {
      var data;
      try { data = JSON.parse(ev.data); } catch (e) { return; }
      if (data.type === 'state') onState(data.session);
      else if (data.type === 'progress') onProgress(data);
      else if (data.type === 'receivers') onReceivers(data.receivers || []);
    };
  }

  function onState(s) {
    if (!s) return;
    if (s.active) {
      state.replacing = false;
      state.session = s;
      renderShare(false);
      if ($('view-share').hidden) showView('view-share');
      return;
    }
    if (state.session && state.session.active && !state.replacing) {
      state.session = null;
      resetShare();
      showView('view-setup');
      renderSelection();
      LD.toast('投送已停止');
    }
  }

  function onReceivers(receivers) {
    if (!state.session) return;
    state.session.receivers = receivers;
    updateConn(receivers);
  }

  /* 进度事件是增量：只包含有变化的文件，总量用事件里的汇总字段 */
  function onProgress(d) {
    var s = state.session;
    if (!s) return;
    var files = d.files || [];
    files.forEach(function (item) {
      var ref = state.rows.get(item.id);
      if (ref) updateRow(ref, item);
    });
    var sent = 0, speed = 0;
    if (typeof d.sent === 'number') {
      sent = d.sent;
      speed = d.speed || 0;
    } else {
      files.forEach(function (item) {
        sent += item.sent || 0;
        speed += item.speed || 0;
      });
    }
    var total = (typeof d.total === 'number') ? d.total : s.totalSize;
    updateOverall(sent, total, speed);
  }

  /* ---------------------------------------------------------- 设置视图 */

  function renderSelection() {
    var list = $('sel-list');
    list.textContent = '';
    $('sel-empty').hidden = state.paths.length > 0;
    $('sel-clear').hidden = state.paths.length === 0;

    state.paths.forEach(function (p) {
      var kind = p.dir ? LD.KINDS.folder : LD.kindOf(p.name, false);
      list.appendChild(LD.el('div', { cls: 'sel-row' },
        LD.tintSquare(kind),
        LD.el('div', { cls: 'sel-body' },
          LD.el('div', { cls: 'sel-name', text: p.name }),
          LD.el('div', { cls: 'sel-path', text: p.label || p.path })
        ),
        LD.el('button', {
          cls: 'icon-btn danger', type: 'button', 'aria-label': '移除 ' + p.name,
          on: {
            click: function () {
              var prev = state.paths;
              state.paths = state.paths.filter(function (x) { return x.path !== p.path; });
              discardOrphanBatches(prev);
              renderSelection();
            }
          }
        }, LD.svg('x', 16))
      ));
    });

    $('sel-count').textContent = state.paths.length
      ? ('共 ' + state.paths.length + ' 项 · 文件夹会在开始时自动展开')
      : '还没有选择文件';
    $('start-summary').textContent = state.paths.length
      ? ('将发送 ' + state.paths.length + ' 项')
      : '选择文件后即可开始';
    $('start-btn').disabled = state.paths.length === 0;
    renderWarnStrip($('warn-strip'), state.warnings);
  }

  function renderWarnStrip(box, warnings) {
    box.textContent = '';
    if (!warnings || !warnings.length) { box.hidden = true; return; }
    box.hidden = false;
    warnings.forEach(function (w) {
      box.appendChild(LD.el('div', { cls: 'warn-item' }, LD.svg('alert', 14), LD.el('span', { text: w })));
    });
  }

  function startShare(replace) {
    var btn = $('start-btn');
    btn.disabled = true;
    var label = btn.textContent;
    btn.textContent = '正在准备…';
    if (replace) { state.replacing = true; setTimeout(function () { state.replacing = false; }, 4000); }

    LD.api('/api/session', {
      body: { paths: state.paths.map(function (p) { return p.path; }), replace: !!replace }
    }).then(function (s) {
      state.session = s;
      renderShare(true);
      showView('view-share');
      if (!replace) { state.paths = []; state.warnings = []; }
    }).catch(function (err) {
      if (err.status === 409) {
        openConfirm({
          glyph: 'warn',
          title: '已有进行中的投送',
          text: '开始新的投送会结束当前投送，已经连接的手机将无法继续下载。是否替换？',
          okText: '替换并开始',
          danger: true,
          onOk: function () { startShare(true); }
        });
      } else {
        state.replacing = false;
        LD.toast(err.message);
        resync();
      }
    }).then(function () {
      btn.textContent = label;
      btn.disabled = state.paths.length === 0;
    });
  }

  function resync() {
    LD.api('/api/session').then(function (s) {
      if (s && s.active) {
        state.session = s;
        renderShare(false);
        showView('view-share');
      } else if (state.session) {
        state.session = null;
        resetShare();
        showView('view-setup');
        renderSelection();
      }
    }).catch(function () { /* 忽略 */ });
  }

  function stopShare() {
    openConfirm({
      glyph: 'danger',
      title: '停止共享？',
      text: '手机会立即无法继续下载，已经下载的部分不受影响。',
      okText: '停止共享',
      danger: true,
      onOk: function () {
        LD.api('/api/session/stop', { body: {} }).then(function () {
          state.session = null;
          resetShare();
          showView('view-setup');
          renderSelection();
          LD.toast('投送已停止');
        }).catch(function (err) { LD.toast(err.message); });
      }
    });
  }

  function addFiles(paths) {
    LD.api('/api/session/add', { body: { paths: paths } }).then(function (s) {
      state.session = s;
      renderShare(false);
      LD.toast('已添加 ' + paths.length + ' 项');
    }).catch(function (err) { LD.toast(err.message); });
  }

  /* ---------------------------------------------------------- 分享视图 */

  function tokenQS() {
    var t = LD.getToken();
    return t ? ('&t=' + encodeURIComponent(t)) : '';
  }

  function renderShare(full) {
    var s = state.session;
    if (!s) return;

    var isNew = full || state.qrCode !== s.code;
    if (isNew) {
      state.rows = new Map();
      state.qrCode = s.code;
      $('share-list').textContent = '';
      $('qr-img').src = '/api/qr.svg?text=' + encodeURIComponent(s.url) + tokenQS();
      $('qr-dl').href = '/api/qr.png?text=' + encodeURIComponent(s.url) + tokenQS();
      $('share-url').textContent = s.url;
      $('code-text').textContent = LD.fmtCode(s.code);
      $('addr-chip').textContent = s.url.replace(/^https?:\/\//, '');
      $('addr-chip').hidden = false;
      $('dev-chips').textContent = '';
    }

    var files = s.files || [];
    var present = {};
    files.forEach(function (f) {
      present[f.id] = true;
      if (!state.rows.has(f.id)) addRow(f);
    });
    // 移除已不在会话中的文件行
    state.rows.forEach(function (ref, id) {
      if (!present[id]) {
        if (ref.row.parentNode) ref.row.parentNode.removeChild(ref.row);
        state.rows.delete(id);
      }
    });

    $('files-title').textContent = '文件 · ' + files.length + ' 个';
    $('files-sub').textContent = '总计 ' + LD.fmtSize(s.totalSize);
    renderWarnStrip($('warn-strip-share'), s.warnings);
    updateConn(s.receivers || []);

    // 用会话快照初始化进度（刷新页面后立即显示正确进度）
    var sent = 0;
    files.forEach(function (f) {
      sent += f.sent || 0;
      var ref = state.rows.get(f.id);
      if (ref) updateRow(ref, { sent: f.sent, size: f.size, done: f.done });
    });
    updateOverall(sent, s.totalSize, null, null);
  }

  function addRow(f) {
    var kind = LD.kindOf(f.name, false);
    var sub = f.relPath && f.relPath !== f.name ? f.relPath : LD.fmtSize(f.size);

    var bar = LD.el('i');
    var pct = LD.el('span', { cls: 'frow-pct', text: '等待中' });
    var speedEl = LD.el('span', { cls: 'frow-meta' });
    var stateEl = LD.el('span', { cls: 'frow-state' }, LD.svg('clock', 16));

    var row = LD.el('div', { cls: 'frow' },
      LD.el('div', { cls: 'frow-main' },
        LD.tintSquare(kind),
        LD.el('div', { cls: 'frow-body' },
          LD.el('div', { cls: 'frow-name', text: f.name }),
          LD.el('div', { cls: 'frow-sub', text: sub })
        ),
        LD.el('div', { cls: 'frow-side' }, speedEl, stateEl)
      ),
      LD.el('div', { cls: 'frow-bar' },
        LD.el('div', { cls: 'progress' }, bar),
        pct
      )
    );

    state.rows.set(f.id, { row: row, bar: bar, pct: pct, speedEl: speedEl, stateEl: stateEl, doneMark: false });
    $('share-list').appendChild(row);
  }

  function updateRow(ref, item) {
    var pct = LD.pct(item.sent, item.size);
    ref.bar.style.width = pct + '%';
    if (item.done) {
      ref.pct.textContent = '已发送 100%';
      ref.row.classList.add('done');
      if (!ref.doneMark) {
        ref.stateEl.textContent = '';
        ref.stateEl.appendChild(LD.svg('check', 16));
        ref.doneMark = true;
      }
      ref.speedEl.textContent = '';
    } else {
      ref.pct.textContent = item.sent > 0 ? ('已发送 ' + pct + '%') : '等待中';
      if (item.speed != null) ref.speedEl.textContent = LD.fmtSpeed(item.speed);
    }
  }

  function updateOverall(sent, total, speed) {
    var pct = LD.pct(sent, total);
    $('overall-bar').style.width = pct + '%';
    $('overall-pct').textContent = pct + '%';
    $('overall-progress').setAttribute('aria-valuenow', String(pct));
    if (total > 0 && sent >= total) $('overall-progress').classList.add('done');
    else $('overall-progress').classList.remove('done');
    if (speed != null) {
      $('overall-speed').textContent = LD.fmtSpeed(speed);
      $('overall-eta').textContent = (speed > 0 && total > sent)
        ? LD.fmtETA((total - sent) / speed) : '';
    }
  }

  function updateConn(receivers) {
    var count = (receivers || []).length;
    var dot = $('conn-dot'), text = $('conn-text'), chips = $('dev-chips');
    if (count > 0) {
      dot.className = 'dot dot-green dot-pulse pulse-green';
      text.textContent = '已连接 · ' + count + ' 台设备';
      chips.hidden = false;
      chips.textContent = '';
      receivers.forEach(function (r) { chips.appendChild(devChip(r)); });
    } else {
      dot.className = 'dot dot-amber dot-pulse';
      text.textContent = '等待手机连接…';
      chips.hidden = true;
      chips.textContent = '';
    }
  }

  function devChip(r) {
    var ua = r.ua || '';
    var isIPad = /iPad/.test(ua) || (/Macintosh/.test(ua) && /Mobile/.test(ua));
    var isIPhone = /iPhone|iPod/.test(ua);
    var isAndroid = /Android/.test(ua);
    var label = isIPad ? 'iPad' : isIPhone ? 'iPhone' : isAndroid ? 'Android' : '浏览器';
    var icon = (isIPad || isIPhone || isAndroid) ? 'phone' : 'monitor';
    return LD.el('span', { cls: 'dev-chip' },
      LD.svg(icon, 15),
      LD.el('span', { text: label + ' · ' + r.ip })
    );
  }

  function resetShare() {
    state.rows = new Map();
    state.qrCode = null;
    $('addr-chip').hidden = true;
    $('share-list').textContent = '';
    $('dev-chips').textContent = '';
    $('overall-bar').style.width = '0%';
    $('overall-pct').textContent = '0%';
    $('overall-speed').textContent = '';
    $('overall-eta').textContent = '';
    $('overall-progress').classList.remove('done');
  }

  /* ---------------------------------------------------------- 浏览弹窗 */

  var SIDE_ICONS = {
    '主目录': 'home', '桌面': 'monitor', '下载': 'download',
    '影片': 'video', '图片': 'image', '文稿': 'doc',
    '外接磁盘': 'archive', '根目录': 'folder'
  };

  function openBrowse(mode) {
    state.browse = {
      mode: mode, path: null, parent: null, entries: [],
      sel: new Map(), shortcuts: [], loading: true, error: null, truncated: false
    };
    $('browse-title').textContent = mode === 'files' ? '选择文件'
      : mode === 'folder' ? '选择文件夹'
        : mode === 'add-files' ? '继续添加文件' : '添加文件夹';
    $('browse-add').textContent = '添加选中 (0)';
    $('browse-add').disabled = true;
    $('browse-info').textContent = '点击文件夹进入，勾选文件后添加';
    $('browse-list').textContent = '';
    $('crumbs').textContent = '';
    $('browse-side').textContent = '';
    openModal($('browse-modal'));
    loadDir('');
  }

  function closeBrowse() {
    state.browse = null;
    closeModal($('browse-modal'));
  }

  function loadDir(path) {
    var b = state.browse;
    if (!b) return;
    var seq = ++state.browseSeq;
    b.loading = true;
    b.error = null;
    renderBrowseList();
    var q = path ? ('?path=' + encodeURIComponent(path)) : '';
    LD.api('/api/fs' + q).then(function (data) {
      var cur = state.browse;
      if (!cur || seq !== state.browseSeq) return; // 过期响应
      cur.loading = false;
      cur.path = data.path;
      cur.parent = data.parent;
      cur.entries = data.entries || [];
      cur.truncated = !!data.truncated;
      if (data.shortcuts && data.shortcuts.length) cur.shortcuts = data.shortcuts;
      renderSide();
      renderCrumbs();
      renderBrowseList();
      $('browse-list').scrollTop = 0;
    }).catch(function (err) {
      var cur = state.browse;
      if (!cur || seq !== state.browseSeq) return;
      cur.loading = false;
      cur.error = err;
      renderBrowseList();
    });
  }

  function renderSide() {
    var b = state.browse, box = $('browse-side');
    box.textContent = '';
    var best = null;
    b.shortcuts.forEach(function (sc) {
      var active = b.path === sc.path || (b.path && b.path.indexOf(sc.path === '/' ? '/' : sc.path + '/') === 0);
      if (active && (!best || sc.path.length > best.path.length)) best = sc;
    });
    b.shortcuts.forEach(function (sc) {
      var isActive = best && best.path === sc.path;
      box.appendChild(LD.el('button', {
        cls: 'side-item' + (isActive ? ' active' : ''), type: 'button',
        on: { click: function () { loadDir(sc.path); } }
      },
        LD.svg(SIDE_ICONS[sc.name] || 'folder', 17),
        LD.el('span', { text: sc.name })
      ));
    });
  }

  function renderCrumbs() {
    var b = state.browse, box = $('crumbs');
    box.textContent = '';
    var segs = [{ label: '根目录', path: '/' }];
    var acc = '';
    b.path.split('/').filter(Boolean).forEach(function (p) {
      acc += '/' + p;
      segs.push({ label: p, path: acc });
    });
    segs.forEach(function (seg, i) {
      var last = i === segs.length - 1;
      if (i > 0) box.appendChild(LD.el('span', { cls: 'crumb-sep' }, LD.svg('chevron-right', 13)));
      box.appendChild(LD.el('button', {
        cls: 'crumb' + (last ? ' last' : ''), type: 'button',
        on: { click: function () { if (!last) loadDir(seg.path); } }
      },
        i === 0 ? LD.svg('home', 13) : null,
        LD.el('span', { text: seg.label })
      ));
    });
    requestAnimationFrame(function () { box.scrollLeft = box.scrollWidth; });
  }

  function skRow() {
    return LD.el('div', { cls: 'sk-row' },
      LD.el('div', { cls: 'sk sk-sq' }),
      LD.el('div', { cls: 'sk-lines' },
        LD.el('div', { cls: 'sk sk-line' }),
        LD.el('div', { cls: 'sk sk-line' })
      )
    );
  }

  function renderBrowseList() {
    var b = state.browse, list = $('browse-list');
    if (!b) return;
    list.textContent = '';

    if (b.loading) {
      for (var i = 0; i < 7; i++) list.appendChild(skRow());
      return;
    }
    if (b.error) {
      list.appendChild(LD.el('div', { cls: 'list-error' },
        LD.svg('alert', 26),
        LD.el('p', { text: b.error.message }),
        LD.el('button', {
          cls: 'btn btn-plain btn-sm', type: 'button',
          on: { click: function () { loadDir(b.path || ''); } }
        }, LD.svg('refresh', 15), LD.el('span', { text: '重试' }))
      ));
      return;
    }

    if (b.path && b.path !== '/') {
      var name = LD.basename(b.path);
      list.appendChild(LD.el('button', {
        cls: 'brow add-here', type: 'button',
        on: { click: function () { confirmAdd([{ path: b.path, name: name, dir: true }]); } }
      },
        LD.svg('folder-plus', 20),
        LD.el('span', { cls: 'brow-name', text: '添加此文件夹「' + name + '」' })
      ));
    }

    if (b.truncated) {
      list.appendChild(LD.el('p', { cls: 'list-note', text: '这个目录内容过多，仅显示前 2000 项，可进入子文件夹继续选择。' }));
    }
    if (!b.entries.length) {
      list.appendChild(LD.el('p', { cls: 'list-note', text: '这个文件夹是空的' }));
    }

    b.entries.forEach(function (e) {
      list.appendChild(e.dir ? dirRow(e) : fileRow(e));
    });
  }

  function dirRow(e) {
    var chev = LD.svg('chevron-right', 16);
    chev.classList.add('chev');
    return LD.el('button', {
      cls: 'brow dir', type: 'button',
      on: { click: function () { loadDir(e.path); } }
    },
      (function () { var ic = LD.svg('folder', 20); ic.classList.add('dir-ico'); return ic; })(),
      LD.el('span', { cls: 'brow-name', text: e.name }),
      chev
    );
  }

  function fileRow(e) {
    var b = state.browse;
    var picked = b.sel.has(e.path);
    var cb = LD.el('input', { type: 'checkbox', 'aria-label': '选择 ' + e.name });
    cb.checked = picked;

    var row = LD.el('label', { cls: 'brow' + (picked ? ' picked' : '') },
      LD.el('span', { cls: 'brow-check' }, cb),
      LD.svg(LD.kindIcon(LD.kindOf(e.name, false)), 19),
      LD.el('span', { cls: 'brow-name', text: e.name }),
      LD.el('span', { cls: 'brow-meta', text: LD.fmtSize(e.size) })
    );

    cb.addEventListener('change', function () {
      if (cb.checked) b.sel.set(e.path, { path: e.path, name: e.name, dir: false });
      else b.sel.delete(e.path);
      row.classList.toggle('picked', cb.checked);
      updateFoot();
    });
    return row;
  }

  function updateFoot() {
    var b = state.browse;
    if (!b) return;
    var n = b.sel.size;
    $('browse-add').textContent = '添加选中 (' + n + ')';
    $('browse-add').disabled = n === 0;
    $('browse-info').textContent = n > 0
      ? ('已选中 ' + n + ' 个文件')
      : '点击文件夹进入，勾选文件后添加';
  }

  function confirmAdd(items) {
    var b = state.browse;
    if (!b || !items.length) return;
    if (b.mode === 'files' || b.mode === 'folder') {
      items.forEach(function (it) {
        if (!state.paths.some(function (p) { return p.path === it.path; })) state.paths.push(it);
      });
      renderSelection();
      closeBrowse();
    } else {
      closeBrowse();
      addFiles(items.map(function (it) { return it.path; }));
    }
  }

  /* ---------------------------------------------------------- 原生选择框 */

  /* 优先用系统自带的选择窗口：macOS 由服务端弹出 Finder 对话框并拿到真实路径，
   * 文件仍然直接从磁盘流式发送、不产生副本。系统不支持或窗口打不开时，
   * 回退到内置的浏览弹窗。 */
  function pick(mode) {
    if (state.picking) { LD.toast('已有一个选择窗口打开'); return; }
    var folder = mode === 'folder' || mode === 'add-folder';
    state.picking = true;
    setPickBusy(true);
    // 对话框由后台进程弹出，偶尔会在浏览器窗口后面：稍后给个提示
    var hint = setTimeout(function () { LD.toast('系统选择窗口已打开；如果没看到，它可能在浏览器窗口后面'); }, 1500);
    LD.api('/api/pick', { body: { mode: folder ? 'folder' : 'files' } }).then(function (res) {
      clearTimeout(hint);
      var items = (res && res.items) || [];
      if (items.length) applyPicked(mode, items);
    }).catch(function (err) {
      clearTimeout(hint);
      if (err.status === 409) { LD.toast(err.message); return; }
      if (err.status === 501 || err.status === 500) { openBrowse(mode); return; } // 回退内置弹窗
      LD.toast(err.message);
    }).then(function () {
      state.picking = false;
      setPickBusy(false);
    });
  }

  function setPickBusy(busy) {
    ['pick-files', 'pick-folder', 'add-more', 'add-folder'].forEach(function (id) {
      var el = $(id);
      if (!el) return;
      el.disabled = busy;
      el.classList.toggle('busy', busy);
    });
  }

  function applyPicked(mode, items) {
    if (mode === 'files' || mode === 'folder') {
      items.forEach(function (it) {
        if (!state.paths.some(function (p) { return p.path === it.path; })) state.paths.push(it);
      });
      renderSelection();
    } else {
      addFiles(items.map(function (it) { return it.path; }));
    }
  }

  /* ------------------------------------------------------------------ 拖拽 */

  /* 浏览器不会告诉网页拖入文件在磁盘上的位置，只给内容，所以拖拽要先把内容
   * 经本机回环上传到服务进程的临时目录（很快），再从那里发送；投送结束后自动删除。 */

  function hasFiles(e) {
    var t = e.dataTransfer && e.dataTransfer.types;
    if (!t) return false;
    for (var i = 0; i < t.length; i++) if (t[i] === 'Files') return true;
    return false;
  }

  function dropAllowed() {
    return !$('view-setup').hidden || !$('view-share').hidden;
  }

  function showDropOverlay(on) {
    var ov = $('drop-overlay');
    if (!ov) return;
    if (on) {
      $('drop-overlay-title').textContent = !$('view-share').hidden
        ? '松开即可添加到当前投送' : '松开即可加入待发送列表';
      ov.hidden = false;
      requestAnimationFrame(function () { ov.classList.add('show'); });
    } else {
      ov.classList.remove('show');
      ov.hidden = true;
    }
  }

  document.addEventListener('dragenter', function (e) {
    if (!hasFiles(e) || !dropAllowed()) return;
    e.preventDefault();
    state.dropDepth++;
    showDropOverlay(true);
  });
  document.addEventListener('dragover', function (e) {
    if (!hasFiles(e) || !dropAllowed()) return;
    e.preventDefault();
    e.dataTransfer.dropEffect = 'copy';
  });
  document.addEventListener('dragleave', function (e) {
    if (!hasFiles(e)) return;
    state.dropDepth = Math.max(0, state.dropDepth - 1);
    if (state.dropDepth === 0) showDropOverlay(false);
  });
  document.addEventListener('drop', function (e) {
    if (!hasFiles(e)) return;
    e.preventDefault();
    state.dropDepth = 0;
    showDropOverlay(false);
    if (!dropAllowed()) return;
    handleDrop(e.dataTransfer);
  });
  window.addEventListener('dragend', function () { state.dropDepth = 0; showDropOverlay(false); });

  function readAllEntries(reader) {
    return new Promise(function (resolve, reject) {
      var all = [];
      (function step() {
        reader.readEntries(function (batch) {
          if (!batch.length) { resolve(all); return; }
          all = all.concat(Array.prototype.slice.call(batch));
          step();
        }, reject);
      })();
    });
  }

  function entryFile(entry) {
    return new Promise(function (resolve, reject) { entry.file(resolve, reject); });
  }

  /* 把拖入项展开成 [{rel, file}]：rel 以顶层项名开头并保留文件夹层级，跳过隐藏文件 */
  function collectDropped(dt) {
    var entries = [];
    var items = dt.items ? Array.prototype.slice.call(dt.items) : [];
    items.forEach(function (it) {
      if (it.kind !== 'file') return;
      var en = it.webkitGetAsEntry ? it.webkitGetAsEntry() : null;
      if (en) entries.push(en);
      else {
        var f = it.getAsFile();
        if (f) entries.push({ isFile: true, name: f.name, _file: f });
      }
    });
    if (!entries.length && dt.files) {
      Array.prototype.forEach.call(dt.files, function (f) {
        entries.push({ isFile: true, name: f.name, _file: f });
      });
    }

    var out = [], tops = [], used = {};
    function uniqueTop(name) {
      var dot = name.lastIndexOf('.');
      var stem = dot > 0 ? name.slice(0, dot) : name, ext = dot > 0 ? name.slice(dot) : '';
      var n = name, i = 2;
      while (used[n]) n = stem + ' (' + (i++) + ')' + ext;
      used[n] = true;
      return n;
    }
    function walk(entry, rel, depth) {
      if (depth > 48) return Promise.resolve();
      if (entry.isFile) {
        var p = entry._file ? Promise.resolve(entry._file) : entryFile(entry);
        return p.then(function (f) { out.push({ rel: rel, file: f }); }, function () { /* 读不了就跳过 */ });
      }
      if (entry.isDirectory) {
        return readAllEntries(entry.createReader()).then(function (kids) {
          var seq = Promise.resolve();
          kids.forEach(function (k) {
            if (k.name.charAt(0) === '.') return;
            seq = seq.then(function () { return walk(k, rel + '/' + k.name, depth + 1); });
          });
          return seq;
        }, function () { /* 跳过 */ });
      }
      return Promise.resolve();
    }
    var seq = Promise.resolve();
    entries.forEach(function (en) {
      var top = uniqueTop(en.name);
      tops.push({ name: top, dir: !!en.isDirectory });
      seq = seq.then(function () { return walk(en, top, 0); });
    });
    return seq.then(function () { return { files: out, tops: tops }; });
  }

  function uploadOne(batch, item, onProgress) {
    return new Promise(function (resolve, reject) {
      var xhr = new XMLHttpRequest();
      xhr.open('POST', '/api/upload?batch=' + encodeURIComponent(batch) + '&rel=' + encodeURIComponent(item.rel));
      var t = LD.getToken();
      if (t) xhr.setRequestHeader('X-Admin-Token', t);
      xhr.setRequestHeader('X-LocalDrop-Upload', '1');
      xhr.setRequestHeader('Content-Type', 'application/octet-stream');
      xhr.upload.onprogress = function (ev) { if (ev.lengthComputable) onProgress(ev.loaded); };
      xhr.onload = function () {
        var data = null;
        try { data = JSON.parse(xhr.responseText); } catch (e) { /* ignore */ }
        if (xhr.status >= 200 && xhr.status < 300 && data) resolve(data);
        else reject(new Error((data && data.error) || ('上传失败（' + xhr.status + '）')));
      };
      xhr.onerror = function () { reject(new Error('上传中断')); };
      xhr.send(item.file);
    });
  }

  function randomBatch() {
    var a = 'abcdefghijklmnopqrstuvwxyz0123456789', s = '';
    var arr = new Uint8Array(16);
    if (window.crypto && crypto.getRandomValues) crypto.getRandomValues(arr);
    else for (var j = 0; j < 16; j++) arr[j] = Math.floor(Math.random() * 256);
    for (var i = 0; i < 16; i++) s += a[arr[i] % a.length];
    return s;
  }

  function showDropProgress(on, done, total, bytes, bytesTotal) {
    var box = $('drop-progress');
    if (!box) return;
    box.hidden = !on;
    if (!on) return;
    var pct = bytesTotal > 0 ? Math.min(100, Math.floor(bytes / bytesTotal * 100)) : 0;
    $('drop-progress-text').textContent = '正在读取拖入的文件 ' + done + '/' + total +
      ' · ' + LD.fmtSize(bytes) + ' / ' + LD.fmtSize(bytesTotal);
    $('drop-progress-bar').style.width = pct + '%';
  }

  function handleDrop(dt) {
    if (state.dropping) { LD.toast('上一批拖入的文件还在处理中'); return; }
    state.dropping = true;
    var batch = randomBatch();
    var total = 0, done = 0, bytesDone = 0, bytesTotal = 0, batchDir = null;
    collectDropped(dt).then(function (r) {
      var files = r.files;
      if (!files.length) { LD.toast('拖入的内容里没有可发送的文件'); return null; }
      total = files.length;
      files.forEach(function (f) { bytesTotal += f.file.size || 0; });
      showDropProgress(true, 0, total, 0, bytesTotal);
      var seq = Promise.resolve();
      files.forEach(function (it) {
        seq = seq.then(function () {
          var base = bytesDone;
          return uploadOne(batch, it, function (loaded) {
            showDropProgress(true, done, total, base + loaded, bytesTotal);
          }).then(function (res) {
            batchDir = res.batchDir;
            done++;
            bytesDone += it.file.size || 0;
            showDropProgress(true, done, total, bytesDone, bytesTotal);
          });
        });
      });
      return seq.then(function () {
        var have = {};
        files.forEach(function (f) { have[f.rel.split('/')[0]] = true; });
        return r.tops.filter(function (t) { return have[t.name]; }).map(function (t) {
          return {
            path: batchDir + '/' + t.name, name: t.name, dir: t.dir, batch: batch,
            label: '拖入 · 临时副本，投送结束后自动删除'
          };
        });
      });
    }).then(function (items) {
      if (!items) return;
      if (!$('view-share').hidden && state.session) {
        addFiles(items.map(function (it) { return it.path; }));
      } else {
        items.forEach(function (it) { state.paths.push(it); });
        renderSelection();
        LD.toast('已加入 ' + items.length + ' 项');
      }
    }).catch(function (err) {
      LD.toast(err.message || '拖入失败');
      discardBatch(batch);
    }).then(function () {
      state.dropping = false;
      showDropProgress(false);
    });
  }

  function discardBatch(batch) {
    if (!batch) return;
    LD.api('/api/upload/discard', { body: { batch: batch } }).catch(function () { /* 忽略 */ });
  }

  /* 选择列表里不再引用的拖入批次：删掉临时副本 */
  function discardOrphanBatches(prevPaths) {
    var still = {};
    state.paths.forEach(function (p) { if (p.batch) still[p.batch] = true; });
    var seen = {};
    (prevPaths || []).forEach(function (p) {
      if (p.batch && !still[p.batch] && !seen[p.batch]) { seen[p.batch] = true; discardBatch(p.batch); }
    });
  }

  /* -------------------------------------------------------------- 弹层 */

  function openModal(el) {
    $('backdrop').hidden = false;
    el.hidden = false;
    requestAnimationFrame(function () {
      $('backdrop').classList.add('show');
      el.classList.add('show');
    });
    state.modalOpen = el;
  }

  function closeModal(el) {
    el.classList.remove('show');
    state.modalOpen = null;
    if (!$('browse-modal').hidden && el !== $('browse-modal')) {
      // 另一个弹层仍开着，保持遮罩
    } else if (!$('confirm-sheet').hidden && el !== $('confirm-sheet')) {
      // 同上
    } else {
      $('backdrop').classList.remove('show');
    }
    setTimeout(function () {
      el.hidden = true;
      if (!state.modalOpen) $('backdrop').hidden = true;
    }, 250);
  }

  function openConfirm(o) {
    var glyph = $('confirm-glyph');
    glyph.className = 'sheet-glyph ' + (o.glyph || '');
    glyph.textContent = '';
    glyph.appendChild(LD.svg(o.glyph === 'danger' ? 'stop' : 'alert', 26));
    $('confirm-title').textContent = o.title;
    $('confirm-text').textContent = o.text;
    var ok = $('confirm-ok');
    ok.textContent = o.okText || '确定';
    ok.className = 'btn btn-block ' + (o.danger ? 'btn-danger' : 'btn-primary');
    state.confirmCb = o.onOk;
    openModal($('confirm-sheet'));
  }

  /* ---------------------------------------------------------------- 绑定 */

  $('pick-files').addEventListener('click', function () { pick('files'); });
  $('pick-folder').addEventListener('click', function () { pick('folder'); });
  $('add-more').addEventListener('click', function () { pick('add-files'); });
  $('add-folder').addEventListener('click', function () { pick('add-folder'); });
  $('sel-clear').addEventListener('click', function () {
    var prev = state.paths;
    state.paths = [];
    state.warnings = [];
    discardOrphanBatches(prev);
    renderSelection();
  });
  $('start-btn').addEventListener('click', function () { startShare(false); });
  $('stop-share').addEventListener('click', stopShare);

  $('browse-close').addEventListener('click', closeBrowse);
  $('browse-cancel').addEventListener('click', closeBrowse);
  $('browse-add').addEventListener('click', function () {
    if (state.browse) confirmAdd(Array.from(state.browse.sel.values()));
  });
  $('backdrop').addEventListener('click', function () {
    if (state.browse && !$('browse-modal').hidden) closeBrowse();
  });
  document.addEventListener('keydown', function (e) {
    if (e.key !== 'Escape') return;
    if (!$('confirm-sheet').hidden) { closeModal($('confirm-sheet')); return; }
    if (!$('browse-modal').hidden) closeBrowse();
  });

  $('confirm-cancel').addEventListener('click', function () { closeModal($('confirm-sheet')); });
  $('confirm-ok').addEventListener('click', function () {
    var cb = state.confirmCb;
    state.confirmCb = null;
    closeModal($('confirm-sheet'));
    if (cb) cb();
  });

  function copySessionUrl() {
    if (!state.session) return;
    LD.copyText(state.session.url).then(function (ok) {
      LD.toast(ok ? '链接已复制' : '复制失败，请手动选择文本');
    });
  }
  $('copy-url').addEventListener('click', copySessionUrl);
  $('addr-chip').addEventListener('click', copySessionUrl);
  $('copy-code').addEventListener('click', function () {
    if (!state.session) return;
    LD.copyText(state.session.code).then(function (ok) {
      LD.toast(ok ? '配对码已复制' : '复制失败，请手动选择文本');
    });
  });

  /* 手机配对 */
  var pairInput = $('pair-input');
  pairInput.addEventListener('input', function () {
    var v = pairInput.value.toLowerCase().replace(/[^a-z0-9]/g, '').slice(0, 10);
    if (v !== pairInput.value) pairInput.value = v;
    $('pair-go').disabled = v.length < 10;
  });
  pairInput.addEventListener('keydown', function (e) {
    if (e.key === 'Enter') tryPair();
  });
  $('pair-go').addEventListener('click', tryPair);

  function shake(el) {
    el.classList.remove('shake');
    void el.offsetWidth;
    el.classList.add('shake');
  }

  function tryPair() {
    var code = pairInput.value.toLowerCase().replace(/[^a-z0-9]/g, '');
    if (code.length < 10) { shake(pairInput); return; }
    var btn = $('pair-go');
    btn.disabled = true;
    btn.textContent = '正在验证…';
    LD.api('/api/r/' + encodeURIComponent(code)).then(function () {
      location.href = '/r/' + encodeURIComponent(code);
    }).catch(function (err) {
      shake(pairInput);
      LD.toast(err.status === 404 ? '配对码无效或投送已结束' : err.message);
    }).then(function () {
      btn.textContent = '进入';
      btn.disabled = false;
    });
  }

  /* 令牌 */
  var deniedInput = $('denied-input');
  deniedInput.addEventListener('keydown', function (e) {
    if (e.key === 'Enter') useToken();
  });
  $('denied-go').addEventListener('click', useToken);
  function useToken() {
    var v = deniedInput.value.trim();
    if (!v) { shake(deniedInput); return; }
    LD.setToken(v);
    showView('view-loading');
    boot();
  }

  boot();
})();
