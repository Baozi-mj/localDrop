package main

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
)

// trackableRange reports whether a request should count as download progress.
// Full requests and open-ended resume requests ("bytes=N-") count; media-style
// probes (closed or suffix ranges) must not mark files as sent.
func trackableRange(r *http.Request) bool {
	h := r.Header.Get("Range")
	if h == "" {
		return true
	}
	spec, ok := strings.CutPrefix(h, "bytes=")
	if !ok || strings.Contains(spec, ",") {
		return false
	}
	first, last, ok := strings.Cut(spec, "-")
	if !ok {
		return false
	}
	first, last = strings.TrimSpace(first), strings.TrimSpace(last)
	if first == "" {
		return false // suffix range ("bytes=-N")
	}
	if _, err := strconv.ParseInt(first, 10, 64); err != nil {
		return false
	}
	return last == ""
}

// contentRangeStart parses "bytes 100-199/1048576" and returns 100.
func contentRangeStart(h string) (int64, bool) {
	spec, ok := strings.CutPrefix(h, "bytes ")
	if !ok {
		return 0, false
	}
	rng, _, ok := strings.Cut(spec, "/")
	if !ok {
		return 0, false
	}
	first, _, ok := strings.Cut(rng, "-")
	if !ok {
		return 0, false
	}
	n, err := strconv.ParseInt(strings.TrimSpace(first), 10, 64)
	if err != nil || n < 0 {
		return 0, false
	}
	return n, true
}

// progressWriter counts bytes written to the response and advances the file's
// sent offset monotonically, so Range resumes continue progress.
//
// It implements io.ReaderFrom with a chunked copy so progress advances while
// the transfer is running (see ReadFrom for why sendfile is deliberately not
// used). Unwrap keeps http.NewResponseController working through the wrapper.
//
// The absolute offset of a response comes from the Content-Range header that
// ServeContent sets, not from the request: an If-Range mismatch silently
// downgrades a 206 to a full 200 and the request header would lie.
type progressWriter struct {
	w      http.ResponseWriter
	f      *FileEntry
	track  bool
	start  int64
	n      int64
	status int
}

func (p *progressWriter) Header() http.Header {
	return p.w.Header()
}

func (p *progressWriter) WriteHeader(code int) {
	p.status = code
	if code == http.StatusPartialContent {
		if start, ok := contentRangeStart(p.w.Header().Get("Content-Range")); ok {
			p.start = start
		}
	}
	p.w.WriteHeader(code)
}

func (p *progressWriter) Write(b []byte) (int, error) {
	n, err := p.w.Write(b)
	p.advance(int64(n))
	return n, err
}

// ReadFrom copies in 256 KiB chunks and advances progress after each chunk.
//
// Delegating to the underlying ResponseWriter's ReadFrom would enable
// sendfile, but that call only returns once the whole body has been written,
// so progress would sit at 0% and jump to 100% at the end of a multi-GB
// transfer. Live progress on both ends is a core feature, so the copy stays
// in user space (still far above Wi-Fi speeds).
func (p *progressWriter) ReadFrom(r io.Reader) (int64, error) {
	buf := make([]byte, 256<<10)
	var total int64
	for {
		nr, er := r.Read(buf)
		if nr > 0 {
			nw, ew := p.w.Write(buf[:nr])
			if nw > 0 {
				total += int64(nw)
				p.advance(int64(nw))
			}
			if ew != nil {
				return total, ew
			}
			if nw != nr {
				return total, io.ErrShortWrite
			}
		}
		if er == io.EOF {
			return total, nil
		}
		if er != nil {
			return total, er
		}
	}
}

// Unwrap lets http.NewResponseController reach the real connection.
func (p *progressWriter) Unwrap() http.ResponseWriter {
	return p.w
}

func (p *progressWriter) advance(n int64) {
	p.n += n
	if !p.track || n <= 0 || p.status == http.StatusRequestedRangeNotSatisfiable {
		return
	}
	end := p.start + p.n
	for {
		cur := p.f.sent.Load()
		if end <= cur {
			break
		}
		if p.f.sent.CompareAndSwap(cur, end) {
			break
		}
	}
}

// handleDownload serves a single file with Range/ETag support so browser
// download managers can resume interrupted transfers.
func (s *server) handleDownload(w http.ResponseWriter, r *http.Request) {
	sess := s.store.ByCode(r.PathValue("code"))
	if sess == nil {
		s.rejectUnknownCode(w, r)
		return
	}
	id, err := strconv.Atoi(r.PathValue("id"))
	if err != nil {
		writeErr(w, http.StatusNotFound, "文件不存在")
		return
	}
	f := sess.fileByID(id)
	if f == nil {
		writeErr(w, http.StatusNotFound, "文件不存在")
		return
	}
	fh, err := os.Open(f.AbsPath)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "无法读取文件")
		return
	}
	defer fh.Close()

	// Validate against the file as it is NOW. A validator frozen at session
	// time would keep matching If-Range after the file changed on disk and
	// let a resume splice bytes from a different file.
	st, err := fh.Stat()
	if err != nil || st.IsDir() {
		writeErr(w, http.StatusInternalServerError, "无法读取文件")
		return
	}
	size := st.Size()
	modTime := st.ModTime()

	track := trackableRange(r)
	sess.touchReceiver(clientIP(r), r.UserAgent())

	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", contentDisposition(f.Name))
	w.Header().Set("ETag", fmt.Sprintf(`"%d-%d-%d"`, f.ID, size, modTime.UnixNano()))
	w.Header().Set("Accept-Ranges", "bytes")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")

	pw := &progressWriter{w: w, f: f, track: track}
	http.ServeContent(pw, r, f.Name, modTime, fh)

	served := pw.status == http.StatusOK || pw.status == http.StatusPartialContent
	if r.Method == http.MethodGet && track && served &&
		(pw.n > 0 || size == 0) && pw.start+pw.n >= size {
		f.done.Store(true)
	}
}

// rejectUnknownCode rate-limits bad session codes and returns 404.
func (s *server) rejectUnknownCode(w http.ResponseWriter, r *http.Request) {
	if !s.limiter.allow(clientIP(r)) {
		writeErr(w, http.StatusTooManyRequests, "尝试过于频繁，请稍后再试")
		return
	}
	writeErr(w, http.StatusNotFound, "投送不存在或已结束")
}
