package daemon

import (
	"net"
	"os"
)

const (
	Ready     = "READY=1"
	Stopping  = "STOPPING=1"
	Reloading = "RELOADING=1"
	Watchdog  = "WATCHDOG=1"
)

func Notify(state string) (bool, error) {
	addr := os.Getenv("NOTIFY_SOCKET")
	if addr == "" {
		return false, nil
	}

	conn, err := net.Dial("unixgram", addr)
	if err != nil {
		return false, err
	}
	defer conn.Close()

	if _, err = conn.Write([]byte(state)); err != nil {
		return false, err
	}
	return true, nil
}
