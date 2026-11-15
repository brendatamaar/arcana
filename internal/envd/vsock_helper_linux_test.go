package envd

import "golang.org/x/sys/unix"

func connectVsock(fd int, cid, port uint32) error {
	return unix.Connect(fd, &unix.SockaddrVM{CID: cid, Port: port})
}
