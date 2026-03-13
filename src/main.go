package main

import (
	"encoding/json"
	"fmt"
	"html"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ---------------------------------------------------------------------------
// Configuration
// ---------------------------------------------------------------------------

type Config struct {
	LogDir  string
	Port    int
	MaxDays int
}

func loadConfig() Config {
	cfg := Config{
		LogDir:  `C:\Program Files\Marquis\Logs`,
		Port:    8023,
		MaxDays: 7,
	}

	// Read INI file next to executable
	exePath, _ := os.Executable()
	iniPath := filepath.Join(filepath.Dir(exePath), "pylogjobs.ini")

	// Also try current working directory
	if _, err := os.Stat(iniPath); os.IsNotExist(err) {
		iniPath = "pylogjobs.ini"
	}

	data, err := os.ReadFile(iniPath)
	if err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "log_dir") {
				parts := strings.SplitN(line, "=", 2)
				if len(parts) == 2 {
					cfg.LogDir = strings.TrimSpace(parts[1])
				}
			}
			if strings.HasPrefix(line, "port") {
				parts := strings.SplitN(line, "=", 2)
				if len(parts) == 2 {
					p, err := strconv.Atoi(strings.TrimSpace(parts[1]))
					if err == nil {
						cfg.Port = p
					}
				}
			}
			if strings.HasPrefix(line, "max_days") {
				parts := strings.SplitN(line, "=", 2)
				if len(parts) == 2 {
					d, err := strconv.Atoi(strings.TrimSpace(parts[1]))
					if err == nil && d > 0 {
						cfg.MaxDays = d
					}
				}
			}
		}
		log.Printf("Loaded config from %s", iniPath)
	}

	// Command line overrides
	for i, arg := range os.Args[1:] {
		switch arg {
		case "--port":
			if i+1 < len(os.Args)-1 {
				p, err := strconv.Atoi(os.Args[i+2])
				if err == nil {
					cfg.Port = p
				}
			}
		case "--log-dir":
			if i+1 < len(os.Args)-1 {
				cfg.LogDir = os.Args[i+2]
			}
		case "--max-days":
			if i+1 < len(os.Args)-1 {
				d, err := strconv.Atoi(os.Args[i+2])
				if err == nil && d > 0 {
					cfg.MaxDays = d
				}
			}
		}
	}

	return cfg
}

// ---------------------------------------------------------------------------
// Data Model
// ---------------------------------------------------------------------------

type Job struct {
	ID        int    `json:"id"`
	ClipName  string `json:"clip_name"`
	Source    string `json:"source"`
	Engine    string `json:"engine"`
	StartTime string `json:"start_time"`
	EndTime   string `json:"end_time"`
	Status    string `json:"status"`
	Level     string `json:"level"`
	MovieID   string `json:"movie_id"`
	ThreadID  int    `json:"thread_id"`
	FileRef   string `json:"file_ref"`
}

type DurationEntry struct {
	ClipName  string `json:"clip_name"`
	Timestamp string `json:"timestamp"`
	Frames    int    `json:"frames"`
}

type Store struct {
	mu           sync.RWMutex
	jobs         []Job
	durations    []DurationEntry
	fileOffsets  map[string]int64 // filename -> parsed bytes
	nextID       int
}

func NewStore() *Store {
	return &Store{
		fileOffsets: make(map[string]int64),
		nextID:      1,
	}
}

func (s *Store) AddJob(j Job) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	j.ID = s.nextID
	s.nextID++
	s.jobs = append(s.jobs, j)
	return j.ID
}

func (s *Store) CompleteJob(threadID int, endTime, status, level, movieID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	// Find most recent transferring job on this thread (search backwards)
	for i := len(s.jobs) - 1; i >= 0; i-- {
		if s.jobs[i].ThreadID == threadID && s.jobs[i].Status == "Transferring" {
			s.jobs[i].EndTime = endTime
			s.jobs[i].Status = status
			s.jobs[i].Level = level
			s.jobs[i].MovieID = movieID
			return
		}
	}
}

func (s *Store) AddDuration(d DurationEntry) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.durations = append(s.durations, d)
}

func (s *Store) GetJobs() []Job {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make([]Job, len(s.jobs))
	copy(result, s.jobs)
	return result
}

func (s *Store) GetActiveJobs() []Job {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var result []Job
	for _, j := range s.jobs {
		if j.Status == "Transferring" {
			result = append(result, j)
		}
	}
	return result
}

func (s *Store) GetDurationsForClip(clip string) []DurationEntry {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var result []DurationEntry
	for _, d := range s.durations {
		if d.ClipName == clip {
			result = append(result, d)
		}
	}
	return result
}

func (s *Store) GetMaxFrames(clip string) int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	maxF := 0
	for _, d := range s.durations {
		if d.ClipName == clip && d.Frames > maxF {
			maxF = d.Frames
		}
	}
	return maxF
}

func (s *Store) GetLatestFrames(clip string) (int, string) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	latestTime := ""
	latestFrames := 0
	for _, d := range s.durations {
		if d.ClipName == clip && d.Timestamp > latestTime {
			latestTime = d.Timestamp
			latestFrames = d.Frames
		}
	}
	return latestFrames, latestTime
}

// CleanupStale marks old Transferring jobs as stale
func (s *Store) CleanupStale(maxAge time.Duration) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	cutoff := time.Now().Add(-maxAge).Format("2006-01-02 15:04:05")
	count := 0
	for i := range s.jobs {
		if s.jobs[i].Status == "Transferring" && s.jobs[i].StartTime < cutoff {
			s.jobs[i].Status = "Unknown; No status log messages found. Possibly ongoing"
			s.jobs[i].Level = "info"
			s.jobs[i].EndTime = "Unknown"
			count++
		}
	}
	return count
}

// ---------------------------------------------------------------------------
// Log Parser
// ---------------------------------------------------------------------------

var (
	lineRE = regexp.MustCompile(
		`[* ]*(\w+)\s*:\s*(\w+)\s+(\d+),\s*(\d{2}:\d{2}:\d{2})\.\s*([\w ]+?)\s*\[(\d+):(\d+)\]\s*:\s*(.*)`)

	startRE = regexp.MustCompile(
		`Transfer of movie "([^"]+)"\s*\(file="([^"]+)"\)\s*initialised on transfer engine "([^"]+)"`)

	successRE = regexp.MustCompile(
		`Movie transfer succeeded for movie id (\d+)`)

	failRE = regexp.MustCompile(
		`Movie transfer failed for movie id (\d+);\s*Error\s*=\s*\S+\s*\("([^"]+)"\)`)

	durationRE = regexp.MustCompile(
		`Movie duration Change for movie (\S+)\s*\((\d+)\)`)

	filenameDateRE = regexp.MustCompile(
		`(\d{2})-(\d{2})-(\d{4})`)
)

var monthMap = map[string]int{
	"Jan": 1, "Feb": 2, "Mar": 3, "Apr": 4, "May": 5, "Jun": 6,
	"Jul": 7, "Aug": 8, "Sep": 9, "Oct": 10, "Nov": 11, "Dec": 12,
}

func detectSource(fileRef string) string {
	if fileRef == "" {
		return "Unknown"
	}
	if strings.HasSuffix(strings.ToLower(fileRef), ".xml") {
		return "EDL/FCP"
	}
	if matched, _ := regexp.MatchString(`^[0-9a-fA-F]{10,}`, fileRef); matched {
		return "Interplay"
	}
	// Direct Nexio transfers: file_ref is clip name (often with + prefix), no extension
	if !strings.Contains(fileRef, ".") {
		return "Nexio"
	}
	return "Unknown"
}

func yearFromFilename(name string) int {
	m := filenameDateRE.FindStringSubmatch(name)
	if m != nil {
		y, _ := strconv.Atoi(m[3])
		return y
	}
	return time.Now().Year()
}

func buildDatetime(monthStr string, dayStr string, timeStr string, year int) string {
	month := monthMap[monthStr]
	if month == 0 {
		month = 1
	}
	day, _ := strconv.Atoi(dayStr)
	return fmt.Sprintf("%04d-%02d-%02d %s", year, month, day, timeStr)
}

func dateFromFilename(name string) time.Time {
	m := filenameDateRE.FindStringSubmatch(name)
	if m != nil {
		day, _ := strconv.Atoi(m[1])
		month, _ := strconv.Atoi(m[2])
		year, _ := strconv.Atoi(m[3])
		return time.Date(year, time.Month(month), day, 0, 0, 0, 0, time.Local)
	}
	return time.Time{}
}

func parseLogFiles(store *Store, logDir string, maxDays int) {
	pattern := filepath.Join(logDir, "Marquis *.log")
	files, err := filepath.Glob(pattern)
	if err != nil {
		log.Printf("Glob error: %v", err)
		return
	}

	// Sort by date descending, keep only newest maxDays
	sort.Slice(files, func(i, k int) bool {
		di := dateFromFilename(filepath.Base(files[i]))
		dk := dateFromFilename(filepath.Base(files[k]))
		return di.After(dk)
	})
	if len(files) > maxDays {
		files = files[:maxDays]
	}

	for _, fpath := range files {
		filename := filepath.Base(fpath)

		info, err := os.Stat(fpath)
		if err != nil {
			continue
		}
		fileSize := info.Size()

		store.mu.RLock()
		offset := store.fileOffsets[filename]
		store.mu.RUnlock()

		if fileSize <= offset {
			continue
		}

		log.Printf("Parsing %s from byte %d (%d new bytes)", filename, offset, fileSize-offset)

		year := yearFromFilename(filename)

		f, err := os.Open(fpath)
		if err != nil {
			log.Printf("Error opening %s: %v", filename, err)
			continue
		}

		if offset > 0 {
			f.Seek(offset, io.SeekStart)
		}

		data, err := io.ReadAll(f)
		f.Close()
		if err != nil {
			log.Printf("Error reading %s: %v", filename, err)
			continue
		}

		content := string(data)
		parseContent(store, content, year)

		store.mu.Lock()
		store.fileOffsets[filename] = fileSize
		store.mu.Unlock()
	}
}

func parseContent(store *Store, content string, year int) {
	for _, rawLine := range strings.Split(content, "\n") {
		line := strings.TrimSpace(rawLine)
		line = strings.TrimRight(line, "\r")
		if line == "" {
			continue
		}

		m := lineRE.FindStringSubmatch(line)
		if m == nil {
			continue
		}

		monthStr := m[2]
		dayStr := m[3]
		timeStr := m[4]
		threadID, _ := strconv.Atoi(m[6])
		message := strings.TrimRight(m[8], "\r")

		dtStr := buildDatetime(monthStr, dayStr, timeStr, year)

		// Transfer start
		if sm := startRE.FindStringSubmatch(message); sm != nil {
			clipName := sm[1]
			fileRef := sm[2]
			engine := sm[3]
			source := detectSource(fileRef)

			store.AddJob(Job{
				ClipName:  clipName,
				Source:    source,
				Engine:    engine,
				StartTime: dtStr,
				Status:    "Transferring",
				Level:     "info",
				ThreadID:  threadID,
				FileRef:   fileRef,
			})
			continue
		}

		// Transfer success
		if sm := successRE.FindStringSubmatch(message); sm != nil {
			movieID := sm[1]
			store.CompleteJob(threadID, dtStr, "Completed", "good", movieID)
			continue
		}

		// Transfer failure
		if sm := failRE.FindStringSubmatch(message); sm != nil {
			movieID := sm[1]
			errorMsg := sm[2]
			store.CompleteJob(threadID, dtStr, errorMsg, "error", movieID)
			continue
		}

		// Duration change
		if sm := durationRE.FindStringSubmatch(message); sm != nil {
			clipName := sm[1]
			frames, _ := strconv.Atoi(sm[2])
			store.AddDuration(DurationEntry{
				ClipName:  clipName,
				Timestamp: dtStr,
				Frames:    frames,
			})
			continue
		}
	}
}

// ---------------------------------------------------------------------------
// HTTP Handlers
// ---------------------------------------------------------------------------

func sendJSON(w http.ResponseWriter, data interface{}) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	json.NewEncoder(w).Encode(data)
}

func sendHTML(w http.ResponseWriter, h string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write([]byte(h))
}

// DataTables server-side endpoint
func handleJobs(store *Store, w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	draw, _ := strconv.Atoi(q.Get("draw"))
	start, _ := strconv.Atoi(q.Get("start"))
	length, _ := strconv.Atoi(q.Get("length"))
	if length <= 0 {
		length = 25
	}
	searchVal := q.Get("search[value]")
	orderCol, _ := strconv.Atoi(q.Get("order[0][column]"))
	orderDir := strings.ToLower(q.Get("order[0][dir]"))
	if orderDir != "asc" {
		orderDir = "desc"
	}

	onlyFailures := q.Get("only_failures") == "true"
	twentyFourH := q.Get("twenty_four_hours") == "true"
	todayOnly := q.Get("today_only") == "true"

	allJobs := store.GetJobs()
	total := len(allJobs)

	today := time.Now().Format("2006-01-02")
	cutoff24h := time.Now().Add(-24 * time.Hour).Format("2006-01-02 15:04:05")

	// Filter
	var filtered []Job
	for _, j := range allJobs {
		if onlyFailures && j.Level != "error" {
			continue
		}
		if twentyFourH && j.StartTime < cutoff24h {
			continue
		}
		if todayOnly && !strings.HasPrefix(j.StartTime, today) {
			continue
		}
		if searchVal != "" {
			sv := strings.ToLower(searchVal)
			if !strings.Contains(strings.ToLower(j.ClipName), sv) &&
				!strings.Contains(strings.ToLower(j.Source), sv) &&
				!strings.Contains(strings.ToLower(j.Engine), sv) &&
				!strings.Contains(strings.ToLower(j.Status), sv) {
				continue
			}
		}
		filtered = append(filtered, j)
	}

	// Sort
	sort.Slice(filtered, func(i, k int) bool {
		var a, b string
		switch orderCol {
		case 0:
			a, b = filtered[i].StartTime, filtered[k].StartTime
		case 1:
			a, b = filtered[i].ClipName, filtered[k].ClipName
		case 2:
			a, b = filtered[i].Source, filtered[k].Source
		case 3:
			a, b = filtered[i].Engine, filtered[k].Engine
		case 4:
			a, b = filtered[i].EndTime, filtered[k].EndTime
		case 5:
			a, b = filtered[i].Status, filtered[k].Status
		default:
			a, b = filtered[i].StartTime, filtered[k].StartTime
		}
		if orderDir == "asc" {
			return a < b
		}
		return a > b
	})

	// Paginate
	filteredTotal := len(filtered)
	end := start + length
	if start > filteredTotal {
		start = filteredTotal
	}
	if end > filteredTotal {
		end = filteredTotal
	}
	page := filtered[start:end]

	// Format as array-of-arrays for DataTables
	var data [][]string
	for _, j := range page {
		endTime := j.EndTime
		if endTime == "" {
			endTime = "Unknown"
		}
		data = append(data, []string{
			j.StartTime, j.ClipName, j.Source, j.Engine, endTime, j.Status, j.Level,
		})
	}
	if data == nil {
		data = [][]string{}
	}

	sendJSON(w, map[string]interface{}{
		"draw":            draw,
		"recordsTotal":    total,
		"recordsFiltered": filteredTotal,
		"data":            data,
	})
}

func handleSummary(store *Store, w http.ResponseWriter, r *http.Request) {
	allJobs := store.GetJobs()

	var ok, errC, active, total int
	today := time.Now().Format("2006-01-02")
	var todayOk, todayErr int

	// Stats by source
	type srcStat struct {
		count    int
		totalDur float64
	}
	sourceStats := make(map[string]*srcStat)

	for _, j := range allJobs {
		total++
		switch j.Level {
		case "good":
			ok++
		case "error":
			errC++
		}
		if j.Status == "Transferring" {
			active++
		}

		if strings.HasPrefix(j.StartTime, today) {
			if j.Level == "good" {
				todayOk++
			} else if j.Level == "error" {
				todayErr++
			}
		}

		if j.Level == "good" && j.EndTime != "" && j.EndTime != "Unknown" {
			dur := durationSecs(j.StartTime, j.EndTime)
			if dur > 0 {
				ss, exists := sourceStats[j.Source]
				if !exists {
					ss = &srcStat{}
					sourceStats[j.Source] = ss
				}
				ss.count++
				ss.totalDur += dur
			}
		}
	}

	var b strings.Builder
	fmt.Fprintf(&b, `<h4>Gesamt</h4>
<table class="table table-condensed" style="color:inherit">
<tr><td>Erfolgreich</td><td><strong style="color:var(--green)">%d</strong></td></tr>
<tr><td>Fehlgeschlagen</td><td><strong style="color:var(--red)">%d</strong></td></tr>
<tr><td>Aktiv</td><td><strong style="color:var(--accent)">%d</strong></td></tr>
<tr><td>Gesamt</td><td><strong>%d</strong></td></tr>
</table>
<h4>Heute</h4>
<table class="table table-condensed" style="color:inherit">
<tr><td>Erfolgreich</td><td><strong style="color:var(--green)">%d</strong></td></tr>
<tr><td>Fehlgeschlagen</td><td><strong style="color:var(--red)">%d</strong></td></tr>
</table>
<h4>Durchschnittliche Dauer nach Quelle</h4>
<table class="table table-condensed" style="color:inherit">
<tr><th>Quelle</th><th>Transfers</th><th>&#216; Dauer</th></tr>`,
		ok, errC, active, total, todayOk, todayErr)

	for src, ss := range sourceStats {
		avg := int(ss.totalDur / float64(ss.count))
		fmt.Fprintf(&b, `<tr><td>%s</td><td>%d</td><td>%s</td></tr>`,
			html.EscapeString(src), ss.count, formatDuration(avg))
	}
	b.WriteString("</table>")

	sendHTML(w, b.String())
}

func handleActive(store *Store, w http.ResponseWriter, r *http.Request) {
	active := store.GetActiveJobs()

	type activeResult struct {
		ID              int    `json:"id"`
		ClipName        string `json:"clip_name"`
		Source          string `json:"source"`
		Engine          string `json:"engine"`
		StartTime       string `json:"start_time"`
		CurrentFrames   *int   `json:"current_frames"`
		MaxFrames       *int   `json:"max_frames"`
		LastFrameUpdate string `json:"last_frame_update"`
	}

	var result []activeResult
	for _, j := range active {
		ar := activeResult{
			ID:        j.ID,
			ClipName:  j.ClipName,
			Source:    j.Source,
			Engine:    j.Engine,
			StartTime: j.StartTime,
		}

		frames, ts := store.GetLatestFrames(j.ClipName)
		if frames > 0 {
			ar.CurrentFrames = &frames
			ar.LastFrameUpdate = ts
		}
		maxF := store.GetMaxFrames(j.ClipName)
		if maxF > 0 {
			ar.MaxFrames = &maxF
		}

		result = append(result, ar)
	}
	if result == nil {
		result = []activeResult{}
	}

	sendJSON(w, result)
}

func handleStats(store *Store, w http.ResponseWriter, r *http.Request) {
	allJobs := store.GetJobs()
	today := time.Now().Format("2006-01-02")

	var active, todayOk, todayErr int
	var todayDurations []float64
	sourceAvgs := make(map[string][]float64)
	var lastCompleted *Job

	for i := range allJobs {
		j := &allJobs[i]
		if j.Status == "Transferring" {
			active++
		}
		if strings.HasPrefix(j.StartTime, today) {
			if j.Level == "good" {
				todayOk++
				dur := durationSecs(j.StartTime, j.EndTime)
				if dur > 0 {
					todayDurations = append(todayDurations, dur)
				}
			} else if j.Level == "error" {
				todayErr++
			}
		}
		if j.Level == "good" && j.EndTime != "" && j.EndTime != "Unknown" {
			dur := durationSecs(j.StartTime, j.EndTime)
			if dur > 0 {
				sourceAvgs[j.Source] = append(sourceAvgs[j.Source], dur)
			}
			if lastCompleted == nil || j.EndTime > lastCompleted.EndTime {
				jCopy := allJobs[i]
				lastCompleted = &jCopy
			}
		}
	}

	var avgDurationToday *int
	if len(todayDurations) > 0 {
		sum := 0.0
		for _, d := range todayDurations {
			sum += d
		}
		avg := int(sum / float64(len(todayDurations)))
		avgDurationToday = &avg
	}

	avgBySource := make(map[string]int)
	for src, durs := range sourceAvgs {
		sum := 0.0
		for _, d := range durs {
			sum += d
		}
		avgBySource[src] = int(sum / float64(len(durs)))
	}

	result := map[string]interface{}{
		"active":             active,
		"today_completed":    todayOk,
		"today_failed":       todayErr,
		"avg_duration_today": avgDurationToday,
		"avg_by_source":      avgBySource,
	}

	if lastCompleted != nil {
		result["last_completed"] = map[string]string{
			"clip_name": lastCompleted.ClipName,
			"end_time":  lastCompleted.EndTime,
		}
	}

	sendJSON(w, result)
}

func handleProgress(store *Store, w http.ResponseWriter, r *http.Request) {
	clip := r.URL.Query().Get("clip")
	if clip == "" {
		w.WriteHeader(400)
		sendJSON(w, map[string]string{"error": "clip parameter required"})
		return
	}

	durs := store.GetDurationsForClip(clip)
	if durs == nil {
		durs = []DurationEntry{}
	}
	sendJSON(w, durs)
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func durationSecs(start, end string) float64 {
	if start == "" || end == "" || end == "Unknown" {
		return 0
	}
	layout := "2006-01-02 15:04:05"
	s, err1 := time.Parse(layout, start)
	e, err2 := time.Parse(layout, end)
	if err1 != nil || err2 != nil {
		return 0
	}
	return e.Sub(s).Seconds()
}

func formatDuration(secs int) string {
	if secs < 60 {
		return fmt.Sprintf("%ds", secs)
	}
	m := secs / 60
	s := secs % 60
	if m < 60 {
		if s > 0 {
			return fmt.Sprintf("%dm %ds", m, s)
		}
		return fmt.Sprintf("%dm", m)
	}
	h := m / 60
	m = m % 60
	return fmt.Sprintf("%dh %dm", h, m)
}

// ---------------------------------------------------------------------------
// Static file server
// ---------------------------------------------------------------------------

var contentTypes = map[string]string{
	".html": "text/html; charset=utf-8",
	".css":  "text/css; charset=utf-8",
	".js":   "application/javascript; charset=utf-8",
	".json": "application/json; charset=utf-8",
	".png":  "image/png",
	".jpg":  "image/jpeg",
	".gif":  "image/gif",
	".ico":  "image/x-icon",
	".svg":  "image/svg+xml",
	".woff": "font/woff",
	".woff2":"font/woff2",
	".ttf":  "font/ttf",
	".eot":  "application/vnd.ms-fontobject",
	".map":  "application/json",
	".swf":  "application/x-shockwave-flash",
}

func serveStatic(staticDir string, w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	if path == "/" || path == "" {
		path = "/index.html"
	}

	// Security
	cleaned := filepath.Clean(path)
	if strings.Contains(cleaned, "..") {
		http.Error(w, "Forbidden", 403)
		return
	}

	fullPath := filepath.Join(staticDir, filepath.FromSlash(cleaned))

	data, err := os.ReadFile(fullPath)
	if err != nil {
		http.Error(w, "Not Found", 404)
		return
	}

	ext := strings.ToLower(filepath.Ext(fullPath))
	ct, ok := contentTypes[ext]
	if !ok {
		ct = "application/octet-stream"
	}

	w.Header().Set("Content-Type", ct)
	if ext != ".html" {
		w.Header().Set("Cache-Control", "public, max-age=3600")
	}
	w.Write(data)
}

// ---------------------------------------------------------------------------
// Main
// ---------------------------------------------------------------------------

func main() {
	cfg := loadConfig()

	log.Printf("PyLogJobs v2")
	log.Printf("Log directory : %s (max %d days)", cfg.LogDir, cfg.MaxDays)
	log.Printf("Port          : %d", cfg.Port)

	// Determine static dir (next to executable, then CWD)
	exePath, _ := os.Executable()
	staticDir := filepath.Join(filepath.Dir(exePath), "static")
	if _, err := os.Stat(staticDir); os.IsNotExist(err) {
		staticDir = filepath.Join(".", "static")
	}
	log.Printf("Static files  : %s", staticDir)

	store := NewStore()

	// Initial parse
	if _, err := os.Stat(cfg.LogDir); err == nil {
		parseLogFiles(store, cfg.LogDir, cfg.MaxDays)
		log.Printf("Initial parse: %d jobs, %d duration entries",
			len(store.jobs), len(store.durations))
	} else {
		log.Printf("WARNING: Log directory not found: %s (will keep trying)", cfg.LogDir)
	}

	// Cleanup stale
	n := store.CleanupStale(24 * time.Hour)
	if n > 0 {
		log.Printf("Marked %d stale transfers", n)
	}

	// Background parser
	go func() {
		for {
			time.Sleep(5 * time.Second)
			parseLogFiles(store, cfg.LogDir, cfg.MaxDays)
			// Periodic stale cleanup
			store.CleanupStale(24 * time.Hour)
		}
	}()

	// HTTP routes
	http.HandleFunc("/jobs", func(w http.ResponseWriter, r *http.Request) {
		handleJobs(store, w, r)
	})
	http.HandleFunc("/summary", func(w http.ResponseWriter, r *http.Request) {
		handleSummary(store, w, r)
	})
	http.HandleFunc("/api/active", func(w http.ResponseWriter, r *http.Request) {
		handleActive(store, w, r)
	})
	http.HandleFunc("/api/stats", func(w http.ResponseWriter, r *http.Request) {
		handleStats(store, w, r)
	})
	http.HandleFunc("/api/progress", func(w http.ResponseWriter, r *http.Request) {
		handleProgress(store, w, r)
	})
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		serveStatic(staticDir, w, r)
	})

	addr := fmt.Sprintf("0.0.0.0:%d", cfg.Port)
	log.Printf("Server running at http://%s/", addr)

	if err := http.ListenAndServe(addr, nil); err != nil {
		log.Fatalf("Server error: %v", err)
	}
}
