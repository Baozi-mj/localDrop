/* LocalDrop · receive.js — 手机接收页逻辑（r.html）。
 * 下载一律走真实链接（浏览器下载管理器接管，锁屏后仍可继续/续传），
 * 页面只负责展示进度、心跳和每个文件的下载入口。 */
(function () {
  'use strict';

  var LD = window.LD;
  function $(id) { return document.getElementById(id); }
  var platform = LD.platform();

  var code = (function () {
    var m = /^\/r\/([A-Za-z0-9]+)/.exec(location.pathname);
    return m ? m[1] : '';
  })();
  var base = '/api/r/' + encodeURIComponent(code);

  var state = {
    session: null,
    rows: new Map(),      // 文件 id -> {row,bar,pct,stateEl,done}
    ids: '',              // 当前渲染的文件 id 列表指纹
    es: null,
    hb: null,
    ended: false,
    lastProbe: 0,
    seqRunning: false,
    seqTimer: null
  };

  /* ------------------------------------------------------------ 视图切换 */

  function showView(id) {
    ['view-loading', 'view-main', 'view-ended'].forEach(function (v) { $(v).hidden = v !== id; });
    $('dock').hidden = id !== 'view-main' || !(state.session && state.session.files.length);
    if (id !== 'view-loading') $('view-loading').setAttribute('aria-busy', 'false');
  }

  function showEnded(title, text) {
    state.ended = true;
    stopStreams();
    if (state.seqTimer) {
      clearTimeout(state.seqTimer);
      state.seqTimer = null;
    }
    state.seqRunning = false;
    $('ended-title').textContent = title;
    $('ended-text').textContent = text;
    showView('view-ended');
  }

  function handleFetchError(err) {
    if (err && err.status === 404) {
      showEnded('投送已结束', '链接已失效或电脑已停止共享。请让电脑重新发起投送后再扫码。');
    } else if (err && err.status === 429) {
      showEnded('请稍后再试', '尝试过于频繁，请稍等片刻后刷新页面。');
    } else {
      showEnded('暂时无法连接', '请确认手机和电脑连接的是同一个 Wi-Fi，然后刷新页面。');
    }
  }

  /* ---------------------------------------------------------------- 启动 */

  function boot() {
    if (!code) {
      showEnded('链接无效', '这不是有效的接收链接，请重新扫描电脑上的二维码。');
      return;
    }
    LD.api(base).then(function (s) {
      onSession(s, true);
      openEvents();
      startHeartbeat();
    }).catch(handleFetchError);
  }

  /* ---------------------------------------------------------------- 会话 */

  function onSession(s, first) {
    if (!s || !s.active) {
      showEnded('投送已结束', '电脑已停止共享。已经下载完成的文件不受影响。');
      return;
    }
    s.files = s.files || [];
    state.session = s;
    renderHero(s);
    renderList(s);
    renderDock(s);
    if (first) {
      showView('view-main');
    }
    var sent = 0;
    s.files.forEach(function (f) {
      sent += f.sent || 0;
      var ref = state.rows.get(f.id);
      if (ref) updateRow(ref, { sent: f.sent, size: f.size, done: f.done });
    });
    updateOverall(sent, s.totalSize, null);
  }

  function renderHero(s) {
    $('hero-name').textContent = s.name || '来自电脑的投送';
    $('hero-meta').textContent = s.files.length + ' 个文件 · 总计 ' + LD.fmtSize(s.totalSize);
    $('files-title').textContent = '文件 · ' + s.files.length + ' 个';
    $('files-sub').textContent = s.files.length ? '点文件右侧的「下载」即可保存到手机，支持断点续传' : '电脑还没有添加文件';
  }

  function renderList(s) {
    var ids = s.files.map(function (f) { return f.id; }).join(',');
    if (ids === state.ids) return;
    state.ids = ids;
    state.rows = new Map();
    var list = $('rx-list');
    list.textContent = '';
    if (!s.files.length) {
      list.appendChild(LD.el('p', { cls: 'list-note', text: '等待电脑添加文件…' }));
      return;
    }
    s.files.forEach(function (f) { list.appendChild(buildRow(f)); });
  }

  function fileURL(f) {
    return '/d/' + encodeURIComponent(code) + '/' + f.id;
  }

  function buildRow(f) {
    var kind = LD.kindOf(f.name, false);
    var sub = (f.relPath && f.relPath !== f.name) ? f.relPath : '';
    var bar = LD.el('i');
    var pct = LD.el('div', { cls: 'rx-row-pct', text: '点击下载' });
    var sizeEl = LD.el('span', { cls: 'rx-row-size', text: LD.fmtSize(f.size) });
    var stateEl = LD.el('span', { cls: 'rx-row-dl' }, LD.svg('download', 15), LD.el('span', { text: '下载' }));
    var barWrap = LD.el('div', { cls: 'rx-row-bar' }, LD.el('div', { cls: 'progress' }, bar), pct);
    barWrap.hidden = true; // 只在有进度时显示

    var row = LD.el('a', {
      cls: 'rx-row',
      href: fileURL(f),
      'aria-label': '下载 ' + f.name + '，' + LD.fmtSize(f.size),
      on: {
        click: function () {
          LD.toast('开始下载 ' + f.name);
        }
      }
    },
      LD.el('div', { cls: 'rx-row-main' },
        LD.tintSquare(kind),
        LD.el('div', { cls: 'rx-row-body' },
          LD.el('div', { cls: 'rx-row-name', text: f.name }),
          sub ? LD.el('div', { cls: 'rx-row-sub', text: sub }) : null
        ),
        LD.el('div', { cls: 'rx-row-side' }, sizeEl, stateEl)
      ),
      barWrap
    );
    state.rows.set(f.id, { row: row, bar: bar, pct: pct, stateEl: stateEl, barWrap: barWrap, done: false });
    return row;
  }

  function updateRow(ref, item) {
    var pct = LD.pct(item.sent, item.size);
    ref.barWrap.hidden = !(item.done || (item.sent || 0) > 0);
    ref.bar.style.width = pct + '%';
    if (item.done) {
      ref.pct.textContent = '已发送 100%';
      if (!ref.done) {
        ref.done = true;
        ref.row.classList.add('done');
        var ic = ref.stateEl.querySelector('svg');
        if (ic) ref.stateEl.replaceChild(LD.svg('check', 15), ic);
        var tx = ref.stateEl.querySelector('span');
        if (tx) tx.textContent = '已保存';
      }
    } else {
      ref.pct.textContent = item.sent > 0 ? ('已发送 ' + pct + '%') : '点击下载';
    }
  }

  /* ------------------------------------------------------------ 底部操作栏 */

  function renderDock(s) {
    var n = s.files.length;
    var a = $('dl-all');
    if (!n) {
      $('dock').hidden = true;
      return;
    }
    if (n === 1) {
      a.href = fileURL(s.files[0]);
      $('dl-main').textContent = '下载文件';
      $('dl-sub').textContent = LD.fmtSize(s.files[0].size) + ' · 支持断点续传';
    } else {
      // 没有打包下载：按钮逐个触发真实下载链接，每个文件都能断点续传
      a.href = '#';
      if (!state.seqRunning) {
        $('dl-main').textContent = '逐个下载 · ' + n + ' 个文件';
        $('dl-sub').textContent = '一个一个保存到手机 · 支持断点续传';
      }
    }
    $('dock').hidden = $('view-main').hidden;
  }

  function updateOverall(sent, total, speed) {
    var box = $('dock-progress');
    if (!(sent > 0)) {
      box.hidden = true;
      return;
    }
    box.hidden = false;
    var pct = LD.pct(sent, total);
    $('dock-ptext').textContent = (total > 0 && sent >= total)
      ? ('全部文件已发送 · ' + LD.fmtSize(total))
      : ('已发送 ' + pct + '% · ' + LD.fmtSize(sent) + ' / ' + LD.fmtSize(total));
    $('dock-pmeta').textContent = (speed && speed > 0) ? LD.fmtSpeed(speed) : '';
    $('dock-bar').style.width = pct + '%';
    var pb = $('dock-progressbar');
    pb.setAttribute('aria-valuenow', String(pct));
    pb.classList.toggle('done', total > 0 && sent >= total);
  }

  /* 进度事件是增量：只包含有变化的文件，总量用事件里的汇总字段 */
  function onProgress(d) {
    if (!state.session) return;
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
    var total = (typeof d.total === 'number') ? d.total : state.session.totalSize;
    updateOverall(sent, total, speed);
  }

  /* 逐个下载（所有平台）：错开触发真实下载链接，一次一个；会话结束或文件被移除时停止 */
  function startSequential() {
    if (state.seqRunning || !state.session) return;
    var files = state.session.files.slice();
    if (!files.length) return;
    state.seqRunning = true;
    var label = $('dl-main');
    var total = files.length;
    LD.toast('若浏览器询问“下载多个文件”，请选择允许');
    var i = 0;
    function finish() {
      state.seqRunning = false;
      state.seqTimer = null;
      label.textContent = '逐个下载 · ' + total + ' 个文件';
    }
    function next() {
      state.seqTimer = null;
      if (state.ended || !state.session || i >= files.length) {
        finish();
        return;
      }
      var f = files[i++];
      var exists = state.session.files.some(function (x) { return x.id === f.id; });
      if (exists) {
        label.textContent = '逐个下载 ' + i + '/' + total;
        var a = LD.el('a', { href: fileURL(f), download: f.name });
        a.hidden = true;
        document.body.appendChild(a);
        a.click();
        setTimeout(function () { if (a.parentNode) a.parentNode.removeChild(a); }, 1000);
      }
      state.seqTimer = setTimeout(next, exists ? 1500 : 0);
    }
    next();
  }

  /* ---------------------------------------------------------------- 提示 */

  /* 下载由浏览器接管：能否保存、保存到哪里都由浏览器决定，本工具既看不到也改不了。 */
  var TIP_TEXT = {
    ios: '文件由 Safari 下载，保存在哪里由浏览器决定（一般在「文件」App 的「下载」里），本工具无法指定或修改。',
    android: '文件由浏览器下载，保存在哪里由浏览器决定（一般在系统「下载」目录，通知栏可看进度），本工具无法指定或修改。',
    other: '文件由浏览器下载，保存在哪里由浏览器决定（一般是浏览器的默认下载目录），本工具无法指定或修改。'
  };

  function renderTip() {
    var key = platform.ios ? 'ios' : (platform.android ? 'android' : 'other');
    $('dl-tip').textContent = TIP_TEXT[key];
  }

  /* -------------------------------------------------------------- 事件流 */

  function openEvents() {
    if (state.es) return;
    var es = new EventSource(base + '/events');
    state.es = es;
    es.onmessage = function (ev) {
      var d;
      try { d = JSON.parse(ev.data); } catch (e) { return; }
      if (d.type === 'state') onSession(d.session, false);
      else if (d.type === 'progress') onProgress(d);
    };
    es.onerror = function () {
      // 会话结束时 SSE 会以 404 永久失败：最多每 5 秒探测一次
      if (state.ended) return;
      var now = Date.now();
      if (now - state.lastProbe < 5000) return;
      state.lastProbe = now;
      LD.api(base).then(function (s) {
        onSession(s, false);
      }).catch(function (err) {
        if (err.status === 404) handleFetchError(err);
      });
    };
  }

  /* 主动对账：拉一次最新会话并在需要时重建 SSE。
   * iOS Safari 息屏或切后台后经常不再重连 EventSource，电脑新加的文件就到不了手机，
   * 所以回到前台、网络恢复、心跳发现 SSE 已关闭时都要对一次账。 */
  function resync(force) {
    if (state.ended || !code) return;
    var now = Date.now();
    if (!force && now - state.lastProbe < 1000) return;
    state.lastProbe = now;
    LD.api(base).then(function (s) {
      onSession(s, false);
    }).catch(function (err) {
      if (err.status === 404) handleFetchError(err);
    });
    if (state.es && state.es.readyState === 2) { // CLOSED：浏览器已放弃重连
      try { state.es.close(); } catch (e) { /* ignore */ }
      state.es = null;
    }
    if (!state.es) openEvents();
  }

  function startHeartbeat() {
    if (state.hb) return;
    var beat = function () {
      if (state.ended) return;
      if (!state.es || state.es.readyState === 2) resync(true);
      LD.api(base + '/hb', { body: {} }).catch(function (err) {
        if (err.status === 404) handleFetchError(err);
      });
    };
    beat();
    state.hb = setInterval(beat, 8000);
  }

  function stopStreams() {
    if (state.es) {
      try { state.es.close(); } catch (e) { /* ignore */ }
      state.es = null;
    }
    if (state.hb) {
      clearInterval(state.hb);
      state.hb = null;
    }
  }

  /* ---------------------------------------------------------------- 绑定 */

  $('dl-all').addEventListener('click', function (ev) {
    var s = state.session;
    if (!s || !s.files.length) {
      ev.preventDefault();
      return;
    }
    if (s.files.length === 1) {
      LD.toast('开始下载 ' + s.files[0].name);
      return; // 单文件：交给浏览器下载管理器
    }
    ev.preventDefault();
    startSequential();
  });
  $('ended-retry').addEventListener('click', function () { location.reload(); });

  document.addEventListener('visibilitychange', function () {
    if (document.visibilityState === 'visible') resync(true);
  });
  window.addEventListener('pageshow', function (e) { if (e.persisted) resync(true); });
  window.addEventListener('focus', function () { resync(false); });
  window.addEventListener('online', function () { resync(true); });

  renderTip();
  boot();
})();
