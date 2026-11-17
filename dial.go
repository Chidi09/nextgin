package nextgin

import "fmt"

func checkTCPNetwork(network string) error {
	switch network {
	case "tcp", "tcp4", "tcp6":
		return nil
	default:
		return fmt.Errorf("nextgin: DialContext supports tcp only, got %q", network)
	}
}
