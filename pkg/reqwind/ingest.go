package reqwind

import (
	"bufio"
	"compress/gzip"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"syscall"
	"time"
)

func (s *State) ingest(path string, now time.Time, vhost string) (scanStats, error) {
	var stats scanStats
	source := vhost
	if source == "" {
		source = sourceVhost(path)
	}
	f, err := os.Open(path)
	if err != nil {
		return stats, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return stats, err
	}
	if !info.Mode().IsRegular() {
		return stats, fmt.Errorf("%s is not a regular file", path)
	}
	stat := info.Sys().(*syscall.Stat_t)
	id := fmt.Sprintf("%d:%d", stat.Dev, stat.Ino)
	cp := s.Files[id]
	compressed := strings.HasSuffix(strings.ToLower(path), ".gz")
	if cp.Offset < 0 {
		return stats, errors.New("negative checkpoint offset")
	}
	if compressed && cp.Compressed && cp.PhysicalSize == info.Size() && cp.Modified == info.ModTime().UnixNano() {
		head, tail, err := fingerprint(f, info.Size())
		if err != nil {
			return stats, err
		}
		if head == cp.ArchiveHead && tail == cp.ArchiveTail {
			cp.Path = path
			s.setCheckpoint(id, cp)
			return stats, nil
		}
	}
	var reader io.Reader
	var samples *logSamples
	var zr *gzip.Reader
	defer func() {
		if zr != nil {
			zr.Close()
		}
	}()
	rewind := func() error {
		if _, err := f.Seek(0, io.SeekStart); err != nil {
			return err
		}
		input := io.LimitReader(f, info.Size())
		if zr == nil {
			zr, err = gzip.NewReader(input)
		} else {
			err = zr.Reset(input)
		}
		if err != nil {
			return err
		}
		samples = &logSamples{}
		reader = io.TeeReader(zr, samples)
		return nil
	}
	matches := func(candidate checkpoint) (bool, error) {
		if candidate.Offset <= 0 {
			return false, nil
		}
		if compressed {
			if err := rewind(); err != nil {
				return false, err
			}
			return skipDecoded(reader, samples, candidate)
		}
		if candidate.Offset > info.Size() {
			return false, nil
		}
		head, tail, err := fingerprint(f, candidate.Offset)
		return head == candidate.Head && tail == candidate.Tail, err
	}
	matched, err := matches(cp)
	if err != nil {
		return stats, err
	}
	if !matched {
		cp = checkpoint{Source: source}
		// Search other files only when the inode checkpoint does not match.
		for _, candidate := range s.checkpointCandidates(id, source, path, compressed) {
			matched, err = matches(candidate)
			if err != nil {
				return stats, err
			}
			if matched {
				cp = candidate
				break
			}
		}
	}
	if compressed {
		if !matched {
			if err := rewind(); err != nil {
				return stats, err
			}
		}
	} else {
		if _, err := f.Seek(cp.Offset, io.SeekStart); err != nil {
			return stats, err
		}
		reader = io.LimitReader(f, info.Size()-cp.Offset)
	}
	if cp.Source == "" || vhost != "" {
		cp.Source = source
	}
	r := bufio.NewReaderSize(reader, 64*1024)
	today := now.UTC().Format("2006-01-02")
	visitorMAC := hmac.New(sha256.New, []byte(s.Salt))
	var line []byte
	var consumed int64
	oversized := false
	for {
		if s.ctx != nil {
			if err := s.ctx.Err(); err != nil {
				return stats, err
			}
		}
		part, readErr := r.ReadSlice('\n')
		consumed += int64(len(part))
		if len(line)+len(part) > maxLine {
			oversized = true
		}
		if !oversized {
			line = append(line, part...)
		}
		if errors.Is(readErr, bufio.ErrBufferFull) {
			continue
		}
		if readErr == io.EOF && (!compressed || consumed == 0) {
			break
		}
		if readErr != nil && readErr != io.EOF {
			return stats, readErr
		}
		stats.Lines++
		var e Entry
		var parseErr error
		if oversized {
			parseErr = fmt.Errorf("line exceeds %d bytes; prefix shown", maxLine)
		} else {
			e, parseErr = ParseLine(strings.TrimSuffix(string(line), "\n"))
		}
		if parseErr != nil {
			stats.Invalid++
			if s.invalidLine != nil {
				s.invalidLine(path, cp.Offset, line, parseErr)
			}
		} else {
			if vhost != "" {
				e.Vhost = vhost
			} else if e.Vhost == "" {
				e.Vhost = cp.Source
			}
			if err := s.add(e, today, visitorMAC); err != nil {
				return stats, err
			}
		}
		cp.Offset += consumed
		stats.Bytes += consumed
		line = line[:0]
		consumed, oversized = 0, false
		if readErr == io.EOF {
			break
		}
	}
	cp.Compressed = compressed
	if compressed {
		cp.Head, cp.Tail = samples.sums()
		cp.PhysicalSize, cp.Modified = info.Size(), info.ModTime().UnixNano()
		cp.ArchiveHead, cp.ArchiveTail, err = fingerprint(f, info.Size())
	} else {
		cp.Head, cp.Tail, err = fingerprint(f, cp.Offset)
		cp.PhysicalSize, cp.Modified, cp.ArchiveHead, cp.ArchiveTail = 0, 0, "", ""
	}
	if err != nil {
		return stats, err
	}
	cp.Path = path
	cp.TotalLines += stats.Lines
	s.setCheckpoint(id, cp)
	s.InvalidLines += stats.Invalid
	return stats, nil
}

type checkpointKey struct {
	source, path string
	compressed   bool
}

func (s *State) indexCheckpoint(id string, cp checkpoint) {
	for _, path := range []string{"", cp.Path} {
		key := checkpointKey{cp.Source, path, cp.Compressed}
		if s.fileIndex[key] == nil {
			s.fileIndex[key] = map[string]bool{}
		}
		s.fileIndex[key][id] = true
	}
}

func (s *State) setCheckpoint(id string, cp checkpoint) {
	if s.fileIndex != nil {
		if old, ok := s.Files[id]; ok {
			for _, path := range []string{"", old.Path} {
				key := checkpointKey{old.Source, path, old.Compressed}
				delete(s.fileIndex[key], id)
				if len(s.fileIndex[key]) == 0 {
					delete(s.fileIndex, key)
				}
			}
		}
		s.indexCheckpoint(id, cp)
	}
	s.Files[id] = cp
}

func (s *State) checkpointCandidates(id, source, path string, compressed bool) []checkpoint {
	if s.fileIndex == nil {
		s.fileIndex = map[checkpointKey]map[string]bool{}
		for id, cp := range s.Files {
			s.indexCheckpoint(id, cp)
		}
	}
	var candidates []checkpoint
	for _, key := range []checkpointKey{{source, "", !compressed}, {source, path, compressed}} {
		for otherID := range s.fileIndex[key] {
			if cp := s.Files[otherID]; otherID != id && cp.Offset > 0 {
				candidates = append(candidates, cp)
			}
		}
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].Offset > candidates[j].Offset })
	return candidates
}

type logSamples struct {
	head, tail [256]byte
	n          int64
}

func (s *logSamples) Write(p []byte) (int, error) {
	n := len(p)
	if s.n < 256 {
		copy(s.head[int(s.n):], p)
	}
	if n >= 256 {
		copy(s.tail[:], p[n-256:])
	} else {
		previous := int(min(s.n, 256))
		if previous+n <= 256 {
			copy(s.tail[previous:], p)
		} else {
			copy(s.tail[:], s.tail[previous+n-256:previous])
			copy(s.tail[256-n:], p)
		}
	}
	s.n += int64(n)
	return n, nil
}

func (s *logSamples) sums() (string, string) {
	n := min(s.n, 256)
	a, b := sha256.Sum256(s.head[:n]), sha256.Sum256(s.tail[:n])
	return hex.EncodeToString(a[:]), hex.EncodeToString(b[:])
}

func skipDecoded(r io.Reader, samples *logSamples, cp checkpoint) (bool, error) {
	first := make([]byte, min(cp.Offset, 256))
	if _, err := io.ReadFull(r, first); err != nil {
		if err == io.EOF || err == io.ErrUnexpectedEOF {
			return false, nil
		}
		return false, err
	}
	head := sha256.Sum256(first)
	if hex.EncodeToString(head[:]) != cp.Head {
		return false, nil
	}
	if _, err := io.CopyN(io.Discard, r, cp.Offset-int64(len(first))); err != nil {
		if err == io.EOF {
			return false, nil
		}
		return false, err
	}
	_, tail := samples.sums()
	return tail == cp.Tail, nil
}
