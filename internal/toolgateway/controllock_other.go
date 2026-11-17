//go:build !unix

package toolgateway

import (
	"errors"
	"os"
)

func lockControl(string) (*os.File, error) {
	return nil, errors.New("the tool gateway's control socket needs a Unix system")
}
