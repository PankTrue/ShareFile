package settings

import (
	"os"
	"path/filepath"
	"sort"
	"strings"

	"golang.org/x/sys/unix"
)

type Volume struct {
	Path       string `json:"path"`
	Device     string `json:"device"`
	Filesystem string `json:"filesystem"`
	TotalBytes uint64 `json:"total_bytes"`
	FreeBytes  uint64 `json:"free_bytes"`
	ReadOnly   bool   `json:"read_only"`
	Current    bool   `json:"current"`
}

// Volumes lists mounted filesystems, never unmounted devices or formatting tools.
func Volumes(dataDir string) []Volume {
	current, _ := filepath.EvalSymlinks(dataDir)
	if current == "" {
		current = dataDir
	}
	var out []Volume
	if data, err := os.ReadFile("/proc/mounts"); err == nil {
		out = parseMounts(string(data))
	} else {
		out = append(out, Volume{Path: "/", Device: "Системный диск"})
		if info, err := os.Stat("/System/Volumes/Data"); err == nil && info.IsDir() {
			out = append(out, Volume{Path: "/System/Volumes/Data", Device: "Системный диск (данные)"})
		}
		entries, _ := os.ReadDir("/Volumes")
		for _, e := range entries {
			if e.IsDir() {
				out = append(out, Volume{Path: filepath.Join("/Volumes", e.Name()), Device: e.Name()})
			}
		}
	}
	best := -1
	for i, v := range out {
		if within(v.Path, current) && (best < 0 || len(v.Path) > len(out[best].Path)) {
			best = i
		}
	}
	result := []Volume{}
	var currentFS unix.Statfs_t
	_ = unix.Statfs(current, &currentFS)
	for i, v := range out {
		var st unix.Statfs_t
		if err := unix.Statfs(v.Path, &st); err != nil {
			continue
		}
		v.TotalBytes, v.FreeBytes = uint64(st.Blocks)*uint64(st.Bsize), uint64(st.Bavail)*uint64(st.Bsize)
		// ST_RDONLY (Linux) and MNT_RDONLY (Darwin) both use bit 0.
		v.ReadOnly = v.ReadOnly || st.Flags&1 != 0
		v.Current = i == best && st.Fsid == currentFS.Fsid
		if best == 0 && !v.Current && st.Fsid == currentFS.Fsid {
			v.Current = true
		}
		result = append(result, v)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Path < result[j].Path })
	return result
}

func parseMounts(data string) []Volume {
	out := []Volume{}
	seen := map[string]bool{}
	unescape := strings.NewReplacer(`\040`, " ", `\011`, "\t", `\012`, "\n", `\134`, `\`)
	for _, line := range strings.Split(data, "\n") {
		f := strings.Fields(line)
		if len(f) < 4 {
			continue
		}
		device, path := unescape.Replace(f[0]), unescape.Replace(f[1])
		if !strings.HasPrefix(device, "/dev/") && path != "/" {
			continue
		}
		if seen[path] {
			continue
		}
		seen[path] = true
		out = append(out, Volume{Path: path, Device: device, Filesystem: f[2], ReadOnly: strings.Contains(","+f[3]+",", ",ro,")})
	}
	return out
}
