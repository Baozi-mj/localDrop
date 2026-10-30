package main

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io/fs"
	"log"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

type server struct {
	store   *Store
	broker  *broker
	limiter *rateLimiter
	mux     *http.ServeMux
	assets  fs.FS
	uploads *uploads

	// progress ticker state (only touched by the ticker goroutine)
	lastProgress map[*FileEntry]progressSample
	lastCode     string
}

type progressSample struct {
	sent int64
	done bool
	at   time.Time
}

func newServer(store *Store, br *broker) *server {
	s := &server{
		store:        store,
		broker:       br,
		limiter:      newRateLimiter(),
		assets:       assetsFS(),
		uploads:      &uploads{},
		lastProgress: make(map[*FileEntry]progressSample),
	}
	s.buildMux()
	return s
}

func (s *server) buildMux() {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", s.handleIndexPage)
	mux.HandleFunc("GET /r/{code}", s.handleReceivePage)
	mux.Handle("GET /static/", noStore(http.StripPrefix("/static/", http.FileServerFS(s.assets))))
	mux.HandleFunc("GET /favicon.ico", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/static/favicon.svg", http.StatusFound)
	})
	mux.HandleFunc("GET /d/{code}/{id}", s.handleDownload)
	mux.HandleFunc("GET /api/qr.svg", s.admin(s.handleQRSVG))
	mux.HandleFunc("GET /api/qr.png", s.admin(s.handleQRPNG))
	mux.HandleFunc("GET /api/fs", s.admin(s.handleBrowse))
	mux.HandleFunc("POST /api/session", s.admin(s.handleCreateSession))
	mux.HandleFunc("GET /api/session", s.admin(s.handleGetSession))
	mux.HandleFunc("POST /api/session/add", s.admin(s.handleAddFiles))
	mux.HandleFunc("POST /api/session/remove", s.admin(s.handleRemoveFiles))
	mux.HandleFunc("POST /api/session/stop", s.admin(s.handleStop))
	mux.HandleFunc("POST /api/pick", s.admin(s.handlePick))
	mux.HandleFunc("POST /api/upload", s.admin(s.handleUpload))
	mux.HandleFunc("POST /api/upload/discard", s.admin(s.handleUploadDiscard))
	mux.HandleFunc("GET /api/events", s.admin(s.handleAdminEvents))
	mux.HandleFunc("GET /api/r/{code}", s.handleRecvMeta)
	mux.HandleFunc("GET /api/r/{code}/events", s.handleRecvEvents)
	mux.HandleFunc("POST /api/r/{code}/hb", s.handleRecvHB)
	s.mux = mux
}

func (s *server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Security-Policy",
		"default-src 'self'; img-src 'self' data:; media-src 'self' data:; style-src 'self'; script-src 'self'; connect-src 'self'; object-src 'none'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	s.mux.ServeHTTP(w, r)
}

func noStore(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}

// admin gates the management API. Requests from a browser on another site
// are refused outright (CSRF, DNS rebinding). Then loopback peers pass only
// when they also addressed a loopback host name (a rebound DNS name does
// not); everyone else needs the admin token (header, or ?t= for EventSource
// and <img>, which cannot set headers).
func (s *server) admin(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !sameSiteRequest(r) {
			writeErr(w, http.StatusForbidden, "拒绝来自其他站点的请求")
			return
		}
		if (isLoopbackRequest(r) && isLoopbackHost(r.Host)) || s.checkAdminToken(r) {
			next(w, r)
			return
		}
		writeErr(w, http.StatusUnauthorized, "需要管理员权限：请在运行服务的电脑上打开管理界面")
	}
}

// sameSiteRequest rejects browser requests that originate from another site.
// Browsers label such requests with Sec-Fetch-Site and Origin; non-browser
// clients (curl, scripts) send neither and pass.
func sameSiteRequest(r *http.Request) bool {
	if sfs := r.Header.Get("Sec-Fetch-Site"); sfs != "" && sfs != "same-origin" && sfs != "none" {
		return false
	}
	if origin := r.Header.Get("Origin"); origin != "" {
		u, err := url.Parse(origin)
		if err != nil || !strings.EqualFold(u.Host, r.Host) {
			return false
		}
	}
	return true
}

// requireJSON refuses state-changing requests that are not JSON, so a plain
// cross-site HTML form can never drive the admin API.
func requireJSON(w http.ResponseWriter, r *http.Request) bool {
	ct, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if ct != "application/json" {
		writeErr(w, http.StatusUnsupportedMediaType, "请求需要 Content-Type: application/json")
		return false
	}
	return true
}

func (s *server) checkAdminToken(r *http.Request) bool {
	t := r.Header.Get("X-Admin-Token")
	if t == "" {
		t = r.URL.Query().Get("t")
	}
	if t == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(t), []byte(s.store.AdminToken)) == 1
}

func (s *server) sessionJSONFor(sess *Session, admin bool) sessionJSON {
	out := sess.snapshot(admin)
	if admin {
		out.URL = s.store.BaseURL + "/r/" + sess.Code
	}
	return out
}

// ---- session management (admin) ----

func (s *server) handleCreateSession(w http.ResponseWriter, r *http.Request) {
	if !requireJSON(w, r) {
		return
	}
	var req struct {
		Paths   []string `json:"paths"`
		Name    string   `json:"name"`
		Replace bool     `json:"replace"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "请求格式错误")
		return
	}
	sess, old, err := s.store.CreateReplacing(req.Paths, req.Name, req.Replace)
	if err != nil {
		if errors.Is(err, errSessionActive) {
			writeErr(w, http.StatusConflict, err.Error())
			return
		}
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	s.noteTemp(sess, req.Paths)
	if old != nil {
		// Tell the old session's receivers it is over before announcing the new one.
		s.publishState(old)
		log.Printf("投送已被替换 code=%s", old.Code)
	}
	log.Printf("投送已创建 code=%s 文件=%d 个 总计=%s", sess.Code, len(sess.filesSnapshot()), humanBytes(sess.totalSize()))
	s.publishState(sess)
	writeJSON(w, http.StatusOK, s.sessionJSONFor(sess, true))
}

func (s *server) handleGetSession(w http.ResponseWriter, r *http.Request) {
	sess := s.store.Active()
	if sess == nil {
		writeJSON(w, http.StatusOK, sessionJSON{Active: false, Files: []fileJSON{}})
		return
	}
	writeJSON(w, http.StatusOK, s.sessionJSONFor(sess, true))
}

func (s *server) handleAddFiles(w http.ResponseWriter, r *http.Request) {
	if !requireJSON(w, r) {
		return
	}
	var req struct {
		Paths []string `json:"paths"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "请求格式错误")
		return
	}
	sess := s.store.Active()
	if sess == nil {
		writeErr(w, http.StatusConflict, "没有进行中的投送")
		return
	}
	if _, err := sess.addFiles(req.Paths); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	s.noteTemp(sess, req.Paths)
	s.publishState(sess)
	writeJSON(w, http.StatusOK, s.sessionJSONFor(sess, true))
}

func (s *server) handleRemoveFiles(w http.ResponseWriter, r *http.Request) {
	if !requireJSON(w, r) {
		return
	}
	var req struct {
		IDs []int `json:"ids"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "请求格式错误")
		return
	}
	sess := s.store.Active()
	if sess == nil {
		writeErr(w, http.StatusConflict, "没有进行中的投送")
		return
	}
	sess.removeFiles(req.IDs)
	s.publishState(sess)
	writeJSON(w, http.StatusOK, s.sessionJSONFor(sess, true))
}

func (s *server) handleStop(w http.ResponseWriter, r *http.Request) {
	if sess := s.store.Active(); sess != nil {
		sess.stop()
		s.publishState(sess)
		log.Printf("投送已停止 code=%s", sess.Code)
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// ---- QR ----

func (s *server) handleQRSVG(w http.ResponseWriter, r *http.Request) {
	text := r.URL.Query().Get("text")
	if text == "" || len(text) > 512 {
		writeErr(w, http.StatusBadRequest, "缺少 text 参数")
		return
	}
	svg, err := qrSVG(text)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "生成二维码失败")
		return
	}
	w.Header().Set("Content-Type", "image/svg+xml; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write([]byte(svg))
}

func (s *server) handleQRPNG(w http.ResponseWriter, r *http.Request) {
	text := r.URL.Query().Get("text")
	if text == "" || len(text) > 512 {
		writeErr(w, http.StatusBadRequest, "缺少 text 参数")
		return
	}
	png, err := qrPNG(text, 768)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "生成二维码失败")
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(png)
}

// ---- receiver API ----

func (s *server) handleRecvMeta(w http.ResponseWriter, r *http.Request) {
	sess := s.store.ByCode(r.PathValue("code"))
	if sess == nil {
		s.rejectUnknownCode(w, r)
		return
	}
	writeJSON(w, http.StatusOK, s.sessionJSONFor(sess, false))
}

func (s *server) handleRecvHB(w http.ResponseWriter, r *http.Request) {
	sess := s.store.ByCode(r.PathValue("code"))
	if sess == nil {
		s.rejectUnknownCode(w, r)
		return
	}
	count, isNew := sess.touchReceiver(clientIP(r), r.UserAgent())
	if isNew {
		log.Printf("设备已连接: %s", clientIP(r))
		s.publishReceivers(sess, count)
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "count": count})
}

// ---- SSE ----

func (s *server) handleAdminEvents(w http.ResponseWriter, r *http.Request) {
	s.streamSSE(w, r, "", func() []byte {
		if sess := s.store.Active(); sess != nil {
			return evt("state", map[string]any{"session": s.sessionJSONFor(sess, true)})
		}
		return evt("state", map[string]any{"session": sessionJSON{Active: false, Files: []fileJSON{}}})
	})
}

func (s *server) handleRecvEvents(w http.ResponseWriter, r *http.Request) {
	code := r.PathValue("code")
	sess := s.store.ByCode(code)
	if sess == nil {
		s.rejectUnknownCode(w, r)
		return
	}
	sess.touchReceiver(clientIP(r), r.UserAgent())
	s.streamSSE(w, r, code, func() []byte {
		return evt("state", map[string]any{"session": s.sessionJSONFor(sess, false)})
	})
}

// streamSSE subscribes first and only then snapshots the initial state, so no
// publish can fall into the gap between the two.
func (s *server) streamSSE(w http.ResponseWriter, r *http.Request, key string, initial func() []byte) {
	rc := http.NewResponseController(w)
	h := w.Header()
	h.Set("Content-Type", "text/event-stream; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	h.Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)

	writeSSE := func(payload []byte) bool {
		buf := make([]byte, 0, len(payload)+8)
		buf = append(buf, "data: "...)
		buf = append(buf, payload...)
		buf = append(buf, '\n', '\n')
		if _, err := w.Write(buf); err != nil {
			return false
		}
		return rc.Flush() == nil
	}

	id, ch := s.broker.subscribe(key)
	defer s.broker.unsubscribe(id)
	if !writeSSE(initial()) {
		return
	}
	for {
		select {
		case <-r.Context().Done():
			return
		case msg, ok := <-ch:
			if !ok {
				return
			}
			if !writeSSE(msg) {
				return
			}
		}
	}
}

// ---- event publishing ----

func (s *server) publishState(sess *Session) {
	s.broker.publishAdmin(evt("state", map[string]any{"session": s.sessionJSONFor(sess, true)}))
	s.broker.publish(sess.Code, evt("state", map[string]any{"session": s.sessionJSONFor(sess, false)}))
}

func (s *server) publishReceivers(sess *Session, count int) {
	s.broker.publishAdmin(evt("receivers", map[string]any{
		"count":     count,
		"receivers": sess.snapshot(true).Receivers,
	}))
}

// publishProgress pushes a delta: only files whose progress changed since the
// last tick, plus session-wide totals. Idle sessions publish nothing, so a
// 20k-file share does not generate megabytes of JSON per second.
func (s *server) publishProgress() {
	sess := s.store.Active()
	if sess == nil {
		return
	}
	if sess.Code != s.lastCode {
		s.lastProgress = make(map[*FileEntry]progressSample)
		s.lastCode = sess.Code
	}
	if s.broker.count(sess.Code) == 0 && s.broker.count("") == 0 {
		return
	}
	now := time.Now()
	files := sess.filesSnapshot()
	changed := make([]map[string]any, 0, 8)
	var totalSent, totalSize, totalSpeed int64
	for _, f := range files {
		sent := min(f.sent.Load(), f.Size)
		done := f.done.Load()
		totalSize += f.Size
		totalSent += sent
		prev, ok := s.lastProgress[f]
		var speed int64
		if ok {
			dt := now.Sub(prev.at).Seconds()
			if dt > 0 && sent > prev.sent {
				speed = int64(float64(sent-prev.sent) / dt)
			}
		}
		totalSpeed += speed
		if !ok || sent != prev.sent || done != prev.done {
			changed = append(changed, map[string]any{
				"id":    f.ID,
				"sent":  sent,
				"size":  f.Size,
				"done":  done,
				"speed": speed,
			})
		}
		s.lastProgress[f] = progressSample{sent: sent, done: done, at: now}
	}
	if len(changed) == 0 {
		return
	}
	payload := evt("progress", map[string]any{
		"files": changed,
		"sent":  totalSent,
		"total": totalSize,
		"speed": totalSpeed,
	})
	s.broker.publish(sess.Code, payload)
	s.broker.publishAdmin(payload)
}

// ---- tickers ----

func (s *server) runTickers(ctx context.Context) {
	progress := time.NewTicker(500 * time.Millisecond)
	defer progress.Stop()
	ping := time.NewTicker(15 * time.Second)
	defer ping.Stop()
	gc := time.NewTicker(5 * time.Second)
	defer gc.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-progress.C:
			s.publishProgress()
		case <-ping.C:
			s.broker.broadcast(evt("ping", nil))
		case <-gc.C:
			s.gc()
		}
	}
}

func (s *server) gc() {
	sess := s.store.Active()
	if sess == nil {
		if len(s.lastProgress) > 0 {
			s.lastProgress = make(map[*FileEntry]progressSample)
			s.lastCode = ""
		}
		return
	}
	if time.Since(sess.Created) > sessionTTL {
		sess.stop()
		s.publishState(sess)
		log.Printf("投送超时已自动结束 code=%s", sess.Code)
		return
	}
	before := sess.receiverCount()
	after := sess.gcReceivers()
	if after != before {
		s.publishReceivers(sess, after)
	}
}

// ---- rate limiter ----

type rateLimiter struct {
	mu      sync.Mutex
	buckets map[string]*tokenBucket
}

type tokenBucket struct {
	tokens float64
	last   time.Time
}

func newRateLimiter() *rateLimiter {
	return &rateLimiter{buckets: make(map[string]*tokenBucket)}
}

// allow implements a token bucket (rate tokens/sec, burst capacity).
func (rl *rateLimiter) allow(key string) bool {
	const rate = 2.0
	const burst = 20.0
	rl.mu.Lock()
	defer rl.mu.Unlock()
	now := time.Now()
	b, ok := rl.buckets[key]
	if !ok {
		if len(rl.buckets) > 4096 {
			rl.buckets = make(map[string]*tokenBucket)
		}
		b = &tokenBucket{tokens: burst, last: now}
		rl.buckets[key] = b
	}
	b.tokens = min(b.tokens+(now.Sub(b.last).Seconds()*rate), burst)
	b.last = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}
