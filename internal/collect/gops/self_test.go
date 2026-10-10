package gops

import "os"

func pidSelf() int { return os.Getpid() }
