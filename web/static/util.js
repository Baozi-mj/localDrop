/* LocalDrop · util.js — index.html 与 r.html 共用的工具与设计基建。
 * 暴露 window.LD。本文件在 <head> 中加载：会同步应用主题，避免深浅色闪烁。 */
(function () {
  'use strict';

  var LD = {};
  window.LD = LD;

  /* ------------------------------------------------------------------ 主题 */

  function storedTheme() {
    var q = null;
    try { q = new URLSearchParams(location.search).get('theme'); } catch (e) { /* ignore */ }
    if (q === 'dark' || q === 'light') {
      try { localStorage.setItem('ld-theme', q); } catch (e) { /* ignore */ }
      return q;
    }
    try {
      var s = localStorage.getItem('ld-theme');
      if (s === 'dark' || s === 'light') return s;
    } catch (e) { /* ignore */ }
    return null;
  }

  LD.applyTheme = function () {
    var t = storedTheme();
    if (t) document.documentElement.setAttribute('data-theme', t);
  };
  LD.applyTheme(); // 尽早执行，避免首屏闪烁

  /* ------------------------------------------------------------ 管理员令牌 */

  var TOKEN_KEY = 'ld-token';

  /* 从 #t=TOKEN 读取管理员令牌（自动打开浏览器时携带），存入 sessionStorage 后抹掉 hash。 */
  LD.captureToken = function () {
    var m = /(?:^|[#&])t=([A-Za-z0-9]+)/.exec(location.hash || '');
    if (!m) return;
    LD.setToken(m[1]);
    try { history.replaceState(null, '', location.pathname + location.search); } catch (e) { /* ignore */ }
  };
  LD.getToken = function () {
    try { return sessionStorage.getItem(TOKEN_KEY) || ''; } catch (e) { return ''; }
  };
  LD.setToken = function (t) {
    try { sessionStorage.setItem(TOKEN_KEY, t); } catch (e) { /* ignore */ }
  };

  /* ---------------------------------------------------------------- 网络 */

  /* fetch 包装：自动带管理员令牌、解析 JSON、把 {"error": ...} 变成 Error。 */
  LD.api = function (path, opts) {
    opts = opts || {};
    var headers = {};
    for (var k in (opts.headers || {})) headers[k] = opts.headers[k];
    var t = LD.getToken();
    if (t) headers['X-Admin-Token'] = t;
    var body = null;
    if (opts.body !== undefined) {
      body = JSON.stringify(opts.body);
      headers['Content-Type'] = 'application/json';
    }
    return fetch(path, {
      method: opts.method || (body !== null ? 'POST' : 'GET'),
      headers: headers,
      body: body
    }).then(function (res) {
      return res.json().catch(function () { return null; }).then(function (data) {
        if (!res.ok) {
          var err = new Error((data && data.error) || ('请求失败（' + res.status + '）'));
          err.status = res.status;
          err.data = data;
          throw err;
        }
        return data;
      });
    });
  };

  /* ------------------------------------------------------------------ DOM */

  function appendKid(n, kid) {
    if (kid === null || kid === undefined || kid === false) return;
    if (Array.isArray(kid)) { kid.forEach(function (k) { appendKid(n, k); }); return; }
    n.appendChild(kid.nodeType ? kid : document.createTextNode(String(kid)));
  }

  /* el('div', {cls, text, on, ...attributes}, ...children) */
  LD.el = function (tag, props) {
    var n = document.createElement(tag);
    if (props) {
      for (var k in props) {
        var v = props[k];
        if (v === null || v === undefined || v === false) continue;
        if (k === 'cls') n.className = v;
        else if (k === 'text') n.textContent = v;
        else if (k === 'on') { for (var ev in v) n.addEventListener(ev, v[ev]); }
        else n.setAttribute(k, v === true ? '' : String(v));
      }
    }
    for (var i = 2; i < arguments.length; i++) appendKid(n, arguments[i]);
    return n;
  };

  /* ---------------------------------------------------------------- 图标 */

  var NS = 'http://www.w3.org/2000/svg';

  var ICONS = {
    check: [['path', { d: 'M5 12.6l4.6 4.6L19 7.5' }]],
    x: [['path', { d: 'M6.6 6.6l10.8 10.8M17.4 6.6L6.6 17.4' }]],
    plus: [['path', { d: 'M12 5.5v13M5.5 12h13' }]],
    copy: [
      ['rect', { x: 9, y: 9, width: 11, height: 11, rx: 2.5 }],
      ['path', { d: 'M6.2 15H5.5A2.5 2.5 0 0 1 3 12.5v-7A2.5 2.5 0 0 1 5.5 3h7A2.5 2.5 0 0 1 15 5.5v.7' }]
    ],
    link: [
      ['path', { d: 'M10.5 13.5a4.2 4.2 0 0 1 0-6l2.9-2.9a4.2 4.2 0 0 1 6 6l-1.5 1.5' }],
      ['path', { d: 'M13.5 10.5a4.2 4.2 0 0 1 0 6l-2.9 2.9a4.2 4.2 0 0 1-6-6l1.5-1.5' }]
    ],
    stop: [['rect', { x: 6.8, y: 6.8, width: 10.4, height: 10.4, rx: 2.6 }]],
    phone: [
      ['rect', { x: 7.5, y: 3, width: 9, height: 18, rx: 2.6 }],
      ['path', { d: 'M11 18.1h2' }]
    ],
    monitor: [
      ['rect', { x: 3.5, y: 5, width: 17, height: 12, rx: 2.5 }],
      ['path', { d: 'M12 17v3.2M8.5 20.2h7' }]
    ],
    qr: [
      ['rect', { x: 4, y: 4, width: 6.4, height: 6.4, rx: 1.8 }],
      ['rect', { x: 13.6, y: 4, width: 6.4, height: 6.4, rx: 1.8 }],
      ['rect', { x: 4, y: 13.6, width: 6.4, height: 6.4, rx: 1.8 }],
      ['path', { d: 'M13.8 13.8h2.9v2.9h-2.9zM18.4 18.4h1.6v1.6h-1.6zM18.4 13.8h1.6M13.8 18.4v1.6' }]
    ],
    folder: [['path', { d: 'M3.5 7A2.5 2.5 0 0 1 6 4.5h3.2a2 2 0 0 1 1.6.8l.9 1.2H18A2.5 2.5 0 0 1 20.5 9v8A2.5 2.5 0 0 1 18 19.5H6A2.5 2.5 0 0 1 3.5 17z' }]],
    'folder-plus': [
      ['path', { d: 'M3.5 7A2.5 2.5 0 0 1 6 4.5h3.2a2 2 0 0 1 1.6.8l.9 1.2H18A2.5 2.5 0 0 1 20.5 9v8A2.5 2.5 0 0 1 18 19.5H6A2.5 2.5 0 0 1 3.5 17z' }],
      ['path', { d: 'M12 11.5v5M9.5 14h5' }]
    ],
    file: [
      ['path', { d: 'M6.5 3.5h6.8L18.5 8.7V19a1.5 1.5 0 0 1-1.5 1.5H6.5A1.5 1.5 0 0 1 5 19V5a1.5 1.5 0 0 1 1.5-1.5z' }],
      ['path', { d: 'M13 3.5v5.5h5.5' }]
    ],
    doc: [
      ['path', { d: 'M6.5 3.5h6.8L18.5 8.7V19a1.5 1.5 0 0 1-1.5 1.5H6.5A1.5 1.5 0 0 1 5 19V5a1.5 1.5 0 0 1 1.5-1.5z' }],
      ['path', { d: 'M13 3.5v5.5h5.5M9 13.5h6M9 16.5h4' }]
    ],
    image: [
      ['rect', { x: 4, y: 5.5, width: 16, height: 13, rx: 2.5 }],
      ['circle', { cx: 9, cy: 10, r: 1.5 }],
      ['path', { d: 'M4.5 16.6l4.1-4.1 3.4 3.4 3-3 4.5 4.5' }]
    ],
    video: [
      ['rect', { x: 4, y: 6, width: 11.5, height: 12, rx: 2.5 }],
      ['path', { d: 'M15.5 10.6l4.5-2.8v8.4l-4.5-2.8z' }]
    ],
    music: [
      ['path', { d: 'M9.5 17.6V6.1l8.5-1.7v11.3' }],
      ['circle', { cx: 7.2, cy: 17.6, r: 2.3 }],
      ['circle', { cx: 15.7, cy: 15.7, r: 2.3 }]
    ],
    archive: [
      ['path', { d: 'M12 3.4l8 4.1v9l-8 4.1-8-4.1v-9z' }],
      ['path', { d: 'M4.2 7.6L12 11.7l7.8-4.1M12 11.7v8.9M12 3.4v4.4' }]
    ],
    code: [
      ['path', { d: 'M9 8.2L4.8 12 9 15.8M15 8.2L19.2 12 15 15.8' }]
    ],
    download: [
      ['path', { d: 'M12 4.5v10.6M7.7 11l4.3 4.3L16.3 11M5 19.4h14' }]
    ],
    'download-box': [
      ['path', { d: 'M12 4v9.2M8.6 10l3.4 3.4L15.4 10' }],
      ['path', { d: 'M5 15.5v2A2.5 2.5 0 0 0 7.5 20h9a2.5 2.5 0 0 0 2.5-2.5v-2' }]
    ],
    refresh: [
      ['path', { d: 'M19.5 12a7.5 7.5 0 1 1-2.2-5.3' }],
      ['path', { d: 'M19.5 4.6v4.4H15' }]
    ],
    alert: [
      ['path', { d: 'M12 4.7l8.7 14.7H3.3z' }],
      ['path', { d: 'M12 10.2v4M12 17.1v.2' }]
    ],
    info: [
      ['circle', { cx: 12, cy: 12, r: 8.5 }],
      ['path', { d: 'M12 11.2v5M12 7.9v.2' }]
    ],
    lock: [
      ['rect', { x: 5, y: 10.5, width: 14, height: 9.5, rx: 2.6 }],
      ['path', { d: 'M8.5 10.5V8a3.5 3.5 0 0 1 7 0v2.5' }]
    ],
    wifi: [
      ['path', { d: 'M4.5 10.2a11 11 0 0 1 15 0M8 13.6a6.2 6.2 0 0 1 8 0' }],
      ['path', { d: 'M12 17.2v.2' }]
    ],
    home: [
      ['path', { d: 'M4 10.8L12 4l8 6.8' }],
      ['path', { d: 'M6.3 9.3V19A1.5 1.5 0 0 0 7.8 20.5h8.4A1.5 1.5 0 0 0 17.7 19V9.3' }]
    ],
    'chevron-right': [['path', { d: 'M9.5 6.5l5.5 5.5-5.5 5.5' }]],
    'chevron-left': [['path', { d: 'M14.5 6.5L9 12l5.5 5.5' }]],
    'arrow-left': [['path', { d: 'M19 12H5.5M11.5 6L5.5 12l6 6' }]],
    clock: [
      ['circle', { cx: 12, cy: 12, r: 8.5 }],
      ['path', { d: 'M12 7.6V12l3 1.9' }]
    ],
    spark: [
      ['path', { d: 'M12 4.5v15M4.5 12h15' }],
      ['path', { d: 'M7 7l10 10M17 7L7 17' }]
    ]
  };

  LD.svg = function (name, size) {
    var spec = ICONS[name] || ICONS.file;
    var s = document.createElementNS(NS, 'svg');
    s.setAttribute('viewBox', '0 0 24 24');
    s.setAttribute('width', String(size || 20));
    s.setAttribute('height', String(size || 20));
    s.setAttribute('fill', 'none');
    s.setAttribute('stroke', 'currentColor');
    s.setAttribute('stroke-width', '1.8');
    s.setAttribute('stroke-linecap', 'round');
    s.setAttribute('stroke-linejoin', 'round');
    s.setAttribute('aria-hidden', 'true');
    spec.forEach(function (item) {
      var n = document.createElementNS(NS, item[0]);
      var attrs = item[1];
      for (var k in attrs) n.setAttribute(k, String(attrs[k]));
      s.appendChild(n);
    });
    return s;
  };

  /* ------------------------------------------------------------- 格式化 */

  LD.fmtSize = function (n) {
    n = Number(n) || 0;
    if (n < 1024) return Math.round(n) + ' B';
    var units = ['KB', 'MB', 'GB', 'TB', 'PB'];
    var v = n, i = -1;
    do { v /= 1024; i++; } while (v >= 1024 && i < units.length - 1);
    return (v >= 100 ? v.toFixed(0) : v.toFixed(1)) + ' ' + units[i];
  };

  LD.fmtSpeed = function (bps) {
    if (!bps || bps <= 0) return '';
    return LD.fmtSize(bps) + '/s';
  };

  LD.fmtETA = function (sec) {
    if (!isFinite(sec) || sec <= 0) return '';
    sec = Math.round(sec);
    if (sec < 60) return '约 ' + sec + ' 秒';
    var m = Math.floor(sec / 60);
    if (m < 60) return '约 ' + m + ' 分钟';
    var h = Math.floor(m / 60), mm = m % 60;
    return mm ? ('约 ' + h + ' 小时 ' + mm + ' 分') : ('约 ' + h + ' 小时');
  };

  LD.pct = function (sent, size) {
    sent = Number(sent) || 0;
    size = Number(size) || 0;
    if (size <= 0) return sent > 0 ? 100 : 0;
    return Math.max(0, Math.min(100, Math.floor((sent / size) * 100)));
  };

  LD.fmtCode = function (code) {
    if (!code) return '';
    return code.length > 5 ? (code.slice(0, 5) + ' ' + code.slice(5)) : code;
  };

  LD.basename = function (p) {
    var parts = String(p || '').split('/').filter(Boolean);
    return parts.length ? parts[parts.length - 1] : '/';
  };

  /* --------------------------------------------------------------- 平台 */

  LD.platform = function () {
    var ua = navigator.userAgent || '';
    var ios = /iPad|iPhone|iPod/.test(ua) ||
      (navigator.platform === 'MacIntel' && (navigator.maxTouchPoints || 0) > 1);
    var android = /Android/.test(ua);
    return {
      ios: ios,
      android: android,
      mobile: ios || android || /Mobile/.test(ua)
    };
  };

  /* ------------------------------------------------------------- 文件类型 */

  var EXT_KINDS = {
    video: 'mp4 mov m4v avi mkv webm flv wmv mpg mpeg ts 3gp m2ts vob rmvb',
    image: 'jpg jpeg png gif webp heic heif bmp tif tiff svg avif raw cr2 nef arw dng psd',
    archive: 'zip rar 7z tar gz bz2 xz tgz dmg iso pkg apk deb rpm',
    audio: 'mp3 wav flac aac m4a ogg opus wma aiff mid amr',
    doc: 'pdf doc docx xls xlsx ppt pptx txt md rtf pages numbers key epub csv',
    code: 'js mjs cjs ts tsx jsx go py rb rs java c h cpp hpp cc cs php swift kt kts sh bash zsh fish json yml yaml toml xml html htm css scss less sql vue svelte ipynb patch diff'
  };

  LD.KINDS = {
    video: { key: 'video', color: '#AF52DE' },
    image: { key: 'image', color: '#FF9500' },
    archive: { key: 'archive', color: '#30B0C7' },
    audio: { key: 'audio', color: '#FF2D55' },
    doc: { key: 'doc', color: '#FF3B30' },
    code: { key: 'code', color: '#5856D6' },
    folder: { key: 'folder', color: '#007AFF' },
    generic: { key: 'generic', color: '#8E8E93' }
  };

  var KIND_ICONS = {
    video: 'video', image: 'image', archive: 'archive', audio: 'music',
    doc: 'doc', code: 'code', folder: 'folder', generic: 'file'
  };

  /* 按文件名猜类型（文件夹直接传 {dir:true} 对应的 key）。 */
  LD.kindOf = function (name, isDir) {
    if (isDir) return LD.KINDS.folder;
    var ext = '';
    var i = String(name || '').lastIndexOf('.');
    if (i > 0) ext = name.slice(i + 1).toLowerCase();
    if (ext) {
      for (var kind in EXT_KINDS) {
        if (EXT_KINDS[kind].split(' ').indexOf(ext) >= 0) return LD.KINDS[kind];
      }
    }
    return LD.KINDS.generic;
  };

  LD.kindIcon = function (kind) { return KIND_ICONS[kind.key] || 'file'; };

  /* 生成一个带底色的类型图标方块。 */
  LD.tintSquare = function (kind, big) {
    var sq = LD.el('span', { cls: 'tint-sq' + (big ? ' lg' : '') });
    sq.style.setProperty('--tint', kind.color);
    sq.appendChild(LD.svg(LD.kindIcon(kind), big ? 22 : 20));
    return sq;
  };

  /* --------------------------------------------------------------- 提示 */

  LD.toast = function (msg) {
    var box = document.getElementById('toasts');
    if (!box) {
      box = LD.el('div', { cls: 'toasts', id: 'toasts', 'aria-live': 'polite' });
      document.body.appendChild(box);
    }
    var t = LD.el('div', { cls: 'toast', role: 'status', text: msg });
    box.appendChild(t);
    while (box.children.length > 3) box.removeChild(box.firstChild);
    setTimeout(function () {
      t.classList.add('out');
      setTimeout(function () {
        if (t.parentNode) t.parentNode.removeChild(t);
      }, 240);
    }, 2600);
  };

  /* --------------------------------------------------------------- 复制 */

  function fallbackCopy(text) {
    try {
      var ta = document.createElement('textarea');
      ta.value = text;
      ta.setAttribute('readonly', '');
      ta.style.position = 'fixed';
      ta.style.top = '0';
      ta.style.left = '0';
      ta.style.opacity = '0';
      document.body.appendChild(ta);
      ta.focus();
      ta.select();
      var ok = document.execCommand('copy');
      document.body.removeChild(ta);
      return ok;
    } catch (e) {
      return false;
    }
  }

  LD.copyText = function (text) {
    if (navigator.clipboard && window.isSecureContext) {
      return navigator.clipboard.writeText(text).then(function () { return true; })
        .catch(function () { return fallbackCopy(text); });
    }
    return Promise.resolve(fallbackCopy(text));
  };

  /* --------------------------------------------------------------- 杂项 */

  LD.sleep = function (ms) {
    return new Promise(function (r) { setTimeout(r, ms); });
  };

  LD.reducedMotion = function () {
    return !!(window.matchMedia && matchMedia('(prefers-reduced-motion: reduce)').matches);
  };
})();
