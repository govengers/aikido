package vfs_test

import (
	"errors"
	"fmt"

	"github.com/govengers/aikido/vfs"
)

func ExampleValidatePath() {
	err := vfs.ValidatePath("../etc/passwd")
	fmt.Println(errors.Is(err, vfs.ErrPathInvalid), err)
	// Output: true aikido/vfs: invalid path: parent-directory segment
}
