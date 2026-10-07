package ocs

import "golang.org/x/sys/unix"

// readKeys feeds terminal input to the returned channel until stop is called.
// A blocked read cannot be cancelled, so the reader polls with a short timeout
// and reads only when input is waiting; stop returns once it has quit. Without
// that, a reader left behind by the picker swallowed the first line typed into
// whatever ran next (agb push's host prompt).
func readKeys(fd int) (<-chan []byte, func()) {
	input := make(chan []byte)
	done := make(chan struct{})
	exited := make(chan struct{})
	go func() {
		defer close(exited)
		defer close(input)
		buffer := make([]byte, 4096)
		fds := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
		for {
			select {
			case <-done:
				return
			default:
			}
			ready, err := unix.Poll(fds, 50)
			if err == unix.EINTR || (err == nil && ready == 0) {
				continue
			}
			if err != nil {
				return
			}
			n, err := unix.Read(fd, buffer)
			if err != nil || n == 0 {
				return
			}
			chunk := append([]byte(nil), buffer[:n]...)
			select {
			case input <- chunk:
			case <-done:
				return
			}
		}
	}()
	return input, func() {
		close(done)
		<-exited
	}
}
