package main

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"math"
	"os"
	"path/filepath"
	"sync"
)

const recorderSampleRate = 16000

var recordingIndexMu sync.Mutex

type recordingStatus string

const (
	recordingDisabled  recordingStatus = "disabled"
	recordingRecording recordingStatus = "recording"
	recordingReady     recordingStatus = "ready"
	recordingFailed    recordingStatus = "failed"
)

type callRecorder struct {
	path string
	f    *os.File
	ch   chan recorderFrame
	done chan struct{}

	mu      sync.Mutex
	err     error
	frames  uint32 // stereo frames written
	closing bool
}

type recorderFrame struct {
	left  []float32
	right []float32
}

func recordingsRoot() string {
	if v := os.Getenv("WACALLS_RECORDINGS_DIR"); v != "" {
		return v
	}
	return "/data/recordings"
}

func recordingIndexPath() string {
	return filepath.Join(recordingsRoot(), "recordings-index.json")
}

func recordingIndexKey(sessionID, callID string) string {
	return safePathPart(sessionID) + "/" + safePathPart(callID)
}

func persistRecordingIndex(sessionID, callID, path string) error {
	if sessionID == "" || callID == "" || path == "" {
		return nil
	}
	recordingIndexMu.Lock()
	defer recordingIndexMu.Unlock()
	indexPath := recordingIndexPath()
	if err := os.MkdirAll(filepath.Dir(indexPath), 0o750); err != nil {
		return err
	}
	idx := map[string]string{}
	if data, err := os.ReadFile(indexPath); err == nil && len(data) > 0 {
		if err := json.Unmarshal(data, &idx); err != nil {
			idx = map[string]string{}
		}
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	idx[recordingIndexKey(sessionID, callID)] = path
	data, err := json.MarshalIndent(idx, "", "  ")
	if err != nil {
		return err
	}
	tmp := indexPath + ".tmp"
	if err := os.WriteFile(tmp, data, 0o640); err != nil {
		return err
	}
	return os.Rename(tmp, indexPath)
}

func indexedRecordingPath(sessionID, callID string) (string, bool) {
	data, err := os.ReadFile(recordingIndexPath())
	if err != nil || len(data) == 0 {
		return "", false
	}
	idx := map[string]string{}
	if err := json.Unmarshal(data, &idx); err != nil {
		return "", false
	}
	path := idx[recordingIndexKey(sessionID, callID)]
	if path == "" {
		return "", false
	}
	if _, err := os.Stat(path); err != nil {
		return "", false
	}
	return path, true
}

func scanRecordingPath(sessionID, callID string) (string, bool) {
	root := recordingsRoot()
	expectedName := safePathPart(callID) + ".wav"
	candidates := []string{
		filepath.Join(root, safePathPart(sessionID), expectedName),
		filepath.Join(root, expectedName),
	}
	for _, path := range candidates {
		if st, err := os.Stat(path); err == nil && !st.IsDir() {
			return path, true
		}
	}
	var found string
	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || d.Name() != expectedName {
			return nil
		}
		found = path
		return filepath.SkipAll
	})
	if found == "" {
		return "", false
	}
	return found, true
}

func persistedRecordingPath(sessionID, callID string) (string, bool) {
	if path, ok := indexedRecordingPath(sessionID, callID); ok {
		return path, true
	}
	path, ok := scanRecordingPath(sessionID, callID)
	if ok {
		_ = persistRecordingIndex(sessionID, callID, path)
	}
	return path, ok
}

func newCallRecorder(sessionID, callID string) (*callRecorder, error) {
	dir := filepath.Join(recordingsRoot(), safePathPart(sessionID))
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, safePathPart(callID)+".wav")
	f, err := os.Create(path)
	if err != nil {
		return nil, err
	}
	r := &callRecorder{path: path, f: f, ch: make(chan recorderFrame, 64), done: make(chan struct{})}
	if err := writeWAVHeader(f, 0); err != nil {
		_ = f.Close()
		return nil, err
	}
	go r.loop()
	return r, nil
}

func safePathPart(s string) string {
	out := make([]rune, 0, len(s))
	for _, c := range s {
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '-' || c == '_' || c == '.' {
			out = append(out, c)
		}
	}
	if len(out) == 0 {
		return "unknown"
	}
	return string(out)
}

func (r *callRecorder) AddOperatorPCM(pcm16 []float32) {
	r.enqueue(recorderFrame{left: cloneFloat32(pcm16)})
}
func (r *callRecorder) AddPeerPCM(pcm16 []float32) {
	r.enqueue(recorderFrame{right: cloneFloat32(pcm16)})
}

func (r *callRecorder) enqueue(fr recorderFrame) {
	if r == nil {
		return
	}
	r.mu.Lock()
	closing := r.closing
	r.mu.Unlock()
	if closing {
		return
	}
	select {
	case r.ch <- fr:
	default:
		r.setErr(errors.New("recording buffer full; dropped audio"))
	}
}

func cloneFloat32(in []float32) []float32 {
	if len(in) == 0 {
		return nil
	}
	out := make([]float32, len(in))
	copy(out, in)
	return out
}

func (r *callRecorder) loop() {
	defer close(r.done)
	for fr := range r.ch {
		if err := r.writeFrame(fr); err != nil {
			r.setErr(err)
		}
	}
}

func (r *callRecorder) writeFrame(fr recorderFrame) error {
	n := len(fr.left)
	if len(fr.right) > n {
		n = len(fr.right)
	}
	if n == 0 {
		return nil
	}
	buf := make([]byte, n*4) // 2 channels * int16
	for i := 0; i < n; i++ {
		var l, rr float32
		if i < len(fr.left) {
			l = fr.left[i]
		}
		if i < len(fr.right) {
			rr = fr.right[i]
		}
		binary.LittleEndian.PutUint16(buf[i*4:], uint16(floatToInt16(l)))
		binary.LittleEndian.PutUint16(buf[i*4+2:], uint16(floatToInt16(rr)))
	}
	if _, err := r.f.Write(buf); err != nil {
		return err
	}
	r.mu.Lock()
	r.frames += uint32(n)
	r.mu.Unlock()
	return nil
}

func floatToInt16(v float32) int16 {
	if v > 1 {
		v = 1
	} else if v < -1 {
		v = -1
	}
	return int16(math.Round(float64(v * 32767)))
}

func (r *callRecorder) Close() (string, error) {
	if r == nil {
		return "", nil
	}
	r.mu.Lock()
	if !r.closing {
		r.closing = true
		close(r.ch)
	}
	r.mu.Unlock()
	<-r.done
	r.mu.Lock()
	frames := r.frames
	priorErr := r.err
	r.mu.Unlock()
	if _, err := r.f.Seek(0, 0); err != nil {
		priorErr = err
	} else if err := writeWAVHeader(r.f, frames); err != nil {
		priorErr = err
	}
	if err := r.f.Close(); err != nil && priorErr == nil {
		priorErr = err
	}
	return r.path, priorErr
}

func (r *callRecorder) setErr(err error) {
	if err == nil {
		return
	}
	r.mu.Lock()
	if r.err == nil {
		r.err = err
	}
	r.mu.Unlock()
}

func writeWAVHeader(f *os.File, frames uint32) error {
	dataSize := frames * 2 * 2
	byteRate := uint32(recorderSampleRate * 2 * 2)
	blockAlign := uint16(2 * 2)
	header := make([]byte, 44)
	copy(header[0:], "RIFF")
	binary.LittleEndian.PutUint32(header[4:], 36+dataSize)
	copy(header[8:], "WAVE")
	copy(header[12:], "fmt ")
	binary.LittleEndian.PutUint32(header[16:], 16)
	binary.LittleEndian.PutUint16(header[20:], 1)
	binary.LittleEndian.PutUint16(header[22:], 2)
	binary.LittleEndian.PutUint32(header[24:], recorderSampleRate)
	binary.LittleEndian.PutUint32(header[28:], byteRate)
	binary.LittleEndian.PutUint16(header[32:], blockAlign)
	binary.LittleEndian.PutUint16(header[34:], 16)
	copy(header[36:], "data")
	binary.LittleEndian.PutUint32(header[40:], dataSize)
	_, err := f.Write(header)
	return err
}
