//go:build linux
// +build linux

package util

import "syscall"

// disk usage of path/disk
// https://ispycode.com/GO/System-Calls/Disk-Usage
func DiskUsage(path string) *DiskStatus {
	fs := syscall.Statfs_t{}
	err := syscall.Statfs(path, &fs)
	if err != nil {
		return nil
	}
	disk := &DiskStatus{
		All:  fs.Blocks * uint64(fs.Bsize),
		Free: fs.Bfree * uint64(fs.Bsize),
		Used: 0,
	}
	disk.Used = disk.All - disk.Free
	return disk
}

const (
	B  = 1
	KB = 1024 * B
	MB = 1024 * KB
	GB = 1024 * MB
)
