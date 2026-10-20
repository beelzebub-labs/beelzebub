package resourceguard

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"

	log "github.com/sirupsen/logrus"
)

type memoryGroup struct {
	directory string
	limitFile string
	usageFile string
}

type memoryGuard struct {
	readFile func(string) ([]byte, error)
	mu       sync.Mutex
	groups   []memoryGroup
	loaded   bool
}

// NewMemoryCheck reads host availability and all visible cgroup memory limits.
// Limits and usage are reread so container limit updates take effect at runtime.
func NewMemoryCheck() func() error {
	if runtime.GOOS != "linux" {
		log.Warn("Memory percentage admission checks require Linux; the memory waiter count remains bounded")
		return func() error { return nil }
	}

	guard := &memoryGuard{readFile: os.ReadFile}
	return guard.check
}

func (g *memoryGuard) check() error {
	data, err := g.readFile("/proc/meminfo")
	if err != nil {
		return fmt.Errorf("measure host memory: %w", err)
	}

	values := make(map[string]uint64, 2)
	for line := range strings.SplitSeq(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 || (fields[0] != "MemTotal:" && fields[0] != "MemAvailable:") {
			continue
		}

		if len(fields) != 3 || fields[2] != "kB" {
			return errors.New("invalid host memory measurement")
		}

		value, parseErr := strconv.ParseUint(fields[1], 10, 64)
		if parseErr != nil {
			return errors.New("invalid host memory measurement")
		}

		values[fields[0]] = value
	}

	// Compare in KiB to avoid multiplication overflow. Missing MemAvailable
	// cannot be treated as zero usage or substituted with reclaimable caches.
	total, totalOK := values["MemTotal:"]
	available, availableOK := values["MemAvailable:"]
	if !totalOK || !availableOK {
		return errors.New("host memory availability measurement missing")
	}

	if err := checkMemory(total, available); err != nil {
		return err
	}

	groups, err := g.memoryGroups()
	if err != nil {
		return err
	}

	for _, group := range groups {
		limitBytes, err := g.readFile(filepath.Join(group.directory, group.limitFile))
		if errors.Is(err, fs.ErrNotExist) {
			// The unified root and cgroups without a memory controller need
			// not expose memory.max. Their visible parents are checked too.
			continue
		}

		if err != nil {
			return fmt.Errorf("measure container memory limit: %w", err)
		}

		if strings.TrimSpace(string(limitBytes)) == "max" {
			continue
		}

		limit, err := parseMemoryValue(limitBytes)
		if err != nil || limit == 0 {
			return errors.New("invalid container memory limit")
		}

		usageBytes, err := g.readFile(filepath.Join(group.directory, group.usageFile))
		if err != nil {
			return fmt.Errorf("measure container memory usage: %w", err)
		}

		usage, err := parseMemoryValue(usageBytes)
		if err != nil {
			return err
		}

		if usage >= limit {
			return ErrMemoryPressure
		}

		if err := checkMemory(limit, limit-usage); err != nil {
			return err
		}
	}

	return nil
}

func (g *memoryGuard) memoryGroups() ([]memoryGroup, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if !g.loaded {
		groups, err := g.discoverGroups()
		if err != nil {
			return nil, err // A transient read failure can recover on the next admission.
		}

		g.groups, g.loaded = groups, true
	}

	return g.groups, nil
}

func parseMemoryValue(data []byte) (uint64, error) {
	value, err := strconv.ParseUint(strings.TrimSpace(string(data)), 10, 64)
	if err != nil {
		return 0, errors.New("invalid container memory measurement")
	}

	return value, nil
}

func (g *memoryGuard) discoverGroups() ([]memoryGroup, error) {
	membership, err := g.readFile("/proc/self/cgroup")
	if err != nil {
		return nil, fmt.Errorf("read memory controller membership: %w", err)
	}

	mounts, err := g.readFile("/proc/self/mountinfo")
	if err != nil {
		return nil, fmt.Errorf("read memory controller mounts: %w", err)
	}

	var groups []memoryGroup
	unescape := strings.NewReplacer(`\040`, " ", `\011`, "\t", `\012`, "\n", `\134`, `\`)
	for line := range strings.SplitSeq(strings.TrimSpace(string(membership)), "\n") {
		if line == "" {
			continue
		}

		entry := strings.SplitN(line, ":", 3)
		if len(entry) != 3 {
			return nil, errors.New("invalid memory controller membership")
		}

		unified := entry[0] == "0" && entry[1] == ""
		if !unified && !containsOption(entry[1], "memory") {
			continue
		}

		found := false
		for mount := range strings.SplitSeq(string(mounts), "\n") {
			parts := strings.SplitN(mount, " - ", 2)
			if len(parts) != 2 {
				continue
			}

			left, right := strings.Fields(parts[0]), strings.Fields(parts[1])
			if len(left) < 5 || len(right) < 3 || (unified && right[0] != "cgroup2") || (!unified && (right[0] != "cgroup" || !containsOption(right[2], "memory"))) {
				continue
			}

			root, mountpoint := filepath.Clean(unescape.Replace(left[3])), filepath.Clean(unescape.Replace(left[4]))
			if !filepath.IsAbs(root) || !filepath.IsAbs(mountpoint) || !filepath.IsAbs(entry[2]) {
				return nil, errors.New("invalid memory controller path")
			}

			relative, err := filepath.Rel(root, entry[2])
			if entry[2] == "/" {
				relative, err = ".", nil // Private cgroup namespace root.
			}

			if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
				continue
			}

			limitFile, usageFile := "memory.max", "memory.current"
			if !unified {
				limitFile, usageFile = "memory.limit_in_bytes", "memory.usage_in_bytes"
			}

			for directory := filepath.Join(mountpoint, relative); ; directory = filepath.Dir(directory) {
				groups = append(groups, memoryGroup{directory, limitFile, usageFile})
				if directory == mountpoint {
					break
				}
			}

			found = true
			break
		}

		if !found {
			return nil, errors.New("memory controller mount unavailable")
		}
	}

	return groups, nil
}

func containsOption(options, wanted string) bool {
	for option := range strings.SplitSeq(options, ",") {
		if option == wanted {
			return true
		}
	}

	return false
}
