package ectx

import (
	"fmt"
)

type Context struct {
	errStack []error
}

type Scope struct {
	ctx            *Context
	format         string
	formatArgs     []any
	lastErrorDepth int
}

func New() *Context {
	return &Context{}
}

func (self *Context) Log() {
	if len(self.errStack) > 0 {
		fmt.Printf("Failed: %v\n", self.errStack[len(self.errStack)-1])
		if len(self.errStack) > 1 {
			fmt.Printf("Caused By:\n")
			i := len(self.errStack) - 2
			for i >= 0 {
				fmt.Printf("  Failed: %v\n", self.errStack[i])
				i -= 1
			}
		}
	}
}

func (self *Context) Report(err error) {
	if err != nil && len(self.errStack) == 0 {
		self.errStack = append(self.errStack, err)
	}
}

func (self *Context) Scope(format string, args ...any) Scope {
	return Scope{
		ctx:            self,
		format:         format,
		formatArgs:     args,
		lastErrorDepth: len(self.errStack),
	}
}

func (self *Scope) Close() {
	if len(self.ctx.errStack) > self.lastErrorDepth {
		err := fmt.Errorf(self.format, self.formatArgs...)
		self.ctx.errStack = append(self.ctx.errStack, err)
	}
}
