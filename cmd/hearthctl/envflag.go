package main

import (
	"flag"
	"os"
)

// envFlag registers a string flag whose value falls back to an environment variable, and keeps that value out of
// the help text. The standard library prints a flag's default in -h, and a default filled from HEARTH_DATABASE_URL
// or a token is a live secret, so the default shown is empty and the usage line names the variable instead. A value
// given on the command line is used as given.
func envFlag(fs *flag.FlagSet, name, env, usage string) *string {
	e := &envString{val: os.Getenv(env)}
	fs.Var(e, name, usage)
	return &e.val
}

// envString is the flag.Value behind envFlag: String reports only what the person set, never the fallback.
type envString struct {
	val string
	set bool
}

func (e *envString) String() string {
	if e == nil || !e.set {
		return ""
	}
	return e.val
}

func (e *envString) Set(v string) error {
	e.val, e.set = v, true
	return nil
}
