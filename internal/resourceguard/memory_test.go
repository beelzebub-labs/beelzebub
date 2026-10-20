package resourceguard

import (
	"io/fs"
	"testing"

	"github.com/stretchr/testify/require"
)

func memoryFixture() (*memoryGuard, map[string]string) {
	files := map[string]string{
		"/proc/meminfo":        "MemTotal: 100 kB\nMemAvailable: 30 kB\nCached: 40 kB\n",
		"/proc/self/cgroup":    "",
		"/proc/self/mountinfo": "",
	}

	guard := &memoryGuard{readFile: func(path string) ([]byte, error) {
		value, ok := files[path]
		if !ok {
			return nil, fs.ErrNotExist
		}

		return []byte(value), nil
	}}

	return guard, files
}

func unifiedFixture() (*memoryGuard, map[string]string) {
	g, files := memoryFixture()
	files["/proc/self/cgroup"] = "0::/tenant/sensor\n"
	files["/proc/self/mountinfo"] = "10 1 0:1 / /cg rw - cgroup2 cgroup rw\n"
	files["/cg/tenant/sensor/memory.max"] = "100"
	files["/cg/tenant/sensor/memory.current"] = "79"
	files["/cg/tenant/memory.max"] = "max"
	return g, files
}

func TestMemoryHostThresholdAndReadFailures(t *testing.T) {
	for _, value := range []string{
		"MemTotal: 100 kB\nMemAvailable: 20 kB\n",
		"MemTotal: 100 kB\nMemAvailable: 19 kB\n",
	} {
		g, files := memoryFixture()
		files["/proc/meminfo"] = value
		require.ErrorIs(t, g.check(), ErrMemoryPressure)
	}

	for _, value := range []string{"", "MemTotal: 100 kB\n", "MemTotal: 100 bytes\nMemAvailable: 30 kB\n", "MemTotal: x kB\nMemAvailable: 30 kB\n", "MemTotal: 0 kB\nMemAvailable: 0 kB\n", "MemTotal: 100 kB\nMemAvailable: 101 kB\n"} {
		g, files := memoryFixture()
		files["/proc/meminfo"] = value
		require.Error(t, g.check())
	}

	g, files := memoryFixture()
	delete(files, "/proc/meminfo")
	require.Error(t, g.check())
	g, files = memoryFixture()
	require.NoError(t, g.check())
	delete(files, "/proc/self/cgroup")

	// Membership is cached after successful discovery; values are not.
	require.NoError(t, g.check())
	files["/proc/meminfo"] = "MemTotal: 100 kB\nMemAvailable: 20 kB\n"
	require.ErrorIs(t, g.check(), ErrMemoryPressure)
}

func TestMemoryContainerLimitsParentsAndRecovery(t *testing.T) {
	g, files := unifiedFixture()
	require.NoError(t, g.check())
	files["/cg/tenant/sensor/memory.current"] = "80"
	require.ErrorIs(t, g.check(), ErrMemoryPressure)
	files["/cg/tenant/sensor/memory.max"] = "200"
	require.NoError(t, g.check(), "limit updates must take effect")
	files["/cg/tenant/memory.max"] = "100"
	files["/cg/tenant/memory.current"] = "81"
	require.ErrorIs(t, g.check(), ErrMemoryPressure, "parent controller also protects sibling workloads")
	files["/cg/tenant/memory.current"] = "79"
	require.NoError(t, g.check())
	files["/cg/tenant/sensor/memory.current"] = "200"
	require.ErrorIs(t, g.check(), ErrMemoryPressure)
	files["/cg/tenant/sensor/memory.max"] = "max"
	delete(files, "/cg/tenant/sensor/memory.current")
	require.NoError(t, g.check())
	for _, limit := range []string{"0", "-1", "invalid"} {
		files["/cg/tenant/sensor/memory.max"] = limit
		require.Error(t, g.check())
	}

	files["/cg/tenant/sensor/memory.max"] = "100"
	require.Error(t, g.check())
	files["/cg/tenant/sensor/memory.current"] = "invalid"
	require.Error(t, g.check())
	read := g.readFile
	g.readFile = func(path string) ([]byte, error) {
		if path == "/cg/tenant/sensor/memory.max" {
			return nil, fs.ErrPermission
		}

		return read(path)
	}

	require.Error(t, g.check())
}

func TestMemoryCgroupV1AndPrivateNamespace(t *testing.T) {
	for _, tc := range []struct{ membership, mount, directory, limit, usage string }{
		{"2:cpu,memory:/sensor\n1:cpuacct:/else\n", "10 1 0:1 / /memory rw - cgroup cgroup rw,cpu,memory\n", "/memory/sensor", "memory.limit_in_bytes", "memory.usage_in_bytes"},
		{"0::/\n", "10 1 0:1 /docker/sensor /cg rw - cgroup2 cgroup rw\n", "/cg", "memory.max", "memory.current"},
		{"0::/tenant/sensor\n", "10 1 0:1 /tenant /cg\\040space rw - cgroup2 cgroup rw\n", "/cg space/sensor", "memory.max", "memory.current"},
	} {
		g, files := memoryFixture()
		files["/proc/self/cgroup"] = tc.membership
		files["/proc/self/mountinfo"] = tc.mount
		files[tc.directory+"/"+tc.limit] = "100"
		files[tc.directory+"/"+tc.usage] = "79"
		require.NoError(t, g.check())
		files[tc.directory+"/"+tc.usage] = "80"
		require.ErrorIs(t, g.check(), ErrMemoryPressure)
	}
}

func TestMemoryDiscoveryFailuresCanRecover(t *testing.T) {
	for _, path := range []string{"/proc/self/cgroup", "/proc/self/mountinfo"} {
		g, files := memoryFixture()
		value := files[path]
		delete(files, path)
		require.Error(t, g.check())
		files[path] = value
		require.NoError(t, g.check())
	}

	for _, tc := range []struct{ membership, mount string }{
		{"malformed", ""},
		{"0::/tenant/sensor", "malformed\n10 1 0:1 / /cg rw - tmpfs tmpfs rw"},
		{"0::/other", "10 1 0:1 /tenant /cg rw - cgroup2 cgroup rw"},
		{"0::relative", "10 1 0:1 / /cg rw - cgroup2 cgroup rw"},
	} {
		g, files := memoryFixture()
		files["/proc/self/cgroup"] = tc.membership
		files["/proc/self/mountinfo"] = tc.mount
		require.Error(t, g.check())
	}
}

func TestMemoryCheckConstruction(t *testing.T) {
	require.NotNil(t, NewMemoryCheck())
}
