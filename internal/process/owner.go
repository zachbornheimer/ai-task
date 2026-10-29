package process

import "os"

// Owner names the process that owns a record (a running verification) well
// enough to tell later whether it is still the same process: its pid, the
// host whose pid space that is, and the start time that separates it from a
// later process the OS gave the same pid.
type Owner struct {
	PID   int
	Host  string
	Start string
}

// Self identifies the calling process. Start is empty where the platform
// cannot report a process start time; such an owner is judged by pid alone.
func Self() Owner {
	host, _ := os.Hostname() // an unknown host is never "this host", so the owner is never judged gone
	pid := os.Getpid()
	start, _ := startTime(pid)
	return Owner{PID: pid, Host: host, Start: start}
}

// Gone reports whether the owner provably no longer exists: its pid is not
// running, or is running with a different start time (the pid was reused).
// An owner from another host, with no pid, or on a platform that cannot
// probe processes is never reported gone; its liveness is unknown.
func (o Owner) Gone() bool {
	if o.PID <= 0 {
		return false
	}
	if host, err := os.Hostname(); err != nil || host != o.Host {
		return false
	}
	exists, probed := pidExists(o.PID)
	if !probed {
		return false
	}
	if !exists {
		return true
	}
	if o.Start == "" {
		return false
	}
	current, ok := startTime(o.PID)
	return ok && current != o.Start
}
