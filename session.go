package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	maxWalkFiles    = 20000
	maxSessionFiles = 20000
	maxWalkDepth    = 48
	sessionTTL      = 8 * time.Hour
	receiverTTL     = 30 * time.Second
)

var errSessionActive = errors.New("已有进行中的投送，请先停止")

// FileEntry is one file inside a drop session.
type FileEntry struct {
	ID      int
	AbsPath string
	Name    string // base name, used for Content-Disposition
	RelPath string // display path inside the drop, e.g. "Movies/a.mp4"
	Size    int64
	ModTime time.Time

	sent atomic.Int64 // highest byte offset served (monotonic; handles Range resume)
	done atomic.Bool
}

// Receiver is a phone/browser currently connected to a session.
type Receiver struct {
	IP       string    `json:"ip"`
	UA       string    `json:"ua"`
	Since    int64     `json:"since"`
	LastSeen time.Time `json:"-"`
}

// Session is one active drop: a set of files plus the receivers downloading them.
type Session struct {
	Code    string
	Name    string
	Created time.Time

	mu        sync.Mutex
	files     []*FileEntry
	nextID    int
	stopped   bool
	receivers map[string]*Receiver
	warnings  []string
	tempDirs  []string // staged drag-and-drop batches, deleted on stop
}

// Store keeps the (single) active session plus recently stopped ones.
type Store struct {
	AdminToken string
	BaseURL    string

	mu       sync.Mutex
	sessions map[string]*Session
	active   *Session
}

func NewStore(adminToken string) *Store {
	return &Store{AdminToken: adminToken, sessions: make(map[string]*Session)}
}

func (st *Store) ByCode(code string) *Session {
	st.mu.Lock()
	defer st.mu.Unlock()
	s := st.sessions[code]
	if s == nil || s.isStopped() {
		return nil
	}
	return s
}

func (st *Store) Active() *Session {
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.active != nil && !st.active.isStopped() {
		return st.active
	}
	return nil
}

// Create builds a new session; see CreateReplacing.
func (st *Store) Create(paths []string, name string, replace bool) (*Session, error) {
	s, _, err := st.CreateReplacing(paths, name, replace)
	return s, err
}

// CreateReplacing builds a new session from absolute paths (files and/or
// directories). The directory walk runs before the store lock is taken so
// receiver requests keep flowing, and the previous session is only stopped
// once the replacement is valid. The stopped session, if any, is returned so
// the caller can notify its receivers.
func (st *Store) CreateReplacing(paths []string, name string, replace bool) (*Session, *Session, error) {
	files, warnings, err := collectFiles(paths, nil)
	if err != nil {
		return nil, nil, err
	}
	if len(files) == 0 {
		return nil, nil, errors.New("没有可发送的文件")
	}
	if len(files) > maxSessionFiles {
		return nil, nil, fmt.Errorf("单次投送最多 %d 个文件", maxSessionFiles)
	}
	if name == "" {
		name = defaultSessionName()
	}

	st.mu.Lock()
	defer st.mu.Unlock()
	var stopped *Session
	if st.active != nil && !st.active.isStopped() {
		if !replace {
			return nil, nil, errSessionActive
		}
		stopped = st.active
		stopped.stop()
	}
	s := &Session{
		Code:      randomCode(10),
		Name:      name,
		Created:   time.Now(),
		files:     files,
		receivers: make(map[string]*Receiver),
		warnings:  warnings,
	}
	for i, f := range files {
		f.ID = i + 1
	}
	s.nextID = len(files)
	st.sessions[s.Code] = s
	st.active = s
	for code, old := range st.sessions {
		if code != s.Code && old.isStopped() {
			delete(st.sessions, code)
		}
	}
	return s, stopped, nil
}

func defaultSessionName() string {
	h, err := os.Hostname()
	if err != nil || h == "" {
		h = "电脑"
	}
	if i := strings.IndexByte(h, '.'); i > 0 {
		h = h[:i]
	}
	return "来自 " + h + " 的投送"
}

// collectFiles expands absolute paths (files and directories) into FileEntry
// values. Directory walks skip hidden files, symlinks, and unreadable entries.
func collectFiles(paths []string, existing map[string]bool) ([]*FileEntry, []string, error) {
	seen := existing
	if seen == nil {
		seen = make(map[string]bool)
	}
	var files []*FileEntry
	var warnings []string

	addFile := func(abs, rel string, info fs.FileInfo) {
		rel = filepath.ToSlash(rel)
		if seen[rel] {
			rel = dedupeRel(rel, seen)
		}
		seen[rel] = true
		files = append(files, &FileEntry{
			AbsPath: abs,
			Name:    info.Name(),
			RelPath: rel,
			Size:    info.Size(),
			ModTime: info.ModTime(),
		})
	}

	for _, p := range paths {
		p = filepath.Clean(p)
		if !filepath.IsAbs(p) {
			return nil, nil, fmt.Errorf("路径必须是绝对路径: %s", p)
		}
		info, err := os.Stat(p)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("无法访问 %s", p))
			continue
		}
		if !info.IsDir() {
			addFile(p, info.Name(), info)
			continue
		}
		root := p
		base := filepath.Dir(p)
		_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil // skip unreadable entries
			}
			if len(files) >= maxWalkFiles {
				warnings = append(warnings, fmt.Sprintf("文件数量超过 %d 个，已截断", maxWalkFiles))
				return fs.SkipAll
			}
			name := d.Name()
			if path != root && strings.HasPrefix(name, ".") {
				if d.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			if d.Type()&fs.ModeSymlink != 0 {
				return nil // never follow symlinks
			}
			if d.IsDir() {
				rel, relErr := filepath.Rel(root, path)
				if relErr == nil && strings.Count(filepath.ToSlash(rel), "/") >= maxWalkDepth {
					return filepath.SkipDir
				}
				return nil
			}
			fi, statErr := d.Info()
			if statErr != nil {
				return nil
			}
			rel, relErr := filepath.Rel(base, path)
			if relErr != nil {
				rel = name
			}
			addFile(path, rel, fi)
			return nil
		})
	}
	sort.SliceStable(files, func(i, j int) bool { return naturalLess(files[i].RelPath, files[j].RelPath) })
	return files, warnings, nil
}

func dedupeRel(rel string, seen map[string]bool) string {
	ext := filepath.Ext(rel)
	stem := strings.TrimSuffix(rel, ext)
	for n := 2; ; n++ {
		cand := fmt.Sprintf("%s (%d)%s", stem, n, ext)
		if !seen[cand] {
			return cand
		}
	}
}

func (s *Session) isStopped() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stopped
}

func (s *Session) stop() {
	s.mu.Lock()
	s.stopped = true
	dirs := s.tempDirs
	s.tempDirs = nil
	s.mu.Unlock()
	// Staged drag-and-drop copies die with the session. Open downloads keep
	// their file descriptors, so an in-flight transfer still completes.
	for _, d := range dirs {
		_ = os.RemoveAll(d)
	}
}

// noteTempDir remembers a staged batch directory to delete when stopping.
func (s *Session) noteTempDir(dir string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, d := range s.tempDirs {
		if d == dir {
			return
		}
	}
	s.tempDirs = append(s.tempDirs, dir)
}

func (s *Session) usesTempDir(dir string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, d := range s.tempDirs {
		if d == dir {
			return true
		}
	}
	return false
}

func (s *Session) fileByID(id int) *FileEntry {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, f := range s.files {
		if f.ID == id {
			return f
		}
	}
	return nil
}

func (s *Session) filesSnapshot() []*FileEntry {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*FileEntry, len(s.files))
	copy(out, s.files)
	return out
}

func (s *Session) totalSize() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	var t int64
	for _, f := range s.files {
		t += f.Size
	}
	return t
}

// addFiles appends more files to the session, deduplicating rel paths. The
// directory walk runs outside the session lock.
func (s *Session) addFiles(paths []string) (int, error) {
	s.mu.Lock()
	existing := make(map[string]bool, len(s.files))
	for _, f := range s.files {
		existing[f.RelPath] = true
	}
	s.mu.Unlock()

	added, warnings, err := collectFiles(paths, existing)
	if err != nil {
		return 0, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.files)+len(added) > maxSessionFiles {
		return 0, fmt.Errorf("单次投送最多 %d 个文件", maxSessionFiles)
	}
	s.warnings = append(s.warnings, warnings...)
	for _, f := range added {
		s.nextID++
		f.ID = s.nextID
		s.files = append(s.files, f)
	}
	sort.SliceStable(s.files, func(i, j int) bool { return naturalLess(s.files[i].RelPath, s.files[j].RelPath) })
	return len(added), nil
}

func (s *Session) removeFiles(ids []int) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	rm := make(map[int]bool, len(ids))
	for _, id := range ids {
		rm[id] = true
	}
	kept := s.files[:0]
	n := 0
	for _, f := range s.files {
		if rm[f.ID] {
			n++
			continue
		}
		kept = append(kept, f)
	}
	s.files = kept
	return n
}

// touchReceiver records a heartbeat; returns (count, isNew).
func (s *Session) touchReceiver(ip, ua string) (int, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	r, ok := s.receivers[ip]
	if !ok {
		r = &Receiver{IP: ip, UA: ua, Since: now.Unix()}
		s.receivers[ip] = r
	}
	r.LastSeen = now
	if ua != "" {
		r.UA = ua
	}
	return len(s.receivers), !ok
}

// gcReceivers drops receivers not seen within receiverTTL.
func (s *Session) gcReceivers() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	cut := time.Now().Add(-receiverTTL)
	for ip, r := range s.receivers {
		if r.LastSeen.Before(cut) {
			delete(s.receivers, ip)
		}
	}
	return len(s.receivers)
}

func (s *Session) receiverCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.receivers)
}

// ---- JSON shapes shared with the frontend ----

type fileJSON struct {
	ID      int    `json:"id"`
	Name    string `json:"name"`
	RelPath string `json:"relPath"`
	Size    int64  `json:"size"`
	Sent    int64  `json:"sent"`
	Done    bool   `json:"done"`
	MTime   int64  `json:"mtime"`
}

type receiverJSON struct {
	IP    string `json:"ip"`
	UA    string `json:"ua"`
	Since int64  `json:"since"`
}

type sessionJSON struct {
	Active    bool           `json:"active"`
	Code      string         `json:"code,omitempty"`
	Name      string         `json:"name,omitempty"`
	CreatedAt int64          `json:"createdAt,omitempty"`
	Files     []fileJSON     `json:"files"`
	TotalSize int64          `json:"totalSize"`
	URL       string         `json:"url,omitempty"`
	Stopped   bool           `json:"stopped"`
	Receivers []receiverJSON `json:"receivers,omitempty"`
	Warnings  []string       `json:"warnings,omitempty"`
}

// snapshot renders the session for JSON; admin adds private fields. Files is
// always a JSON array (never omitted) so clients can rely on it.
func (s *Session) snapshot(admin bool) sessionJSON {
	s.mu.Lock()
	files := make([]*FileEntry, len(s.files))
	copy(files, s.files)
	stopped := s.stopped
	var receivers []receiverJSON
	if admin {
		receivers = make([]receiverJSON, 0, len(s.receivers))
		for _, r := range s.receivers {
			receivers = append(receivers, receiverJSON{IP: r.IP, UA: r.UA, Since: r.Since})
		}
		sort.Slice(receivers, func(i, j int) bool { return receivers[i].Since < receivers[j].Since })
	}
	warnings := append([]string(nil), s.warnings...)
	s.mu.Unlock()

	out := sessionJSON{
		Active:    !stopped,
		Code:      s.Code,
		Name:      s.Name,
		CreatedAt: s.Created.Unix(),
		Stopped:   stopped,
		Receivers: receivers,
		Warnings:  warnings,
		Files:     make([]fileJSON, 0, len(files)),
	}
	for _, f := range files {
		out.Files = append(out.Files, fileJSON{
			ID:      f.ID,
			Name:    f.Name,
			RelPath: f.RelPath,
			Size:    f.Size,
			Sent:    min(f.sent.Load(), f.Size),
			Done:    f.done.Load(),
			MTime:   f.ModTime.Unix(),
		})
		out.TotalSize += f.Size
	}
	return out
}
